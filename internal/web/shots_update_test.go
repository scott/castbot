//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/update"
)

func TestShotsUpdate(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	dir := filepath.Join("..", "..", "docs", "evidence", "update")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		vp := vp
		t.Run("waiting-"+vp.name, func(t *testing.T) {
			shootUpdate(t, browser, dir, vp.w, vp.h, vp.name, "waiting")
		})
		t.Run("current-"+vp.name, func(t *testing.T) {
			shootUpdate(t, browser, dir, vp.w, vp.h, vp.name, "current")
		})
		t.Run("home-"+vp.name, func(t *testing.T) {
			shootUpdate(t, browser, dir, vp.w, vp.h, vp.name, "home")
		})
	}
}

func shootUpdate(t *testing.T, browser context.Context, dir string, width, height int, name, kind string) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	user, _ := h.account("shot-update-"+kind+"-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	seedAgent(t, h.st, user.ID, "Atlas")
	path := filepath.Join(t.TempDir(), "update.json")
	page := "/settings#update"
	needle := `(function(){var t=document.body.innerText||'';var n=document.getElementById('update');return !!n&&t.indexOf('Up to date.')>=0&&t.indexOf('Check now')>=0&&t.indexOf('Update now')<0&&t.indexOf('v0.20261124.2')<0&&t.indexOf('\u2014')<0&&t.indexOf('\u2013')<0;})()`
	file := "settings-current-" + name + ".png"
	switch kind {
	case "waiting":
		writeUpdateCache(t, path, true, true)
		needle = `(function(){var t=document.body.innerText||'';var n=document.getElementById('update');return !!n&&t.indexOf('Castbot v0.20261124.2 is ready.')>=0&&t.indexOf('Release notes')>=0&&t.indexOf('Replace machine')>=0&&t.indexOf('Check now')>=0&&t.indexOf('Update now')>=0&&t.indexOf('Ask me')>=0&&t.indexOf('\u2014')<0&&t.indexOf('\u2013')<0;})()`
		file = "settings-waiting-" + name + ".png"
	case "home":
		writeUpdateCache(t, path, true, true)
		page = "/?list=1"
		needle = `(function(){var t=document.body.innerText||'';var b=document.querySelector('.sb-up');return !!b&&t.indexOf('Castbot v0.20261124.2 is ready.')>=0&&t.indexOf('Atlas')>=0&&t.indexOf('\u2014')<0&&t.indexOf('\u2013')<0;})()`
		file = "home-banner-" + name + ".png"
	default:
		if err := update.WriteCache(path, update.Cache{}); err != nil {
			t.Fatal(err)
		}
	}
	h.srv.SetUpdate("0.20261124.1", "4f3a9c2e8b1d70a6c5e4f3a9c2e8b1d70a6c5e4f", "", path)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+page, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "update "+kind+" "+name, js(needle))); err != nil {
		t.Fatal(err)
	}
	if kind != "home" {
		var scrolled bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var n=document.getElementById('update');if(!n)return false;n.scrollIntoView({block:'start'});return true;})()`), &scrolled)); err != nil || !scrolled {
			t.Fatalf("scroll update %v", err)
		}
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveShot(t, ctx, filepath.Join(dir, file))
}
