//go:build shots

package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

// TestGrantShots checks Always approve on a pending card at both widths.
func TestGrantShots(t *testing.T) {
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
	dir := filepath.Join("..", "..", "docs", "evidence", "96")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run(vp.name, func(t *testing.T) {
			shootGrant(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
	}
}

func shootGrant(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newPermitHarness(t)
	h.srv.pauseSweeps()
	ctx := context.Background()
	user, _ := h.account("grant-shot-"+name+"@example.com", "Shot User", "correct-horse-1")
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
	raw, err := json.Marshal(map[string]any{
		"id": "req-shot", "tool": "bash",
		"arguments": map[string]string{"command": "git status"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.RecordPermission(ctx, user.ID, runRow.ID, raw, h.st.Now().Add(time.Hour), 1); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	if _, err := h.st.CreateConnectApproval(ctx, user.ID, agent.ID, thread.ID, runRow.ID, connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/demo/issues", hash, h.st.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	tabCtx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID + "?thread=" + thread.ID + "&panel=approvals"
	size := ""
	if width <= 760 {
		size = `&&(function(){var nodes=document.querySelectorAll("form.always button");var n=0;for(var i=0;i<nodes.length;i++){var r=nodes[i].getBoundingClientRect();if(r.width<1||r.height<1||r.bottom<=0||r.right<=0||r.top>=window.innerHeight||r.left>=window.innerWidth)continue;n++;if(r.width<44||r.height<44)return false;}return n>=1;})()`
	}
	pending := `(function(){var t=document.body.innerText||"";var n=document.querySelectorAll("form.always button").length;return t.indexOf("Always approve")>=0&&t.indexOf("Approve")>=0&&t.indexOf("Deny")>=0&&n>=2` + size + `;})()`
	if err := chromedp.Run(tabCtx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
		shotPoll(t, "always approve on the pending cards", js(pending)),
		chromedp.Evaluate(js(`(function(){var n=document.querySelector("form.always");if(n)n.scrollIntoView({block:"center"});return true;})()`), nil),
		chromedp.Sleep(400 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, tabCtx, filepath.Join(dir, "always-pending-"+name+".png"))
	assertNoSideScroll(t, tabCtx)
	if err := chromedp.Run(tabCtx, chromedp.Tasks{
		chromedp.Click(`form.always button`, chromedp.ByQuery),
		shotPoll(t, "approved for this prompt", js(`(function(){var t=document.body.textContent||"";return t.indexOf("Approved for this prompt")>=0&&t.indexOf("git status")>=0;})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, tabCtx, filepath.Join(dir, "always-done-"+name+".png"))
	assertNoSideScroll(t, tabCtx)
}
