//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
)

func TestShotsS12(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	dir := filepath.Join("..", "..", "docs", "evidence", "S12")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			theme, vp := theme, vp
			for _, kind := range []string{"panel", "confirm"} {
				kind := kind
				t.Run(kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootS12(t, browser, dir, vp.w, vp.h, theme, vp.name, kind)
				})
			}
		}
	}
}

func shootS12(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	fake := host.NewFake()
	const current = "example.invalid/castbot-runtime:ff6d7ea"
	const previous = "example.invalid/castbot-runtime:7b5e414"
	h.srv.SetHost(fake, current, "https://castbot.example")
	user, _ := h.account("s12-"+kind+"-"+theme+"-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), current); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-s12", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	if err := h.st.NoteRuntimeRollback(context.Background(), user.ID, agent.ID, "s12", previous); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-s12"] = host.Machine{ID: "vm-s12", Name: host.MachineName(agent.ID), State: "running", Endpoint: "https://vm.example", Image: current, Tags: []string{"agent:" + agent.ID}}
	fake.LogTail = "machine ready\n"
	fake.SeedDisk("https://vm.example", nil, map[string][]byte{"notes.md": []byte("hello")})

	page := "/agents/" + agent.ID + "?panel=machine"
	needle := `(function(){var t=document.body.innerText||'';return t.indexOf('Roll back runtime')>=0&&t.indexOf('Previous runtime 7b5e414 can be restored for 7 days.')>=0&&t.indexOf('Memory and workspace files are copied again.')>=0&&t.indexOf('\u2014')<0&&t.indexOf('example.invalid')<0;})()`
	scroll := `#rollback-open`
	if kind == "confirm" {
		page = "/agents/" + agent.ID + "/machine/rollback?opener=rollback-open"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Roll back this runtime?')>=0&&t.indexOf('previous runtime (7b5e414)')>=0&&t.indexOf('Safe if something goes wrong.')>=0&&t.indexOf("copies this agent's memory and workspace files")>=0&&t.indexOf('\u2014')<0&&document.body.className.indexOf('p-confirm')>=0;})()`
		scroll = `#rollback-title`
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+page, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s12 "+kind, js(needle))); err != nil {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &text))
		t.Fatalf("needle: %v\n%s", err, text)
	}
	if err := chromedp.Run(ctx, chromedp.ScrollIntoView(scroll, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
}
