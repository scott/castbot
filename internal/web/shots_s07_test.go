//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func shootS07Start(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	fake := host.NewFake()
	hold := make(chan struct{})
	fake.WaitCreate = hold
	t.Cleanup(func() { close(hold) })
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s07", "https://castbot.example")
	user, _ := h.account("start-s07@example.com", "Start User", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := openS07(t, browser, h, user.ID, "/agents/"+agent.ID, width, height, `(function(){var t=document.body.innerText||"";var s="No machine yet. Start it to chat.";var n=0,i=0;while((i=t.indexOf(s,i))>=0){n++;i+=s.length;}return n===1&&t.indexOf("Start")>=0;})()`)
	shotPage(t, ctx, filepath.Join(dir, "s07-start-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
	if err := chromedp.Run(ctx, chromedp.Click(`.start-form button`, chromedp.ByQuery)); err != nil && !strings.Contains(err.Error(), "navigated or closed") {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	calls, _ := fake.Snapshot()
	for _, call := range calls {
		if call == "create" {
			t.Fatal("create finished while Start was held")
		}
	}
	deadline := time.Now().Add(8 * time.Second)
	var ok bool
	var last error
	for time.Now().Before(deadline) {
		last = chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){return (location.pathname.indexOf("/machine")>=0||location.search.indexOf("panel=machine")>=0)&&document.querySelectorAll(".app").length===1&&document.querySelectorAll("header.ch-h").length===1&&(document.body.innerText||"").indexOf("Starting")>=0;})()`), &ok))
		if last == nil && ok {
			break
		}
		if last != nil && !strings.Contains(last.Error(), "navigated or closed") && !strings.Contains(last.Error(), "context") {
			t.Fatal(last)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if last != nil || !ok {
		t.Fatalf("start page %v ok %v", last, ok)
	}
}

func shootS07Paused(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h, agent := s07ShotAgent(t, theme, "paused")
	ctx := openS07(t, browser, h, agent.UserID, "/agents/"+agent.ID+"/machine", width, height, `(function(){var t=document.body.innerText||"";return t.indexOf("Paused")>=0;})()`)
	shotPage(t, ctx, filepath.Join(dir, "s07-paused-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootS07Waking(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h, agent := s07ShotAgent(t, theme, "waking")
	ctx := openS07(t, browser, h, agent.UserID, "/agents/"+agent.ID, width, height, `(function(){return document.querySelector("#composer textarea")!=null;})()`)
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.SendKeys(`#composer textarea`, "hello", chromedp.ByQuery),
		chromedp.Click(`#send-btn`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var t=document.body.innerText||"";return t.indexOf("Waking")>=0;})()`), nil, chromedp.WithPollingTimeout(10*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	shotPage(t, ctx, filepath.Join(dir, "s07-waking-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootS07Reply(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", "fixture-s07-key") // gitleaks:allow
	h, agent := s07ShotAgent(t, theme, "reply")
	ctx := openS07(t, browser, h, agent.UserID, "/agents/"+agent.ID, width, height, `(function(){return document.querySelector("#composer textarea")!=null;})()`)
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.SendKeys(`#composer textarea`, "hello", chromedp.ByQuery),
		chromedp.Click(`#send-btn`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var t=document.body.innerText||"";return t.indexOf("Streamed from the fixture provider.")>=0;})()`), nil, chromedp.WithPollingTimeout(10*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	shotPage(t, ctx, filepath.Join(dir, "s07-reply-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootS07NoKey(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", "")
	h, agent := s07ShotAgent(t, theme, "nokey")
	ctx := openS07(t, browser, h, agent.UserID, "/agents/"+agent.ID, width, height, `(function(){return document.querySelector("#composer textarea")!=null;})()`)
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.SendKeys(`#composer textarea`, "hello", chromedp.ByQuery),
		chromedp.Click(`#send-btn`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var t=document.body.innerText||"";return t.indexOf("No key for provider digitalocean. Ask the owner to add it.")>=0;})()`), nil, chromedp.WithPollingTimeout(10*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	shotPage(t, ctx, filepath.Join(dir, "s07-nokey-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func s07ShotAgent(t *testing.T, theme, kind string) (*harness, store.Agent) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s07", "https://castbot.example")
	user, _ := h.account(kind+"-s07@example.com", "Shot User", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	state := "paused"
	if kind == "reply" || kind == "nokey" {
		state = "running"
		if err := h.st.SetAgentModel(context.Background(), user.ID, agent.ID, "digitalocean/deepseek-v3.2"); err != nil {
			t.Fatal(err)
		}
		fake.Frames = []runtime.Frame{{
			Data:      `{"jsonrpc":"2.0","result":{"parts":[{"kind":"text","text":"Streamed from the fixture provider."}]}}`,
			DataEvent: true,
		}}
	}
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-shot", "https://vm.example", state, ""); err != nil {
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
	fake.Machines["vm-shot"] = host.Machine{ID: "vm-shot", State: state, Endpoint: "https://vm.example"}
	if state == "paused" {
		fake.SetForce("paused")
		if kind == "waking" {
			wake := make(chan struct{})
			fake.WakeWait = wake
			t.Cleanup(func() { close(wake) })
		}
	}
	agent.UserID = user.ID
	return h, agent
}

func openS07(t *testing.T, browser context.Context, h *harness, userID, path string, width, height int, ready string) context.Context {
	t.Helper()
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, userID)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(ts.URL, token, ts.URL+path, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Poll(js(ready), nil, chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		t.Fatal(err)
	}
	return ctx
}
