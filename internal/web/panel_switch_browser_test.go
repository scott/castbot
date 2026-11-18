//go:build browsercheck

package web

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// TestAgentSwitchBrowser keeps the shell when another agent is chosen
// while the right column is open.
func TestAgentSwitchBrowser(t *testing.T) {
	chrome := os.Getenv("CASTBOT_CHROME")
	if chrome == "" {
		chrome = "/usr/bin/google-chrome"
	}
	h, user, _ := panelSession(t, "switch-browser@example.com", "Alex Rivera")
	atlas := makeAgent(t, h.st, user.ID, "Atlas")
	pip := makeAgent(t, h.st, user.ID, "Pip")
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
	browserWait(t, ctx, "atlas machine panel", `!!document.getElementById('stage') && !!document.querySelector('.sb') && !!document.querySelector('.ptabs a.on') && (document.body.innerText||'').indexOf('State from the cloud API')>=0 && !!document.querySelector('.who-btn .nm') && document.querySelector('.who-btn .nm').textContent==='Atlas'`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var sb=document.querySelector('.sb'); var col=document.getElementById('chat-col'); sb.dataset.stay='1'; window.__sb=sb; window.__stay=1; window.__hist=history.length; var box=col.getBoundingClientRect(); window.__chatBox={w:Math.round(box.width), t:Math.round(box.top)}; return true;})()`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`a.ag[data-name="Pip"]`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	wait := fmt.Sprintf(`(function(){var sb=document.querySelector('.sb'); var col=document.getElementById('chat-col'); var nm=document.querySelector('.who-btn .nm'); var box=col&&col.getBoundingClientRect(); var same=window.__chatBox&&box&&Math.abs(Math.round(box.width)-window.__chatBox.w)<=1&&Math.abs(Math.round(box.top)-window.__chatBox.t)<=1; var comp=document.getElementById('composer'); return window.__stay===1 && sb===window.__sb && sb.dataset.stay==='1' && same && history.length===window.__hist+1 && location.pathname.indexOf(%q)>=0 && location.search.indexOf('panel=machine')>=0 && nm&&nm.textContent==='Pip' && comp&&comp.getAttribute('data-agent-id')===%q && (document.body.innerText||'').indexOf('State from the cloud API')>=0 && document.querySelectorAll('.app').length===1 && document.querySelectorAll('#stage').length===1 && !document.getElementById('agent-panel').hidden;})()`, pip.ID, pip.ID)
	browserWait(t, ctx, "pip without reload", wait)
	browserNoScroll(t, ctx, "agent switch")
	browserNoDash(t, ctx, "agent switch")
}
