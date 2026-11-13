//go:build shots

package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func shootS20(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h, fake, release := s20Lab(t, theme, kind)
	if kind == "14f" || kind == "14f2" || kind == "14g" || kind == "14m" {
		fake.WaitCreate = release.hold
	}
	if kind == "14m" {
		h.srv.SetBootBudget(300 * time.Millisecond)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, s20User), ts.URL+"/new-agent", width, height)...); err != nil {
		t.Fatal(err)
	}
	s20Drive(t, ctx, kind, release, fake)
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var el=document.querySelector('.setup, .boot, .nhero, .card.prefill, .keyout');if(el)el.scrollIntoView({block:'start'});})()`), nil)); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	s20CopyClean(t, ctx, kind)
	if height < 800 {
		s20TapTargets(t, ctx)
	}
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
	s20After(t, ctx, kind, release, fake)
}

// s20User is filled by s20Lab for the session cookie. One shot runs at a time
// inside TestShots, so a package variable is enough.
var s20User string

type s20Gate struct {
	hold chan struct{}
	once sync.Once
}

func (g *s20Gate) release() {
	if g == nil {
		return
	}
	g.once.Do(func() { close(g.hold) })
}

func s20Lab(t *testing.T, theme, kind string) (*harness, *host.Fake, *s20Gate) {
	t.Helper()
	clearServerProviderKeys(t)
	h := newHarness(t)
	when := mst(2026, 11, 11, 13, 21)
	h.st.SetNow(func() time.Time { return when })
	fake := host.NewFake()
	fake.OnCreate = func(spec host.CreateSpec) {
		guest, err := h.st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			return
		}
		_ = h.st.SaveVMToken(context.Background(), spec.AgentID, guest)
	}
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s20", "https://castbot.example")
	h.srv.SetDescribe(&countDescribe{})
	h.srv.SetDataDir(t.TempDir())
	h.srv.SetDiskFree(func(string) (uint64, error) { return 1 << 40, nil })
	email := "s20-" + kind + "-" + theme + "@example.com"
	user, _ := h.account(email, "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	if kind != "14k" {
		addModelKey(t, h, user.ID)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	s20User = user.ID
	gate := &s20Gate{hold: make(chan struct{})}
	t.Cleanup(gate.release)
	return h, fake, gate
}

func s20Drive(t *testing.T, ctx context.Context, kind string, gate *s20Gate, fake *host.Fake) {
	t.Helper()
	s20Wait(t, ctx, `!!document.querySelector('#composer textarea') && (document.body.innerText||'').indexOf('What should your new agent do?')>=0`)
	switch kind {
	case "14a":
		s20Wait(t, ctx, `document.getElementById('send-btn').disabled && !!document.querySelector('.nfoot .link')`)
	case "14b":
		s20Send(t, ctx, s20Scout, s20Card("Create Scout", "every weekday morning"))
	case "14b2":
		s20Send(t, ctx, s20Scout, s20Card("Create Scout", "Pauses after 15 min idle"))
		s20Click(t, ctx, `document.querySelector('form[action="/new-agent/edit"] button').click()`, ` (document.body.innerText||'').indexOf('Editing')>=0 && (document.body.innerText||'').indexOf('Save changes')>=0 && (document.body.innerText||'').indexOf('Create Scout')<0`)
	case "14c":
		s20Send(t, ctx, s20Juno, s20Card("Create Juno", "Not set yet"))
	case "14d":
		s20Send(t, ctx, s20Relay, s20Card("Create Relay", "Picked by castbot"))
	case "14e":
		s20Send(t, ctx, s20Scout, s20Card("Create Scout", "Small, 1 vCPU / 2 GiB"))
		s20Click(t, ctx, `document.querySelector('.setup a.btn').click()`, `(document.body.innerText||'').indexOf('New agent, all fields')>=0 && (document.body.innerText||'').indexOf('From your chat')>=0 && (document.body.innerText||'').indexOf('Create and start')>=0 && (document.body.innerText||'').indexOf('Back to the chat')>=0`)
	case "14f":
		s20ArriveStarting(t, ctx, s20Scout, true)
	case "14f2":
		s20ArriveStarting(t, ctx, s20Scout, false)
		var loc string
		if err := chromedp.Run(ctx, chromedp.Location(&loc)); err != nil {
			t.Fatal(err)
		}
		if i := strings.Index(loc, "?"); i >= 0 {
			loc = loc[:i]
		}
		if err := chromedp.Run(ctx, chromedp.Navigate(loc+"?panel=machine"), pageReady(t)); err != nil {
			t.Fatal(err)
		}
		s20Wait(t, ctx, `!!document.querySelector('#agent-panel') && (document.body.innerText||'').indexOf('Machine')>=0 && (document.body.innerText||'').indexOf('Size')>=0 && (document.body.innerText||'').indexOf('Auto-pause')>=0`)
	case "14g":
		s20ArriveStarting(t, ctx, s20Scout, true)
		gate.release()
		s20Wait(t, ctx, `(document.body.innerText||'').indexOf('Nice to meet you')>=0 && (document.body.innerText||'').indexOf('My job:')>=0 && (document.body.innerText||'').indexOf('docs/guides')>=0`)
	case "14h":
		s20Send(t, ctx, s20Juno, s20Card("Create Juno", "Not set yet"))
		s20Click(t, ctx, `document.querySelector('.cacts button.btn.pri').click()`, `(document.body.innerText||'').indexOf('What should my job be')>=0 && (document.body.innerText||'').indexOf('Watch a repo for new issues')>=0 && ((document.querySelector('#composer textarea')||{}).placeholder||'').indexOf('Tell Juno what its job is')>=0`)
	case "14i":
		s20Send(t, ctx, "hi", `(document.body.innerText||'').indexOf('Happy to set one up')>=0 && (document.body.innerText||'').indexOf('What should your agent do')>=0 && !document.querySelector('.setup')`)
	case "14j":
		text := "Make me an agent called Scout that checks example-org/docs for broken links. Token " + s20Token()
		s20Send(t, ctx, text, `(document.body.innerText||'').indexOf('I left the token out')>=0 && (document.body.innerText||'').indexOf('********')>=0 && (document.body.innerText||'').indexOf('Create Scout')>=0`)
	case "14k":
		s20Send(t, ctx, s20Scout, `(document.body.innerText||'').indexOf('Add a model key first')>=0 && (document.body.innerText||'').indexOf('Add a key in Secrets')>=0 && document.querySelector('.cacts button.btn.pri').disabled`)
	case "14l":
		s20Send(t, ctx, "Make me an agent called Atlas that checks example-org/docs for broken links.", `(document.body.innerText||'').indexOf('already have an agent named Atlas')>=0 && (document.body.innerText||'').indexOf('Atlas Spanish')>=0 && document.querySelector('.cacts button.btn.pri').disabled && !!document.querySelector('form[action="/new-agent/edit"] button')`)
	case "14m":
		s20Send(t, ctx, s20Scout, s20Card("Create Scout", "Small, 1 vCPU / 2 GiB"))
		s20Click(t, ctx, `document.querySelector('.cacts button.btn.pri').click()`, `(document.body.innerText||'').indexOf('did not start')>=0 && (document.body.innerText||'').indexOf('Try again')>=0 && (document.body.innerText||'').indexOf('Machine details')>=0`)
		s20Chat(t, ctx, "Please start with docs/guides.")
		s20Reload(t, ctx, `(document.body.innerText||'').indexOf('did not start')>=0 && (document.body.innerText||'').indexOf('Not sent yet')>=0 && (document.body.innerText||'').indexOf('Machine details')>=0`)
	default:
		t.Fatalf("kind %s", kind)
	}
	_ = fake
}

func s20After(t *testing.T, ctx context.Context, kind string, gate *s20Gate, fake *host.Fake) {
	t.Helper()
	switch kind {
	case "14a":
		s20Click(t, ctx, `document.querySelector('.sugs .pchip').click()`, `(function(){var ta=document.querySelector('#composer textarea');return !!ta && ta.value.indexOf('example-org/docs-bot')>=0 && !document.querySelector('.setup') && location.pathname==='/new-agent';})()`)
	case "14k", "14l":
		s20Click(t, ctx, `var b=document.querySelector('.cacts button.btn.pri'); if(b) b.click();`, `location.pathname==='/new-agent' && document.querySelector('.cacts button.btn.pri').disabled`)
	case "14m":
		fake.WaitCreate = nil
		gate.release()
		s20Click(t, ctx, `document.querySelector('.boot-f button').click()`, `(document.body.innerText||'').indexOf('is starting up')>=0 || (document.body.innerText||'').indexOf('is up')>=0`)
	}
}

func s20ArriveStarting(t *testing.T, ctx context.Context, text string, withWait bool) {
	t.Helper()
	s20Send(t, ctx, text, s20Card("Create Scout", modelLabel(defaultModelID())))
	s20Click(t, ctx, `document.querySelector('.cacts button.btn.pri').click()`, `(document.body.innerText||'').indexOf('is starting up')>=0 && (document.body.innerText||'').indexOf('You created Scout at')>=0`)
	if withWait {
		s20Chat(t, ctx, "Please start with docs/guides.")
		s20Reload(t, ctx, `(document.body.innerText||'').indexOf('is starting up')>=0 && (document.body.innerText||'').indexOf('Waiting')>=0 && (document.body.innerText||'').indexOf('docs/guides')>=0`)
	}
}

func s20Send(t *testing.T, ctx context.Context, text, needle string) {
	t.Helper()
	s20Click(t, ctx, s20Fill("#composer textarea", text)+`var btn=document.getElementById('send-btn');if(btn){btn.disabled=false;btn.click();}`, needle)
}

func s20Chat(t *testing.T, ctx context.Context, text string) {
	t.Helper()
	s20Click(t, ctx, s20Fill("#composer textarea", text)+`var btn=document.getElementById('send-btn');if(btn){btn.disabled=false;btn.click();}`, `(document.body.innerText||'').indexOf('docs/guides')>=0`)
}

func s20Fill(sel, text string) string {
	quoted, _ := json.Marshal(text)
	return `(function(){var ta=document.querySelector('` + sel + `');if(!ta)return;ta.value=` + string(quoted) + `;ta.dispatchEvent(new Event('input',{bubbles:true}));})();`
}

func s20Click(t *testing.T, ctx context.Context, script, needle string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(script), nil)); err != nil {
		t.Fatal(err)
	}
	s20Wait(t, ctx, needle)
}

func s20Reload(t *testing.T, ctx context.Context, needle string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Reload(), pageReady(t)); err != nil {
		t.Fatal(err)
	}
	s20Wait(t, ctx, needle)
}

func s20Wait(t *testing.T, ctx context.Context, needle string) {
	t.Helper()
	var last string
	ok := withShotRetry(t, "s20 "+squash(needle), func() bool {
		deadline := time.Now().Add(shotWait())
		for time.Now().Before(deadline) {
			var good bool
			err := chromedp.Run(ctx, chromedp.Evaluate(js(`!!(`+needle+`)`), &good))
			if err == nil && good {
				return true
			}
			_ = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &last))
			time.Sleep(150 * time.Millisecond)
		}
		return false
	})
	if !ok {
		t.Fatalf("wait s20 timed out after %s\ncondition: %s\n%s", shotWait(), squash(needle), squash(last))
	}
}

func s20Card(a, b string) string {
	return `(document.body.innerText||'').indexOf('` + a + `')>=0 && (document.body.innerText||'').indexOf('` + b + `')>=0 && (document.body.innerText||'').indexOf('` + modelLabel(defaultModelID()) + `')>=0`
}

func s20Token() string {
	return strings.Join([]string{"sk", "demo", "EXAMPLE", "0000", "notlive"}, "-")
}

func s20CopyClean(t *testing.T, ctx context.Context, kind string) {
	t.Helper()
	var html, text, dashesOK string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.documentElement.outerHTML`), &html)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var root=document.body.cloneNode(true);var panel=root.querySelector('#agent-panel');if(panel)panel.remove();return root.innerText||'';})()`), &text)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "demo-model-key") || strings.Contains(html, "server-side-demo") || strings.Contains(html, "CASTBOT_") {
		t.Fatal("shot page contains a key")
	}
	if kind == "14j" && (strings.Contains(html, s20Token()) || strings.Contains(text, s20Token())) {
		t.Fatal("shot page contains the pasted token")
	}
	if strings.Contains(html, "Provider A") || strings.Contains(text, "Provider A") {
		t.Fatal("shot page still says Provider A")
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(document.body.innerText||'').indexOf('\u2014')<0 && (document.body.innerText||'').indexOf('\u2013')<0 ? 'ok' : 'dash'`), &dashesOK)); err != nil {
		t.Fatal(err)
	}
	if dashesOK != "ok" {
		t.Fatal("shot copy has a dash")
	}
}

func s20TapTargets(t *testing.T, ctx context.Context) {
	t.Helper()
	var bad string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		var bad = [];
		document.querySelectorAll('.nclose, .cacts .btn, .keymiss .btn, .boot-f .btn, .qreplies .pchip, .sugs .pchip, .nfoot .link, .nh .advlink').forEach(function(el){
			var r = el.getBoundingClientRect();
			if (r.width < 1 || r.height < 1) return;
			if (r.height < 44 || r.width < 44) bad.push((el.getAttribute('aria-label')||el.textContent||'').trim().slice(0,40)+' '+Math.round(r.width)+'x'+Math.round(r.height));
		});
		return bad.join(' | ');
	})()`), &bad)); err != nil {
		t.Fatal(err)
	}
	if bad != "" {
		t.Fatalf("tap target %s", bad)
	}
}

const (
	s20Scout = "Make me an agent called Scout that checks example-org/docs for broken links every weekday morning."
	s20Juno  = "Call it Juno. I will tell it what to do later."
	s20Relay = "Write release notes from merged PRs in example-org/app every Friday."
)
