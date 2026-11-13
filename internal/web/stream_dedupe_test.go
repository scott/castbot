package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/store"
)

func TestStreamSkipsUnchangedActAndTools(t *testing.T) {
	h := newHarness(t)
	frozen := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return frozen })
	ip := "203.0.113.221:9"
	_, secret := h.account("dedupe@example.com", "Dedupe", "correct-horse-1")
	h.login("dedupe@example.com", "correct-horse-1", secret, ip, false)
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
	tool := `{"kind":"read","label":"notes","status":"running"}`
	if _, err := h.st.AppendRunEvent(context.Background(), userID, runRow.ID, "tool", tool, 1); err != nil {
		t.Fatal(err)
	}
	for i, word := range []string{"alpha ", "beta ", "gamma ", "delta "} {
		raw, err := json.Marshal(map[string]string{"text": word})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.AppendRunEvent(context.Background(), userID, runRow.ID, "text", string(raw), i+2); err != nil {
			t.Fatal(err)
		}
	}
	release := h.srv.runs.PinWatch(runRow.ID, agent.ID)
	t.Cleanup(release)
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)

	got := collectSSE(t, live.URL+"/runs/"+runRow.ID+"/events", h.cookieList(), "", 3*time.Second, func(ev []sseEvent) bool {
		return countEvent(ev, "text") >= 4
	})
	if countEvent(got, "text") < 4 {
		t.Fatalf("texts %#v", got)
	}
	if countEvent(got, "tools") != 1 {
		t.Fatalf("tools sent %d, want 1: %#v", countEvent(got, "tools"), got)
	}
	if countEvent(got, "act") != 0 || countEvent(got, "actxt") != 0 {
		t.Fatalf("activity resent: %#v", got)
	}
}

func TestLabelAndClockUpdateTextWithoutNewActRow(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { clearDwell("run-label") })
	base := time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)
	freeze(h, base.Add(10*time.Second))
	runRow := store.Run{ID: "run-label", Status: "streaming", StartedAt: base.Format(time.RFC3339)}
	st := &liveSend{run: runRow}
	out, elapsed := h.srv.activityNow(st)
	if out.Label != "Thinking" || out.HideThread || elapsed != "10s" {
		t.Fatalf("seed %+v %q", out, elapsed)
	}
	st.noteAct(out, elapsed)

	freeze(h, base.Add(10*time.Second+actMinDwell))
	labelEv := emitRunEvent(t, h, st, runRow, store.RunEvent{
		Seq: 1, Kind: "tool", Payload: `{"kind":"read","label":"notes","status":"running"}`,
	})
	if countEvent(labelEv, "act") != 0 {
		t.Fatalf("label change remounted the row: %#v", labelEv)
	}
	if countEvent(labelEv, "tools") != 1 {
		t.Fatalf("tools %#v", labelEv)
	}
	actxt := eventData(t, labelEv, "actxt")
	if !strings.Contains(actxt, ">Reading files<") || strings.Contains(actxt, "<div class=\"act\"") || strings.Contains(actxt, "actico") {
		t.Fatalf("label frame %q", actxt)
	}

	same := emitRunEvent(t, h, st, runRow, textRunEvent(2, "hello "))
	if countEvent(same, "act") != 0 || countEvent(same, "actxt") != 0 || countEvent(same, "tools") != 0 {
		t.Fatalf("unchanged chunk resent activity or tools: %#v", same)
	}

	freeze(h, base.Add(11*time.Second))
	clock := emitRunEvent(t, h, st, runRow, textRunEvent(3, "there"))
	if countEvent(clock, "act") != 0 || countEvent(clock, "tools") != 0 {
		t.Fatalf("clock change remounted the row: %#v", clock)
	}
	clockText := eventData(t, clock, "actxt")
	if !strings.Contains(clockText, ">11s<") || !strings.Contains(clockText, ">Reading files<") || strings.Contains(clockText, "<div class=\"act\"") || strings.Contains(clockText, "actico") {
		t.Fatalf("clock frame %q", clockText)
	}
}

func TestMoreTextDoesNotRemountHiddenActivity(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { clearDwell("run-hide") })
	base := time.Date(2026, 11, 13, 16, 30, 0, 0, time.UTC)
	freeze(h, base)
	runRow := store.Run{ID: "run-hide", Status: "streaming", StartedAt: base.Format(time.RFC3339)}
	st := &liveSend{run: runRow}
	out, elapsed := h.srv.activityNow(st)
	st.noteAct(out, elapsed)

	first := emitRunEvent(t, h, st, runRow, textRunEvent(1, "one "))
	if countEvent(first, "act") != 1 || !strings.Contains(eventData(t, first, "act"), "<div class=\"act\"") {
		t.Fatalf("first row %#v", first)
	}
	for i, word := range []string{"two ", "three "} {
		again := emitRunEvent(t, h, st, runRow, textRunEvent(i+2, word))
		if countEvent(again, "act") != 0 || countEvent(again, "actxt") != 0 || countEvent(again, "tools") != 0 {
			t.Fatalf("chunk %d resent %#v", i+2, again)
		}
	}
	freeze(h, base.Add(6*time.Second))
	clock := emitRunEvent(t, h, st, runRow, textRunEvent(4, "four"))
	if countEvent(clock, "act") != 0 {
		t.Fatalf("elapsed remount %#v", clock)
	}
	if !strings.Contains(eventData(t, clock, "actxt"), ">6s<") || strings.Contains(eventData(t, clock, "actxt"), "<div class=\"act\"") {
		t.Fatalf("elapsed frame %q", eventData(t, clock, "actxt"))
	}
}

func freeze(h *harness, at time.Time) {
	h.st.SetNow(func() time.Time { return at })
}

func emitRunEvent(t *testing.T, h *harness, st *liveSend, runRow store.Run, ev store.RunEvent) []sseEvent {
	t.Helper()
	var buf bytes.Buffer
	if _, err := h.srv.writeRunEvent(context.Background(), &buf, store.User{ID: "user-dedupe"}, runRow, ev, "", -1, st); err != nil {
		t.Fatal(err)
	}
	return parseSSE(buf.String())
}

func textRunEvent(seq int, text string) store.RunEvent {
	raw, _ := json.Marshal(map[string]string{"text": text})
	return store.RunEvent{Seq: seq, Kind: "text", Payload: string(raw)}
}

func eventData(t *testing.T, ev []sseEvent, name string) string {
	t.Helper()
	for _, e := range ev {
		if e.Event == name {
			return e.Data
		}
	}
	t.Fatalf("no %s in %#v", name, ev)
	return ""
}

func parseSSE(raw string) []sseEvent {
	var out []sseEvent
	var cur sseEvent
	var data []string
	flush := func() {
		if cur.ID == "" && cur.Event == "" && len(data) == 0 {
			return
		}
		cur.Data = strings.Join(data, "\n")
		out = append(out, cur)
		cur = sseEvent{}
		data = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "id:"):
			cur.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "event:"):
			cur.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(v, " ") {
				v = v[1:]
			}
			data = append(data, v)
		}
	}
	flush()
	return out
}
