//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

func shootEditReturn(t *testing.T, browser context.Context, width, height int) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("edit-return@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, "dark"); err != nil {
		t.Fatal(err)
	}
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	model := firstModel(t)
	if err := h.st.SaveAgentFields(context.Background(), user.ID, agent.ID, store.AgentInput{
		SectionID: agent.SectionID, Name: agent.Name, Title: agent.Title, Model: model,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Atlas\nbrain:\n  model: "+model+"\n  instructions_file: instructions.md\ntools:\n  builtin: []\n", "Be concise."); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	edit := ts.URL + "/agents/" + agent.ID + "/edit"
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), edit, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "edit form open", js(`(document.body.innerText||'').indexOf('Edit agent')>=0 && !!document.querySelector('a.sclose[href]') && (document.body.innerText||'').indexOf('Cancel')<0 && document.querySelector('form.na input[name="name"]').value==='Atlas'`))); err != nil {
		t.Fatal(err)
	}
	assertEditClose(t, ctx)
	if err := chromedp.Run(ctx, chromedp.SetValue(`form.na input[name="name"]`, "Not saved", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	clickVisibleClose(t, ctx)
	editWait(t, ctx, "cancel", `location.pathname==='/agents/`+agent.ID+`' && (document.querySelector('.who-btn .nm')||{}).textContent==='Atlas' && pageText().indexOf('Not saved')<0 && pageText().indexOf('Edit agent')<0`)
	if err := chromedp.Run(ctx, chromedp.Navigate(edit)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	editWait(t, ctx, "reopen esc", `pageText().indexOf('Edit agent')>=0 && !!document.querySelector('a.sclose[href]') && document.querySelector('form.na input[name="name"]').value==='Atlas'`)
	if err := chromedp.Run(ctx, chromedp.SetValue(`form.na input[name="name"]`, "Escaped", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))`), nil)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	editWait(t, ctx, "escape", `location.pathname==='/agents/`+agent.ID+`' && (document.querySelector('.who-btn .nm')||{}).textContent==='Atlas' && pageText().indexOf('Escaped')<0 && pageText().indexOf('Edit agent')<0`)
	if err := chromedp.Run(ctx, chromedp.Navigate(edit)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	editWait(t, ctx, "reopen", `pageText().indexOf('Edit agent')>=0 && !!document.querySelector('form.na input[name="name"]') && document.querySelector('form.na input[name="name"]').value==='Atlas'`)
	if err := chromedp.Run(ctx, chromedp.SetValue(`form.na input[name="name"]`, "", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`form.na button.pri`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	editWait(t, ctx, "validation", `location.pathname.indexOf('/edit')>=0 && pageText().indexOf('A name is required.')>=0 && pageText().indexOf('Edit agent')>=0`)
	if err := chromedp.Run(ctx, chromedp.SetValue(`form.na input[name="name"]`, "Harbor", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`form.na button.pri`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	editWait(t, ctx, "save", `location.pathname==='/agents/`+agent.ID+`' && (document.querySelector('.who-btn .nm')||{}).textContent==='Harbor' && pageText().indexOf('Edit agent')<0 && pageText().indexOf('Not saved')<0`)
}

func waitVisibleClose(t *testing.T, ctx context.Context) {
	t.Helper()
	err := chromedp.Run(ctx, shotPoll(t, "visible close control", js(`(function(){
		var links=document.querySelectorAll('a.sclose[href]');
		for (var i=0;i<links.length;i++){
			var r=links[i].getBoundingClientRect();
			if(r.width>1 && r.height>1) return true;
		}
		return false;
	})()`)))
	if err != nil {
		t.Fatal(err)
	}
}

func clickVisibleClose(t *testing.T, ctx context.Context) {
	t.Helper()
	var ok bool
	err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		var links=document.querySelectorAll('a.sclose[href]');
		for (var i=0;i<links.length;i++){
			var r=links[i].getBoundingClientRect();
			if(r.width>1 && r.height>1){ links[i].click(); return true; }
		}
		return false;
	})()`), &ok))
	if err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	if err == nil && !ok {
		t.Fatal("close control")
	}
}

func assertEditClose(t *testing.T, ctx context.Context) {
	t.Helper()
	var got string
	err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		function box(sel){
			var el=document.querySelector(sel);
			if(!el) return null;
			var r=el.getBoundingClientRect();
			return {w:r.width,h:r.height,top:r.top};
		}
		var desk=box('.page-h a.sclose');
		var phone=box('.ph-top a.sclose');
		if(window.innerWidth<=400){
			if(!phone||phone.w<44||phone.h<44) return 'phone';
			return 'ok';
		}
		if(!desk||desk.w<1||desk.h<1||desk.top>80) return 'desk';
		if(phone&&phone.w>1) return 'phone shown';
		return 'ok';
	})()`), &got))
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("close control %s", got)
	}
}

func frameEditForm(t *testing.T, ctx context.Context) {
	t.Helper()
	err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		var main=document.querySelector('.main');
		if(main) main.scrollTop=0;
		window.scrollTo(0,0);
		if(window.innerWidth>=700) return;
		var sum=document.querySelector('.sum');
		if(sum) sum.scrollIntoView({block:'end'});
	})()`), nil))
	if err != nil {
		t.Fatal(err)
	}
}

func assertModelLabelShown(t *testing.T, ctx context.Context, label string) {
	t.Helper()
	var got string
	err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){
		var want=`+strconv.Quote(label)+`;
		var nodes=document.querySelectorAll('.sum div');
		for (var i=0;i<nodes.length;i++){
			var el=nodes[i];
			if ((el.textContent||'').indexOf(want)<0) continue;
			var nested=false;
			for (var c=0;c<el.children.length;c++){
				if ((el.children[c].textContent||'').indexOf(want)>=0) nested=true;
			}
			if (nested) continue;
			var r=el.getBoundingClientRect();
			if (r.width>1 && r.height>1 && r.top>=0 && r.bottom<=window.innerHeight && r.left<window.innerWidth && r.right>0) return 'ok';
			return 'offscreen';
		}
		return 'missing';
	})()`), &got))
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" {
		t.Fatalf("model label %s", got)
	}
}

func editWait(t *testing.T, ctx context.Context, label, expr string) {
	t.Helper()
	var last string
	ok := withShotRetry(t, label, func() bool {
		return editWaitOnce(t, ctx, expr, &last)
	})
	if ok {
		return
	}
	t.Fatalf("wait %s timed out after %s: %s\ncondition: %s\n%s", label, shotWait(), last, squash(expr), pageSnap(ctx))
}

func editWaitOnce(t *testing.T, ctx context.Context, expr string, last *string) bool {
	t.Helper()
	deadline := time.Now().Add(shotWait())
	for time.Now().Before(deadline) {
		var ok bool
		err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){function pageText(){return document.body&&document.body.innerText||'';}return !!(`+expr+`);})()`), &ok))
		if err == nil && ok {
			return true
		}
		if err != nil && !containsNav(err) && !strings.Contains(err.Error(), "Uncaught") && !strings.Contains(err.Error(), "exception") {
			t.Fatal(err)
		}
		if err != nil {
			*last = err.Error()
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}
