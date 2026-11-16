//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

// TestHomeShots checks that home opens the most recently used chat, and that
// the phone Back control opens the agent list.
func TestHomeShots(t *testing.T) {
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
	dir := filepath.Join("..", "..", "docs", "evidence", "104")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run(vp.name, func(t *testing.T) {
			shootHome(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
	}
}

func shootHome(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("home-shot-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	sec, err := h.st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	atlas, thread, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
		CreatedAt: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Pip", Title: "Notes", Status: "ready",
		CreatedAt: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Draft the weekly note."); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "agent", atlas.ID, "The weekly note is ready."); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	tabCtx := tab(t, browser)
	chatReady := `(function(){var t=document.body.innerText||'';return location.pathname==='/agents/` + atlas.ID + `'&&t.indexOf('Pick an agent')<0&&t.indexOf('Draft the weekly note.')>=0&&!!document.querySelector('#composer');})()`
	listReady := `(function(){var t=document.body.innerText||'';var u=new URL(location.href);return u.pathname==='/'&&u.searchParams.get('list')==='1'&&t.indexOf('Atlas')>=0&&t.indexOf('Pip')>=0&&t.indexOf('Pick an agent')<0&&!!document.querySelector('a[aria-label="New agent"]')&&!!document.querySelector('a.brand[aria-label="castbot home"]');})()`
	if err := chromedp.Run(tabCtx, cliOpen(t, ts.URL, token, ts.URL+"/agents/"+atlas.ID, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(tabCtx, chromedp.Navigate(ts.URL+"/")); err != nil {
		t.Fatal(err)
	}
	waitShot(t, tabCtx, "home opens the used chat", chatReady)
	saveShot(t, tabCtx, filepath.Join(dir, "chat-"+name+".png"))
	assertNoSideScroll(t, tabCtx)
	if width <= 760 {
		if err := chromedp.Run(tabCtx, chromedp.Click(`a[aria-label="Back to agents"]`, chromedp.ByQuery)); err != nil && !containsNav(err) {
			t.Fatal(err)
		}
	} else {
		if err := chromedp.Run(tabCtx, chromedp.Navigate(ts.URL+"/?list=1")); err != nil {
			t.Fatal(err)
		}
	}
	waitShot(t, tabCtx, "agent list", listReady)
	saveShot(t, tabCtx, filepath.Join(dir, "list-"+name+".png"))
	assertNoSideScroll(t, tabCtx)
	if err := chromedp.Run(tabCtx, chromedp.Click(`a.brand[aria-label="castbot home"]`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	waitShot(t, tabCtx, "brand returns to the used chat", chatReady)
	assertNoSideScroll(t, tabCtx)
}

// waitShot polls until the page is ready. A redirect or a click can destroy
// the document context while the next page is still loading, so that error is retried.
func waitShot(t *testing.T, ctx context.Context, label, expr string) {
	t.Helper()
	var last error
	for i := 0; i < 8; i++ {
		last = chromedp.Run(ctx, pageReady(t), shotPoll(t, label, js(expr)))
		if last == nil {
			return
		}
		msg := last.Error()
		if !strings.Contains(msg, "Cannot find context") && !strings.Contains(msg, "Execution context was destroyed") {
			t.Fatal(last)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal(last)
}
