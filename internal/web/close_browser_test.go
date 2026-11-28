//go:build browsercheck

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
)

// TestCloseDoesNotWalkHistory checks the upper-right close control.
// It dismisses the open surface instead of stepping to the previous history entry.
func TestCloseDoesNotWalkHistory(t *testing.T) {
	chrome := os.Getenv("CASTBOT_CHROME")
	if chrome == "" {
		chrome = "/usr/bin/google-chrome"
	}
	h, user, _ := panelSession(t, "close-browser@example.com", "Alex Rivera")
	h.srv.SetHost(host.NewFake(), "example.invalid/castbot-runtime:ff6d7ea", "https://castbot.example")
	atlas := makeAgent(t, h.st, user.ID, "Atlas")
	pip := makeAgent(t, h.st, user.ID, "Pip")
	if _, err := h.st.BeginVM(context.Background(), user.ID, atlas.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, atlas.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := browserSession(t, h, user.ID)

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
	browser, browserCancel := chromedp.NewContext(allocCtx)
	t.Cleanup(browserCancel)
	if err := chromedp.Run(browser); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := chromedp.NewContext(browser)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 2*time.Minute)
	defer timeout()

	page := ts.URL + "/agents/" + atlas.ID + "?panel=machine"
	if err := chromedp.Run(ctx,
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(1280, 800),
		chromedp.Navigate(page),
	); err != nil {
		t.Fatal(err)
	}
	browserWait(t, ctx, "atlas machine panel", `!!document.querySelector('.ptabs a.on') && (document.body.innerText||'').indexOf('State from the cloud API')>=0 && document.querySelector('.who-btn .nm').textContent==='Atlas'`)
	if err := chromedp.Run(ctx, chromedp.Click(`a.ag[data-name="Pip"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	browserWait(t, ctx, "pip with the panel open", `location.pathname.indexOf('`+pip.ID+`')>=0 && location.search.indexOf('panel=machine')>=0 && document.querySelector('.who-btn .nm').textContent==='Pip' && !document.getElementById('agent-panel').hidden`)
	browserClick(t, ctx, `a[aria-label="Close agent panel (Esc)"]`)
	browserWait(t, ctx, "close stays on pip", `(function(){var p=document.getElementById('agent-panel'); return location.pathname.indexOf('`+pip.ID+`')>=0 && location.search.indexOf('panel=')<0 && document.querySelector('.who-btn .nm').textContent==='Pip' && (!p || p.hidden || !p.classList.contains('s19pane'));})()`)
	browserNoScroll(t, ctx, "panel close")

	if err := chromedp.Run(ctx, chromedp.Navigate(ts.URL+"/settings/connections/github")); err != nil {
		t.Fatal(err)
	}
	browserWait(t, ctx, "github settings", `location.pathname==='/settings/connections/github' && !!document.querySelector('.page-h [data-sclose]')`)
	browserClick(t, ctx, `.page-h [data-sclose]`)
	browserWait(t, ctx, "settings close returns to pip", `location.pathname.indexOf('`+pip.ID+`')>=0 && location.pathname.indexOf('/settings')<0`)

	if err := chromedp.Run(ctx, chromedp.Navigate(ts.URL+"/agents/"+atlas.ID+"/machine/replace")); err != nil {
		t.Fatal(err)
	}
	browserWait(t, ctx, "replace dialog", `(document.body.innerText||'').indexOf('Replace this machine?')>=0 && !!document.querySelector('.rmodal [data-close]')`)
	browserClick(t, ctx, `.rmodal a[aria-label="Close"]`)
	browserWait(t, ctx, "dialog close opens the machine panel", `location.pathname.indexOf('`+atlas.ID+`')>=0 && location.pathname.indexOf('/machine/replace')<0 && location.search.indexOf('panel=machine')>=0 && (document.body.innerText||'').indexOf('Replace this machine?')<0`)
	browserNoScroll(t, ctx, "dialog close")
	browserNoDash(t, ctx, "dialog close")
}
