//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/scott/castbot/internal/store"
)

func shootS27(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h := newHarness(t)
	email := "s27-" + kind + "-" + theme + "-" + name + "@example.com"
	user, _ := h.account(email, "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	scout, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Scout", Title: "Docs helper", Color: "c4", Model: model, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range [][2]string{
		{"user", "Check the docs for broken links."},
		{"agent", "I found two broken links in the pricing page."},
		{"user", "Please fix the annual billing link too."},
	} {
		author := user.ID
		if line[0] == "agent" {
			author = scout.ID
		}
		if _, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, line[0], author, line[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Nia", Title: "Inbox triage", Color: "c6", Status: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Pip", Title: "Field notes", Color: "c1", Status: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	page := "/agents/" + scout.ID
	switch kind {
	case "list":
		page = "/"
	case "advanced":
		page = "/new-agent?advanced=1"
	case "edit":
		page = "/agents/" + scout.ID + "/edit"
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+page, width, height)...); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "chat":
		s20Wait(t, ctx, `(document.body.innerText||'').indexOf('Check the docs for broken links.')>=0 && (document.body.innerText||'').indexOf('two broken links')>=0 && !!document.querySelector('.m-user.c4') && !!document.querySelector('.m-agent .av.c4')`)
	case "list":
		s20Wait(t, ctx, `(document.body.innerText||'').indexOf('Scout')>=0 && (document.body.innerText||'').indexOf('Nia')>=0 && (document.body.innerText||'').indexOf('Pip')>=0`)
	case "advanced", "edit":
		s20Wait(t, ctx, `document.querySelectorAll('.swatches input[name="color"]').length===9 && (document.body.innerText||'').indexOf('Coral')>=0 && (document.body.innerText||'').indexOf('Sand')>=0`)
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var el=document.querySelector('.swatches');if(el)el.scrollIntoView({block:'center'});})()`), nil)); err != nil {
			t.Fatal(err)
		}
		s27Keyboard(t, ctx)
		if height < 800 {
			s27TapTargets(t, ctx)
		}
	}
	var html string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.documentElement.outerHTML`), &html)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, `style="`) {
		t.Fatal("inline style")
	}
	if kind == "chat" && (!strings.Contains(html, `class="m-user c4"`) || strings.Count(html, `class="m-user c4"`) != 2) {
		t.Fatal("chat bubbles")
	}
	if kind == "edit" {
		frameEditForm(t, ctx)
		waitVisibleClose(t, ctx)
		assertEditClose(t, ctx)
		assertModelLabelShown(t, ctx, modelLabel(model))
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	var dashes string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(document.body.innerText||'').indexOf('\u2014')<0 && (document.body.innerText||'').indexOf('\u2013')<0 ? 'ok' : 'dash'`), &dashes)); err != nil {
		t.Fatal(err)
	}
	if dashes != "ok" {
		t.Fatal("shot copy has a dash")
	}
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
}

func s27Keyboard(t *testing.T, ctx context.Context) {
	t.Helper()
	var before string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.querySelector('.swatches input:checked').value`), &before)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.SendKeys(`.swatches input:checked`, kb.ArrowRight, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var after, preview string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var on=document.querySelector('.swatches input:checked');var preview=document.getElementById('color-preview');return on?on.value:'';})()`), &after)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var el=document.getElementById('color-preview');return el?el.className:'';})()`), &preview)); err != nil {
		t.Fatal(err)
	}
	if after == "" || after == before || !strings.Contains(preview, after) {
		t.Fatalf("arrow key before %s after %s preview %s", before, after, preview)
	}
	if err := chromedp.Run(ctx, chromedp.SendKeys(`.swatches input:checked`, kb.ArrowLeft, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var back string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.querySelector('.swatches input:checked').value`), &back)); err != nil {
		t.Fatal(err)
	}
	if back != before {
		t.Fatalf("arrow left left %s want %s", back, before)
	}
}

func s27TapTargets(t *testing.T, ctx context.Context) {
	t.Helper()
	var bad string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		var bad = [];
		document.querySelectorAll('.swatch').forEach(function(el){
			var r = el.getBoundingClientRect();
			if (r.width < 1 || r.height < 1) return;
			if (r.height < 44 || r.width < 44) bad.push((el.innerText||'').trim().slice(0,20)+' '+Math.round(r.width)+'x'+Math.round(r.height));
		});
		return bad.join(' | ');
	})()`), &bad)); err != nil {
		t.Fatal(err)
	}
	if bad != "" {
		t.Fatalf("tap target %s", bad)
	}
}
