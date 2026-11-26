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

func TestShotsPageCards(t *testing.T) {
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	dir := filepath.Join("..", "..", "docs", "evidence", "pages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		vp := vp
		t.Run("card-"+vp.name, func(t *testing.T) {
			shootPageCard(t, browser, dir, vp.w, vp.h, vp.name)
		})
		t.Run("list-"+vp.name, func(t *testing.T) {
			shootEmptyPageList(t, browser, dir, vp.w, vp.h, vp.name)
		})
	}
}

func shootPageCard(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("page-card-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	agent := hostedAgent(t, h.st, user, "Atlas")
	made := settleOwnedPage(t, h, user.ID, agent, "Weekly trial results for the north campus observation desk", samplePageText("Hello from the weekly report"))
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	steps := append(signedOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+"/agents/"+agent.ID, width, height),
		shotPoll(t, "card", js(`(function(){var t=document.body.innerText||'';return t.indexOf('Weekly trial results')>=0&&t.indexOf('Only you')>=0&&t.indexOf('Open')>=0&&document.querySelector('.page-card')&&t.indexOf('\u2014')<0;})()`)),
	)
	if err := chromedp.Run(ctx, steps...); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveHosted(t, ctx, filepath.Join(dir, "card-"+name+".png"))
	if err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL+"/p/"+made.Token),
		pageReady(t),
		shotPoll(t, "opened", js(`(function(){var t=document.body.innerText||'';return t.indexOf('Weekly trial results')>=0&&t.indexOf('Hello from the weekly report')>=0&&t.indexOf('\u2014')<0;})()`)),
	); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveHosted(t, ctx, filepath.Join(dir, "card-open-"+name+".png"))
	if made.Token == "" {
		t.Fatal("missing token")
	}
}

func shootEmptyPageList(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("page-list-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	steps := append(signedOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+"/pages", width, height),
		shotPoll(t, "empty", js(`(function(){var t=document.body.innerText||'';return t.indexOf('No pages yet.')>=0&&t.indexOf('A page appears when an agent makes one.')>=0&&t.indexOf('\u2014')<0;})()`)),
	)
	if err := chromedp.Run(ctx, steps...); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveHosted(t, ctx, filepath.Join(dir, "list-"+name+".png"))
}

func signedOpen(t *testing.T, origin, token, page string, width, height int) []chromedp.Action {
	t.Helper()
	return []chromedp.Action{
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := cdppage.AddScriptToEvaluateOnNewDocument(`window.__errs=[];window.addEventListener('securitypolicyviolation',function(e){window.__errs.push(String(e.violatedDirective||'csp'))});window.addEventListener('error',function(e){window.__errs.push(String(e.message||'error'))});`).Do(ctx)
			return err
		}),
		network.SetCookie("castbot_session", token).WithURL(origin).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
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

func TestShotsPageShare(t *testing.T) {
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	dir := filepath.Join("..", "..", "docs", "evidence", "pages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		vp := vp
		t.Run("invited-"+vp.name, func(t *testing.T) {
			shootShare(t, browser, dir, vp.w, vp.h, "invited", vp.name)
		})
		t.Run("link-"+vp.name, func(t *testing.T) {
			shootShare(t, browser, dir, vp.w, vp.h, "link", vp.name)
		})
	}
}

func shootShare(t *testing.T, browser context.Context, dir string, width, height int, kind, name string) {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	owner, _ := h.account("share-shot-"+kind+"-"+name+"@example.com", "Nora Hale", "correct-horse-1")
	member := acceptHostedInvite(t, h, owner, "pip-"+kind+"-"+name+"@example.com", "Pip Lane")
	atlas := hostedAgent(t, h.st, owner, "Atlas")
	moss := hostedAgent(t, h.st, member.User, "Moss")
	made, err := h.st.CreatePage(ctx, owner.ID, atlas.ID, "North desk notes", `{"v":1,"blocks":[{"type":"markdown","text":"Notes from the north desk."}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if kind == "invited" {
		if _, err := h.st.SharePage(ctx, owner.ID, "", made.ID, store.PageInvited, []string{member.ID}, []string{moss.ID}); err != nil {
			t.Fatal(err)
		}
	} else if _, err := h.st.SharePage(ctx, owner.ID, "", made.ID, store.PageLink, nil, nil); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	tabCtx := tab(t, browser)
	var ready string
	if kind == "invited" {
		ready = `(function(){var t=document.body.innerText||'';var invited=document.querySelector('input[name=visibility][value=invited]');var person=document.querySelector('input[name=person]:checked');var agent=document.querySelector('input[name=agent]:checked');var link=document.querySelector('.hosted-link input');return t.indexOf('North desk notes')>=0&&t.indexOf('Pip Lane')>=0&&t.indexOf('Moss')>=0&&t.indexOf('Who can view')>=0&&t.indexOf('Copy link')>=0&&invited&&invited.checked&&person&&agent&&link&&link.value.indexOf('/p/')>=0&&t.indexOf('Nora Hale')<0&&t.indexOf('only_you')<0&&t.indexOf('\u2014')<0;})()`
	} else {
		ready = `(function(){var t=document.body.innerText||'';var linkRadio=document.querySelector('input[name=visibility][value=link]');var link=document.querySelector('.hosted-link input');return t.indexOf('North desk notes')>=0&&t.indexOf('Anyone with the link')>=0&&t.indexOf('Copy link')>=0&&linkRadio&&linkRadio.checked&&link&&link.value.indexOf('/p/')>=0&&t.indexOf('Nora Hale')<0&&t.indexOf('only_you')<0&&t.indexOf('\u2014')<0;})()`
	}
	steps := append(signedOpen(t, ts.URL, sessionToken(t, h, owner.ID), ts.URL+"/p/"+made.Token, width, height),
		shotPoll(t, kind, js(ready)),
	)
	if err := chromedp.Run(tabCtx, steps...); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, tabCtx)
	assertShotClean(t, tabCtx)
	saveHosted(t, tabCtx, filepath.Join(dir, "share-"+kind+"-"+name+".png"))
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
