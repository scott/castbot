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
		needle = `(function(){var t=document.body.innerText||'';var sel=document.querySelector('#agent-panel select[name="section"]');var tog=document.querySelector('[aria-label="Group panel"]');var boxes=document.querySelectorAll('#agent-panel input[name="agent"]');return !!tog&&t.indexOf('Unpin')>=0&&t.indexOf('Delete group')>=0&&t.indexOf('Move')>=0&&t.indexOf('Section')>=0&&t.indexOf('Agents here can reply.')>=0&&t.indexOf('Mention one with @Name when you want that agent to reply.')>=0&&t.indexOf('What should we cover today?')>=0&&t.indexOf('I can take the first pass.')>=0&&t.indexOf('Mention an agent with @Name and that agent replies here.')<0&&!!sel&&sel.value!=='groups'&&boxes.length>=2&&t.indexOf('\u2014')<0;})()`
	case "edit":
		page = base + "/groups/" + groupID + "/edit"
		needle = `(function(){var t=document.body.innerText||'';var title=document.querySelector('input[name="title"]');var boxes=document.querySelectorAll('input[name="agent"]:checked');return t.indexOf('Edit group')>=0&&t.indexOf('You stay in this group.')>=0&&t.indexOf('Cancel')>=0&&t.indexOf('Save')>=0&&!!title&&title.value==='Standup'&&boxes.length>=2&&t.indexOf('\u2014')<0;})()`
	case "delete":
		page = base + "/groups/" + groupID + "/delete"
		needle = `(function(){var t=document.body.innerText||'';return t.indexOf('Delete Standup?')>=0&&t.indexOf('The chat is removed for everyone.')>=0&&t.indexOf('The agents are kept.')>=0&&t.indexOf('Delete group')>=0&&t.indexOf('Cancel')>=0&&t.indexOf('\u2014')<0;})()`
	case "new":
		page = base + "/?list=1"
		needle = `(function(){var t=document.body.innerText||'';var title=document.querySelector('.group-pane form[action$="/edit"] input[name="title"]');var boxes=document.querySelectorAll('#agent-panel input[name="agent"]');var checked=document.querySelectorAll('#agent-panel input[name="agent"]:checked');var sel=document.querySelector('#agent-panel select[name="section"]');var pin=document.querySelector('#agent-panel form[action$="/pin"] button');return t.indexOf('New group')>=0&&!!pin&&pin.textContent.trim()==='Pin'&&t.indexOf('Section')>=0&&t.indexOf('This places the group in your sidebar only.')>=0&&t.indexOf('A group needs at least two of your agents.')>=0&&t.indexOf('Agents here can reply.')>=0&&t.indexOf('Create group')<0&&!!title&&title.value==='New group'&&boxes.length>=2&&checked.length===0&&!!sel&&sel.value==='groups'&&t.indexOf('\u2014')<0;})()`
	}
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, base, session, page, width, height)...); err != nil {
		t.Fatalf("open %s: %v", kind, err)
	}
	if kind == "group" && width < 800 {
		if err := chromedp.Run(ctx, chromedp.Click(`[aria-label="Group panel"]`, chromedp.ByQuery)); err != nil {
			t.Fatalf("open group panel: %v", err)
		}
	}
	if kind == "new" {
		if err := chromedp.Run(ctx, chromedp.Click(`form[action="/groups"] button`, chromedp.ByQuery)); err != nil && !containsNav(err) {
			t.Fatalf("open new group: %v", err)
		}
		waitNavShot(t, ctx, "105 "+kind, needle)
		if err := chromedp.Run(ctx, pageReady(t)); err != nil {
			t.Fatal(err)
		}
	} else if err := chromedp.Run(ctx, shotPoll(t, "105 "+kind, js(needle))); err != nil {
		var text string
		_ = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &text))
		t.Fatalf("needle %s: %v\n%s", kind, err, text)
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
}

func markDoc(t *testing.T, ctx context.Context) {
	t.Helper()
	var ok bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){document.documentElement.dataset.shotmark='1';return true;})()`), &ok)); err != nil || !ok {
		t.Fatalf("mark page: %v %v", ok, err)
	}
}

func waitNavShot(t *testing.T, ctx context.Context, label, expr string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		var ok bool
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := chromedp.Run(attempt, chromedp.Evaluate(js(expr), &ok))
		cancel()
		if err == nil && ok {
			return
		}
		if err != nil {
			last = err.Error()
		} else {
			last = "false"
		}
		time.Sleep(150 * time.Millisecond)
	}
	var text string
	snap, snapCancel := context.WithTimeout(ctx, 2*time.Second)
	_ = chromedp.Run(snap, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &text))
	snapCancel()
	t.Fatalf("%s: %s\n%s", label, last, text)
}

func TestGroupSetupReflects(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	browser, cleanup := shotBrowser(t)
	defer cleanup()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	ctx := context.Background()
	owner, _ := h.account("alex-setup@example.com", "Alex Rivera", "correct-horse-1")
	sec, err := h.st.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Pip", Title: "Notes", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateSection(ctx, owner.ID, "Research"); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	tabCtx := tab(t, browser)
	if err := chromedp.Run(tabCtx, cliOpen(t, ts.URL, sessionToken(t, h, owner.ID), ts.URL+"/?list=1", 1280, 800)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(tabCtx, chromedp.Click(`form[action="/groups"] button`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	ready := `(function(){var form=document.querySelector('.group-pane form[action$="/edit"]');var title=document.querySelector('.group-pane form[action$="/edit"] input[name="title"]');return !!form&&form.dataset.bound==='1'&&!!title&&title.value==='New group';})()`
	waitNavShot(t, tabCtx, "setup ready", ready)
	var set bool
	err = chromedp.Run(tabCtx, chromedp.Evaluate(js(`(function(){var input=document.querySelector('.group-pane form[action$="/edit"] input[name="title"]');if(!input)return false;input.value='Planning';input.dispatchEvent(new Event('input',{bubbles:true}));return true;})()`), &set))
	if err != nil || !set {
		t.Fatalf("type title %v %v", set, err)
	}
	named := `(function(){var nm=document.querySelector('.s19h .who-btn .nm');var side=document.querySelector('.sb a[aria-current="page"] .nm');return !!nm&&nm.textContent==='Planning'&&!!side&&side.textContent==='Planning';})()`
	waitNavShot(t, tabCtx, "title preview", named)
	markDoc(t, tabCtx)
	var moved bool
	err = chromedp.Run(tabCtx, chromedp.Evaluate(js(`(function(){var sel=document.querySelector('#agent-panel select[name="section"]');if(!sel)return false;var found=false;for(var i=0;i<sel.options.length;i++){if(sel.options[i].text==='Research'){sel.selectedIndex=i;found=true;}}if(!found)return false;sel.dispatchEvent(new Event('change',{bubbles:true}));return true;})()`), &moved))
	if err != nil && !containsNav(err) {
		t.Fatalf("choose section %v %v", moved, err)
	}
	if err == nil && !moved {
		t.Fatal("section option missing")
	}
	placed := `(function(){if(document.documentElement.dataset.shotmark==='1')return false;var form=document.querySelector('.group-pane form[action$="/edit"]');var title=document.querySelector('.group-pane form[action$="/edit"] input[name="title"]');var nm=document.querySelector('.s19h .who-btn .nm');var sub=document.querySelector('.s19h .who-btn .sub');var link=document.querySelector('.sb a[aria-current="page"]');var head=link&&link.closest('.sec')&&link.closest('.sec').querySelector('h4');return !!form&&form.dataset.bound==='1'&&!!title&&title.value==='Planning'&&!!nm&&nm.textContent==='Planning'&&!!sub&&sub.textContent==='Group chat · Research'&&!!head&&(head.innerText||'').toLowerCase().indexOf('research')>=0;})()`
	waitNavShot(t, tabCtx, "placed", placed)
	markDoc(t, tabCtx)
	var checked bool
	atlasCtx, atlasCancel := context.WithTimeout(tabCtx, 8*time.Second)
	err = chromedp.Run(atlasCtx, chromedp.Evaluate(js(`(function(){var labels=document.querySelectorAll('#agent-panel label');for(var i=0;i<labels.length;i++){var t1=labels[i].querySelector('.t1');if(!t1||t1.textContent!=='Atlas')continue;var box=labels[i].querySelector('input[name="agent"]');if(!box)return false;box.checked=true;box.dispatchEvent(new Event('change',{bubbles:true}));return true;}return false;})()`), &checked))
	atlasCancel()
	if err != nil && !containsNav(err) && atlasCtx.Err() == nil {
		t.Fatalf("choose atlas %v %v", checked, err)
	}
	if err == nil && !checked {
		t.Fatal("atlas checkbox missing")
	}
	withAtlas := `(function(){if(document.documentElement.dataset.shotmark==='1')return false;var t=document.body.innerText||'';var title=document.querySelector('.group-pane form[action$="/edit"] input[name="title"]');var box=document.querySelector('#agent-panel input[name="agent"]:checked');var sub=document.querySelector('.s19h .who-btn .sub');return t.indexOf('With Atlas.')>=0&&!!title&&title.value==='Planning'&&!!box&&!!sub&&sub.textContent==='Group chat · Research';})()`
	waitNavShot(t, tabCtx, "with atlas", withAtlas)
	markDoc(t, tabCtx)
	var pinnedClick bool
	pinCtx, pinCancel := context.WithTimeout(tabCtx, 8*time.Second)
	err = chromedp.Run(pinCtx, chromedp.Evaluate(js(`(function(){var form=document.querySelector('#agent-panel form[action$="/pin"]');if(!form)return false;form.requestSubmit();return true;})()`), &pinnedClick))
	pinCancel()
	if err != nil && !containsNav(err) && pinCtx.Err() == nil {
		t.Fatalf("pin group %v %v", pinnedClick, err)
	}
	if err == nil && !pinnedClick {
		t.Fatal("pin form missing")
	}
	pinned := `(function(){if(document.documentElement.dataset.shotmark==='1')return false;var t=document.body.innerText||'';var title=document.querySelector('.group-pane form[action$="/edit"] input[name="title"]');var sub=document.querySelector('.s19h .who-btn .sub');var side=document.querySelector('.sb a[aria-current="page"] .sub');return t.indexOf('With Atlas.')>=0&&!!title&&title.value==='Planning'&&!!sub&&sub.textContent==='Pinned · Research'&&!!side&&side.textContent==='Pinned';})()`
	waitNavShot(t, tabCtx, "pinned", pinned)
	assertShotClean(t, tabCtx)
}
