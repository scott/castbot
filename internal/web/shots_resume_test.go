//go:build shots

package web

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func shootPausedOpen(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s42", "https://castbot.example")
	user, _ := h.account("s42-"+theme+"-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	atlas, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Wren", Title: "Inbox",
	}); err != nil {
		t.Fatal(err)
	}
	readyMachine(t, h, fake, atlas, "vm-"+atlas.ID, "paused")
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	wake := make(chan struct{})
	fake.WakeWait = wake
	t.Cleanup(func() { close(wake) })
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+"/", width, height)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "paused sidebar", js(`!!document.querySelector('a.ag[data-name="Atlas"]') && (document.body.innerText||'').indexOf('Wren')>=0`))); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`a.ag[data-name="Atlas"]`, chromedp.ByQuery)); err != nil && !containsNav(err) {
		t.Fatal(err)
	}
	editWait(t, ctx, "waking", `location.pathname==='/agents/`+atlas.ID+`' && (document.body.innerText||'').indexOf('Waking up')>=0 && !!document.querySelector('#resume-live .actico') && document.getElementById('resume-live').classList.contains('htmx-request')`)
	if err := chromedp.Run(ctx, chromedp.Sleep(450*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	scrollTop(t, ctx)
	shotPage(t, ctx, filepath.Join(dir, "sidebar-paused-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}
