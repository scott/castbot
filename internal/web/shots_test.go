//go:build shots

package web

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	cdppage "github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestShots(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	if _, err := parseShotWait(os.Getenv("CASTBOT_SHOT_WAIT")); err != nil {
		t.Fatal(err)
	}
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
	dir := filepath.Join("..", "..", "docs", "evidence", "S02")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run("stream-"+vp.name, func(t *testing.T) {
			shootStream(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
		t.Run("error-"+vp.name, func(t *testing.T) {
			shootError(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
		t.Run("502-"+vp.name, func(t *testing.T) {
			shoot502(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
		t.Run("restart-"+vp.name, func(t *testing.T) {
			shootRestart(t, browserCtx, dir, vp.w, vp.h, vp.name)
		})
	}
	dir03 := filepath.Join("..", "..", "docs", "evidence", "S03")
	if err := os.MkdirAll(dir03, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run("container-"+vp.name, func(t *testing.T) {
			shootContainer(t, browserCtx, dir03, vp.w, vp.h, vp.name)
		})
	}
	dir04 := filepath.Join("..", "..", "docs", "evidence", "S04")
	if err := os.MkdirAll(dir04, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		t.Run("machine-"+vp.name, func(t *testing.T) {
			shootMachine(t, browserCtx, dir04, vp.w, vp.h, vp.name)
		})
	}
	dir05 := filepath.Join("..", "..", "docs", "evidence", "S05")
	if err := os.MkdirAll(dir05, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			t.Run("agentform-"+theme+"-"+vp.name, func(t *testing.T) {
				shootAgentForm(t, browserCtx, dir05, vp.w, vp.h, theme, vp.name)
			})
		}
	}
	t.Run("agentform-return-1280", func(t *testing.T) {
		shootEditReturn(t, browserCtx, 1280, 800)
	})
	dir42 := filepath.Join("..", "..", "docs", "evidence", "issue42")
	if err := os.MkdirAll(dir42, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			t.Run("resume-"+theme+"-"+vp.name, func(t *testing.T) {
				shootPausedOpen(t, browserCtx, dir42, vp.w, vp.h, theme, vp.name)
			})
		}
	}
	dir06 := filepath.Join("..", "..", "docs", "evidence", "S06")
	if err := os.MkdirAll(dir06, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			t.Run("cli-export-"+theme+"-"+vp.name, func(t *testing.T) {
				shootCLIExport(t, browserCtx, dir06, vp.w, vp.h, theme, vp.name)
			})
			t.Run("cli-tokens-"+theme+"-"+vp.name, func(t *testing.T) {
				shootCLITokens(t, browserCtx, dir06, vp.w, vp.h, theme, vp.name)
			})
			t.Run("cli-thread-"+theme+"-"+vp.name, func(t *testing.T) {
				shootCLIThread(t, browserCtx, dir06, vp.w, vp.h, theme, vp.name)
			})
		}
	}
	dir07 := filepath.Join("..", "..", "docs", "evidence", "S07")
	if err := os.MkdirAll(dir07, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			t.Run("s07-start-"+theme+"-"+vp.name, func(t *testing.T) {
				shootS07Start(t, browserCtx, dir07, vp.w, vp.h, theme, vp.name)
			})
			t.Run("s07-paused-"+theme+"-"+vp.name, func(t *testing.T) {
				shootS07Paused(t, browserCtx, dir07, vp.w, vp.h, theme, vp.name)
			})
			t.Run("s07-waking-"+theme+"-"+vp.name, func(t *testing.T) {
				shootS07Waking(t, browserCtx, dir07, vp.w, vp.h, theme, vp.name)
			})
			t.Run("s07-reply-"+theme+"-"+vp.name, func(t *testing.T) {
				shootS07Reply(t, browserCtx, dir07, vp.w, vp.h, theme, vp.name)
			})
			t.Run("s07-nokey-"+theme+"-"+vp.name, func(t *testing.T) {
				shootS07NoKey(t, browserCtx, dir07, vp.w, vp.h, theme, vp.name)
			})
		}
	}
	dir07b := filepath.Join("..", "..", "docs", "evidence", "S07b")
	if err := os.MkdirAll(dir07b, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			for _, kind := range []string{"pending", "approved", "denied", "expired", "cli"} {
				kind := kind
				t.Run("s07b-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootApproval(t, browserCtx, dir07b, vp.w, vp.h, theme, vp.name, kind)
				})
			}
		}
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		vp := vp
		t.Run("s07b-bar-"+vp.name, func(t *testing.T) {
			shootDecisionBar(t, browserCtx, vp.w, vp.h, 12)
		})
	}
	dir07c := filepath.Join("..", "..", "docs", "evidence", "S07c")
	if err := os.MkdirAll(dir07c, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			t.Run("s07c-nomachine-"+theme+"-"+vp.name, func(t *testing.T) {
				shootNoMachineComposer(t, browserCtx, dir07c, vp.w, vp.h, theme, vp.name)
			})
			for _, kind := range []string{"09a", "09a2", "09b", "09c", "09c2", "09d", "09d2", "09e", "09f", "09f2", "09f3", "09f4", "09g"} {
				kind := kind
				t.Run("s07c-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootReplace(t, browserCtx, dir07c, vp.w, vp.h, theme, vp.name, kind)
				})
			}
		}
	}
	dir08 := filepath.Join("..", "..", "docs", "evidence", "S08")
	if err := os.MkdirAll(dir08, 0o755); err != nil {
		t.Fatal(err)
	}
	dir09 := filepath.Join("..", "..", "docs", "evidence", "S09")
	if err := os.MkdirAll(dir09, 0o755); err != nil {
		t.Fatal(err)
	}
	dir10 := filepath.Join("..", "..", "docs", "evidence", "S10")
	if err := os.MkdirAll(dir10, 0o755); err != nil {
		t.Fatal(err)
	}
	dir20 := filepath.Join("..", "..", "docs", "evidence", "S20")
	if err := os.MkdirAll(dir20, 0o755); err != nil {
		t.Fatal(err)
	}
	dir27 := filepath.Join("..", "..", "docs", "evidence", "S27")
	if err := os.MkdirAll(dir27, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			for _, kind := range []string{"10a", "10a2", "10b", "10b2", "10b3", "10c", "10d", "10d2", "10d3", "10e", "10f", "10f2", "10f3", "10g", "10g2"} {
				kind := kind
				t.Run("s08-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootVault(t, browserCtx, dir08, vp.w, vp.h, theme, vp.name, kind)
				})
			}
			for _, kind := range []string{"11a", "11a2", "11b", "11b2", "11c", "11d", "11d2", "11d3", "11d4", "11e", "11f", "11g"} {
				kind := kind
				t.Run("s09-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootRoutine(t, browserCtx, dir09, vp.w, vp.h, theme, vp.name, kind)
				})
			}
			for _, kind := range []string{"12a", "12a2", "12b", "12c", "12d", "12e", "12f", "12g", "12i", "12j", "12k", "12k2"} {
				kind := kind
				t.Run("s10-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootS10(t, browserCtx, dir10, vp.w, vp.h, theme, vp.name, kind)
				})
			}
			for _, kind := range []string{"14a", "14b", "14b2", "14c", "14d", "14e", "14f", "14f2", "14g", "14h", "14i", "14j", "14k", "14l", "14m"} {
				kind := kind
				t.Run("s20-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootS20(t, browserCtx, dir20, vp.w, vp.h, theme, vp.name, kind)
				})
			}
			if theme == "dark" && vp.name == "1280" {
				t.Run("s20-keep", func(t *testing.T) {
					shootCreateKeep(t, browserCtx)
				})
			}
			for _, kind := range []string{"chat", "list", "advanced", "edit"} {
				kind := kind
				t.Run("s27-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootS27(t, browserCtx, dir27, vp.w, vp.h, theme, vp.name, kind)
				})
			}
		}
	}
	dir19 := filepath.Join("..", "..", "docs", "evidence", "S19")
	if err := os.MkdirAll(dir19, 0o755); err != nil {
		t.Fatal(err)
	}
	dir22 := filepath.Join("..", "..", "docs", "evidence", "S22")
	if err := os.MkdirAll(dir22, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			for _, kind := range []string{"13a", "13a2", "13b", "13b2", "13c", "13d", "13e", "13f", "13g", "13h"} {
				kind := kind
				t.Run("s19-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootS19(t, browserCtx, dir19, vp.w, vp.h, theme, vp.name, kind)
				})
			}
			for _, kind := range []string{"15a", "15b", "15b2", "15d", "15d2", "15d3", "15d4", "15e", "15e2", "15e3", "15f", "15d-still"} {
				kind := kind
				t.Run("s22-"+kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootS22(t, browserCtx, dir22, vp.w, vp.h, theme, vp.name, kind)
				})
			}
		}
	}
	t.Run("s19-sec-dark-1280", func(t *testing.T) { shootS19Secrets(t, browserCtx, 1280, 800) })
	t.Run("s19-sec-dark-390", func(t *testing.T) { shootS19Secrets(t, browserCtx, 390, 844) })
	t.Run("s19-back-dark-1280", func(t *testing.T) { shootS19Back(t, browserCtx) })
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {1440, 900, "1440"}} {
		vp := vp
		t.Run("s19-toggle-dark-"+vp.name, func(t *testing.T) {
			shootPanelToggle(t, browserCtx, dir19, vp.w, vp.h, vp.name)
		})
	}
	dirT2 := filepath.Join("..", "..", "docs", "evidence", "T2")
	if err := os.MkdirAll(dirT2, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"light", "dark"} {
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			theme, vp := theme, vp
			t.Run("t2-conn-"+theme+"-"+vp.name, func(t *testing.T) {
				shootT2Connections(t, browserCtx, dirT2, vp.w, vp.h, theme, vp.name)
			})
			t.Run("t2-secret-"+theme+"-"+vp.name, func(t *testing.T) {
				shootT2Secret(t, browserCtx, dirT2, vp.w, vp.h, theme, vp.name)
			})
		}
	}
	t.Run("secret-paste", func(t *testing.T) {
		shootSecretPaste(t, browserCtx)
	})
	dirModels := filepath.Join("..", "..", "docs", "evidence", "models")
	if err := os.MkdirAll(dirModels, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, vp := range []struct {
		w, h int
		name string
	}{{1280, 800, "1280"}, {390, 844, "390"}} {
		vp := vp
		t.Run("models-setup-"+vp.name, func(t *testing.T) {
			shootModelSetup(t, browserCtx, dirModels, vp.w, vp.h, vp.name)
		})
		t.Run("models-advanced-"+vp.name, func(t *testing.T) {
			shootModelAdvanced(t, browserCtx, dirModels, vp.w, vp.h, vp.name)
		})
		t.Run("models-edit-"+vp.name, func(t *testing.T) {
			shootModelEdit(t, browserCtx, dirModels, vp.w, vp.h, vp.name)
		})
		t.Run("models-do-"+vp.name, func(t *testing.T) {
			shootModelDO(t, browserCtx, dirModels, vp.w, vp.h, vp.name)
		})
		t.Run("models-delisted-"+vp.name, func(t *testing.T) {
			shootModelDelisted(t, browserCtx, dirModels, vp.w, vp.h, vp.name)
		})
		t.Run("models-nokey-"+vp.name, func(t *testing.T) {
			shootModelNoKey(t, browserCtx, dirModels, vp.w, vp.h, vp.name)
		})
	}
}

// htmxApproveReady is true once HTMX has processed the approve control.
// The button and its form carry htmx-internal-data, the form is listening
// for submit, and the control is not still swapping or settling.
func htmxApproveReady(sel string) string {
	return `(function(){
		var btn = document.querySelector('` + sel + `');
		if (!btn || !window.htmx) return false;
		var form = btn.form || (btn.closest && btn.closest('form'));
		if (!form) return false;
		var bd = btn['htmx-internal-data'];
		var fd = form['htmx-internal-data'];
		if (!bd || !bd.firstInitCompleted || !fd || !fd.firstInitCompleted) return false;
		if (!fd.listenerInfos || !fd.listenerInfos.length) return false;
		function settling(el) {
			if (!el || !el.classList) return false;
			return el.classList.contains('htmx-added') || el.classList.contains('htmx-settling') || el.classList.contains('htmx-swapping') || el.classList.contains('htmx-request');
		}
		if (settling(btn) || settling(form) || settling(document.body)) return false;
		var cards = form.closest && form.closest('.cards');
		if (settling(cards)) return false;
		return true;
	})()`
}

// shootDecisionBar clicks Approve on a live card and checks the waiting bar
// and the nav badges clear. The stream can swap the card before the click
// response lands, so the bar has to ride the stream event.
func shootDecisionBar(t *testing.T, browser context.Context, width, height, n int) {
	t.Helper()
	stale := 0
	for i := 0; i < n; i++ {
		h := newHarness(t)
		ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "ls demo"}})
		up := httptest.NewServer(ask)
		t.Cleanup(up.Close)
		h.srv.SetClient(runtime.NewHTTP(up.URL))
		email := "bar-" + strconv.Itoa(width) + "-" + strconv.Itoa(i) + "@example.com"
		user, secret := h.account(email, "Demo User", "correct-horse-1")
		ip := "203.0.113.70:9"
		h.login(email, "correct-horse-1", secret, ip, false)
		agent := h.newAgent(ip, "Atlas")
		sendPrompt(t, h, agent, ip, "go")
		ap := waitPending(t, h, user.ID)
		ts := httptest.NewTLSServer(h.srv.Handler())
		t.Cleanup(ts.Close)
		token := sessionToken(t, h, user.ID)
		sel := "#appr-" + ap.ID + " button.pri"
		ctx := tab(t, browser)
		if err := chromedp.Run(ctx, cliOpen(t, ts.URL, token, ts.URL+"/agents/"+agent.ID, width, height)...); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := chromedp.Run(ctx, shotVisible(t, sel, chromedp.ByQuery)); err != nil {
			t.Fatalf("button %d: %v", i, err)
		}
		// The button is in the DOM before HTMX settles the swap and binds the
		// form. A click in that gap is a full page POST.
		if err := chromedp.Run(ctx, shotPoll(t, "htmx processed the approve button", js(htmxApproveReady(sel)))); err != nil {
			t.Fatalf("htmx %d: %v", i, err)
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.querySelector('`+sel+`').click()`), nil)); err != nil {
			t.Fatalf("click %d: %v", i, err)
		}
		if err := chromedp.Run(ctx, shotPoll(t, "bash finished", js(`(document.body.innerText||'').indexOf('bash finished.')>=0`))); err != nil {
			t.Fatalf("finish %d: %v", i, err)
		}
		time.Sleep(700 * time.Millisecond)
		var res struct {
			Vis  bool   `json:"vis"`
			Nav  string `json:"nav"`
			Bnav string `json:"bnav"`
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var e=document.querySelector('#pending-bar');var nav=document.querySelector('#nav-approvals');var bnav=document.querySelector('#bnav-approvals');return {vis:!!(e&&e.offsetParent!==null),nav:(nav&&nav.innerText||'').trim(),bnav:(bnav&&bnav.innerText||'').trim()};})()`), &res)); err != nil {
			t.Fatal(err)
		}
		if res.Vis || res.Nav != "" || res.Bnav != "" {
			stale++
			t.Logf("stale click %d vis=%v nav=%q bnav=%q", i, res.Vis, res.Nav, res.Bnav)
		}
		if calls := ask.Calls(); len(calls) != 1 || !calls[0] {
			t.Fatalf("calls %#v", calls)
		}
		_ = chromedp.Run(ctx, chromedp.Navigate("about:blank"))
	}
	if stale != 0 {
		t.Fatalf("%d of %d left the waiting bar or a badge visible at %d px", stale, n, width)
	}
	t.Logf("0 of %d stale at %d px", n, width)
}

func shootStream(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas", WordDelay: 1100 * time.Millisecond})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	user, _ := h.account("shot@example.com", "Shot User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
		shotVisible(t, `textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "hello", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		shotPoll(t, "stream shows Hello before the rest of the reply", js(`(function(){var s=document.querySelector(".stream");return !!(s&&s.textContent.indexOf("Hello")>=0&&s.textContent.indexOf("today?")<0);})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "streaming-"+name+".png"))
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		pageReady(t),
		shotPoll(t, "reloaded reply is on the page", js(`(function(){var s=document.querySelector(".stream");var t=document.body.innerText||"";return !!((s&&s.textContent.indexOf("Hello")>=0)||t.indexOf("Hello! I'm Atlas")>=0);})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "reload-"+name+".png"))
	if err := chromedp.Run(ctx,
		shotPoll(t, "reply finished once", js(`(function(){var t=document.querySelector("#msgs").innerText;var n=t.split("How can I help you today?").length-1;return n===1&&t.indexOf("Hello! I'm Atlas.")>=0;})()`)),
	); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, ctx)
}

func shootError(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	h := newHarnessWith(t, runtime.NewHTTP("http://"+addr))
	h.srv.SetRetryBudget(400 * time.Millisecond)
	user, _ := h.account("err@example.com", "Err User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
		shotVisible(t, `textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "hello", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		shotPoll(t, "retry control inside the thread", retryInside()),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "error-"+name+".png"))
	assertNoSideScroll(t, ctx)
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		pageReady(t),
		shotPoll(t, "retry control inside the thread", retryInside()),
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.UpdateTheme(context.Background(), user.ID, "light"); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		pageReady(t),
		shotPoll(t, "light theme and retry control", js(`(function(){return document.documentElement.classList.contains("light")&&`+retryInsideExpr()+`;})()`)),
		chromedp.SendKeys(`textarea[name="body"]`, "hello again", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		shotPoll(t, "second retry control", js(`(function(){var n=document.querySelectorAll(".errbox button, .ch-body .calm button").length;return n>=2&&`+retryInsideExpr()+`;})()`)),
	}); err != nil {
		t.Fatal(err)
	}
}

func shoot502(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas", WordDelay: 700 * time.Millisecond})
	t.Cleanup(echo.Close)
	gate := &browserGate{}
	h := newHarnessWith(t, runtime.NewHTTP(mustEchoURL(t, echo)))
	user, _ := h.account("gate@example.com", "Gate User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	gate.next = h.srv.Handler()
	ts := httptest.NewTLSServer(gate)
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
		shotVisible(t, `textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "please keep going now", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		shotPoll(t, "502 stream started", js(`(function(){var s=document.querySelector(".stream");return !!(s&&s.textContent.indexOf("You said:")>=0);})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	var armed bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		window.__dup = 0;
		var root = document.querySelector("#msgs");
		var look = function(){
			var t = (root && root.innerText) || "";
			var n = t.split("You said:").length - 1;
			if (n > 1) window.__dup = n;
		};
		new MutationObserver(look).observe(root, {childList:true, subtree:true, characterData:true});
		look();
		return true;
	})()`), &armed)); err != nil {
		t.Fatal(err)
	}
	gate.outage(12 * time.Second)
	time.Sleep(1 * time.Second)
	var during int
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(document.body.innerText.split("You said:").length-1)`), &during)); err != nil {
		t.Fatal(err)
	}
	if during != 1 {
		t.Fatalf("text repeated during the 502 window: %d", during)
	}
	saveShot(t, ctx, filepath.Join(dir, "502-"+name+".png"))
	// The gate answers 502 for 12s, so this wait is longer than the usual budget.
	if err := chromedp.Run(ctx,
		shotPollFor(t, "502 reply settled", js(`(function(){var t=document.querySelector("#msgs").innerText;return t.indexOf("OPENROUTER_API_KEY")>=0&&!document.querySelector(".pending");})()`), shotWait()+15*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	var dup, count int
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Evaluate(js(`window.__dup||0`), &dup),
		chromedp.Evaluate(js(`(document.querySelector("#msgs").innerText.split("You said:").length-1)`), &count),
	}); err != nil {
		t.Fatal(err)
	}
	if dup != 0 || count != 1 {
		t.Fatalf("duplicated text dup %d count %d", dup, count)
	}
	assertNoSideScroll(t, ctx)
}

func shootRestart(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas", WordDelay: 300 * time.Millisecond})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	user, _ := h.account("rest@example.com", "Rest User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
		shotVisible(t, `textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "hello", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		shotPoll(t, "restart stream started", js(`(function(){var s=document.querySelector(".stream");return !!(s&&s.textContent.indexOf("Hello")>=0);})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	h.srv.Close()
	if err := h.st.FailAbandoned(context.Background(), run.Interrupted); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		pageReady(t),
		shotPoll(t, "interrupted reply and retry", js(`(function(){var t=document.body.innerText||"";return t.indexOf("The reply was interrupted.")>=0&&t.indexOf("Retry")>=0;})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "restart-"+name+".png"))
	assertNoSideScroll(t, ctx)
}

func shootContainer(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newHarnessWith(t, runtime.NewHTTP("http://127.0.0.1:1"))
	user, _ := h.account("box@example.com", "Box User", "correct-horse-1")
	sec, err := h.st.CreateSection(context.Background(), user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Atlas\n", "Reply briefly.\n"); err != nil {
		t.Fatal(err)
	}
	secRow, err := h.st.PutSecret(context.Background(), user.ID, "FIXTURE_TOKEN", "fixture-value-s03")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, secRow.ID); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	guest, token := startGuest(t, h, agent.ID, bootstrap, "800ms")
	client := runtime.NewHTTP(guest.URL())
	client.SetToken(token)
	h.srv.SetClient(client)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	page := ts.URL + "/agents/" + agent.ID
	session := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", session).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
		shotVisible(t, `textarea[name="body"]`, chromedp.ByQuery),
		chromedp.SendKeys(`textarea[name="body"]`, "hello", chromedp.ByQuery),
		chromedp.Click(`.comp button[type="submit"]`, chromedp.ByQuery),
		shotPoll(t, "container stream shows Hello before the rest of the reply", js(`(function(){var s=document.querySelector(".stream");return !!(s&&s.textContent.indexOf("Hello")>=0&&s.textContent.indexOf("today?")<0);})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "streaming-"+name+".png"))
	assertNoSideScroll(t, ctx)
}

func mustEchoURL(t *testing.T, echo *runtime.Echo) string {
	t.Helper()
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	return up.URL
}

type browserGate struct {
	next     http.Handler
	mu       sync.Mutex
	cancel   context.CancelFunc
	badUntil time.Time
}

func (g *browserGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/events") {
		g.next.ServeHTTP(w, r)
		return
	}
	g.mu.Lock()
	until := g.badUntil
	g.mu.Unlock()
	if time.Now().Before(until) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	g.mu.Lock()
	if time.Now().Before(g.badUntil) {
		g.mu.Unlock()
		cancel()
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	if g.cancel != nil {
		g.cancel()
	}
	g.cancel = cancel
	g.mu.Unlock()
	defer cancel()
	g.next.ServeHTTP(w, r.WithContext(ctx))
}

func (g *browserGate) outage(d time.Duration) {
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
	}
	g.badUntil = time.Now().Add(d)
	g.mu.Unlock()
}

func longTab(t *testing.T, browser context.Context, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := chromedp.NewContext(browser)
	t.Cleanup(cancel)
	ctx, timeout := context.WithTimeout(ctx, d)
	t.Cleanup(timeout)
	return ctx
}

func shootAgentForm(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "fixture-shot-key")
	h := newHarness(t)
	h.srv.ModelCatalog().SetOffline()
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s05", "https://castbot.example")
	user, secret := h.account("s05-"+theme+"-"+name+"@example.com", "Shot User", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	models, err := castcfg.Models()
	if err != nil || len(models) == 0 {
		t.Fatal(err)
	}
	model := models[0].ID
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	pageURL := ts.URL + "/new-agent?advanced=1"
	steps := []chromedp.Action{
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := cdppage.AddScriptToEvaluateOnNewDocument(`window.__errs=[];window.addEventListener('securitypolicyviolation',function(e){window.__errs.push(String(e.violatedDirective||'csp'))});window.addEventListener('error',function(e){window.__errs.push(String(e.message||'error'))});`).Do(ctx)
			return err
		}),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(pageURL),
		pageReady(t),
		shotVisible(t, `select[name="model"]`, chromedp.ByQuery),
	}
	for i, step := range steps {
		if err := chromedp.Run(ctx, step); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "empty-"+theme+"-"+name+".png"))
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.SetValue(`form.na input[name="name"]`, "Atlas", chromedp.ByQuery),
		chromedp.SetValue(`form.na input[name="title"]`, "Helper", chromedp.ByQuery),
		chromedp.SetValue(`form.na textarea[name="instructions"]`, "Be concise.", chromedp.ByQuery),
		chromedp.SetValue(`form.na select[name="model"]`, model, chromedp.ByQuery),
		chromedp.Click(`form.na input[value="bash"]`, chromedp.ByQuery),
	}); err != nil {
		t.Fatal(err)
	}
	var filledName string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.querySelector('form.na input[name="name"]').value`), &filledName)); err != nil {
		t.Fatal(err)
	}
	if filledName != "Atlas" {
		t.Fatalf("name field %q", filledName)
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "filled-"+theme+"-"+name+".png"))
	saveFull(t, ctx, filepath.Join(dir, "controls-"+theme+"-"+name+".png"))
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.SetValue(`form.na input[name="name"]`, "", chromedp.ByQuery),
		chromedp.SetValue(`form.na select[name="model"]`, "", chromedp.ByQuery),
		chromedp.Click(`form.na button.pri`, chromedp.ByQuery),
		shotVisible(t, `.err`, chromedp.ByQuery),
	}); err != nil {
		t.Fatal(err)
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "validation-"+theme+"-"+name+".png"))
	zipPath := filepath.Join(t.TempDir(), "agent.zip")
	writeShotZip(t, zipPath, model)
	if err := chromedp.Run(ctx, chromedp.SetUploadFiles(`input[name="archive"]`, []string{zipPath}, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`button[formaction]`, chromedp.ByQuery)); err != nil && !strings.Contains(err.Error(), "navigated or closed") {
		t.Fatal(err)
	}
	var imported string
	if err := chromedp.Run(ctx, chromedp.Tasks{
		shotVisible(t, `ul.skip`, chromedp.ByQuery),
		chromedp.Evaluate(js(`document.body.innerText`), &imported),
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(imported, "Skipped files") || !strings.Contains(imported, ".env") || strings.Contains(imported, "super-secret-value") {
		t.Fatalf("import review %s", imported)
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "import-"+theme+"-"+name+".png"))

	ip := "203.0.113.190:9"
	h.login("s05-"+theme+"-"+name+"@example.com", "correct-horse-1", secret, ip, false)
	agent := seedAgent(t, h.st, user.ID, "Quill")
	if err := h.st.SaveAgentFields(context.Background(), user.ID, agent.ID, store.AgentInput{
		SectionID: agent.SectionID, Name: agent.Name, Title: agent.Title, Model: model,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Quill\nbrain:\n  model: "+model+"\n  instructions_file: instructions.md\ntools:\n  builtin: []\n", "Be concise."); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, guest); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Navigate(ts.URL + "/agents/" + agent.ID + "/edit"),
		pageReady(t),
		shotVisible(t, `input[name="title"]`, chromedp.ByQuery),
		chromedp.SetValue(`form.na input[name="title"]`, "Reloaded helper", chromedp.ByQuery),
	}); err != nil {
		t.Fatal(err)
	}
	label := modelLabel(model)
	editWait(t, ctx, "edit form", `!!document.querySelector('a.sclose[href]') && pageText().indexOf('Cancel')<0 && pageText().indexOf('`+label+`')>=0 && (document.querySelector('select[name="model"]')||{}).value==='`+model+`'`)
	waitVisibleClose(t, ctx)
	frameEditForm(t, ctx)
	assertEditClose(t, ctx)
	assertModelLabelShown(t, ctx, label)
	shotPage(t, ctx, filepath.Join(dir, "editform-"+theme+"-"+name+".png"))
	shotPage(t, ctx, filepath.Join(dir, "edit-"+theme+"-"+name+".png"))
	if err := chromedp.Run(ctx, chromedp.Click(`form.na button.pri`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	editWait(t, ctx, "reload chat", `location.pathname==='/agents/`+agent.ID+`' && pageText().indexOf('Reloaded helper')>=0 && pageText().indexOf('Edit agent')<0`)
	if len(fake.Creates) != 1 {
		t.Fatalf("creates %d", len(fake.Creates))
	}
	var errs []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`window.__errs||[]`), &errs)); err != nil {
		t.Fatal(err)
	}
	for _, note := range errs {
		if note != "" {
			t.Fatalf("console %s", note)
		}
	}
}

func scrollTop(t *testing.T, ctx context.Context) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var m=document.querySelector('.main');if(m)m.scrollTop=0;window.scrollTo(0,0);})()`), nil)); err != nil {
		t.Fatal(err)
	}
}

func shotPage(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	assertNoSideScroll(t, ctx)
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.body.innerText`), &text)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "\u2014") || strings.Contains(text, "\u2013") {
		t.Fatal("em dash")
	}
	saveShot(t, ctx, path)
}

func writeShotZip(t *testing.T, path, model string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	files := map[string]string{
		"agent.yaml":      "name: Imported\ndescription: From a folder\nbrain:\n  model: " + model + "\n  instructions_file: instructions.md\ntools:\n  builtin: [read]\n",
		"instructions.md": "Imported instructions.\n",
		".env":            "API_KEY=super-secret-value\n",
	}
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func tab(t *testing.T, browser context.Context) context.Context {
	t.Helper()
	ctx, cancel := chromedp.NewContext(browser)
	t.Cleanup(cancel)
	ctx, timeout := context.WithTimeout(ctx, 8*time.Minute)
	t.Cleanup(timeout)
	return ctx
}

func sessionToken(t *testing.T, h *harness, userID string) string {
	t.Helper()
	sess, err := h.st.CreateSession(context.Background(), userID, "Mozilla/5.0 Chrome/120.0", true)
	if err != nil {
		t.Fatal(err)
	}
	return seal("session", sess.ID, h.srv.key)
}

func shootMachine(t *testing.T, browser context.Context, dir string, width, height int, name string) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s04", "https://castbot.example")
	user, _ := h.account("machine@example.com", "Machine User", "correct-horse-1")
	agent := seedAgent(t, h.st, user.ID, "Wren")
	vm, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s04")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-shot", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-shot"] = host.Machine{ID: "vm-shot", Name: vm.Name, State: "paused"}
	fake.SetForce("paused")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)
	ctx := tab(t, browser)
	page := ts.URL + "/agents/" + agent.ID + "/machine"
	if err := chromedp.Run(ctx, chromedp.Tasks{
		network.Enable(),
		network.SetCookie("castbot_session", token).WithURL(ts.URL).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
		shotVisible(t, `#confirm-name`, chromedp.ByQuery),
		shotPoll(t, "paused machine", js(`(function(){var t=document.body.innerText||"";return t.indexOf("Paused")>=0&&t.indexOf("Wake")>=0&&t.indexOf("Type Wren")>=0;})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "paused-"+name+".png"))
	assertNoSideScroll(t, ctx)
	fake.SetForce("creating")
	if err := chromedp.Run(ctx, chromedp.Tasks{
		chromedp.Reload(),
		pageReady(t),
		shotPoll(t, "machine waking", js(`(function(){var t=document.body.innerText||"";return t.indexOf("Waking")>=0&&t.indexOf("The machine is waking.")>=0&&t.indexOf("The machine is starting.")<0;})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	saveShot(t, ctx, filepath.Join(dir, "waking-"+name+".png"))
	assertNoSideScroll(t, ctx)
}

func shootCLIExport(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h, fake, agent, token, ts := cliShot(t, theme, name)
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	endpoint := "https://demo.microvm.ondigitalocean.com"
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-shot", endpoint, "running", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, guest); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-shot"] = host.Machine{ID: "vm-shot", State: "running", Endpoint: endpoint}
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, token, ts.URL+"/agents/"+agent.ID+"/machine", width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Tasks{
		shotVisible(t, `#cast-cli button`, chromedp.ByQuery),
		chromedp.Click(`#cast-cli button`, chromedp.ByQuery),
		shotVisible(t, `pre.cli-blob`, chromedp.ByQuery),
		shotPoll(t, "cli export", js(`(function(){var t=document.body.innerText||"";return t.indexOf("microvm")>=0&&t.indexOf("Copy this now")>=0&&t.indexOf("Add to Cast CLI")>=0;})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	shotPage(t, ctx, filepath.Join(dir, "cli-export-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootCLITokens(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h, fake, agent, token, ts := cliShot(t, theme, name)
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-shot", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-shot"] = host.Machine{ID: "vm-shot", State: "paused"}
	fake.SetForce("paused")
	if _, _, err := h.st.IssueCLIToken(context.Background(), agent.UserID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.IssueCLIToken(context.Background(), agent.UserID, agent.ID); err != nil {
		t.Fatal(err)
	}
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, token, ts.URL+"/agents/"+agent.ID+"/machine", width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Tasks{
		shotVisible(t, `#cast-cli .tok button`, chromedp.ByQuery),
		chromedp.Click(`#cast-cli .tok button`, chromedp.ByQuery),
		shotPoll(t, "cli tokens", js(`(function(){var t=document.body.innerText||"";return t.indexOf("Revoked")>=0&&t.indexOf("Not yet")>=0&&t.indexOf("Not shown")>=0;})()`)),
	}); err != nil {
		t.Fatal(err)
	}
	shotPage(t, ctx, filepath.Join(dir, "cli-tokens-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootCLIThread(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h, _, agent, token, ts := cliShot(t, theme, name)
	if _, err := h.st.IngestJournal(context.Background(), agent.UserID, agent.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-10T18:00:00Z", SessionID: "sess-shot", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "request", Text: "from the cli"},
		{Seq: 2, At: "2026-11-10T18:00:01Z", SessionID: "sess-shot", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "reply", Text: "echo from the cli"},
		{Seq: 3, At: "2026-11-10T18:00:02Z", SessionID: "sess-shot", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "permission", Tool: "bash"},
	}); err != nil {
		t.Fatal(err)
	}
	threads, err := h.st.ListThreadsByAgent(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, th := range threads {
		if th.Kind == "cli" {
			id = th.ID
		}
	}
	if id == "" {
		t.Fatal("missing cli thread")
	}
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, token, ts.URL+"/agents/"+agent.ID+"?thread="+id, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "cli thread", js(`(function(){var t=document.body.innerText||"";return t.indexOf("Cast CLI")>=0&&t.indexOf("from the cli")>=0&&t.indexOf("Answer it in the Cast CLI")>=0;})()`))); err != nil {
		t.Fatal(err)
	}
	shotPage(t, ctx, filepath.Join(dir, "cli-thread-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootApproval(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h := newHarness(t)
	user, secret := h.account("s07b-"+kind+"-"+theme+"-"+name+"@example.com", "Demo User", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetTimezone(context.Background(), user.ID, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 10, 20, 42, 0, 0, time.UTC)
	cur := when
	h.st.SetNow(func() time.Time { return cur })
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	var page string
	var ask *runtime.Ask
	if kind == "pending" {
		ask = runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "ls demo", "api_key": "demo-secret-value"}})
		up := httptest.NewServer(ask)
		t.Cleanup(up.Close)
		h.srv.SetClient(runtime.NewHTTP(up.URL))
		ip := "203.0.113.60:9"
		h.login("s07b-"+kind+"-"+theme+"-"+name+"@example.com", "correct-horse-1", secret, ip, false)
		code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"List the demo folder"}}), ip, map[string]string{"HX-Request": "true"})
		if code != http.StatusOK {
			t.Fatalf("post %d %s", code, body)
		}
		deadline := time.Now().Add(8 * time.Second)
		for {
			rows, err := h.st.ListPendingApprovals(context.Background(), user.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("no approval")
			}
			time.Sleep(20 * time.Millisecond)
		}
		c := h.jar["castbot_session"]
		if c == nil {
			t.Fatal("no session")
		}
		page = "/agents/" + agent.ID
		ts := httptest.NewTLSServer(h.srv.Handler())
		t.Cleanup(ts.Close)
		ctx := tab(t, browser)
		if err := chromedp.Run(ctx, cliOpen(t, ts.URL, c.Value, ts.URL+page, width, height)...); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Run(ctx, shotPoll(t, "pending approval", js(`(function(){var t=document.body.innerText||"";return t.indexOf("Approve")>=0&&t.indexOf("Deny")>=0&&t.indexOf("ls demo")>=0&&t.indexOf("Hidden")>=0&&t.indexOf("demo-secret-value")<0;})()`))); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var n=document.querySelector(".appr");if(n)n.scrollIntoView({block:"center"});return true;})()`), new(bool))); err != nil {
			t.Fatal(err)
		}
		shotPage(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
		assertShotClean(t, ctx)
		if len(ask.Calls()) != 0 {
			t.Fatalf("shot posted %#v", ask.Calls())
		}
		// Approve after the shot so the runtime stream and the page
		// stream both finish. An open stream keeps the test server in Close.
		rows, err := h.st.ListPendingApprovals(context.Background(), user.ID)
		if err != nil || len(rows) != 1 {
			t.Fatalf("pending %v %d", err, len(rows))
		}
		if code, body := postDecision(h, rows[0], ip, true); code != http.StatusOK {
			t.Fatalf("release %d %s", code, body)
		}
		deadline = time.Now().Add(8 * time.Second)
		for len(ask.Calls()) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("approval did not finish")
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = chromedp.Run(ctx, chromedp.Navigate("about:blank"))
		return
	}
	if kind == "cli" {
		if _, err := h.st.IngestJournal(context.Background(), user.ID, agent.ID, []store.JournalEntry{
			{Seq: 1, At: "2026-11-10T20:40:00Z", SessionID: "sess-shot", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "request", Text: "List the demo folder"},
			{Seq: 2, At: "2026-11-10T20:41:00Z", SessionID: "", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "decision", RequestID: "req-shot", Decision: "approved"},
			{Seq: 3, At: "2026-11-10T20:42:00Z", SessionID: "sess-shot", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "permission", Tool: "bash", RequestID: "req-shot"},
		}); err != nil {
			t.Fatal(err)
		}
		threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range threads {
			if th.Kind == "cli" {
				page = "/agents/" + agent.ID + "?thread=" + th.ID
			}
		}
		if page == "" {
			t.Fatal("missing cli thread")
		}
	} else {
		cur = when.Add(-2 * time.Minute)
		threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
		if err != nil || len(threads) == 0 {
			t.Fatal(err)
		}
		if _, err := h.st.CreateMessage(context.Background(), user.ID, threads[0].ID, "user", user.ID, "List the demo folder"); err != nil {
			t.Fatal(err)
		}
		runRow, err := h.st.CreateRun(context.Background(), user.ID, threads[0].ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		cur = when.Add(-time.Minute)
		raw := json.RawMessage(`{"id":"req-demo","tool":"bash","arguments":{"command":"ls demo","api_key":"demo-secret-value"}}`)
		ap, err := h.st.RecordPermission(context.Background(), user.ID, runRow.ID, raw, when, 1)
		if err != nil {
			t.Fatal(err)
		}
		cur = when
		switch kind {
		case "approved":
			if _, claimed, err := h.st.ClaimDecision(context.Background(), user.ID, ap.ID, runRow.ID, "approved", user.ID); err != nil || !claimed {
				t.Fatal(err)
			}
			if _, _, err := h.st.FinishRun(context.Background(), user.ID, runRow.ID, "bash finished.", 2); err != nil {
				t.Fatal(err)
			}
		case "denied":
			if _, claimed, err := h.st.ClaimDecision(context.Background(), user.ID, ap.ID, runRow.ID, "denied", user.ID); err != nil || !claimed {
				t.Fatal(err)
			}
			if _, _, err := h.st.FinishRun(context.Background(), user.ID, runRow.ID, runtime.DenialMessage("bash"), 2); err != nil {
				t.Fatal(err)
			}
		case "expired":
			if _, err := h.st.ExpireDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, _, err := h.st.FinishRun(context.Background(), user.ID, runRow.ID, runtime.DenialMessage("bash"), 2); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("kind %s", kind)
		}
		page = "/agents/" + agent.ID
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	token := sessionToken(t, h, user.ID)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, token, ts.URL+page, width, height)...); err != nil {
		t.Fatal(err)
	}
	needle := "Expired"
	switch kind {
	case "approved":
		needle = "Approved by you at 3:42 PM"
	case "denied":
		needle = "Denied"
	case "cli":
		needle = "An approval was asked for bash."
	}
	visible := needle
	if kind == "approved" {
		visible = "Approved"
	}
	fold := ""
	if kind == "approved" || kind == "denied" || kind == "expired" {
		fold = `&&full.indexOf("` + needle + `")>=0&&fold&&!fold.open&&t.indexOf("bash")>=0`
		if kind == "approved" {
			fold += `&&t.indexOf("3:42 PM")>=0`
		}
	}
	if err := chromedp.Run(ctx, shotPoll(t, "approval "+needle, js(`(function(){var t=document.body.innerText||"";var full=document.body.textContent||"";var fold=document.querySelector("details.appr-fold");return t.indexOf("`+visible+`")>=0&&t.indexOf("demo-secret-value")<0`+fold+`;})()`))); err != nil {
		t.Fatal(err)
	}
	if kind == "cli" {
		var ordered bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var t=document.body.innerText||"";var a=t.indexOf("An approval was asked for bash.");var d=t.indexOf("An approval for bash was approved.");return a>=0&&d>a;})()`), &ordered)); err != nil {
			t.Fatal(err)
		}
		if !ordered {
			t.Fatal("cli cards are not asked then decided")
		}
	}
	if kind == "cli" {
		var writable bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`!!document.querySelector(".appr button")`), &writable)); err != nil {
			t.Fatal(err)
		}
		if writable {
			t.Fatal("cli card has a button")
		}
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var n=document.querySelector(".appr");if(n)n.scrollIntoView({block:"center"});return true;})()`), new(bool))); err != nil {
		t.Fatal(err)
	}
	shotPage(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func cliShot(t *testing.T, theme, name string) (*harness, *host.Fake, store.Agent, string, *httptest.Server) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s06", "https://castbot.example")
	user, _ := h.account("s06-"+theme+"-"+name+"@example.com", "Shot User", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	agent := seedAgent(t, h.st, user.ID, "Wren")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	return h, fake, agent, sessionToken(t, h, user.ID), ts
}

func cliOpen(t *testing.T, base, token, page string, width, height int) []chromedp.Action {
	return []chromedp.Action{
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := cdppage.AddScriptToEvaluateOnNewDocument(`window.__errs=[];window.addEventListener('securitypolicyviolation',function(e){window.__errs.push(String(e.violatedDirective||'csp'))});window.addEventListener('error',function(e){window.__errs.push(String(e.message||'error'))});`).Do(ctx)
			return err
		}),
		network.SetCookie("castbot_session", token).WithURL(base).WithSecure(true).WithHTTPOnly(true).WithSameSite(network.CookieSameSiteStrict),
		chromedp.EmulateViewport(int64(width), int64(height)),
		chromedp.Navigate(page),
		pageReady(t),
	}
}

func assertShotClean(t *testing.T, ctx context.Context) {
	t.Helper()
	var errs []string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`window.__errs||[]`), &errs)); err != nil {
		t.Fatal(err)
	}
	for _, note := range errs {
		if note != "" {
			t.Fatalf("console %s", note)
		}
	}
}

func seedAgent(t *testing.T, st *store.Store, userID, name string) store.Agent {
	t.Helper()
	sec, err := st.CreateSection(context.Background(), userID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(context.Background(), userID, store.AgentInput{
		SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func saveFull(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	var height int
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(() => {
		const app = document.querySelector('.app');
		const main = document.querySelector('.main');
		const sb = document.querySelector('.sb');
		if (app) app.style.height = 'auto';
		if (main) { main.style.height = 'auto'; main.style.overflow = 'visible'; }
		if (sb) { sb.style.height = 'auto'; sb.style.overflow = 'visible'; }
		const card = document.querySelector('.na');
		return card ? Math.ceil(card.getBoundingClientRect().bottom + window.scrollY) : 0;
	})()`), &height)); err != nil {
		t.Fatal(err)
	}
	if height < 700 {
		t.Fatalf("form bottom %d", height)
	}
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
	if len(buf) < 24 || string(buf[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("screenshot %s is not a png (%d bytes)", path, len(buf))
	}
	wide := int(buf[16])<<24 | int(buf[17])<<16 | int(buf[18])<<8 | int(buf[19])
	tall := int(buf[20])<<24 | int(buf[21])<<16 | int(buf[22])<<8 | int(buf[23])
	if tall < 1100 || wide > 1280 {
		t.Fatalf("screenshot %s is %dx%d", path, wide, tall)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func saveShot(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		t.Fatal(err)
	}
	if len(buf) < 1000 {
		t.Fatalf("screenshot %s is %d bytes", path, len(buf))
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func retryInsideExpr() string {
	return `(function(){var box=document.querySelector(".ch-body");var btns=box&&box.querySelectorAll(".errbox button, .calm button");var btn=btns&&btns.length?btns[btns.length-1]:null;if(!box||!btn)return false;var a=box.getBoundingClientRect();var b=btn.getBoundingClientRect();return b.top>=a.top-1&&b.bottom<=a.bottom+1&&b.left>=a.left-1&&b.right<=a.right+1;})()`
}

func retryInside() string {
	return js(`(function(){var t=document.body.innerText||"";return t.indexOf("The agent is not reachable right now.")>=0&&t.indexOf("Retry")>=0&&` + retryInsideExpr() + `;})()`)
}

func assertNoSideScroll(t *testing.T, ctx context.Context) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.documentElement.scrollWidth<=document.documentElement.clientWidth&&document.body.scrollWidth<=document.body.clientWidth`), &ok)); err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("sideways scroll")
	}
}

func js(s string) string { return s }

func quietLog(string, ...interface{}) {}
