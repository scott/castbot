//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

func TestShotsS13(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	dir := filepath.Join("..", "..", "docs", "evidence", "S13")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		theme := theme
		t.Run(theme, func(t *testing.T) {
			base, session, groupID, invitePath := seedS13(t, theme)
			for _, vp := range []struct {
				w, h int
				name string
			}{{1280, 800, "1280"}, {390, 844, "390"}} {
				vp := vp
				for _, kind := range []string{"people", "usage", "group", "new", "invite"} {
					kind := kind
					t.Run(kind+"-"+vp.name, func(t *testing.T) {
						shootS13(t, browser, dir, base, session, groupID, invitePath, vp.w, vp.h, theme, vp.name, kind)
					})
				}
			}
		})
	}
}

func seedS13(t *testing.T, theme string) (base, session, groupID, invitePath string) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	when := time.Date(2026, 11, 16, 16, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return when })
	ctx := context.Background()
	owner, _ := h.account("alex-s13-"+theme+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(ctx, owner.ID, theme); err != nil {
		t.Fatal(err)
	}
	_, memberToken, err := h.st.CreateInvite(ctx, owner.ID, "quinn-s13-"+theme+"@example.com", "Quinn Hale")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.Hash("correct-horse-1", h.st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	totp, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	member, err := h.st.AcceptInvite(ctx, memberToken, hash, totp)
	if err != nil {
		t.Fatal(err)
	}
	_, pendingToken, err := h.st.CreateInvite(ctx, owner.ID, "pip-s13-"+theme+"@example.com", "Pip Lane")
	if err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	atlas, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	pip, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Pip", Title: "Notes", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, owner.ID, atlas.ID, "vm-s13", "example.invalid/castbot-runtime:s13"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(ctx, owner.ID, atlas.ID, "vm-s13", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	group, err := h.st.CreateGroup(ctx, owner.ID, "Standup", []string{atlas.ID, pip.ID}, []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, owner.ID, group.ID, "user", owner.ID, "@Atlas and @Pip can you review the notes?"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, owner.ID, group.ID, "agent", atlas.ID, "I can review the notes."); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, owner.ID, group.ID, "agent", pip.ID, "I will take the notes."); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateGroupMessage(ctx, member.ID, group.ID, "Thanks, I will read them too."); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL, sessionToken(t, h, owner.ID), group.ID, "/invite/" + pendingToken
}

func shootS13(t *testing.T, browser context.Context, dir, base, session, groupID, invitePath string, width, height int, theme, name, kind string) {
	t.Helper()
	page := base + "/settings#people"
	needle := `(function(){var t=document.body.innerText||'';return t.indexOf('People')>=0&&t.indexOf('Pip Lane')>=0&&t.indexOf('Invite pending')>=0&&t.indexOf('Quinn Hale')>=0&&t.indexOf('Create invite')>=0&&t.indexOf('Invite link')<0&&t.indexOf('cbi_')<0&&t.indexOf('\u2014')<0&&t.indexOf('\u2013')<0;})()`
	scroll := `#people`
	switch kind {
	case "usage":
		page = base + "/settings#usage"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Your machines')>=0&&t.indexOf('1 of 4')>=0&&t.indexOf('Your uploads')>=0&&t.indexOf('0 B of 2 GB')>=0&&t.indexOf('cbi_')<0&&t.indexOf('\u2014')<0;})()`
		scroll = `#usage`
	case "group":
		page = base + "/groups/" + groupID
		needle = `(function(){var t=document.body.innerText||'';var box=document.querySelector('#composer textarea');return t.indexOf('Standup')>=0&&t.indexOf('@Atlas and @Pip can you review the notes?')>=0&&t.indexOf('I can review the notes.')>=0&&t.indexOf('I will take the notes.')>=0&&t.indexOf('Thanks, I will read them too.')>=0&&!!box&&box.placeholder==='Message the group'&&t.indexOf('OPENROUTER')<0&&t.indexOf('\u2014')<0;})()`
		scroll = `#composer`
	case "new":
		page = base + "/groups/new"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('New group')>=0&&t.indexOf('Atlas')>=0&&t.indexOf('Pip')>=0&&t.indexOf('Quinn Hale')>=0&&t.indexOf('Checked people can read this chat and post in it.')>=0&&t.indexOf('A group needs at least two of your agents.')<0&&t.indexOf('\u2014')<0;})()`
		scroll = `form[action="/groups"]`
	case "invite":
		page = base + invitePath
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Join Personal')>=0&&t.indexOf('Alex Rivera invited Pip Lane')>=0&&t.indexOf('Create account')>=0&&t.indexOf('cbi_')<0&&t.indexOf('\u2014')<0;})()`
		scroll = `h1`
	}
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, base, session, page, width, height)...); err != nil {
		t.Fatalf("open %s: %s", kind, redactInvite(err.Error(), invitePath))
	}
	if err := chromedp.Run(ctx, shotPoll(t, "s13 "+kind, js(needle))); err != nil {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &text))
		t.Fatalf("needle %s: %s\n%s", kind, redactInvite(err.Error(), invitePath), redactInvite(text, invitePath))
	}
	if kind == "people" || kind == "usage" {
		var ok bool
		err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var el=document.querySelector(`+strconv.Quote(scroll)+`);if(!el)return false;el.scrollIntoView({block:'start',inline:'nearest'});var sc=el.closest('.main');var bar=document.querySelector('.ph-top');var pad=bar?bar.getBoundingClientRect().height+8:12;if(sc){sc.scrollTop=Math.max(0,sc.scrollTop-pad);}return true;})()`), &ok))
		if err != nil || !ok {
			t.Fatalf("scroll %s", kind)
		}
	} else if err := chromedp.Run(ctx, chromedp.ScrollIntoView(scroll, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
}

func redactInvite(msg, invitePath string) string {
	if invitePath == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, invitePath, "/invite/redacted")
	if i := strings.Index(msg, "cbi_"); i >= 0 {
		end := i
		for end < len(msg) && msg[end] != ' ' && msg[end] != '"' && msg[end] != '\'' && msg[end] != '\n' {
			end++
		}
		msg = msg[:i] + "cbi_redacted" + msg[end:]
	}
	return msg
}
