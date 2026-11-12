//go:build shots

package web

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

// shotSecretValue is stored and must never appear in a page or a screenshot source.
const shotSecretValue = "vault-sample-value"

func shootVault(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h, user, path, ready := vaultShot(t, theme, kind)
	ctx := openS07(t, browser, h, user.ID, path, width, height, ready)
	if kind == "10b" {
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
			document.querySelectorAll('.gpick input').forEach(function(el){
				var n = el.getAttribute('data-name');
				if (n === 'Atlas' || n === 'Pip') { el.checked = true; el.dispatchEvent(new Event('change', {bubbles:true})); }
			});
		})()`), nil)); err != nil {
			t.Fatal(err)
		}
		if err := chromedp.Run(ctx, chromedp.SendKeys(`#sec-value`, "typed-demo", chromedp.ByQuery)); err != nil {
			t.Fatal(err)
		}
		waitText(t, ctx, "Atlas and Pip reload to get it")
	}
	assertShotClean(t, ctx)
	assertNoSideScroll(t, ctx)
	assertVaultShot(t, ctx, height)
	saveVaultShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"), width)
}

func saveVaultShot(t *testing.T, ctx context.Context, path string, width int) {
	t.Helper()
	var tall int
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(() => {
		const drawer = document.querySelector('.drawer');
		if (drawer) return Math.ceil(drawer.scrollHeight + 16);
		if (document.querySelector('.modal')) return 0;
		const wrap = document.querySelector('.vwrap') || document.querySelector('.s19pane');
		if (!wrap) return 0;
		const main = document.querySelector('.main');
		const app = document.querySelector('.app');
		if (main) { main.style.height = 'auto'; main.style.overflow = 'visible'; }
		if (app) app.style.height = 'auto';
		document.documentElement.style.height = 'auto';
		document.body.style.height = 'auto';
		const bottom = wrap.getBoundingClientRect().bottom + window.scrollY;
		if (!(bottom > 200 && bottom < 4000)) return 0;
		return Math.ceil(bottom + 24);
	})()`), &tall)); err != nil {
		t.Fatal(err)
	}
	if tall > 400 && tall < 4000 {
		if err := chromedp.Run(ctx, chromedp.EmulateViewport(int64(width), int64(tall))); err != nil {
			t.Fatal(err)
		}
	}
	var buf []byte
	if err := chromedp.Run(ctx, chromedp.CaptureScreenshot(&buf)); err != nil {
		t.Fatal(err)
	}
	if len(buf) < 1000 || string(buf[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("screenshot %s is not a png (%d bytes)", path, len(buf))
	}
	wide := int(buf[16])<<24 | int(buf[17])<<16 | int(buf[18])<<8 | int(buf[19])
	if wide > width*2 {
		t.Fatalf("screenshot %s is %d px wide", path, wide)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertVaultShot(t *testing.T, ctx context.Context, height int) {
	t.Helper()
	var html, bad string
	var dashes bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.documentElement.outerHTML`), &html)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, shotSecretValue) {
		t.Fatal("shot page contains the stored secret")
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(document.body.innerText||"").indexOf("\u2014")<0 && (document.body.innerText||"").indexOf("\u2013")<0`), &dashes)); err != nil {
		t.Fatal(err)
	}
	if !dashes {
		t.Fatal("shot copy has a dash")
	}
	if height >= 800 {
		return
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		var bad = [];
		document.querySelectorAll('.vwrap .btn, .drawer .btn, .smodal .btn, .stoc a, .ptabs a, .sclose, .revoke, a.back-to').forEach(function(el){
			var r = el.getBoundingClientRect();
			if (r.width < 1 || r.height < 1) return;
			if (r.height < 44 || r.width < 44) bad.push((el.getAttribute('aria-label')||el.textContent||'').trim().slice(0,40)+' '+Math.round(r.width)+'x'+Math.round(r.height));
		});
		return bad.join(' | ');
	})()`), &bad)); err != nil {
		t.Fatal(err)
	}
	if bad != "" {
		t.Fatalf("tap target %s", bad)
	}
}

type vaultIDs struct {
	atlas, pip, juniper, ledger, wren string
	github, docs, stripe, slack, key  string
}

func vaultShot(t *testing.T, theme, kind string) (*harness, store.User, string, string) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("alex@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetTimezone(context.Background(), user.ID, "America/Denver"); err != nil {
		t.Fatal(err)
	}
	ids := seedVaultCast(t, h.st, user.ID, kind != "10a2")
	path, ready := vaultPath(kind, ids)
	if kind == "10d2" || kind == "10d3" || kind == "10e" {
		stampVaultNotes(h, user.ID, ids, kind)
	}
	return h, user, path, ready
}

func vaultPath(kind string, ids vaultIDs) (string, string) {
	switch kind {
	case "10a":
		return "/settings/secrets", `(function(){var t=document.body.innerText||"";return t.indexOf("GITHUB_TOKEN")>=0 && t.indexOf("Agent secrets")>=0;})()`
	case "10a2":
		return "/settings/secrets", `(function(){return (document.body.innerText||"").indexOf("No secrets yet")>=0;})()`
	case "10b":
		return "/settings/secrets/new?name=SEARCH_API_KEY", `(function(){return (document.body.innerText||"").indexOf("Which agents get it")>=0;})()`
	case "10b2":
		return "/settings/secrets/new?bad=1&name=search-api%20key", `(function(){return (document.body.innerText||"").indexOf("Use capital letters")>=0;})()`
	case "10b3":
		return "/settings/secrets/new?taken=1&name=GITHUB_TOKEN", `(function(){return (document.body.innerText||"").indexOf("already have a secret named GITHUB_TOKEN")>=0;})()`
	case "10c":
		return "/settings/secrets/item/" + ids.github, `(function(){return (document.body.innerText||"").indexOf("Hidden after save")>=0;})()`
	case "10d":
		return "/settings/secrets/item/" + ids.github + "/replace", `(function(){return (document.body.innerText||"").indexOf("Replace GITHUB_TOKEN?")>=0;})()`
	case "10d2":
		return "/settings/secrets/item/" + ids.github + "?watch=1", `(function(){return (document.body.innerText||"").indexOf("New value saved")>=0;})()`
	case "10d3":
		return "/settings/secrets/item/" + ids.github + "?watch=1", `(function(){return (document.body.innerText||"").indexOf("GITHUB_TOKEN replaced")>=0;})()`
	case "10e":
		return "/settings/secrets/item/" + ids.github + "?watch=1", `(function(){return (document.body.innerText||"").indexOf("did not reload")>=0;})()`
	case "10f":
		return "/agents/" + ids.atlas + "?panel=secrets", `(function(){var t=document.body.innerText||"";var main=document.querySelector(".main");var shown=main&&getComputedStyle(main).display!=="none";var chat=!shown||(main.innerText||"").indexOf("Check docs/")>=0;return t.indexOf("GITHUB_TOKEN")>=0 && t.indexOf("OpenRouter key")>=0 && chat;})()`
	case "10f2":
		return "/agents/" + ids.atlas + "?panel=secrets&revoke=" + ids.github, `(function(){return (document.body.innerText||"").indexOf("Take GITHUB_TOKEN away from Atlas?")>=0;})()`
	case "10f3":
		return "/agents/" + ids.atlas + "?panel=secrets&give=1", `(function(){return (document.body.innerText||"").indexOf("Give Atlas a secret")>=0;})()`
	case "10g":
		return "/settings/secrets/item/" + ids.github + "/delete", `(function(){return (document.body.innerText||"").indexOf("Delete GITHUB_TOKEN?")>=0;})()`
	case "10g2":
		return "/settings/secrets/item/" + ids.key + "/delete", `(function(){var t=document.body.innerText||"";return t.indexOf("Replace this key, or change those agents' model first.")>=0 && t.indexOf("Agent secrets")>=0;})()`
	default:
		return "/settings/secrets", `true`
	}
}

func stampVaultNotes(h *harness, userID string, ids vaultIDs, kind string) {
	one := time.Date(2026, 11, 3, 20, 4, 0, 0, time.UTC)
	two := time.Date(2026, 11, 3, 20, 5, 0, 0, time.UTC)
	h.srv.noteReload(userID, ids.atlas, ids.github, "reloaded")
	setNoteAt(h.srv, userID, ids.atlas, one)
	switch kind {
	case "10d2":
		h.srv.noteReload(userID, ids.juniper, ids.github, "deferred")
		h.srv.noteReload(userID, ids.wren, ids.github, "wake")
	case "10d3":
		h.srv.noteReload(userID, ids.juniper, ids.github, "reloaded")
		setNoteAt(h.srv, userID, ids.juniper, two)
		h.srv.noteReload(userID, ids.wren, ids.github, "wake")
	case "10e":
		h.srv.noteReload(userID, ids.juniper, ids.github, "failed")
	}
}

func setNoteAt(s *Server, userID, agentID string, at time.Time) {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	key := userID + "\x00" + agentID
	note := s.vaultReload[key]
	note.At = at
	s.vaultReload[key] = note
}

func seedVaultCast(t *testing.T, st *store.Store, userID string, withSecrets bool) vaultIDs {
	t.Helper()
	ctx := context.Background()
	pinned, err := st.SystemSection(ctx, userID, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	agents, err := st.SystemSection(ctx, userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	var ids vaultIDs
	mk := func(sec, name, title, color, status, activity, model string) string {
		st.SetNow(func() time.Time { return time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC) })
		agent, thread, err := st.CreateAgent(ctx, userID, store.AgentInput{
			SectionID: sec, Name: name, Title: title, Color: color,
			Status: status, Activity: activity, Model: model,
		})
		if err != nil {
			t.Fatal(err)
		}
		if name == "Atlas" {
			if _, err := st.CreateMessage(ctx, userID, thread.ID, "user", userID, "Check docs/ for broken links and fix what you can on a branch."); err != nil {
				t.Fatal(err)
			}
			if _, err := st.CreateMessage(ctx, userID, thread.ID, "agent", agent.ID, "Done. I fixed 3 broken links in docs/setup.md and docs/faq.md on the branch docs/fix-links."); err != nil {
				t.Fatal(err)
			}
		}
		return agent.ID
	}
	model := defaultModelID()
	ids.atlas = mk(pinned.ID, "Atlas", "Docs helper", "c1", "ready", "", model)
	ids.pip = mk(agents.ID, "Pip", "Inbox", "c2", "ready", "", model)
	ids.juniper = mk(agents.ID, "Juniper", "Research", "c3", "provisioning", "Resuming", model)
	ids.ledger = mk(agents.ID, "Ledger", "Books", "c4", "paused", "", model)
	ids.wren = mk(agents.ID, "Wren", "Notes", "c6", "paused", "", "")
	_ = mk(agents.ID, "Moss", "Ops", "c5", "failed", "", "")
	if !withSecrets {
		return ids
	}
	put := func(name, label, provider string, at time.Time) string {
		st.SetNow(func() time.Time { return at })
		sec, err := st.CreateSecret(ctx, userID, store.SecretInput{Name: name, Label: label, Provider: provider, Value: shotSecretValue})
		if err != nil {
			t.Fatal(err)
		}
		return sec.ID
	}
	ids.key = put("PROVIDER_A_MAIN", "provider-a-main", "a", time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC))
	ids.github = put("GITHUB_TOKEN", "", "", time.Date(2026, 10, 28, 15, 0, 1, 0, time.UTC))
	ids.docs = put("DOCS_REPO_TOKEN", "", "", time.Date(2026, 10, 28, 15, 1, 0, 0, time.UTC))
	ids.stripe = put("STRIPE_TEST_KEY", "", "", time.Date(2026, 11, 2, 16, 0, 0, 0, time.UTC))
	ids.slack = put("SLACK_WEBHOOK_URL", "", "", time.Date(2026, 11, 5, 16, 0, 0, 0, time.UTC))
	grant := func(agent, secret string) {
		if err := st.GrantSecret(ctx, userID, agent, secret); err != nil {
			t.Fatal(err)
		}
	}
	grant(ids.atlas, ids.github)
	grant(ids.juniper, ids.github)
	grant(ids.wren, ids.github)
	grant(ids.atlas, ids.docs)
	grant(ids.ledger, ids.stripe)
	st.SetNow(func() time.Time { return time.Date(2026, 10, 28, 20, 20, 0, 0, time.UTC) })
	if err := st.RecordSecretAudit(ctx, userID, "secret.added", ids.github, secretEvent{Name: "GITHUB_TOKEN", Agent: "Atlas", AgentID: ids.atlas}); err != nil {
		t.Fatal(err)
	}
	st.SetNow(func() time.Time { return time.Date(2026, 10, 30, 17, 5, 0, 0, time.UTC) })
	if err := st.RecordSecretAudit(ctx, userID, "secret.granted", ids.github, secretEvent{Name: "GITHUB_TOKEN", Agent: "Juniper", AgentID: ids.juniper}); err != nil {
		t.Fatal(err)
	}
	st.SetNow(func() time.Time { return time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC) })
	if err := st.RecordSecretAudit(ctx, userID, "secret.granted", ids.github, secretEvent{Name: "GITHUB_TOKEN", Agent: "Wren", AgentID: ids.wren}); err != nil {
		t.Fatal(err)
	}
	st.SetNow(func() time.Time { return time.Date(2026, 11, 3, 23, 12, 0, 0, time.UTC) })
	if _, err := st.ReplaceSecret(ctx, userID, ids.github, shotSecretValue); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSecretAudit(ctx, userID, "secret.replaced", ids.github, secretEvent{Name: "GITHUB_TOKEN", Email: "alex@example.com"}); err != nil {
		t.Fatal(err)
	}
	return ids
}
