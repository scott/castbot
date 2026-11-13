package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/store"
)

func TestTailFramesStayBounded(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { clearDwell("run-tail") })
	freeze(h, time.Date(2026, 11, 13, 17, 0, 0, 0, time.UTC))
	runRow := store.Run{ID: "run-tail", Status: "streaming", StartedAt: time.Date(2026, 11, 13, 17, 0, 0, 0, time.UTC).Format(time.RFC3339)}
	st := &liveSend{run: runRow}
	out, elapsed := h.srv.activityNow(st)
	st.noteAct(out, elapsed)

	const n = 40
	var all []sseEvent
	for i := 0; i < n; i++ {
		word := fmt.Sprintf("w%02d ", i)
		if i == n-1 {
			word = "w39tailend"
		}
		all = append(all, emitRunEvent(t, h, st, runRow, textRunEvent(i+1, word))...)
	}
	during := countEvent(all, "tail")
	if during < 1 || during > 8 {
		t.Fatalf("tail frames in a short burst %d, want a handful", during)
	}
	var buf bytes.Buffer
	if err := st.flushTail(&buf); err != nil {
		t.Fatal(err)
	}
	all = append(all, parseSSE(buf.String())...)
	if countEvent(all, "tail") > 8 {
		t.Fatalf("tail frames %d", countEvent(all, "tail"))
	}
	last := lastEventData(all, "tail")
	if !strings.Contains(last, "w00") || !strings.Contains(last, "w39tailend") {
		t.Fatalf("final tail missing text: %q", last)
	}
}

func TestBlockAndToolFlushTheTail(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { clearDwell("run-block") })
	freeze(h, time.Date(2026, 11, 13, 17, 5, 0, 0, time.UTC))
	runRow := store.Run{ID: "run-block", Status: "streaming", StartedAt: time.Date(2026, 11, 13, 17, 5, 0, 0, time.UTC).Format(time.RFC3339)}
	st := &liveSend{run: runRow}
	out, elapsed := h.srv.activityNow(st)
	st.noteAct(out, elapsed)

	first := emitRunEvent(t, h, st, runRow, textRunEvent(1, "Alpha beta"))
	if countEvent(first, "tail") != 1 || !strings.Contains(eventData(t, first, "tail"), "Alpha beta") {
		t.Fatalf("open tail %#v", first)
	}
	closed := emitRunEvent(t, h, st, runRow, textRunEvent(2, " gamma.\n\n"))
	if countEvent(closed, "block") != 1 || !strings.Contains(eventData(t, closed, "block"), "Alpha beta gamma") {
		t.Fatalf("block %#v", closed)
	}
	bi := indexEvent(closed, "block")
	prev := ""
	for i := bi - 1; i >= 0; i-- {
		if closed[i].Event == "tail" {
			prev = closed[i].Data
			break
		}
	}
	if prev == "" || strings.Contains(prev, "Alpha beta") {
		t.Fatalf("closed block left the paragraph in the tail: %q before %#v", prev, closed)
	}

	held := emitRunEvent(t, h, st, runRow, textRunEvent(3, "Next line"))
	if countEvent(held, "tail") != 0 || st.heldTail == "" {
		t.Fatalf("expected the next line to be held: %#v held %q", held, st.heldTail)
	}
	tool := emitRunEvent(t, h, st, runRow, store.RunEvent{
		Seq: 4, Kind: "tool", Payload: `{"kind":"read","label":"notes","status":"running"}`,
	})
	if st.heldTail != "" || !strings.Contains(eventData(t, tool, "tail"), "Next line") {
		t.Fatalf("tool did not flush the tail: held %q %#v", st.heldTail, tool)
	}
}

func TestRunEndFlushesTheFullTail(t *testing.T) {
	h := newHarness(t)
	frozen := time.Date(2026, 11, 13, 17, 10, 0, 0, time.UTC)
	freeze(h, frozen)
	ip := "203.0.113.222:9"
	_, secret := h.account("tail@example.com", "Tail", "correct-horse-1")
	h.login("tail@example.com", "correct-horse-1", secret, ip, false)
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
	const n = 30
	var full strings.Builder
	for i := 0; i < n; i++ {
		word := fmt.Sprintf("p%02d ", i)
		if i == n-1 {
			word = "p29tailend"
		}
		full.WriteString(word)
		raw, err := json.Marshal(map[string]string{"text": word})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.AppendRunEvent(context.Background(), userID, runRow.ID, "text", string(raw), i+1); err != nil {
			t.Fatal(err)
		}
	}
	msg, err := h.st.CreateMessage(context.Background(), userID, thread.ID, "agent", agent.ID, full.String())
	if err != nil {
		t.Fatal(err)
	}
	done, err := json.Marshal(map[string]string{"messageId": msg.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendRunEvent(context.Background(), userID, runRow.ID, "done", string(done), n+1); err != nil {
		t.Fatal(err)
	}
	release := h.srv.runs.PinWatch(runRow.ID, agent.ID)
	t.Cleanup(release)
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	started := time.Now()
	got := collectSSE(t, live.URL+"/runs/"+runRow.ID+"/events", h.cookieList(), "", 3*time.Second, func(ev []sseEvent) bool {
		return countEvent(ev, "done") > 0
	})
	if time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("run end waited %s", time.Since(started))
	}
	if countEvent(got, "text") != n || countEvent(got, "done") != 1 {
		t.Fatalf("text %d done %d", countEvent(got, "text"), countEvent(got, "done"))
	}
	tails := countEvent(got, "tail")
	if tails < 1 || tails > 8 {
		t.Fatalf("tail frames %d", tails)
	}
	last := lastEventData(got, "tail")
	if !strings.Contains(last, "p00") || !strings.Contains(last, "p29tailend") {
		t.Fatalf("final tail %q", last)
	}
	blocks := 0
	for _, e := range got {
		if e.Event == "block" {
			blocks++
		}
	}
	if blocks != 0 {
		t.Fatalf("open paragraph was closed into %d blocks", blocks)
	}
}

func TestAutoscrollOncePerFrame(t *testing.T) {
	b, err := staticFS.ReadFile("static/stream.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	if !strings.Contains(js, "function scrollSoon()") || !strings.Contains(js, "scrollQueued") || !strings.Contains(js, "scrollSoon();") {
		t.Fatal("sse autoscroll is not limited to one scroll per frame")
	}
	if !strings.Contains(js, `closest("#msgs")) scrollSoon()`) {
		t.Fatal("a live swap still scrolls on every event")
	}
}

func lastEventData(ev []sseEvent, name string) string {
	last := ""
	for _, e := range ev {
		if e.Event == name {
			last = e.Data
		}
	}
	return last
}
