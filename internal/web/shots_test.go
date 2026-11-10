//go:build shots

package web

import (
	"archive/zip"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/host"
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
	dir03 := filepath.Join("..", "..", "docs", "evidence", "S03")
	if err := os.MkdirAll(dir03, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run("container-"+vp.name, func(t *testing.T) {
			shootContainer(t, browserCtx, dir03, vp.w, vp.h, vp.name)
		})
	}
	dir04 := filepath.Join("..", "..", "docs", "evidence", "S04")
	if err := os.MkdirAll(dir04, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run("machine-"+vp.name, func(t *testing.T) {
			shootMachine(t, browserCtx, dir04, vp.w, vp.h, vp.name)
		})
	}
	dir05 := filepath.Join("..", "..", "docs", "evidence", "S05")
	if err := os.MkdirAll(dir05, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			t.Run("agentform-"+theme+"-"+vp.name, func(t *testing.T) {
				shootAgentForm(t, browserCtx, dir05, vp.w, vp.h, theme, vp.name)
			})
		}
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
		chromedp.Poll(retryInside(), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "error-"+name+".png"))
	assertNoSideScroll(t, ctx)
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		chromedp.Poll(retryInside(), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpdateTheme(context.Background(), user.ID, "light"); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		chromedp.Poll(js(`(function(){return document.documentElement.classList.contains("light")&&`+retryInsideExpr()+`;})()`), nil, chromedp.WithPollingTimeout(15*time.Second)),
		chromedp.SendKeys(`textarea[name="body"]`, "hello again", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var n=document.querySelectorAll(".errbox button").length;return n>=2&&`+retryInsideExpr()+`;})()`), nil, chromedp.WithPollingTimeout(15*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
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

func shootContainer(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newHarnessWith(t, runtime.NewHTTP("http://127.0.0.1:1"))
	user, _ := h.account("box@example.com", "Box User", "correct-horse-1")
	sec, err := h.st.CreateSection(context.Background(), user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Atlas\n", "Reply briefly.\n"); err != nil {
		t.Fatal(err)
	}
	secRow, err := h.st.PutSecret(context.Background(), user.ID, "FIXTURE_TOKEN", "fixture-value-s03")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, secRow.ID); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	guest, token := startGuest(t, h, agent.ID, bootstrap, "800ms")
	client := runtime.NewHTTP(guest.URL())
	client.SetToken(token)
	h.srv.SetClient(client)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	page := ts.URL + "/agents/" + agent.ID
	session := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", session).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		chromedp.WaitVisible(`textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "hello", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var s=document.querySelector(".stream");return !!(s&&s.textContent.indexOf("Hello")>=0&&s.textContent.indexOf("today?")<0);})()`), nil, chromedp.WithPollingTimeout(20*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "streaming-"+name+".png"))
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

func shootAgentForm(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s05", "https://castbot.example")
	user, secret := h.account("s05-"+theme+"-"+name+"@example.com", "Shot User", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	models, err := castcfg.Models()
	if err != nil || len(models) == 0 {
		t.Fatal(err)
	}
	model := models[0].ID
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := longTab(t, browser, 3*time.Minute)
	pageURL := ts.URL + "/new-agent"
	steps := []chromedp.Action{
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := cdppage.AddScriptToEvaluateOnNewDocument(`window.__errs=[];window.addEventListener('securitypolicyviolation',function(e){window.__errs.push(String(e.violatedDirective||'csp'))});window.addEventListener('error',function(e){window.__errs.push(String(e.message||'error'))});`).Do(ctx)
			return err
		}),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(pageURL),
		chromedp.WaitVisible(`select[name="model"]`, chromedp.ByQuery),
	}
	for i, step := range steps {
		if err := chromedp.Run(ctx, step); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "empty-"+theme+"-"+name+".png"))
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.SetValue(`form.na input[name="name"]`, "Atlas", chromedp.ByQuery),
		chromedp.SetValue(`form.na input[name="title"]`, "Helper", chromedp.ByQuery),
		chromedp.SetValue(`form.na textarea[name="instructions"]`, "Be concise.", chromedp.ByQuery),
		chromedp.SetValue(`form.na select[name="model"]`, model, chromedp.ByQuery),
		chromedp.Click(`form.na input[value="bash"]`, chromedp.ByQuery),
	}); err != nil {
		t.Fatal(err)
	}
	var filledName string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.querySelector('form.na input[name="name"]').value`), &filledName)); err != nil {
		t.Fatal(err)
	}
	if filledName != "Atlas" {
		t.Fatalf("name field %q", filledName)
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "filled-"+theme+"-"+name+".png"))
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.SetValue(`form.na input[name="name"]`, "", chromedp.ByQuery),
		chromedp.SetValue(`form.na select[name="model"]`, "", chromedp.ByQuery),
		chromedp.Click(`form.na button.pri`, chromedp.ByQuery),
		chromedp.WaitVisible(`.err`, chromedp.ByQuery),
	}); err != nil {
		t.Fatal(err)
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "validation-"+theme+"-"+name+".png"))
	zipPath := filepath.Join(t.TempDir(), "agent.zip")
	writeShotZip(t, zipPath, model)
	if err := chromedp.Run(ctx, chromedp.SetUploadFiles(`input[name="archive"]`, []string{zipPath}, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`button[formaction]`, chromedp.ByQuery)); err != nil && !strings.Contains(err.Error(), "navigated or closed") {
		t.Fatal(err)
	}
	var imported string
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.WaitVisible(`ul.skip`, chromedp.ByQuery),
		chromedp.Evaluate(js(`document.body.innerText`), &imported),
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(imported, "Skipped files") || !strings.Contains(imported, ".env") || strings.Contains(imported, "super-secret-value") {
		t.Fatalf("import review %s", imported)
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "import-"+theme+"-"+name+".png"))

	ip := "203.0.113.190:9"
	h.login("s05-"+theme+"-"+name+"@example.com", "correct-horse-1", secret, ip, false)
	agent := seedAgent(t, h.st, user.ID, "Quill")
	if err := h.st.SaveAgentFields(context.Background(), user.ID, agent.ID, store.AgentInput{
		SectionID: agent.SectionID, Name: agent.Name, Title: agent.Title, Model: model,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Quill\nbrain:\n  model: "+model+"\n  instructions_file: instructions.md\ntools:\n  builtin: []\n", "Be concise."); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, guest); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Navigate(ts.URL + "/agents/" + agent.ID + "/edit"),
		chromedp.WaitVisible(`input[name="title"]`, chromedp.ByQuery),
		chromedp.SetValue(`form.na input[name="title"]`, "Reloaded helper", chromedp.ByQuery),
	}); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`form.na button.pri`, chromedp.ByQuery)); err != nil && !strings.Contains(err.Error(), "navigated or closed") {
		t.Fatal(err)
	}
	var saved string
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.WaitVisible(`.banner.ok`, chromedp.ByQuery),
		chromedp.Evaluate(js(`document.body.innerText`), &saved),
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(saved, "No new machine was created.") {
		t.Fatalf("reload notice %s", saved)
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "edit-"+theme+"-"+name+".png"))
	if len(fake.Creates) != 1 {
		t.Fatalf("creates %d", len(fake.Creates))
	}
	var errs []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`window.__errs||[]`), &errs)); err != nil {
		t.Fatal(err)
	}
	for _, note := range errs {
		if note != "" {
			t.Fatalf("console %s", note)
		}
	}
}

func scrollTop(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var m=document.querySelector('.main');if(m)m.scrollTop=0;window.scrollTo(0,0);})()`), nil)); err != nil {
		t.Fatal(err)
	}
}

func shotPage(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	assertNoSideScroll(t, ctx)
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.body.innerText`), &text)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "\u2014") || strings.Contains(text, "\u2013") {
		t.Fatal("em dash")
	}
	saveShot(t, ctx, path)
}

func writeShotZip(t *testing.T, path, model string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	files := map[string]string{
		"agent.yaml":      "name: Imported\ndescription: From a folder\nbrain:\n  model: " + model + "\n  instructions_file: instructions.md\ntools:\n  builtin: [read]\n",
		"instructions.md": "Imported instructions.\n",
		".env":            "API_KEY=super-secret-value\n",
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
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

func shootMachine(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s04", "https://castbot.example")
	user, _ := h.account("machine@example.com", "Machine User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Wren")
	vm, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s04")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-shot", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-shot"] = host.Machine{ID: "vm-shot", Name: vm.Name, State: "paused"}
	fake.SetForce("paused")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID + "/machine"
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		chromedp.WaitVisible(`#confirm-name`, chromedp.ByQuery),
		chromedp.Poll(js(`(function(){var t=document.body.innerText||"";return t.indexOf("Paused")>=0&&t.indexOf("Wake")>=0&&t.indexOf("Type Wren")>=0;})()`), nil, chromedp.WithPollingTimeout(10*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "paused-"+name+".png"))
	assertNoSideScroll(t, ctx)
	fake.SetForce("creating")
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		chromedp.Poll(js(`(function(){var t=document.body.innerText||"";return t.indexOf("Waking")>=0&&t.indexOf("The machine is waking.")>=0;})()`), nil, chromedp.WithPollingTimeout(10*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "waking-"+name+".png"))
	assertNoSideScroll(t, ctx)
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

func retryInsideExpr() string {
	return `(function(){var box=document.querySelector(".ch-body");var btns=box&&box.querySelectorAll(".errbox button");var btn=btns&&btns.length?btns[btns.length-1]:null;if(!box||!btn)return false;var a=box.getBoundingClientRect();var b=btn.getBoundingClientRect();return b.top>=a.top-1&&b.bottom<=a.bottom+1&&b.left>=a.left-1&&b.right<=a.right+1;})()`
}

func retryInside() string {
	return js(`(function(){var t=document.body.innerText||"";return t.indexOf("The agent is not reachable right now.")>=0&&t.indexOf("Retry")>=0&&` + retryInsideExpr() + `;})()`)
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
