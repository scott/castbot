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
	cards := []struct {
		id, ready string
	}{
		{"web", `(function(){var t=document.body.innerText||"";return t.indexOf("Search key")>=0&&t.indexOf("web_search")>=0&&t.indexOf("web_read")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"linear", `(function(){var t=document.body.innerText||"";return t.indexOf("API key")>=0&&t.indexOf("linear_list_issues")>=0&&t.indexOf("linear_create_issue")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"google", `(function(){var t=document.body.innerText||"";return t.indexOf("App password")>=0&&t.indexOf("Gmail address")>=0&&t.indexOf("google_list_messages")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"slack", `(function(){var t=document.body.innerText||"";return t.indexOf("Bot token")>=0&&t.indexOf("slack_list_channels")>=0&&t.indexOf("slack_post")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"notion", `(function(){var t=document.body.innerText||"";return t.indexOf("Integration token")>=0&&t.indexOf("notion_search")>=0&&t.indexOf("notion_append")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"telegram", `(function(){var t=document.body.innerText||"";return t.indexOf("Bot token")>=0&&t.indexOf("Chat id")>=0&&t.indexOf("telegram_read")>=0&&t.indexOf("telegram_send")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"sentry", `(function(){var t=document.body.innerText||"";return t.indexOf("Auth token")>=0&&t.indexOf("sentry_list_orgs")>=0&&t.indexOf("sentry_resolve")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"cloudflare", `(function(){var t=document.body.innerText||"";return t.indexOf("API token")>=0&&t.indexOf("cloudflare_list_zones")>=0&&t.indexOf("cloudflare_purge_cache")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"todoist", `(function(){var t=document.body.innerText||"";return t.indexOf("API token")>=0&&t.indexOf("todoist_list_tasks")>=0&&t.indexOf("todoist_complete")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
		{"stripe", `(function(){var t=document.body.innerText||"";return t.indexOf("Restricted key")>=0&&t.indexOf("stripe_balance")>=0&&t.indexOf("stripe_list_charges")>=0&&t.indexOf("\u2014")<0&&t.indexOf("\u2013")<0;})()`},
	}
	for _, card := range cards {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			t.Run(card.id+"-"+vp.name, func(t *testing.T) {
				h := newHarness(t)
				user, _ := h.account(card.id+"-shot@example.com", "Ada", "correct-horse-1")
				ctx := openS07(t, browserCtx, h, user.ID, "/settings/connections/"+card.id, vp.w, vp.h, card.ready)
				assertShotClean(t, ctx)
				assertNoSideScroll(t, ctx)
				saveConnectionShot(t, ctx, filepath.Join(dir, card.id+"-"+vp.name+".png"), vp.w)
			})
		}
	}
}

func saveConnectionShot(t *testing.T, ctx context.Context, path string, width int) {
	t.Helper()
	var tall int
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(() => {
		const card = document.getElementById('conn-detail');
		const acts = document.querySelector('.conn-acts');
		if (!card) return 0;
		const bottom = (acts || card).getBoundingClientRect().bottom + window.scrollY;
		if (!(bottom > 200 && bottom < 4000)) return 0;
		return Math.ceil(bottom + 32);
	})()`), &tall)); err != nil {
		t.Fatal(err)
	}
	if tall > 400 && tall < 4000 {
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(int64(width), int64(tall))); err != nil {
			t.Fatal(err)
		}
	}
	assertNoSideScroll(t, ctx)
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		t.Fatal(err)
	}
	if len(buf) < 1000 || string(buf[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("screenshot %s is not a png (%d bytes)", path, len(buf))
	}
	wide := int(buf[16])<<24 | int(buf[17])<<16 | int(buf[18])<<8 | int(buf[19])
	if wide != width {
		t.Fatalf("screenshot %s is %d px wide", path, wide)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}
