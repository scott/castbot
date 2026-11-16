//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

func TestShotsGroupEdit(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	dir := filepath.Join("..", "..", "docs", "evidence", "105")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, theme := range []string{"dark", "light"} {
		theme := theme
		base, session, groupID := seedGroupShots(t, theme)
		for _, vp := range []struct {
			w, h int
			name string
		}{{1280, 800, "1280"}, {390, 844, "390"}} {
			vp := vp
			for _, kind := range []string{"sidebar", "group", "edit", "delete", "new"} {
				kind := kind
				t.Run(kind+"-"+theme+"-"+vp.name, func(t *testing.T) {
					shootGroup(t, browser, dir, base, session, groupID, vp.w, vp.h, theme, vp.name, kind)
				})
			}
		}
	}
}

func seedGroupShots(t *testing.T, theme string) (base, session, groupID string) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	when := time.Date(2026, 11, 16, 18, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return when })
	ctx := context.Background()
	owner, _ := h.account("alex-105-"+theme+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(ctx, owner.ID, theme); err != nil {
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
	research, err := h.st.CreateSection(ctx, owner.ID, "Research")
	if err != nil {
		t.Fatal(err)
	}
	standup, err := h.st.CreateGroup(ctx, owner.ID, "Standup", []string{atlas.ID, pip.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateGroup(ctx, owner.ID, "Planning", []string{atlas.ID, pip.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.st.PlaceGroup(ctx, owner.ID, standup.ID, research.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetGroupPin(ctx, owner.ID, standup.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ToggleFold(ctx, owner.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, owner.ID, standup.ID, "user", owner.ID, "What should we cover today?"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, owner.ID, standup.ID, "agent", atlas.ID, "I can take the first pass."); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	return ts.URL, sessionToken(t, h, owner.ID), standup.ID
}

func shootGroup(t *testing.T, browser context.Context, dir, base, session, groupID string, width, height int, theme, name, kind string) {
	t.Helper()
	page := base + "/?list=1"
	needle := `(function(){var t=(document.body.innerText||'').toLowerCase();var fold=document.querySelector('button[aria-label="Expand Agents"]');return t.indexOf('research')>=0&&t.indexOf('standup')>=0&&t.indexOf('pinned')>=0&&t.indexOf('planning')>=0&&t.indexOf('group chat')>=0&&t.indexOf('atlas')<0&&t.indexOf('pip')<0&&!!fold&&t.indexOf('\u2014')<0&&t.indexOf('\u2013')<0;})()`
	switch kind {
	case "group":
		page = base + "/groups/" + groupID
		needle = `(function(){var t=document.body.innerText||'';var sel=document.querySelector('select[name="section"]');return t.indexOf('Unpin')>=0&&t.indexOf('Edit')>=0&&t.indexOf('Delete')>=0&&t.indexOf('Move')>=0&&t.indexOf('Agents here can reply.')>=0&&t.indexOf('Mention one with @Name when you want that agent to reply.')>=0&&t.indexOf('What should we cover today?')>=0&&t.indexOf('I can take the first pass.')>=0&&t.indexOf('Mention an agent with @Name and that agent replies here.')<0&&!!sel&&sel.value!=='groups'&&t.indexOf('\u2014')<0;})()`
	case "edit":
		page = base + "/groups/" + groupID + "/edit"
		needle = `(function(){var t=document.body.innerText||'';var title=document.querySelector('input[name="title"]');var boxes=document.querySelectorAll('input[name="agent"]:checked');return t.indexOf('Edit group')>=0&&t.indexOf('You stay in this group.')>=0&&t.indexOf('Cancel')>=0&&t.indexOf('Save')>=0&&!!title&&title.value==='Standup'&&boxes.length>=2&&t.indexOf('\u2014')<0;})()`
	case "delete":
		page = base + "/groups/" + groupID + "/delete"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Delete Standup?')>=0&&t.indexOf('The chat is removed for everyone.')>=0&&t.indexOf('The agents are kept.')>=0&&t.indexOf('Delete group')>=0&&t.indexOf('Cancel')>=0&&t.indexOf('\u2014')<0;})()`
	case "new":
		page = base + "/groups/new"
		needle = `(function(){var t=document.body.innerText||'';var cancel=document.querySelector('a[href="/"]');return t.indexOf('New group')>=0&&t.indexOf('They can reply on their own.')>=0&&t.indexOf('This places the group in your sidebar only.')>=0&&t.indexOf('Cancel')>=0&&t.indexOf('Create group')>=0&&t.indexOf('A group needs at least two of your agents.')<0&&!!cancel&&t.indexOf('\u2014')<0;})()`
	}
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, base, session, page, width, height)...); err != nil {
		t.Fatalf("open %s: %v", kind, err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "105 "+kind, js(needle))); err != nil {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &text))
		t.Fatalf("needle %s: %v\n%s", kind, err, text)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
}
