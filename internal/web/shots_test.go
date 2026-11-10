//go:build shots

package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestShots(t *testing.T) {
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
		t.Run("502-"+vp.name, func(t *testing.T) {
			shoot502(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
		t.Run("restart-"+vp.name, func(t *testing.T) {
			shootRestart(t, browserCtx, dir, vp.w, vp.h, vp.name)
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
	h.srv.SetRetryBudget(400 * time.Millisecond)
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

func shoot502(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas", WordDelay: 700 * time.Millisecond})
	t.Cleanup(echo.Close)
	gate := &browserGate{}
	h := newHarnessWith(t, runtime.NewHTTP(mustEchoURL(t, echo)))
	user, _ := h.account("gate@example.com", "Gate User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	gate.next = h.srv.Handler()
	ts := httptest.NewTLSServer(gate)
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := longTab(t, browser, 90*time.Second)
	page := ts.URL + "/agents/" + agent.ID
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		chromedp.WaitVisible(`textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "please keep going now", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var s=document.querySelector(".stream");return !!(s&&s.textContent.indexOf("You said:")>=0);})()`), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	var armed bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		window.__dup = 0;
		var root = document.querySelector("#msgs");
		var look = function(){
			var t = (root && root.innerText) || "";
			var n = t.split("You said:").length - 1;
			if (n > 1) window.__dup = n;
		};
		new MutationObserver(look).observe(root, {childList:true, subtree:true, characterData:true});
		look();
		return true;
	})()`), &armed)); err != nil {
		t.Fatal(err)
	}
	gate.outage(12 * time.Second)
	time.Sleep(1 * time.Second)
	var during int
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(document.body.innerText.split("You said:").length-1)`), &during)); err != nil {
		t.Fatal(err)
	}
	if during != 1 {
		t.Fatalf("text repeated during the 502 window: %d", during)
	}
	saveShot(t, ctx, filepath.Join(dir, "502-"+name+".png"))
	if err := chromedp.Run(ctx,
		chromedp.Poll(js(`(function(){var t=document.querySelector("#msgs").innerText;return t.indexOf("OPENROUTER_API_KEY")>=0&&!document.querySelector(".pending");})()`), nil, chromedp.WithPollingTimeout(45*time.Second)),
	); err != nil {
		t.Fatal(err)
	}
	var dup, count int
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Evaluate(js(`window.__dup||0`), &dup),
		chromedp.Evaluate(js(`(document.querySelector("#msgs").innerText.split("You said:").length-1)`), &count),
	}); err != nil {
		t.Fatal(err)
	}
	if dup != 0 || count != 1 {
		t.Fatalf("duplicated text dup %d count %d", dup, count)
	}
	assertNoSideScroll(t, ctx)
}

func shootRestart(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas", WordDelay: 300 * time.Millisecond})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	user, _ := h.account("rest@example.com", "Rest User", "correct-horse-1")
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
		chromedp.Poll(js(`(function(){var s=document.querySelector(".stream");return !!(s&&s.textContent.indexOf("Hello")>=0);})()`), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	h.srv.Close()
	if err := h.st.FailAbandoned(context.Background(), run.Interrupted); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		chromedp.Poll(js(`(function(){var t=document.body.innerText||"";return t.indexOf("The reply was interrupted.")>=0&&t.indexOf("Retry")>=0;})()`), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "restart-"+name+".png"))
	assertNoSideScroll(t, ctx)
}

func mustEchoURL(t *testing.T, echo *runtime.Echo) string {
	t.Helper()
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	return up.URL
}

type browserGate struct {
	next     http.Handler
	mu       sync.Mutex
	cancel   context.CancelFunc
	badUntil time.Time
}

func (g *browserGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/events") {
		g.next.ServeHTTP(w, r)
		return
	}
	g.mu.Lock()
	until := g.badUntil
	g.mu.Unlock()
	if time.Now().Before(until) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	g.mu.Lock()
	if time.Now().Before(g.badUntil) {
		g.mu.Unlock()
		cancel()
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if g.cancel != nil {
		g.cancel()
	}
	g.cancel = cancel
	g.mu.Unlock()
	defer cancel()
	g.next.ServeHTTP(w, r.WithContext(ctx))
}

func (g *browserGate) outage(d time.Duration) {
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
	}
	g.badUntil = time.Now().Add(d)
	g.mu.Unlock()
}

func longTab(t *testing.T, browser context.Context, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := chromedp.NewContext(browser)
	t.Cleanup(cancel)
	ctx, timeout := context.WithTimeout(ctx, d)
	t.Cleanup(timeout)
	return ctx
}

func tab(t *testing.T, browser context.Context) context.Context {
	t.Helper()
	ctx, cancel := chromedp.NewContext(browser)
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
