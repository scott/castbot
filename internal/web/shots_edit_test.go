//go:build shots

package web

import (
	"context"
	"net/http/httptest"
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
	if err := chromedp.Run(ctx, cliOpen(ts.URL, sessionToken(t, h, user.ID), edit, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Poll(js(`(document.body.innerText||'').indexOf('Edit agent')>=0 && !!document.querySelector('form.na a.btn') && document.querySelector('form.na input[name="name"]').value==='Atlas'`), nil, chromedp.WithPollingTimeout(8*time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.SetValue(`form.na input[name="name"]`, "Not saved", chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`form.na a.btn`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	editWait(t, ctx, "cancel", `location.pathname==='/agents/`+agent.ID+`' && (document.querySelector('.who-btn .nm')||{}).textContent==='Atlas' && pageText().indexOf('Not saved')<0 && pageText().indexOf('Edit agent')<0`)
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

func editWait(t *testing.T, ctx context.Context, label, expr string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		var ok bool
		err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){function pageText(){return document.body&&document.body.innerText||'';}return !!(`+expr+`);})()`), &ok))
		if err == nil && ok {
			return
		}
		if err != nil && !containsNav(err) && !strings.Contains(err.Error(), "Uncaught") && !strings.Contains(err.Error(), "exception") {
			t.Fatal(err)
		}
		if err != nil {
			last = err.Error()
		}
		time.Sleep(150 * time.Millisecond)
	}
	var now string
	_ = chromedp.Run(ctx, chromedp.Evaluate(js(`location.href+"\n"+(document.body?document.body.innerText:"")`), &now))
	t.Fatalf("%s: %s\n%s", label, last, now)
}
