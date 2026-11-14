//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
)

func TestShotsS11(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	dir := filepath.Join("..", "..", "docs", "evidence", "S11")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			theme, vp := theme, vp
			for _, kind := range []string{"running", "paused", "usage"} {
				kind := kind
				t.Run(kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootS11(t, browser, dir, vp.w, vp.h, theme, vp.name, kind)
				})
			}
		}
	}
}

func shotBrowser(t *testing.T) (context.Context, func()) {
	t.Helper()
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
	browserCtx, browserCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(quietLog), chromedp.WithErrorf(quietLog))
	if err := chromedp.Run(browserCtx); err != nil {
		allocCancel()
		t.Fatal(err)
	}
	return browserCtx, func() {
		browserCancel()
		allocCancel()
	}
}

func shootS11(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s11", "https://castbot.example")
	when := time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return when })
	user, _ := h.account("s11-"+kind+"-"+theme+"-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	readyMachine(t, h, fake, agent, "vm-s11", "running")
	page := "/agents/" + agent.ID + "?panel=machine"
	needle := `(function(){var t=document.body.innerText||'';return t.indexOf('This month')>=0&&t.indexOf('$0.04')>=0&&t.indexOf('machine ready')>=0&&t.indexOf('notes.md')>=0&&t.indexOf('An estimate, not a bill.')>=0&&t.indexOf('\u2014')<0;})()`
	switch kind {
	case "running":
		h.st.SetNow(func() time.Time { return when.Add(time.Hour) })
		fake.LogTail = "machine ready\n"
		fake.SeedDisk("https://vm.example", nil, map[string][]byte{"notes.md": []byte("hello")})
	case "paused":
		h.st.SetNow(func() time.Time { return when.Add(time.Hour) })
		fake.LogTail = "machine ready\n"
		fake.SetMachineState("vm-s11", "paused")
		if err := h.st.ApplyVMState(context.Background(), user.ID, agent.ID, "paused", "", ""); err != nil {
			t.Fatal(err)
		}
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('The machine is paused.')>=0&&t.indexOf('This check does not wake it.')>=0&&t.indexOf('Opening this panel does not wake')>=0&&t.indexOf('Wake')>=0&&t.indexOf('\u2014')<0;})()`
	case "usage":
		h.st.SetNow(func() time.Time { return when.Add(10 * time.Hour) })
		page = "/settings#usage"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('This month')>=0&&t.indexOf('$0.39')>=0&&t.indexOf('An estimate, not a bill.')>=0&&t.indexOf('Atlas')>=0&&t.indexOf('Billed by your provider')>=0&&t.indexOf('\u2014')<0;})()`
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+page, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s11 "+kind, js(needle))); err != nil {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &text))
		t.Fatalf("needle: %v\n%s", err, text)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
}
