//go:build browsercheck

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/castcfg"
)

// TestConnectionsBrowser walks the settings connections flow in Chrome
// at the desktop and phone widths used for UI evidence.
func TestConnectionsBrowser(t *testing.T) {
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "fixture-or-key")
	chrome := os.Getenv("CASTBOT_CHROME")
	if chrome == "" {
		chrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	}
	h := newHarness(t)
	user, secret := h.account("conn-browser@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.80:9"
	h.login("conn-browser@example.com", "correct-horse-1", secret, ip, false)
	agent := postNewAgent(t, h, ip, user.ID, "Atlas", firstModel(t))
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

	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run(vp.name, func(t *testing.T) {
			ctx, cancel := chromedp.NewContext(browser)
			defer cancel()
			ctx, timeout := context.WithTimeout(ctx, 2*time.Minute)
			defer timeout()
			if err := chromedp.Run(ctx,
				network.Enable(),
				network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
				chromedp.EmulateViewport(int64(vp.w), int64(vp.h)),
				chromedp.Navigate(ts.URL+"/settings/connections"),
			); err != nil {
				t.Fatal(err)
			}
			browserWait(t, ctx, "connections grid", `!!document.querySelector('#conn-grid') && !!document.querySelector('nav.stoc a[href="/settings/connections"].on') && (document.body.innerText||'').indexOf('GitHub')>=0 && (document.body.innerText||'').indexOf('DigitalOcean')>=0 && (document.body.innerText||'').indexOf('Shell Actions')>=0`)
			browserNoScroll(t, ctx, "grid")
			browserNoDash(t, ctx, "grid")

			if err := chromedp.Run(ctx, chromedp.Click(`a[href="/settings/connections/github"]`, chromedp.ByQuery)); err != nil {
				t.Fatal(err)
			}
			browserWait(t, ctx, "github panel", `!!document.querySelector('#conn-token') && !!document.querySelector('select[name="level_create_issue"]') && (document.body.innerText||'').indexOf('GitHub')>=0`)
			browserNoScroll(t, ctx, "github")
			if err := chromedp.Run(ctx,
				chromedp.SetValue(`#conn-token`, "browser-flow-token", chromedp.ByQuery),
				chromedp.Evaluate(`(function(){var s=document.querySelector('select[name="level_create_issue"]'); if(!s) return false; s.value='deny'; s.dispatchEvent(new Event('change',{bubbles:true})); return s.value==='deny';})()`, nil),
			); err != nil {
				t.Fatal(err)
			}
			browserClick(t, ctx, `#conn-save`)
			browserWait(t, ctx, "github connected", `(document.body.innerText||'').indexOf('Connected. Every agent you own can use this.')>=0 && (document.body.innerText||'').indexOf('A token is saved')>=0 && (document.querySelector('#conn-token')||{}).value===''`)
			browserNoSecret(t, ctx)
			browserNoScroll(t, ctx, "github saved")

			browserClick(t, ctx, `#conn-disconnect button`)
			browserWait(t, ctx, "github disconnected", `(document.body.innerText||'').indexOf('Disconnected. Tool choices stay.')>=0 && (document.body.innerText||'').indexOf('Not connected')>=0`)
			browserNoSecret(t, ctx)
			browserNoScroll(t, ctx, "github disconnected")

			if err := chromedp.Run(ctx, chromedp.Navigate(ts.URL+"/settings/connections/shell")); err != nil {
				t.Fatal(err)
			}
			browserWait(t, ctx, "shell panel", `!!document.querySelector('select[name="level_bash"]') && !document.querySelector('#conn-token') && (document.body.innerText||'').indexOf('Shell Actions')>=0`)
			browserNoScroll(t, ctx, "shell")
			if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var s=document.querySelector('select[name="level_bash"]'); if(!s) return false; s.value='deny'; s.dispatchEvent(new Event('change',{bubbles:true})); return s.value==='deny';})()`, nil)); err != nil {
				t.Fatal(err)
			}
			browserClick(t, ctx, `#conn-save`)
			browserWait(t, ctx, "shell saved", `(document.body.innerText||'').indexOf('Saved. Every agent you own uses these choices.')>=0 || (document.body.innerText||'').indexOf('Connected. Every agent you own can use this.')>=0`)
			browserNoScroll(t, ctx, "shell saved")

			edit := ts.URL + "/agents/" + agent.ID + "/edit"
			if err := chromedp.Run(ctx, chromedp.Navigate(edit)); err != nil {
				t.Fatal(err)
			}
			browserWait(t, ctx, "edit form", `!!document.querySelector('form.na input[name="name"]') && !document.querySelector('#tools-card') && !document.querySelector('#connections') && (document.body.innerText||'').indexOf('Edit agent')>=0`)
			browserNoScroll(t, ctx, "edit")
			browserNoSecret(t, ctx)
			if err := chromedp.Run(ctx, chromedp.SetValue(`form.na input[name="title"]`, "Browser saved", chromedp.ByQuery)); err != nil {
				t.Fatal(err)
			}
			browserClick(t, ctx, `form.na .acts button.btn.pri`)
			browserWait(t, ctx, "edit saved", `location.pathname==='/agents/`+agent.ID+`' && !document.querySelector('#tools-card') && !document.querySelector('#connections')`)
			fresh, err := h.st.GetAgent(context.Background(), user.ID, agent.ID)
			if err != nil || fresh.Title != "Browser saved" {
				t.Fatalf("title %q %v", fresh.Title, err)
			}
			on, err := h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, "github")
			if err != nil || on {
				t.Fatal("edit turned GitHub back on")
			}
		})
	}
}

func browserSession(t *testing.T, h *harness, userID string) string {
	t.Helper()
	sess, err := h.st.CreateSession(context.Background(), userID, "Mozilla/5.0 Chrome/120.0", true)
	if err != nil {
		t.Fatal(err)
	}
	return seal("session", sess.ID, h.srv.key)
}

func browserClick(t *testing.T, ctx context.Context, sel string) {
	t.Helper()
	err := chromedp.Run(ctx, chromedp.Click(sel, chromedp.ByQuery))
	if err != nil && !strings.Contains(err.Error(), "navigat") && !strings.Contains(err.Error(), "canceled") {
		t.Fatal(err)
	}
}

func browserWait(t *testing.T, ctx context.Context, label, expr string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		var ok bool
		err := chromedp.Run(ctx, chromedp.Evaluate(expr, &ok))
		if err == nil && ok {
			return
		}
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.body && document.body.innerText || '').slice(0, 500)`, &last))
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("%s\n%s", label, last)
}

func browserNoScroll(t *testing.T, ctx context.Context, label string) {
	t.Helper()
	var extra int
	if err := chromedp.Run(ctx, chromedp.Evaluate(`Math.max(document.documentElement.scrollWidth - document.documentElement.clientWidth, document.body ? document.body.scrollWidth - document.body.clientWidth : 0)`, &extra)); err != nil {
		t.Fatal(err)
	}
	if extra > 1 {
		t.Fatalf("%s sideways scroll %d", label, extra)
	}
}

func browserNoDash(t *testing.T, ctx context.Context, label string) {
	t.Helper()
	var ok bool
	err := chromedp.Run(ctx, chromedp.Evaluate(`(document.body.innerText||'').indexOf('\u2014')<0 && (document.body.innerText||'').indexOf('\u2013')<0`, &ok))
	if err != nil || !ok {
		t.Fatalf("%s dash %v", label, err)
	}
}

func browserNoSecret(t *testing.T, ctx context.Context) {
	t.Helper()
	var leaked bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(document.documentElement.outerHTML||'').indexOf('browser-flow-token')>=0`, &leaked)); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("token rendered")
	}
}
