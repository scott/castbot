package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/store"
)

func TestResumeCursorWholeVersusPartial(t *testing.T) {
	q := httptest.NewRequest(http.MethodGet, "/runs/r/events?after=7", nil)
	got := streamCursor(q)
	if !got.whole || got.seq != 7 {
		t.Fatalf("query after=7: %+v", got)
	}
	h := httptest.NewRequest(http.MethodGet, "/runs/r/events", nil)
	h.Header.Set("Last-Event-ID", "7")
	got = streamCursor(h)
	if got.whole || got.seq != 7 || got.part != 0 {
		t.Fatalf("header 7: %+v", got)
	}
	d := httptest.NewRequest(http.MethodGet, "/runs/r/events?after=7.0", nil)
	d.Header.Set("Last-Event-ID", "7.2")
	got = streamCursor(d)
	if got.whole || got.seq != 7 || got.part != 2 {
		t.Fatalf("further partial: %+v", got)
	}
	page := httptest.NewRequest(http.MethodGet, "/runs/r/events?after=5", nil)
	page.Header.Set("Last-Event-ID", "5")
	got = streamCursor(page)
	if !got.whole || got.seq != 5 {
		t.Fatalf("page wins over frame 0: %+v", got)
	}
}

func TestN1ResumeResendsClosedBlock(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.241:9"
	_, secret := h.account("n1@example.com", "Nia", "correct-horse-1")
	h.login("n1@example.com", "correct-horse-1", secret, ip, false)
	userID := h.currentUser()
	sec, err := h.st.SystemSection(context.Background(), userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), userID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), userID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), userID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"text": "Hello paragraph.\n\n"})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := h.st.AppendRunEvent(context.Background(), userID, runRow.ID, "text", string(payload), 1)
	if err != nil {
		t.Fatal(err)
	}
	release := h.srv.runs.PinWatch(runRow.ID, agent.ID)
	t.Cleanup(release)
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	eventsURL := live.URL + "/runs/" + runRow.ID + "/events"

	first := collectSSE(t, eventsURL, h.cookieList(), "", time.Second, func(ev []sseEvent) bool {
		return countEvent(ev, "block") > 0
	})
	var textID, blockID string
	for _, e := range first {
		if e.Event == "text" && textID == "" {
			textID = e.ID
		}
		if e.Event == "block" && strings.Contains(e.Data, "Hello paragraph") {
			blockID = e.ID
		}
	}
	if textID == "" || blockID == "" || textID == blockID || !strings.Contains(blockID, ".") {
		t.Fatalf("ids text %q block %q in %#v", textID, blockID, first)
	}
	if strconv.Itoa(ev.Seq) != textID {
		t.Fatalf("text id %s want seq %d", textID, ev.Seq)
	}

	resend := collectSSE(t, eventsURL, h.cookieList(), textID, time.Second, func(ev []sseEvent) bool {
		return countEvent(ev, "block") > 0
	})
	if countEvent(resend, "text") != 0 || countEvent(resend, "block") != 1 || !strings.Contains(resend[indexEvent(resend, "block")].Data, "Hello paragraph") {
		t.Fatalf("resume from text id: %#v", resend)
	}

	again := collectSSE(t, eventsURL, h.cookieList(), blockID, 400*time.Millisecond, nil)
	if countEvent(again, "block") != 0 || countEvent(again, "text") != 0 {
		t.Fatalf("resume from block id replayed: %#v", again)
	}

	whole := collectSSE(t, eventsURL+"?after="+textID, h.cookieList(), "", 400*time.Millisecond, nil)
	if countEvent(whole, "block") != 0 || countEvent(whole, "text") != 0 {
		t.Fatalf("whole after replayed: %#v", whole)
	}
}

func collectSSE(t *testing.T, rawURL string, cookies []*http.Cookie, lastID string, wait time.Duration, stop func([]sseEvent) bool) []sseEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	ev, err := readSSE(ctx, rawURL, cookies, lastID, stop)
	if err != nil && ctx.Err() == nil && (stop == nil || !stop(ev)) {
		t.Fatal(err)
	}
	return ev
}

func indexEvent(ev []sseEvent, name string) int {
	for i := range ev {
		if ev[i].Event == name {
			return i
		}
	}
	return 0
}
