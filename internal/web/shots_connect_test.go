//go:build shots

package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestConnectionShots(t *testing.T) {
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
	dir := filepath.Join("..", "..", "docs", "evidence", "connections")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ready := `(function(){var t=document.body.innerText||"";return t.indexOf("Search key")>=0&&t.indexOf("web_search")>=0&&t.indexOf("web_read")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run(vp.name, func(t *testing.T) {
			h := newHarness(t)
			user, _ := h.account("web-shot@example.com", "Ada", "correct-horse-1")
			ctx := openS07(t, browserCtx, h, user.ID, "/settings/connections/web", vp.w, vp.h, ready)
			assertShotClean(t, ctx)
			assertNoSideScroll(t, ctx)
			saveShot(t, ctx, filepath.Join(dir, "web-"+vp.name+".png"))
		})
	}
}
