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

// TestReadmeShots writes the two pictures linked from the README.
// Demo data only. The header omits a model id so the picture does not name a provider.
func TestReadmeShots(t *testing.T) {
	t.Setenv("CASTBOT_MODELS_OFFLINE", "1")
	chrome := "/usr/bin/google-chrome"
	if p := os.Getenv("CASTBOT_CHROME"); p != "" {
		chrome = p
	}
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("ignore-certificate-errors", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(allocCancel)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(quietLog), chromedp.WithErrorf(quietLog))
	t.Cleanup(browserCancel)
	if err := chromedp.Run(browserCtx); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join("..", "..", "docs", "readme")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t)
	user, _ := h.account("readme-shot@example.com", "Alex Rivera", "correct-horse-1")
	atlas := seedReadme(t, h.st, user.ID)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	token := sessionToken(t, h, user.ID)

	chatReady := `(function(){var t=document.body.innerText||'';var p=document.getElementById('agent-panel');return location.pathname==='/agents/` + atlas + `'&&!location.search&&t.indexOf('Update the FAQ')>=0&&t.indexOf('annual billing')>=0&&t.indexOf('Alex Rivera')>=0&&t.indexOf('Atlas')>=0&&!!document.querySelector('#composer')&&(!p||p.hidden)&&t.indexOf('\u2014')<0&&t.indexOf('\u2013')<0;})()`
	tabCtx := tab(t, browserCtx)
	if err := chromedp.Run(tabCtx, cliOpen(t, ts.URL, token, ts.URL+"/agents/"+atlas+"?panel=closed", 1280, 800)...); err != nil {
		t.Fatal(err)
	}
	waitShot(t, tabCtx, "readme chat", chatReady)
	assertNoSideScroll(t, tabCtx)
	saveShot(t, tabCtx, filepath.Join(dir, "chat.png"))

	listReady := `(function(){var t=document.body.innerText||'';var u=new URL(location.href);return u.pathname==='/'&&u.searchParams.get('list')==='1'&&t.indexOf('Atlas')>=0&&t.indexOf('Pip')>=0&&t.indexOf('Ledger')>=0&&t.indexOf('Birch')>=0&&t.indexOf('GROUPS')>=0&&!!document.querySelector('a[aria-label="New agent"]')&&t.indexOf('\u2014')<0&&t.indexOf('\u2013')<0;})()`
	phone := tab(t, browserCtx)
	if err := chromedp.Run(phone, cliOpen(t, ts.URL, token, ts.URL+"/?list=1", 390, 844)...); err != nil {
		t.Fatal(err)
	}
	waitShot(t, phone, "readme phone list", listReady)
	assertNoSideScroll(t, phone)
	saveShot(t, phone, filepath.Join(dir, "phone.png"))
}

func seedReadme(t *testing.T, st *store.Store, userID string) string {
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
	research, err := st.CreateSection(ctx, userID, "Research")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	type spec struct {
		section string
		in      store.AgentInput
		lines   [][2]string
	}
	rows := []spec{
		{pinned.ID, store.AgentInput{Name: "Atlas", Title: "Docs helper", Color: "c1", Status: "ready", Activity: "writing", CreatedAt: created}, [][2]string{
			{"user", "Update the FAQ in example-org/docs-bot with the new pricing tiers from pricing.md, then push a branch for review."},
			{"agent", "On it. I will read the current FAQ and pricing.md first."},
			{"user", "Also add a line about annual billing while you wait."},
			{"agent", "Sure. I added a line about annual billing to the same draft."},
		}},
		{pinned.ID, store.AgentInput{Name: "Ledger", Title: "Finance reports", Color: "c4", Status: "paused", CreatedAt: created}, nil},
		{agents.ID, store.AgentInput{Name: "Pip", Title: "Inbox triage", Color: "c2", Status: "ready", Unread: 2, CreatedAt: created}, nil},
		{agents.ID, store.AgentInput{Name: "Juniper", Title: "Repo maintainer", Color: "c3", Status: "provisioning", Activity: "Resuming", CreatedAt: created}, nil},
		{agents.ID, store.AgentInput{Name: "Moss", Title: "Web research", Color: "c5", Status: "failed", CreatedAt: created}, nil},
		{agents.ID, store.AgentInput{Name: "Wren", Title: "Release notes", Color: "c6", Status: "paused", CreatedAt: created}, nil},
		{research.ID, store.AgentInput{Name: "Birch", Title: "Field notes", Color: "c7", Status: "ready", CreatedAt: created}, nil},
	}
	var atlasID string
	for _, row := range rows {
		row.in.SectionID = row.section
		agent, thread, err := st.CreateAgent(ctx, userID, row.in)
		if err != nil {
			t.Fatal(err)
		}
		if agent.Name == "Atlas" {
			atlasID = agent.ID
		}
		for _, line := range row.lines {
			author := userID
			if line[0] == "agent" {
				author = agent.ID
			}
			if _, err := st.CreateMessage(ctx, userID, thread.ID, line[0], author, line[1]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if atlasID == "" {
		t.Fatal("atlas missing")
	}
	return atlasID
}
