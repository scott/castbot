//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

const shotPageDoc = `{"v":1,"blocks":[
  {"type":"control","id":"range","label":"Range","options":["7 days","30 days"]},
  {"type":"markdown","text":"Week view shows the latest trials.","when":{"range":"7 days"}},
  {"type":"markdown","text":"Month view rolls the same trials into one total.","when":{"range":"30 days"}},
  {"type":"stats","items":[{"label":"Trials","value":"12","note":"Finished"}]},
  {"type":"chart","kind":"bar","title":"Trials by day","series":[{"name":"Trials","points":[{"x":"Mon","y":3},{"x":"Tue","y":1},{"x":"Wed","y":4},{"x":"Thu","y":2},{"x":"Fri","y":5}]}]},
  {"type":"chart","kind":"line","title":"Trend","series":[{"name":"Trend","points":[{"x":"Mon","y":3},{"x":"Tue","y":1},{"x":"Wed","y":4},{"x":"Thu","y":2},{"x":"Fri","y":5}]}]},
  {"type":"table","caption":"By site","columns":["Day","Site","Note"],"rows":[{"cells":["Mon","North","North campus observation that stays inside the table"]}]}
]}`

func TestShotsPages(t *testing.T) {
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	dir := filepath.Join("..", "..", "docs", "evidence", "pages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	owner, _ := h.account("pages-shot@example.com", "Alex Rivera", "correct-horse-1")
	agent := hostedAgent(t, h.st, owner, "Atlas")
	made, err := h.st.CreatePage(context.Background(), owner.ID, agent.ID, "Trial results", shotPageDoc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SharePage(context.Background(), owner.ID, "", made.ID, store.PageLink, nil, nil); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	page := ts.URL + "/p/" + made.Token
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		vp := vp
		t.Run(vp.name, func(t *testing.T) {
			ctx := tab(t, browser)
			if err := chromedp.Run(ctx, hostedOpen(t, page, vp.w, vp.h)...); err != nil {
				t.Fatal(err)
			}
			if err := chromedp.Run(ctx, shotPoll(t, "page", js(`(function(){var t=document.body.innerText||'';return t.indexOf('Trial results')>=0&&t.indexOf('Week view')>=0&&t.indexOf('Month view')<0&&t.indexOf('Trials by day')>=0&&document.querySelector('svg')&&document.querySelector('.hosted-table')&&document.querySelector('.hosted-control')&&t.indexOf('\u2014')<0;})()`))); err != nil {
				t.Fatal(err)
			}
			assertNoSideScroll(t, ctx)
			assertShotClean(t, ctx)
			if err := chromedp.Run(ctx, chromedp.Click(`input[value="30 days"]`, chromedp.ByQuery)); err != nil {
				t.Fatal(err)
			}
			if err := chromedp.Run(ctx, shotPoll(t, "switched", js(`(function(){var t=document.body.innerText||'';return t.indexOf('Month view')>=0&&t.indexOf('Week view')<0;})()`))); err != nil {
				t.Fatal(err)
			}
			assertNoSideScroll(t, ctx)
			if err := chromedp.Run(ctx, chromedp.Reload(), pageReady(t)); err != nil {
				t.Fatal(err)
			}
			if err := chromedp.Run(ctx, shotPoll(t, "reloaded", js(`(function(){var t=document.body.innerText||'';return t.indexOf('Week view')>=0&&t.indexOf('Month view')<0;})()`))); err != nil {
				t.Fatal(err)
			}
			assertNoSideScroll(t, ctx)
			saveHosted(t, ctx, filepath.Join(dir, "page-"+vp.name+".png"))
		})
	}
}

func saveHosted(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		buf, err = cdppage.CaptureScreenshot().
			WithFormat(cdppage.CaptureScreenshotFormatPng).
			WithCaptureBeyondViewport(true).
			Do(ctx)
		return err
	})); err != nil {
		t.Fatal(err)
	}
	if len(buf) < 1000 || string(buf[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("screenshot %s is not a png", path)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func hostedOpen(t *testing.T, page string, width, height int) []chromedp.Action {
	t.Helper()
	return []chromedp.Action{
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := cdppage.AddScriptToEvaluateOnNewDocument(`window.__errs=[];window.addEventListener('securitypolicyviolation',function(e){window.__errs.push(String(e.violatedDirective||'csp'))});window.addEventListener('error',function(e){window.__errs.push(String(e.message||'error'))});`).Do(ctx)
			return err
		}),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
	}
}
