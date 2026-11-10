package web

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func runJob(userID, threadID, agentID, runID, messageID, text, sessionID string, history []runtime.Turn) run.Job {
	return run.Job{UserID: userID, ThreadID: threadID, AgentID: agentID, RunID: runID, MessageID: messageID, Text: text, SessionID: sessionID, History: history}
}

// priorTurns is the transcript before this user message.
// The runtime receives the newest 50 turns, or about 100 KB of role
// plus text, whichever is smaller. Older turns are left out.
func (s *Server) priorTurns(ctx context.Context, userID, threadID, skip string) []runtime.Turn {
	msgs, err := s.store.ListMessages(ctx, userID, threadID)
	if err != nil {
		return nil
	}
	var out []runtime.Turn
	for _, msg := range msgs {
		if msg.ID == skip || (msg.AuthorKind != "user" && msg.AuthorKind != "agent") {
			continue
		}
		out = append(out, runtime.Turn{Role: msg.AuthorKind, Text: msg.Body})
	}
	return runtime.CapHistory(out)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	runRow, err := s.store.GetRun(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	writeText(w, http.StatusOK, runRow.ID+"\t"+runRow.Status+"\n")
}

func (s *Server) retryRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	prev, err := s.store.GetRun(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	next, msg, err := s.store.RetryRun(r.Context(), user.ID, prev.ID)
	if mapStoreErr(w, r, err) {
		return
	}
	sessionID := ""
	if th, err := s.store.GetThread(r.Context(), user.ID, prev.ThreadID); err == nil {
		sessionID = th.CastSessionID
	}
	s.runs.Start(runJob(user.ID, prev.ThreadID, prev.AgentID, next.ID, msg.ID, msg.Body, sessionID, s.priorTurns(r.Context(), user.ID, prev.ThreadID, msg.ID)))
	agent, err := s.store.GetAgent(r.Context(), user.ID, prev.AgentID)
	if mapStoreErr(w, r, err) {
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		av := presentAgent(agent, true, userLocation(user.Timezone))
		body, err := execTemplate("pending", liveView{RunID: next.ID, Color: av.Color, Letter: av.Letter, CSRF: from(r).CSRF})
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
		return
	}
	http.Redirect(w, r, "/agents/"+agent.ID, http.StatusSeeOther)
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	runRow, err := s.store.GetRun(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	flush := func() { _ = rc.Flush() }
	flush()

	after := streamCursor(r)
	signals, unsub := s.runs.Hub.Subscribe(runRow.ID)
	defer unsub()
	sentTerminal := false
	orphaned := false
	lastKA := time.Now()
	for {
		if err := r.Context().Err(); err != nil {
			return
		}
		if time.Since(lastKA) >= s.keepaliveEvery() {
			if _, err := io.WriteString(w, ": ka\n\n"); err != nil {
				return
			}
			flush()
			lastKA = time.Now()
		}
		events, err := s.store.ListRunEvents(r.Context(), user.ID, runRow.ID, after)
		if err != nil {
			return
		}
		for _, ev := range events {
			if err := s.writeRunEvent(r.Context(), w, user, runRow, ev, from(r).CSRF); err != nil {
				return
			}
			after = ev.Seq
			flush()
			if ev.Kind == "done" || ev.Kind == "error" {
				sentTerminal = true
			}
		}
		if sentTerminal {
			_ = writeSSE(w, 0, "stop", "")
			flush()
			return
		}
		current, err := s.store.GetRun(r.Context(), user.ID, runRow.ID)
		if err != nil {
			return
		}
		// Status and the terminal event commit together. A list from before
		// that commit can be empty while this read already sees done or
		// failed, so look once more before telling the browser to stop.
		if runTerminal(current.Status) && len(events) == 0 {
			more, err := s.store.ListRunEvents(r.Context(), user.ID, runRow.ID, after)
			if err != nil {
				return
			}
			if len(more) > 0 {
				continue
			}
			if err := s.writeCaughtUp(r.Context(), w, user, current, from(r).CSRF); err != nil {
				return
			}
			flush()
			_ = writeSSE(w, 0, "stop", "")
			flush()
			return
		}
		// A queued run is waiting for its goroutine. Start tracks the run
		// before the response that names it, but a page read can still see
		// the row in that gap. Failing it here would interrupt a brand-new
		// run. A streaming run whose goroutine is gone still fails.
		if !runTerminal(current.Status) && current.Status != "queued" && !s.runs.Watching(current.ID) {
			if orphaned {
				return
			}
			orphaned = true
			_, _ = s.store.FailRun(context.Background(), user.ID, current.ID, run.Interrupted, current.StreamOffset)
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.runs.Done():
			if !orphaned && !runTerminal(current.Status) {
				orphaned = true
				_, _ = s.store.FailRun(context.Background(), user.ID, current.ID, run.Interrupted, current.StreamOffset)
			}
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		case <-signals:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func streamCursor(r *http.Request) int {
	after := 0
	if n, ok := parseSeq(r.URL.Query().Get("after")); ok && n > after {
		after = n
	}
	if n, ok := parseSeq(r.Header.Get("Last-Event-ID")); ok && n > after {
		after = n
	}
	return after
}

func parseSeq(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func runTerminal(status string) bool {
	switch status {
	case "done", "failed", "stopped", "retried":
		return true
	default:
		return false
	}
}

func (s *Server) writeCaughtUp(ctx context.Context, w http.ResponseWriter, user store.User, runRow store.Run, csrf string) error {
	events, err := s.store.ListRunEvents(ctx, user.ID, runRow.ID, 0)
	if err != nil {
		return err
	}
	var last *store.RunEvent
	for i := range events {
		if events[i].Kind == "done" || events[i].Kind == "error" {
			last = &events[i]
		}
	}
	if last == nil {
		return nil
	}
	return s.writeRunEvent(ctx, w, user, runRow, *last, csrf)
}

func (s *Server) writeRunEvent(ctx context.Context, w http.ResponseWriter, user store.User, runRow store.Run, ev store.RunEvent, csrf string) error {
	switch ev.Kind {
	case "text":
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			return err
		}
		return writeSSE(w, ev.Seq, "text", html.EscapeString(payload.Text))
	case "error":
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			return err
		}
		body, err := execTemplate("runError", liveView{RunID: runRow.ID, Error: payload.Message, CSRF: csrf})
		if err != nil {
			return err
		}
		return writeSSE(w, ev.Seq, "fail", body)
	case "done":
		var payload struct {
			MessageID string `json:"messageId"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			return err
		}
		return s.writeDone(ctx, w, user, runRow, ev.Seq, payload.MessageID)
	default:
		return writeSSE(w, ev.Seq, "note", "")
	}
}

func (s *Server) writeDone(ctx context.Context, w http.ResponseWriter, user store.User, runRow store.Run, seq int, messageID string) error {
	msg, err := s.store.GetMessage(ctx, user.ID, messageID)
	if err != nil {
		return err
	}
	agent := agentView{Color: "c0", Letter: "?"}
	if row, err := s.store.GetAgent(ctx, user.ID, runRow.AgentID); err == nil {
		agent = presentAgent(row, false, userLocation(user.Timezone))
	}
	views := presentMessages([]store.Message{msg}, agent, s.store.Now(), userLocation(user.Timezone))
	if len(views) == 0 {
		return writeSSE(w, seq, "done", "")
	}
	views[0].ShowDay = false
	body, err := execTemplate("messages", page{Messages: views})
	if err != nil {
		return err
	}
	return writeSSE(w, seq, "done", body)
}

func writeSSE(w http.ResponseWriter, id int, event, data string) error {
	var b strings.Builder
	if id > 0 {
		b.WriteString("id: ")
		b.WriteString(strconv.Itoa(id))
		b.WriteByte('\n')
	}
	if event != "" {
		b.WriteString("event: ")
		b.WriteString(event)
		b.WriteByte('\n')
	}
	if data == "" {
		b.WriteString("data: \n")
	} else {
		for _, line := range strings.Split(data, "\n") {
			b.WriteString("data: ")
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	_, err := io.WriteString(w, b.String())
	return err
}

func (s *Server) loadLives(r *http.Request, user store.User, threadID string, agent agentView) []liveView {
	runs, err := s.store.ListThreadRuns(r.Context(), user.ID, threadID)
	if err != nil || len(runs) == 0 {
		return nil
	}
	last := runs[len(runs)-1]
	var out []liveView
	for _, row := range runs {
		view := liveView{RunID: row.ID, Color: agent.Color, Letter: agent.Letter, CSRF: from(r).CSRF}
		switch row.Status {
		case "queued", "streaming", "waiting_approval":
			out = append(out, view)
		case "failed":
			if row.ID != last.ID {
				continue
			}
			view.Failed = true
			view.Text, view.Error = s.runProgress(r, user.ID, row)
			out = append(out, view)
		}
	}
	return out
}

func (s *Server) runProgress(r *http.Request, userID string, row store.Run) (string, string) {
	events, err := s.store.ListRunEvents(r.Context(), userID, row.ID, 0)
	if err != nil {
		return "", row.Error
	}
	var b strings.Builder
	message := row.Error
	for _, ev := range events {
		switch ev.Kind {
		case "text":
			var payload struct {
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(ev.Payload), &payload) == nil {
				b.WriteString(payload.Text)
			}
		case "error":
			var payload struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(ev.Payload), &payload) == nil && payload.Message != "" {
				message = payload.Message
			}
		}
	}
	return b.String(), message
}
