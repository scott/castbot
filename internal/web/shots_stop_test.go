//go:build shots

package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

// TestStopShots checks the Stop control on a live reply at both widths.
func TestStopShots(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	chrome := "/usr/bin/google-chrome"
	if p := os.Getenv("CASTBOT_CHROME"); p != "" {
		chrome = p
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("ignore-certificate-errors", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(allocCancel)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(quietLog), chromedp.WithErrorf(quietLog))
	t.Cleanup(browserCancel)
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "docs", "evidence", "97")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run(vp.name, func(t *testing.T) {
			shootStop(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
	}
}

func shootStop(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	block := make(chan struct{})
	h := newHarnessWith(t, &scriptClient{block: block})
	h.srv.pauseSweeps()
	t.Cleanup(func() { close(block) })
	ctx := context.Background()
	user, _ := h.account("stop-shot-"+name+"@example.com", "Shot User", "correct-horse-1")
	sec, err := h.st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Draft the notes")
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, msg.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	h.srv.runs.Start(runJob(user.ID, thread.ID, agent.ID, runRow.ID, msg.ID, msg.Body, "", nil))
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := h.st.GetRun(ctx, user.ID, runRow.ID)
		if err == nil && got.Status == "streaming" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not start: %s", got.Status)
		}
		time.Sleep(15 * time.Millisecond)
	}
	payload, err := json.Marshal(map[string]string{"text": "Half a reply"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendRunEvent(ctx, user.ID, runRow.ID, "text", string(payload), 1); err != nil {
		t.Fatal(err)
	}
	fake := pauseMachine(t, h, user.ID, agent.ID, "running")
	fake.CancelOK = true
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	tabCtx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID + "?thread=" + thread.ID
	size := `&&(function(){var b=document.querySelector(".act form.stop button");if(!b)return false;var r=b.getBoundingClientRect();return r.height>0&&r.height<=28&&r.width<80&&(b.textContent||"").trim()==="Stop";})()`
	working := `(function(){var t=document.body.innerText||"";var form=document.querySelector(".act form.stop");var comp=document.querySelector("#composer");return t.indexOf("Working")>=0&&t.indexOf("Half a reply")>=0&&!!form&&!!comp&&!comp.contains(form)` + size + `;})()`
	if err := chromedp.Run(tabCtx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
		shotPoll(t, "stop on the working row", js(working)),
		chromedp.Evaluate(js(`(function(){var n=document.querySelector(".act form.stop");if(n)n.scrollIntoView({block:"center"});return true;})()`), nil),
		chromedp.Sleep(400 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, tabCtx, filepath.Join(dir, "stop-working-"+name+".png"))
	assertNoSideScroll(t, tabCtx)
	if err := chromedp.Run(tabCtx, chromedp.Tasks{
		chromedp.Click(`.act form.stop button`, chromedp.ByQuery),
		shotPoll(t, "stopped reply", js(`(function(){var t=document.body.innerText||"";return t.indexOf("You stopped the reply.")>=0&&t.indexOf("Half a reply")>=0&&!document.querySelector(".act form.stop");})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil || got.Status != "stopped" {
		t.Fatalf("status %s %v", got.Status, err)
	}
	saveShot(t, tabCtx, filepath.Join(dir, "stop-done-"+name+".png"))
	assertNoSideScroll(t, tabCtx)
}
