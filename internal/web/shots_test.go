//go:build shots

package web

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
	"net/http/httptest"
)

func TestShots(t *testing.T) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath("/usr/bin/google-chrome"),
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
	dir := filepath.Join("..", "..", "docs", "evidence", "S02")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run("stream-"+vp.name, func(t *testing.T) {
			shootStream(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
		t.Run("error-"+vp.name, func(t *testing.T) {
			shootError(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
	}
}

func shootStream(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas", WordDelay: 1100 * time.Millisecond})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	user, _ := h.account("shot@example.com", "Shot User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		chromedp.WaitVisible(`textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "hello", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var s=document.querySelector(".stream");return !!(s&&s.textContent.indexOf("Hello")>=0&&s.textContent.indexOf("today?")<0);})()`), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "streaming-"+name+".png"))
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		chromedp.Poll(js(`(function(){var s=document.querySelector(".stream");var t=document.body.innerText||"";return !!((s&&s.textContent.indexOf("Hello")>=0)||t.indexOf("Hello! I'm Atlas")>=0);})()`), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "reload-"+name+".png"))
	if err := chromedp.Run(ctx,
		chromedp.Poll(js(`(function(){var t=document.querySelector("#msgs").innerText;var n=t.split("How can I help you today?").length-1;return n===1&&t.indexOf("Hello! I'm Atlas.")>=0;})()`), nil, chromedp.WithPollingTimeout(20*time.Second)),
	); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, ctx)
}

func shootError(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	h := newHarnessWith(t, runtime.NewHTTP("http://"+addr))
	user, _ := h.account("err@example.com", "Err User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		chromedp.WaitVisible(`textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "hello", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var t=document.body.innerText||"";return t.indexOf("The agent is not reachable right now.")>=0&&t.indexOf("Retry")>=0;})()`), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "error-"+name+".png"))
	assertNoSideScroll(t, ctx)
}

func tab(t *testing.T, browser context.Context) context.Context {
	t.Helper()
	ctx, cancel := chromedp.NewContext(browser, chromedp.WithLogf(quietLog), chromedp.WithErrorf(quietLog))
	t.Cleanup(cancel)
	ctx, timeout := context.WithTimeout(ctx, 60*time.Second)
	t.Cleanup(timeout)
	return ctx
}

func sessionToken(t *testing.T, h *harness, userID string) string {
	t.Helper()
	sess, err := h.st.CreateSession(context.Background(), userID, "Mozilla/5.0 Chrome/120.0", true)
	if err != nil {
		t.Fatal(err)
	}
	return seal("session", sess.ID, h.srv.key)
}

func seedAgent(t *testing.T, st *store.Store, userID, name string) store.Agent {
	t.Helper()
	sec, err := st.CreateSection(context.Background(), userID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(context.Background(), userID, store.AgentInput{
		SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func saveShot(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		t.Fatal(err)
	}
	if len(buf) < 1000 {
		t.Fatalf("screenshot %s is %d bytes", path, len(buf))
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertNoSideScroll(t *testing.T, ctx context.Context) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.documentElement.scrollWidth<=document.documentElement.clientWidth&&document.body.scrollWidth<=document.body.clientWidth`), &ok)); err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("sideways scroll")
	}
}

func js(s string) string { return s }

func quietLog(string, ...interface{}) {}
