package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func TestPageCardsInChatAndList(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// The expiry loop reads this clock from another goroutine.
	var clockMu sync.Mutex
	clock := time.Date(2026, 11, 26, 16, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	})
	advance := func(d time.Duration) {
		clockMu.Lock()
		clock = clock.Add(d)
		clockMu.Unlock()
	}
	owner, secret := h.account("card-owner@example.com", "Alex Rivera", "correct-horse-1")
	other, otherSecret := h.account("card-other@example.com", "Blair Quinn", "correct-horse-1")
	member := acceptHostedInvite(t, h, owner, "card-member@example.com", "Pip Lane")
	agent := hostedAgent(t, h.st, owner, "Atlas")
	second := hostedAgent(t, h.st, owner, "Moss")
	if _, err := h.st.CreatePage(ctx, owner.ID, agent.ID, "Earlier notes", samplePageText("earlier")); err != nil {
		t.Fatal(err)
	}
	advance(time.Minute)
	made := settleOwnedPage(t, h, owner.ID, agent, "North campus trials", samplePageText("first body"))
	advance(time.Minute)
	group, err := h.st.CreateGroup(ctx, owner.ID, "Lab", []string{agent.ID, second.ID}, []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	groupPage := settleGroupPage(t, h, owner.ID, agent, group.ID, "Group lab notes", samplePageText("group body"))

	h.login(owner.Email, "correct-horse-1", secret, "203.0.113.80:9", false)
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", "203.0.113.80:9", nil)
	if code != http.StatusOK || !pageCardShown(body, made.Title, made.Token) || !strings.Contains(body, "Only you") || strings.Contains(body, "Earlier notes") || strings.Contains(body, "Updated below") {
		t.Fatalf("chat card %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=agent", "", "203.0.113.80:9", nil)
	if code != http.StatusOK || !strings.Contains(body, "All pages") || !strings.Contains(body, made.Title) || !strings.Contains(body, `class="page-row"`) || strings.Contains(body, "Earlier notes") == false {
		t.Fatalf("agent pages %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/pages", "", "203.0.113.80:9", nil)
	if code != http.StatusOK || !strings.Contains(body, "Yours") || !strings.Contains(body, made.Title) || !strings.Contains(body, "Earlier notes") || strings.Contains(body, "Shared with you") || strings.Contains(body, "A page appears when an agent makes one.") {
		t.Fatalf("owner library %d", code)
	}

	advance(time.Minute)
	if _, err := h.st.UpdatePage(ctx, owner.ID, agent.ID, made.ID, samplePageText("later body"), 0); err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", "203.0.113.80:9", nil)
	if code != http.StatusOK || !strings.Contains(body, "Updated below") || !strings.Contains(body, "/p/"+made.Token) || !strings.Contains(body, "Only you") {
		t.Fatalf("stale card %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.80:9", nil)
	if code != http.StatusOK || !strings.Contains(body, made.Title) || !strings.Contains(body, "later body") || strings.Contains(body, "first body") {
		t.Fatalf("open latest %d", code)
	}

	if _, err := h.st.SharePage(ctx, owner.ID, "", made.ID, store.PageWorkspace, nil, nil); err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", "203.0.113.80:9", nil)
	if code != http.StatusOK || !strings.Contains(body, "Personal · Updated below") || strings.Contains(body, "Only you") {
		t.Fatalf("shared subtitle %d", code)
	}

	advance(2 * time.Minute)
	h.switchUser(other.Email, "correct-horse-1", otherSecret, "203.0.113.81:9")
	code, body, _ = h.do(http.MethodGet, "/pages", "", "203.0.113.81:9", nil)
	if code != http.StatusOK || !strings.Contains(body, "A page appears when an agent makes one.") || strings.Contains(body, made.Title) || strings.Contains(body, "Group lab notes") {
		t.Fatalf("other library %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.81:9", nil)
	if code != http.StatusNotFound || !strings.Contains(body, "This page is not available.") {
		t.Fatalf("other open %d", code)
	}
	code, _, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", "203.0.113.81:9", nil)
	if code != http.StatusNotFound {
		t.Fatalf("other agent %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+group.ID, "", "203.0.113.81:9", nil)
	if code != http.StatusNotFound || strings.Contains(body, "Group lab notes") {
		t.Fatalf("other group %d", code)
	}

	advance(2 * time.Minute)
	h.switchUser(member.Email, "correct-horse-1", member.secret, "203.0.113.82:9")
	code, body, _ = h.do(http.MethodGet, "/pages", "", "203.0.113.82:9", nil)
	if code != http.StatusOK || !strings.Contains(body, "Shared with you") || !strings.Contains(body, made.Title) || !strings.Contains(body, "Personal") || strings.Contains(body, "Group lab notes") {
		t.Fatalf("member library %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/groups/"+group.ID, "", "203.0.113.82:9", nil)
	if code != http.StatusOK || !pageCardShown(body, groupPage.Title, groupPage.Token) {
		t.Fatalf("member card %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/p/"+groupPage.Token, "", "203.0.113.82:9", nil)
	if code != http.StatusNotFound || !strings.Contains(body, "This page is not available.") {
		t.Fatalf("member private open %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/p/"+made.Token, "", "203.0.113.82:9", nil)
	if code != http.StatusOK || !strings.Contains(body, "later body") {
		t.Fatalf("member shared open %d", code)
	}
}

func TestAgentPagePreview(t *testing.T) {
	h := newHarness(t)
	// The expiry loop reads this clock from another goroutine.
	var clockMu sync.Mutex
	clock := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	})
	user, secret := h.account("card-preview@example.com", "Alex Rivera", "correct-horse-1")
	agent := hostedAgent(t, h.st, user, "Atlas")
	oldest := "Ledger 0"
	newest := "Ledger 5"
	for i := 0; i < 6; i++ {
		if _, err := h.st.CreatePage(context.Background(), user.ID, agent.ID, fmt.Sprintf("Ledger %d", i), samplePageText("row")); err != nil {
			t.Fatal(err)
		}
		clockMu.Lock()
		clock = clock.Add(time.Second)
		clockMu.Unlock()
	}
	h.login(user.Email, "correct-horse-1", secret, "203.0.113.83:9", false)
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=agent", "", "203.0.113.83:9", nil)
	if code != http.StatusOK || !strings.Contains(body, newest) || strings.Contains(body, oldest) || strings.Count(body, `class="page-row"`) != 5 {
		t.Fatalf("preview %d rows %d", code, strings.Count(body, `class="page-row"`))
	}
	code, body, _ = h.do(http.MethodGet, "/pages", "", "203.0.113.83:9", nil)
	if code != http.StatusOK || !strings.Contains(body, oldest) || !strings.Contains(body, newest) || strings.Count(body, `class="page-row"`) != 6 {
		t.Fatalf("library %d rows %d", code, strings.Count(body, `class="page-row"`))
	}
}

func pageCardShown(body, title, token string) bool {
	return strings.Contains(body, title) && strings.Contains(body, `class="page-card"`) && strings.Contains(body, "Open") && strings.Contains(body, "/p/"+token) && strings.Contains(body, "Only you")
}

func samplePageText(text string) string {
	return `{"v":1,"blocks":[{"type":"markdown","text":"` + text + `"}]}`
}

func settleOwnedPage(t *testing.T, h *harness, userID string, agent store.Agent, title, body string) store.Page {
	t.Helper()
	return settleThreadPage(t, h, userID, agent, "", title, body)
}

func settleGroupPage(t *testing.T, h *harness, userID string, agent store.Agent, threadID, title, body string) store.Page {
	t.Helper()
	return settleThreadPage(t, h, userID, agent, threadID, title, body)
}

func settleThreadPage(t *testing.T, h *harness, userID string, agent store.Agent, threadID, title, body string) store.Page {
	t.Helper()
	ctx := context.Background()
	if threadID == "" {
		th, err := h.st.DMThread(ctx, userID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		threadID = th.ID
	}
	runRow, err := h.st.CreateRun(ctx, userID, threadID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	made, err := h.st.CreatePage(ctx, userID, agent.ID, title, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(ctx, userID, runRow.ID, "Saved the report.", 0); err != nil {
		t.Fatal(err)
	}
	h.srv.settleTalk(run.Job{UserID: userID, ThreadID: threadID, AgentID: agent.ID, RunID: runRow.ID}, "done")
	return made
}
