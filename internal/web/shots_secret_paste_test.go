//go:build shots

package web

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
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

func shootSecretPaste(t *testing.T, browser context.Context) {
	t.Helper()
	h := newHarness(t)
	var logs bytes.Buffer
	h.srv.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	user, _ := h.account("secret-paste@example.com", "Paste User", "correct-horse-1")
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), user.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	release := h.srv.runs.PinWatch(runRow.ID, agent.ID)
	t.Cleanup(release)
	if _, _, err := h.st.CreateSecretRequest(context.Background(), user.ID, agent.ID, thread.ID, runRow.ID, connect.DigitalOcean, "List droplets", h.st.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	token := "dop_v1_" + strings.Repeat("ab", 32)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(t, ts.URL, sessionToken(t, h, user.ID), ts.URL+"/agents/"+agent.ID, 1280, 800)...); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "live secret card", js(`!!document.querySelector('.cards input.secret-in')`))); err != nil {
		t.Fatal(err)
	}
	var pasted string
	script := fmt.Sprintf(`(function(){
		var el = document.querySelector('.cards input.secret-in');
		if (!el) return 'missing';
		var token = %q;
		var ev = new ClipboardEvent('paste', {bubbles:true, cancelable:true});
		var canceled = !el.dispatchEvent(ev);
		if (!canceled) {
			el.focus();
			el.setRangeText(token, 0, el.value.length, 'end');
			el.dispatchEvent(new InputEvent('input', {bubbles:true, inputType:'insertFromPaste'}));
		}
		return el.value === token ? 'ok' : 'cleared';
	})()`, token)
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &pasted)); err != nil {
		t.Fatal(err)
	}
	if pasted != "ok" {
		t.Fatal("paste did not land in the token field")
	}
	if err := chromedp.Run(ctx, chromedp.Sleep(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	var still string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var el=document.querySelector('.cards input.secret-in');return el?el.value:'';})()`), &still)); err != nil {
		t.Fatal(err)
	}
	if still != token {
		t.Fatal("pasted token did not survive the activity ticks")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`.cards .appr.secret button.pri`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, shotPoll(t, "saved secret card", js(`(document.body.innerText||'').indexOf('Saved. Tools from this connection')>=0`))); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListSecretRequests(context.Background(), user.ID, agent.ID, time.Unix(0, 0))
	if err != nil || len(rows) == 0 || rows[0].Status != "saved" {
		t.Fatal("request was not saved")
	}
	saved, err := h.st.ConnectionSecret(context.Background(), user.ID, connect.DigitalOcean)
	if err != nil || saved.Name != connect.SecretNameDigitalOcean {
		t.Fatal("vault row")
	}
	kind, err := h.st.SecretKind(context.Background(), user.ID, saved.ID)
	if err != nil || kind != "connection" {
		t.Fatal("kind")
	}
	on, err := h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, connect.DigitalOcean)
	if err != nil || !on {
		t.Fatal("enabled")
	}
	var html string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.documentElement.outerHTML`), &html)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, token) {
		t.Fatal("page held the pasted token")
	}
	path := filepath.Join(t.TempDir(), "secret-paste.png")
	shotPage(t, ctx, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(token)) {
		t.Fatal("screenshot held the pasted token")
	}
	if bytes.Contains(logs.Bytes(), []byte(token)) {
		t.Fatal("log held the pasted token")
	}
}
