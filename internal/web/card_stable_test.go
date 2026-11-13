package web

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

func TestCardsHTMLStableAcrossElapsedTicks(t *testing.T) {
	h := newHarness(t)
	base := time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)
	freeze(h, base)
	user, _ := h.account("cards-stable@example.com", "Ada", "correct-horse-1")
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
	if _, _, err := h.st.CreateSecretRequest(context.Background(), user.ID, agent.ID, thread.ID, runRow.ID, connect.DigitalOcean, "List droplets", base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	user, err = h.st.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	const csrf = "csrf-stable"
	var snaps []string
	for _, add := range []time.Duration{4 * time.Second, 6 * time.Second, 7 * time.Second} {
		freeze(h, base.Add(add))
		html, err := h.srv.cardsHTML(context.Background(), user, runRow.ID, csrf)
		if err != nil {
			t.Fatal(err)
		}
		snaps = append(snaps, html)
	}
	if snaps[0] == "" || snaps[0] != snaps[1] || snaps[1] != snaps[2] {
		t.Fatal("card html changed with the clock")
	}
	if strings.Contains(snaps[2], ">6s<") || strings.Contains(snaps[2], ">7s<") {
		t.Fatal("card html included the activity clock")
	}
	freeze(h, base.Add(4*time.Second))
	aside, err := h.srv.liveAsideHTML(context.Background(), user, agent.ID, runRow.ID, csrf)
	if err != nil {
		t.Fatal(err)
	}
	freeze(h, base.Add(7*time.Second))
	later, err := h.srv.liveAsideHTML(context.Background(), user, agent.ID, runRow.ID, csrf)
	if err != nil {
		t.Fatal(err)
	}
	if aside != later || strings.Contains(later, ">6s<") || strings.Contains(later, ">7s<") {
		t.Fatal("badge chrome included the activity clock")
	}

	freeze(h, base.Add(4*time.Second))
	rec := httptest.NewRecorder()
	last := ""
	lastPane := aside
	if err := h.srv.writeLiveCards(context.Background(), rec, user, runRow, csrf, &last, &lastPane); err != nil {
		t.Fatal(err)
	}
	if countEvent(parseSSE(rec.Body.String()), "card") != 1 {
		t.Fatalf("first card frames %d", countEvent(parseSSE(rec.Body.String()), "card"))
	}
	if data := eventData(t, parseSSE(rec.Body.String()), "card"); data != snaps[0] {
		t.Fatal("card frame was not the card list")
	}
	for _, add := range []time.Duration{6 * time.Second, 7 * time.Second} {
		freeze(h, base.Add(add))
		var buf bytes.Buffer
		rec := httptest.NewRecorder()
		rec.Body = &buf
		if err := h.srv.writeLiveCards(context.Background(), rec, user, runRow, csrf, &last, &lastPane); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(buf.String(), "event: card") || strings.Contains(buf.String(), "event: approvals") {
			t.Fatal("clock tick emitted a card or approvals frame")
		}
	}
}
