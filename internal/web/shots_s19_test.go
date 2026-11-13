//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func shootS19(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h, userID, page, needle := s19Shot(t, theme, name, kind)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, userID), ts.URL+page, width, height)...); err != nil {
		t.Fatal(err)
	}
	if kind == "13a2" {
		s19DraftAndClose(t, ctx)
	}
	if kind == "13f" {
		if err := chromedp.Run(ctx, shotPoll(t, "s19 composer", js(`!!document.querySelector('#composer')`))); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Run(ctx, chromedp.Navigate(ts.URL+"/settings#usage"), pageReady(t)); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var el=document.getElementById('usage');if(el)el.scrollIntoView({block:'center'});return true;})()`), nil)); err != nil {
			t.Fatal(err)
		}
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s19 content", js(needle))); err != nil {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &text))
		t.Fatalf("needle: %v\n%s", err, text)
	}
	if width < 760 {
		s19TapTargets(t, ctx)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
	if kind == "13f" {
		s19SettingsReturns(t, ctx)
	}
}

func s19DraftAndClose(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, shotPoll(t, "s19 draft kept", js(`(function(){
		var box=document.querySelector('#composer textarea');
		if(!box)return false;
		box.value='keep this draft';
		box.dispatchEvent(new Event('input',{bubbles:true}));
		var open=document.querySelector('#agent-panel.s19pane')&&!document.getElementById('agent-panel').hidden;
		if(!open){var tog=document.querySelector('[data-panel-toggle]');if(tog)tog.click();}
		return true;
	})()`))); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s19 panel open", js(`!!(document.querySelector('#agent-panel.s19pane')&&!document.getElementById('agent-panel').hidden)`))); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))`), nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s19 panel closed after escape", js(`(function(){var p=document.getElementById('agent-panel');return !p||p.hidden||!p.classList.contains('s19pane');})()`))); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Reload(), pageReady(t)); err != nil {
		t.Fatal(err)
	}
}

func s19SettingsReturns(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.querySelector('[data-sclose]').click()`), nil)); err != nil {
		t.Fatal(err)
	}
	var loc string
	ok := withShotRetry(t, "settings close returns to the agent", func() bool {
		deadline := time.Now().Add(shotWait())
		for time.Now().Before(deadline) {
			err := chromedp.Run(ctx, chromedp.Evaluate(js(`location.pathname`), &loc))
			if err == nil && len(loc) >= 8 && loc[:8] == "/agents/" {
				return true
			}
			if err != nil && !strings.Contains(err.Error(), "context") && !strings.Contains(err.Error(), "navigated") {
				t.Fatal(err)
			}
			time.Sleep(150 * time.Millisecond)
		}
		return false
	})
	if !ok {
		t.Fatalf("wait settings close timed out after %s; stayed on %s", shotWait(), loc)
	}
}

func s19TapTargets(t *testing.T, ctx context.Context) {
	t.Helper()
	var bad string
	err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		var nodes=document.querySelectorAll('.ptabs a,.pclose-ph,.apill,.ptoggle,.peek,.glance,[data-sclose],.s19toast .btn,.s19toast .ibtn,.inchat');
		var bad=[];
		for(var i=0;i<nodes.length;i++){
			var b=nodes[i].getBoundingClientRect();
			if(b.width<1||b.height<1)continue;
			if(b.width<44||b.height<44)bad.push((nodes[i].className||nodes[i].tagName)+' '+Math.round(b.width)+'x'+Math.round(b.height));
		}
		return bad.join(' | ');
	})()`), &bad))
	if err != nil {
		t.Fatal(err)
	}
	if bad != "" {
		t.Fatalf("tap targets under 44px: %s", bad)
	}
}

func s19Shot(t *testing.T, theme, name, kind string) (*harness, string, string, string) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("s19-"+kind+"-"+theme+"-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	roster := []struct{ Name, Title string }{
		{"Atlas", "Docs helper"},
		{"Pip", "Inbox"},
		{"Ledger", "Numbers"},
		{"Juniper", "Research"},
		{"Moss", "Notes"},
		{"Wren", "Triage"},
	}
	var atlas, pip store.Agent
	for _, row := range roster {
		agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
			SectionID: sec.ID, Name: row.Name, Title: row.Title, Status: "ready",
		})
		if err != nil {
			t.Fatal(err)
		}
		switch row.Name {
		case "Atlas":
			atlas = agent
		case "Pip":
			pip = agent
		}
	}
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, atlas.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, threads[0].ID, "user", user.ID, "Draft the weekly note."); err != nil {
		t.Fatal(err)
	}
	if kind == "13a" || kind == "13a2" || kind == "13c" || kind == "13g" || kind == "13h" {
		panelApproval(t, h, user.ID, atlas.ID, "git_push", "ls demo")
	}
	if kind == "13b" || kind == "13b2" {
		s19Machine(t, h, user.ID, atlas.ID)
	}
	focus := atlas
	if kind == "13g" {
		focus = pip
	}
	page := "/agents/" + focus.ID + "?panel=agent"
	needle := `(function(){var t=(document.body.innerText||'').toLowerCase();return !!document.querySelector('.ptabs')&&!document.querySelector('.tabs')&&t.indexOf('at a glance')>=0&&t.indexOf('draft the weekly note.')>=0&&t.indexOf('\u2014')<0&&t.indexOf('\u2013')<0;})()`
	switch kind {
	case "13a2":
		page = "/agents/" + atlas.ID + "?panel=closed"
		needle = `(function(){var box=document.querySelector('#composer textarea');var p=document.getElementById('agent-panel');var open=p&&p.classList.contains('s19pane')&&!p.hidden;var t=document.body.innerText||'';return box&&box.value==='keep this draft'&&!open&&!document.querySelector('.tabs')&&t.indexOf('\u2014')<0;})()`
	case "13b":
		page = "/agents/" + atlas.ID + "?panel=machine"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Update available')>=0&&t.indexOf('State from the cloud API. Checking it does not wake the machine.')>=0&&!document.querySelector('.tabs');})()`
	case "13b2":
		page = "/agents/" + atlas.ID + "/machine/replace"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Replace this machine?')>=0&&!!document.querySelector('.rmodal')&&!document.querySelector('.tabs');})()`
	case "13c":
		page = "/agents/" + atlas.ID + "?panel=approvals"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf("Only Atlas's approvals")>=0&&t.indexOf('Show in chat')>=0&&t.indexOf('ls demo')>=0&&t.indexOf('needs your approval')<0;})()`
	case "13d":
		page = "/agents/" + atlas.ID + "?panel=routines"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('No routines yet')>=0&&t.indexOf("Only Atlas's routines")>=0;})()`
	case "13e":
		page = "/agents/" + atlas.ID + "?panel=files"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('No files yet')>=0&&t.indexOf("Only Atlas's files")>=0;})()`
	case "13f":
		page = "/agents/" + atlas.ID
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Machine time is not metered in this version.')>=0&&t.indexOf('The estimate arrives with the machine panel update.')>=0&&t.indexOf('6 agents, each on its own machine')>=0&&t.indexOf('Billed by your provider')>=0&&t.indexOf('castbot never adds a charge on top.')>=0&&t.indexOf('$')<0;})()`
	case "13g":
		page = "/agents/" + pip.ID
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Atlas needs your approval')>=0&&t.indexOf('git_push')>=0&&t.indexOf('ls demo')<0&&document.title.indexOf('(1)')===0;})()`
	case "13h":
		page = "/"
		needle = `(function(){var t=document.body.innerText||'';var pick=document.querySelector('.pick');var box=pick?pick.getBoundingClientRect():{width:0};if(window.innerWidth<760){return t.indexOf('Atlas')>=0&&t.indexOf('Pip')>=0&&box.width<1;}return t.indexOf('Pick an agent')>=0&&t.indexOf('Agents that need you show a shield with a count.')>=0;})()`
	}
	return h, user.ID, page, needle
}

func s19Machine(t *testing.T, h *harness, userID, agentID string) {
	t.Helper()
	fake := host.NewFake()
	target := "example.invalid/castbot-runtime:ff6d7ea"
	current := "example.invalid/castbot-runtime:7b5e414"
	h.srv.SetHost(fake, target, "https://castbot.example")
	if _, err := h.st.BeginVM(context.Background(), userID, agentID, "castbot-s19", current); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), userID, agentID, "vm-s19", "https://s19.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-s19"] = host.Machine{
		ID: "vm-s19", Name: "castbot-s19", State: "running", Endpoint: "https://s19.example",
		Image: current, Tags: []string{"agent:" + agentID},
	}
}

func shootS19Secrets(t *testing.T, browser context.Context, width, height int) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	user, _ := h.account("s19-sec-"+strconv.Itoa(width)+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, "dark"); err != nil {
		t.Fatal(err)
	}
	ids := seedVaultCast(t, h.st, user.ID, true)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + ids.atlas + "?panel=secrets"
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), page, width, height)...); err != nil {
		t.Fatal(err)
	}
	waitText(t, ctx, "NOTES_TOKEN")
	var before string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:""`), &before)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(before, "Give Atlas a secret") || strings.Contains(before, "Take NOTES_TOKEN away from Atlas?") {
		t.Fatal("the drawer was open before the click")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`a[href*="give=1"]`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	waitText(t, ctx, "Give Atlas a secret")
	if err := chromedp.Run(ctx, chromedp.Navigate(page), pageReady(t)); err != nil {
		t.Fatal(err)
	}
	waitText(t, ctx, "NOTES_TOKEN")
	if err := chromedp.Run(ctx, chromedp.Click(`a[aria-label="Revoke NOTES_TOKEN from Atlas"]`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	waitText(t, ctx, "Take NOTES_TOKEN away from Atlas?")
	assertVaultShot(t, ctx, height)
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
}

func shootS19Back(t *testing.T, browser context.Context) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	user, _ := h.account("s19-back@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, "dark"); err != nil {
		t.Fatal(err)
	}
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID + "?panel=routines"
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), page, 1280, 800)...); err != nil {
		t.Fatal(err)
	}
	waitText(t, ctx, "No routines yet")
	if err := chromedp.Run(ctx, chromedp.Click(`a.btn.pri[href*="routine=new"]`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	waitText(t, ctx, "What should Atlas do?")
	var search string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`location.search`), &search)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(search, "routine=new") {
		t.Fatalf("editor url %s", search)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`history.back()`), nil)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s19 back to the routine list", js(`location.search.indexOf("panel=routines")>=0 && location.search.indexOf("routine=")<0 && (document.body.innerText||"").indexOf("What should Atlas do?")<0`))); err != nil {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`location.search+"\n"+(document.body?document.body.innerText:"")`), &now))
		t.Fatalf("back to the list: %v\n%s", err, now)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`history.back()`), nil)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s19 back to chat", js(`location.search.indexOf("panel=")<0 && (function(){var p=document.getElementById("agent-panel");return !p||p.hidden||!p.classList.contains("s19pane");})()`))); err != nil {
		var now string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`location.href`), &now))
		t.Fatalf("back to chat: %v %s", err, now)
	}
}

func shootPanelToggle(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("s19-toggle-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, "dark"); err != nil {
		t.Fatal(err)
	}
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, threads[0].ID, "user", user.ID, "Draft the weekly note."); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID + "?panel=closed"
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), page, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s19 panel closed", js(panelClosedJS))); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`[data-panel-toggle]`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s19 panel placed", js(panelPlacedJS+`==='ok'`))); err != nil {
		var why string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(panelPlacedJS), &why))
		t.Fatalf("toggle open: %v %s", err, why)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveShot(t, ctx, filepath.Join(dir, "13toggle-dark-"+name+".png"))
	if err := chromedp.Run(ctx, chromedp.Click(`[data-panel-toggle]`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s19 panel closed", js(panelClosedJS))); err != nil {
		var why string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`location.href+' '+(document.querySelector('.app')&&document.querySelector('.app').className)+' aria='+(document.querySelector('[data-panel-toggle]')&&document.querySelector('[data-panel-toggle]').getAttribute('aria-expanded'))`), &why))
		t.Fatalf("toggle close: %v %s", err, why)
	}
}

const panelClosedJS = `(function(){
	if(location.search.indexOf('panel=')>=0) return false;
	var app=document.querySelector('.app');
	var tog=document.querySelector('[data-panel-toggle]');
	var panel=document.getElementById('agent-panel');
	var open=panel&&!panel.hidden&&panel.classList.contains('s19pane');
	return !!(app&&tog&&!open&&!app.classList.contains('with-pane')&&tog.getAttribute('aria-expanded')==='false');
})()`

const panelPlacedJS = `(function(){
	var app=document.querySelector('.app');
	var main=document.querySelector('.main');
	var panel=document.getElementById('agent-panel');
	var tog=document.querySelector('[data-panel-toggle]');
	if(!app||!main||!panel||!tog) return 'missing';
	if(!app.classList.contains('with-pane')) return 'no with-pane class='+panel.className+' hidden='+panel.hidden;
	if(panel.hidden||!panel.classList.contains('s19pane')) return 'panel closed';
	if(tog.getAttribute('aria-expanded')!=='true') return 'aria '+tog.getAttribute('aria-expanded');
	var pb=panel.getBoundingClientRect();
	var mb=main.getBoundingClientRect();
	if(pb.left+1<mb.right) return 'left '+Math.round(pb.left)+' mainRight '+Math.round(mb.right);
	if(pb.top<-2||pb.top>24) return 'top '+Math.round(pb.top);
	if(pb.left<0||pb.right>window.innerWidth+1||pb.bottom>window.innerHeight+1) return 'outside '+Math.round(pb.left)+','+Math.round(pb.top)+' '+Math.round(pb.right)+'x'+Math.round(pb.bottom);
	return 'ok';
})()`
