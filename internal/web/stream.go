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
	"github.com/scott/castbot/internal/store"
)

func runJob(userID, threadID, agentID, runID, text string) run.Job {
	return run.Job{UserID: userID, ThreadID: threadID, AgentID: agentID, RunID: runID, Text: text}
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
	if prev.Status != "failed" {
		writeText(w, http.StatusConflict, "Conflict\n")
		return
	}
	msg, err := s.store.UserMessageForRun(r.Context(), user.ID, prev.ID)
	if mapStoreErr(w, r, err) {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, prev.AgentID)
	if mapStoreErr(w, r, err) {
		return
	}
	next, err := s.store.CreateRun(r.Context(), user.ID, prev.ThreadID, prev.AgentID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetMessageRun(r.Context(), user.ID, msg.ID, next.ID); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.runs.Start(runJob(user.ID, prev.ThreadID, prev.AgentID, next.ID, msg.Body))
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

	after := 0
	if raw := r.Header.Get("Last-Event-ID"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			after = n
		}
	}
	signals, unsub := s.runs.Hub.Subscribe(runRow.ID)
	defer unsub()
	sentTerminal := false
	for {
		if err := r.Context().Err(); err != nil {
			return
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
			_ = writeSSE(w, 0, "stop", "")
			flush()
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-signals:
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func runTerminal(status string) bool {
	switch status {
	case "done", "failed", "stopped":
		return true
	default:
		return false
	}
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
