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

	"github.com/scott/castbot/internal/md"
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
		live := s.paintLive(r.Context(), user, next, av, from(r).CSRF)
		body, err := execTemplate("pending", live)
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

	cur := streamCursor(r)
	after := cur.listAfter()
	signals, unsub := s.runs.Hub.Subscribe(runRow.ID)
	defer unsub()
	sentTerminal := false
	orphaned := false
	lastKA := time.Now()
	lastCards := ""
	// Seed the pane with what the page already painted. An approvals
	// frame is sent only after that list changes, so activity ticks
	// do not swap the pane.
	lastPane, _ := s.approvalsSyncHTML(r.Context(), user, runRow.AgentID, from(r).CSRF)
	st := &liveSend{run: runRow}
	if err := s.primeLive(r.Context(), user, st, cur); err != nil {
		return
	}
	for {
		if err := r.Context().Err(); err != nil {
			return
		}
		if st.heldTail != "" && time.Since(st.lastTailAt) >= tailGap {
			if err := st.flushTail(w); err != nil {
				return
			}
			flush()
		}
		if time.Since(lastKA) >= s.keepaliveEvery() {
			gap := time.Since(lastKA)
			if gap > 20*time.Second {
				s.logger().Debug("sse keepalive gap", "run_id", runRow.ID, "gap_ms", gap.Milliseconds())
			}
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
		// A decision's card swap can detach the click's target, and the done
		// swap then closes this stream. Send the bar and badges first, while
		// the listener is still attached, and again on the done event.
		if batchTerminal(events) {
			if err := s.writeLiveCards(r.Context(), w, user, runRow, from(r).CSRF, &lastCards, &lastPane); err != nil {
				return
			}
			flush()
		}
		delivered := false
		for _, ev := range events {
			n, err := s.writeRunEvent(r.Context(), w, user, runRow, ev, from(r).CSRF, cur.skip(ev.Seq), st)
			if err != nil {
				return
			}
			after = ev.Seq
			if n > 0 {
				delivered = true
				flush()
			}
			if n > 0 && (ev.Kind == "done" || ev.Kind == "error") {
				sentTerminal = true
			}
		}
		if sentTerminal {
			_ = writeSSE(w, "", "stop", "")
			flush()
			return
		}
		// A plain Last-Event-ID already consumed a one-frame terminal event.
		// The list is not empty, but nothing new was written, so resend the
		// closing card the same way a cursor past the end does.
		if !delivered && batchTerminal(events) {
			if err := s.writeCaughtUp(r.Context(), w, user, runRow, from(r).CSRF, st); err != nil {
				return
			}
			flush()
			_ = writeSSE(w, "", "stop", "")
			flush()
			return
		}
		if err := s.writeLiveCards(r.Context(), w, user, runRow, from(r).CSRF, &lastCards, &lastPane); err != nil {
			return
		}
		if err := s.refreshLive(r.Context(), user, st); err != nil {
			return
		}
		flush()
		// Status and the terminal event commit together. A list from before
		// that commit can be empty while this read already sees done or
		// failed, so look once more before telling the browser to stop.
		if runTerminal(st.run.Status) && len(events) == 0 {
			more, err := s.store.ListRunEvents(r.Context(), user.ID, runRow.ID, after)
			if err != nil {
				return
			}
			if len(more) > 0 {
				continue
			}
			if err := s.writeCaughtUp(r.Context(), w, user, st.run, from(r).CSRF, st); err != nil {
				return
			}
			flush()
			_ = writeSSE(w, "", "stop", "")
			flush()
			return
		}
		// A queued run is waiting for its goroutine. Start tracks the run
		// before the response that names it, but a page read can still see
		// the row in that gap. Failing it here would interrupt a brand-new
		// run. A waiting approval is the user's decision, not a dead stream.
		// A streaming run whose goroutine is gone still fails.
		if !runTerminal(st.run.Status) && st.run.Status != "queued" && st.run.Status != "waiting_approval" && !s.runs.Watching(st.run.ID) {
			if orphaned {
				return
			}
			orphaned = true
			_, _ = s.store.FailRun(context.Background(), user.ID, st.run.ID, run.Interrupted, st.run.StreamOffset)
			continue
		}
		if !runTerminal(st.run.Status) {
			if err := s.syncActivity(w, st); err != nil {
				return
			}
			flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.runs.Done():
			if !orphaned && !runTerminal(st.run.Status) {
				orphaned = true
				_, _ = s.store.FailRun(context.Background(), user.ID, st.run.ID, run.Interrupted, st.run.StreamOffset)
			}
			timer := time.NewTimer(20 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		case <-signals:
		case <-time.After(st.tailDelay(200 * time.Millisecond)):
		}
	}
}

// resumePos is how far the browser got inside one store sequence.
// A text event is several frames (text, each closed block, tools, act)
// that share one store seq. The tail frame is not one of them: it is sent
// on its own cadence and carries no id, so holding it does not move the
// resume cursor. A plain query after=N means the page already
// painted every frame of N. A Last-Event-ID of N means only frame 0 arrived.
// A dotted id N.k means frame k arrived, so later frames of N must be resent.
type resumePos struct {
	seq   int
	part  int
	whole bool
}

func streamCursor(r *http.Request) resumePos {
	q, qOK := parseResume(r.URL.Query().Get("after"), true)
	h, hOK := parseResume(r.Header.Get("Last-Event-ID"), false)
	if !qOK {
		return h
	}
	if !hOK || !h.after(q) {
		return q
	}
	return h
}

func parseResume(raw string, query bool) (resumePos, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return resumePos{}, false
	}
	if i := strings.IndexByte(raw, '.'); i >= 0 {
		seq, err1 := strconv.Atoi(raw[:i])
		part, err2 := strconv.Atoi(raw[i+1:])
		if err1 != nil || err2 != nil || seq <= 0 || part < 0 || strings.Contains(raw[i+1:], ".") {
			return resumePos{}, false
		}
		return resumePos{seq: seq, part: part}, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return resumePos{}, false
	}
	if query {
		return resumePos{seq: n, whole: true}, true
	}
	return resumePos{seq: n, part: 0}, true
}

func (p resumePos) after(o resumePos) bool {
	if p.seq != o.seq {
		return p.seq > o.seq
	}
	if p.whole != o.whole {
		return p.whole
	}
	return p.part > o.part
}

func (p resumePos) listAfter() int {
	if p.seq <= 0 {
		return 0
	}
	if p.whole {
		return p.seq
	}
	return p.seq - 1
}

// skip is the last frame index already delivered for this seq, or -1 to send all.
func (p resumePos) skip(seq int) int {
	if !p.whole && p.seq == seq {
		return p.part
	}
	return -1
}

func runTerminal(status string) bool {
	switch status {
	case "done", "failed", "stopped", "retried":
		return true
	default:
		return false
	}
}

func (s *Server) writeCaughtUp(ctx context.Context, w http.ResponseWriter, user store.User, runRow store.Run, csrf string, st *liveSend) error {
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
	_, err = s.writeRunEvent(ctx, w, user, runRow, *last, csrf, -1, st)
	return err
}

func (s *Server) writeRunEvent(ctx context.Context, w io.Writer, user store.User, runRow store.Run, ev store.RunEvent, csrf string, fromPart int, st *liveSend) (n int, err error) {
	fw := &frameWriter{w: w, seq: ev.Seq, from: fromPart}
	defer func() { n = fw.sent }()
	switch ev.Kind {
	case "text":
		var payload struct {
			Text string `json:"text"`
		}
		if err = json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			return
		}
		if err = fw.write("text", html.EscapeString(payload.Text)); err != nil {
			return
		}
		err = s.writeMarkdownProgress(fw, st, ev)
		return
	case "tool":
		if err = fw.write("note", ""); err != nil {
			return
		}
		err = s.writeMarkdownProgress(fw, st, ev)
		return
	case "error":
		if err = st.flushTail(fw.w); err != nil {
			return
		}
		var payload struct {
			Message string `json:"message"`
		}
		if err = json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			return
		}
		var body string
		body, err = execTemplate("runError", s.calmLive(ctx, user, runRow, payload.Message, csrf))
		if err != nil {
			return
		}
		body += `<div id="tail-` + html.EscapeString(runRow.ID) + `" class="act-hold" hidden hx-swap-oob="outerHTML"></div>`
		body += s.sideOOB(ctx, user, runRow.AgentID)
		clearDwell(runRow.ID)
		err = fw.write("fail", body)
		return
	case "done":
		if err = st.flushTail(fw.w); err != nil {
			return
		}
		var payload struct {
			MessageID string `json:"messageId"`
		}
		if err = json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			return
		}
		err = s.writeDone(ctx, fw, user, runRow, payload.MessageID, csrf)
		return
	case "permission":
		st.pending = true
		if err = st.flushTail(fw.w); err != nil {
			return
		}
		var body string
		body, err = s.cardsHTML(ctx, user, runRow.ID, csrf)
		if err != nil {
			return
		}
		if err = fw.write("card", body); err != nil {
			return
		}
		err = s.writeLiveActivity(fw, st)
		return
	default:
		err = fw.write("note", "")
		return
	}
}

func (s *Server) writeDone(ctx context.Context, fw *frameWriter, user store.User, runRow store.Run, messageID, csrf string) error {
	msg, err := s.store.GetMessage(ctx, user.ID, messageID)
	if err != nil {
		return err
	}
	agent := agentView{Color: "c0", Letter: "?"}
	if row, err := s.store.GetAgent(ctx, user.ID, runRow.AgentID); err == nil {
		agent = presentAgent(row, false, userLocation(user.Timezone))
	}
	views := presentMessages([]store.Message{msg}, agent, s.store.Now(), userLocation(user.Timezone))
	s.linkMentionedFiles(ctx, user.ID, runRow.AgentID, views)
	if len(views) == 0 {
		return fw.write("done", "")
	}
	views[0].ShowDay = false
	cards, err := s.cardsFor(ctx, user, runRow.ID, csrf)
	if err != nil {
		return err
	}
	secrets := s.secretCardViews(ctx, user, runRow.AgentID, csrf)
	cards = append(cards, s.connectCardViews(ctx, user, runRow.AgentID, csrf)...)
	body, err := execTemplate("doneTurn", page{Messages: views, Cards: cards, SecretCards: secrets})
	if err != nil {
		return err
	}
	if chrome, cerr := s.approvalChrome(ctx, user, runRow.AgentID, runRow.ID); cerr == nil {
		body += chrome
	}
	body += s.sideOOB(ctx, user, runRow.AgentID)
	clearDwell(runRow.ID)
	return fw.write("done", body)
}

func (s *Server) calmLive(ctx context.Context, user store.User, runRow store.Run, message, csrf string) liveView {
	name := "The agent"
	if row, err := s.store.GetAgent(ctx, user.ID, runRow.AgentID); err == nil && row.Name != "" {
		name = row.Name
	}
	events, _ := s.store.ListRunEvents(ctx, user.ID, runRow.ID, 0)
	text, _, _, _, _ := foldRun(events)
	calm := calmFor(name, message, strings.TrimSpace(text) != "")
	return liveView{
		RunID: runRow.ID, Error: message, CSRF: csrf, AgentID: runRow.AgentID, AgentName: name,
		CalmTitle: calm.Title, CalmLine: calm.Line, CalmRetry: calm.Retry, CalmSecond: calm.Second, CalmWake: calm.Wake,
	}
}

func (s *Server) writeMarkdownProgress(fw *frameWriter, st *liveSend, ev store.RunEvent) error {
	before := st.fold.apply(ev)
	through := st.fold.body()
	prev, _ := md.Split(before)
	closed, tail := md.Split(through)
	out, elapsed := s.activityNow(st)
	inner := ""
	if strings.TrimSpace(tail.Raw) != "" {
		inner = md.RenderTail(tail)
	}
	if out.HideThread {
		if inner == "" && strings.TrimSpace(through) != "" {
			inner = `<span class="caret" aria-hidden="true"></span>`
		}
	} else {
		inner = strings.ReplaceAll(inner, `<span class="caret" aria-hidden="true"></span>`, "")
	}
	immediate := len(closed) > len(prev) || ev.Kind == "tool" || st.pending
	if err := st.emitTail(fw.w, inner, immediate); err != nil {
		return err
	}
	for i := len(prev); i < len(closed); i++ {
		if err := fw.write("block", md.Render(closed[i].Raw)); err != nil {
			return err
		}
	}
	tools := st.fold.toolsHTML()
	if tools != st.lastTools {
		if err := fw.write("tools", tools); err != nil {
			return err
		}
		st.lastTools = tools
	}
	return st.putAct(fw, fw.w, out, elapsed)
}

func (s *Server) writeLiveActivity(fw *frameWriter, st *liveSend) error {
	out, elapsed := s.activityNow(st)
	return st.putAct(fw, fw.w, out, elapsed)
}

// frameWriter gives each SSE frame of one store seq its own id.
// Frame 0 keeps the plain integer so existing text cursors still parse.
// Later frames use seq.part. A resume that saw only an earlier part
// skips those frames and sends the rest, including a block that closed
// in the same batch.
type frameWriter struct {
	w    io.Writer
	seq  int
	part int
	from int
	sent int
}

func (f *frameWriter) write(event, data string) error {
	id := ""
	if f.seq > 0 {
		id = strconv.Itoa(f.seq)
		if f.part > 0 {
			id += "." + strconv.Itoa(f.part)
		}
	}
	part := f.part
	f.part++
	if part <= f.from {
		return nil
	}
	f.sent++
	return writeSSE(f.w, id, event, data)
}

func batchTerminal(events []store.RunEvent) bool {
	for _, ev := range events {
		if ev.Kind == "done" || ev.Kind == "error" {
			return true
		}
	}
	return false
}

func (s *Server) writeLiveCards(ctx context.Context, w http.ResponseWriter, user store.User, runRow store.Run, csrf string, last, lastPane *string) error {
	if lastPane != nil {
		if next, perr := s.approvalsSyncHTML(ctx, user, runRow.AgentID, csrf); perr == nil && next != *lastPane {
			if err := writeSSE(w, "", "approvals", next); err != nil {
				return err
			}
			*lastPane = next
		}
	}
	cards, err := s.cardsHTML(ctx, user, runRow.ID, csrf)
	if err != nil || cards == "" || cards == *last {
		return nil
	}
	if err := writeSSE(w, "", "card", cards); err != nil {
		return err
	}
	*last = cards
	return nil
}

func writeSSE(w io.Writer, id, event, data string) error {
	var b strings.Builder
	if id != "" && id != "0" {
		b.WriteString("id: ")
		b.WriteString(id)
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
	secrets := s.secretCardViews(r.Context(), user, agent.ID, from(r).CSRF)
	connects := s.connectCardViews(r.Context(), user, agent.ID, from(r).CSRF)
	attached := false
	var out []liveView
	for _, row := range runs {
		if s.isGreetRun(row.ID) {
			continue
		}
		cards, _ := s.cardsFor(r.Context(), user, row.ID, from(r).CSRF)
		if !attached {
			cards = append(cards, connects...)
		}
		switch row.Status {
		case "queued", "streaming", "waiting_approval":
			view := s.paintLive(r.Context(), user, row, agent, from(r).CSRF)
			view.Cards = cards
			if !attached {
				view.SecretCards = secrets
				attached = true
			}
			out = append(out, view)
		case "failed":
			if row.ID != last.ID || row.RoutineRunID != "" {
				continue
			}
			view := s.paintLive(r.Context(), user, row, agent, from(r).CSRF)
			view.Cards = cards
			if !attached {
				view.SecretCards = secrets
				attached = true
			}
			view.Failed = true
			if view.Error == "" {
				_, view.Error = s.runProgress(r, user.ID, row)
				calm := calmFor(agent.Name, view.Error, strings.TrimSpace(view.Text) != "")
				view.CalmTitle, view.CalmLine, view.CalmRetry, view.CalmSecond = calm.Title, calm.Line, calm.Retry, calm.Second
				view.CalmWake = calm.Wake
			}
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
