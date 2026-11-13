//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

func shootT2Connections(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("t2-conn-"+theme+"-"+name+"@example.com", "Ada", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
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
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.GitHub, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := longTab(t, browser, 2*time.Minute)
	page := ts.URL + "/agents/" + agent.ID + "/edit"
	if err := chromedp.Run(ctx, cliOpen(ts.URL, sessionToken(t, h, user.ID), page, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Poll(js(`!!document.querySelector('#connections') && (document.body.innerText||'').indexOf('A token is saved')>=0`), nil, chromedp.WithPollingTimeout(8*time.Second))); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.ScrollIntoView(`#connections`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	assertNoCanary(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "connections-"+theme+"-"+name+".png"))
	assertShotFileClean(t, filepath.Join(dir, "connections-"+theme+"-"+name+".png"))
}

func shootT2Secret(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("t2-secret-"+theme+"-"+name+"@example.com", "Ada", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	agent := seedAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.GitHub, connectCanary); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateSecretRequest(context.Background(), user.ID, agent.ID, "", "", connect.GitHub, "Read issues for the notes", h.st.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := longTab(t, browser, 2*time.Minute)
	page := ts.URL + "/agents/" + agent.ID
	if err := chromedp.Run(ctx, cliOpen(ts.URL, sessionToken(t, h, user.ID), page, width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Poll(js(`!!document.querySelector('.appr.secret input.secret-in') && (document.body.innerText||'').indexOf('Connect GitHub')>=0 && (document.body.innerText||'').indexOf('Use saved token')>=0`), nil, chromedp.WithPollingTimeout(8*time.Second))); err != nil {
		t.Fatal(err)
	}
	assertNoCanary(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "secret-"+theme+"-"+name+".png"))
	assertShotFileClean(t, filepath.Join(dir, "secret-"+theme+"-"+name+".png"))
}

func assertNoCanary(t *testing.T, ctx context.Context) {
	t.Helper()
	var html string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.documentElement.outerHTML`), &html)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, connectCanary) || strings.Contains(html, "GITHUB_CONNECTION") {
		t.Fatal("page held a connection secret")
	}
}

func assertShotFileClean(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), connectCanary) {
		t.Fatal("screenshot held a connection secret")
	}
}
