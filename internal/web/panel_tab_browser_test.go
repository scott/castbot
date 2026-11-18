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
)

// TestPanelTabBrowser keeps the chat node when a right-column tab is clicked.
func TestPanelTabBrowser(t *testing.T) {
	chrome := os.Getenv("CASTBOT_CHROME")
	if chrome == "" {
		chrome = "/usr/bin/google-chrome"
	}
	h, user, _ := panelSession(t, "tab-browser@example.com", "Alex Rivera")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
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

	page := ts.URL + "/agents/" + agent.ID + "?panel=agent"
	if err := chromedp.Run(ctx,
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(1280, 800),
		chromedp.Navigate(page),
	); err != nil {
		t.Fatal(err)
	}
	browserWait(t, ctx, "agent tab", `!!document.querySelector('.ptabs a.on') && !!document.getElementById('msgs') && !!document.getElementById('composer') && (document.body.innerText||'').indexOf('Name, instructions, and model.')>=0`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var msgs=document.getElementById('msgs'); var comp=document.getElementById('composer'); var main=document.querySelector('.main'); msgs.dataset.stay='1'; comp.dataset.stay='1'; window.__stay=1; var box=msgs.getBoundingClientRect(); window.__chatBox={w:Math.round(box.width), h:Math.round(box.height), t:Math.round(box.top)}; if(main){ main.scrollTop=0; } return true;})()`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`.ptabs a[href*="panel=machine"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	browserWait(t, ctx, "machine tab without reload", `(function(){var msgs=document.getElementById('msgs'); var comp=document.getElementById('composer'); var box=msgs.getBoundingClientRect(); var same=window.__chatBox && Math.abs(Math.round(box.width)-window.__chatBox.w)<=1 && Math.abs(Math.round(box.top)-window.__chatBox.t)<=1; return window.__stay===1 && !!msgs && msgs.dataset.stay==='1' && !!comp && comp.dataset.stay==='1' && same && location.search.indexOf('panel=machine')>=0 && (document.body.innerText||'').indexOf('State from the cloud API')>=0 && document.querySelectorAll('.app').length===1 && document.querySelectorAll('#msgs').length===1;})()`)
	if err := chromedp.Run(ctx, chromedp.Click(`.ptabs a[href*="panel=files"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	browserWait(t, ctx, "files tab without reload", `(function(){var msgs=document.getElementById('msgs'); var comp=document.getElementById('composer'); var box=msgs.getBoundingClientRect(); var same=window.__chatBox && Math.abs(Math.round(box.width)-window.__chatBox.w)<=1 && Math.abs(Math.round(box.top)-window.__chatBox.t)<=1; return window.__stay===1 && msgs.dataset.stay==='1' && comp.dataset.stay==='1' && same && location.search.indexOf('panel=files')>=0 && (document.body.innerText||'').indexOf("Only Atlas's files")>=0;})()`)
	browserNoScroll(t, ctx, "panel tab")
	browserNoDash(t, ctx, "panel tab")
}
