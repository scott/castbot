package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/chattool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const (
	chatMiss      = "No thread matches that."
	chatNone      = "Nothing is running."
	chatMany      = "More than one reply is in progress. Say which thread."
	chatCLI       = "A Cast CLI thread has no Stop."
	chatGone      = "That reply is no longer running."
	chatUnstop    = "The reply could not be stopped."
	chatStopped   = "Stopped the reply."
	chatRetryNone = "Nothing failed."
	chatRetryMany = "More than one reply failed. Say which thread."
	chatRetryNo   = "That reply could not be retried."
	chatRetryWait = "That reply is not waiting to be retried."
	chatRetryOK   = "Retrying."
	chatThreads   = "Threads could not be read."
	chatOpenAsk   = "Say which thread to open."
	chatOpenMany  = "More than one thread matches. Say which one."
	chatEmpty     = "No threads yet."
)

// machineChat runs one chat tool for the machine's account and the agent
// in the path. A user_id or agent_id argument is ignored.
func (s *Server) machineChat(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	caller, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	var args chatArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, "That request was rejected.", true)
			return
		}
	}
	text, failed := s.runChatTool(r.Context(), user, caller, name, args)
	writeAgentResult(w, text, failed)
}

type chatArgs struct {
	Thread string `json:"thread"`
}

// maybeChat reads one castbot-chat fence from the latest reply.
func (s *Server) maybeChat(job run.Job) {
	ctx := context.Background()
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, fields, ok := chattool.Parse(reply.Body)
	if !ok {
		return
	}
	user, err := s.store.GetUser(ctx, job.UserID)
	if err != nil {
		return
	}
	caller, err := s.store.GetAgent(ctx, job.UserID, job.AgentID)
	if err != nil {
		return
	}
	text, failed := s.runChatTool(ctx, user, caller, fields.Action, chatArgs{Thread: fields.Thread})
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func (s *Server) runChatTool(ctx context.Context, user store.User, caller store.Agent, name string, args chatArgs) (string, bool) {
	switch name {
	case chattool.ToolStop:
		return s.stopReplyNow(ctx, user, caller, args.Thread)
	case chattool.ToolRetry:
		return s.retryReplyNow(ctx, user, caller, args.Thread)
	case chattool.ToolList:
		return s.listThreadText(ctx, user.ID, caller.ID)
	case chattool.ToolOpen:
		return s.openNamedThread(ctx, user.ID, caller.ID, args.Thread)
	default:
		return "That request was rejected.", true
	}
}

func (s *Server) stopReplyNow(ctx context.Context, user store.User, caller store.Agent, query string) (string, bool) {
	threads, err := s.store.ListThreadsByAgent(ctx, user.ID, caller.ID)
	if err != nil {
		return chatThreads, true
	}
	chosen := threads
	if strings.TrimSpace(query) != "" {
		chosen = matchThreads(threads, query)
		if len(chosen) == 0 {
			return chatMiss, true
		}
		if len(chosen) > 1 {
			return threadChoice(chatOpenMany, chosen), true
		}
	}
	open, err := s.openRunsFor(ctx, user.ID, caller.ID, chosen)
	if err != nil {
		return chatThreads, true
	}
	if len(chosen) == 1 && chosen[0].Kind == "cli" && len(open) > 0 {
		return chatCLI, true
	}
	var stops []store.OpenRun
	for _, row := range open {
		if s.stopTarget(ctx, user.ID, row.Run) == "" {
			continue
		}
		stops = append(stops, row)
	}
	if len(stops) == 0 {
		return chatNone, true
	}
	if len(stops) > 1 {
		return s.openChoice(ctx, user.ID, chatMany, stops)
	}
	return s.stopOne(ctx, user, stops[0].Run)
}

func (s *Server) openRunsFor(ctx context.Context, userID, agentID string, threads []store.Thread) ([]store.OpenRun, error) {
	rows, err := s.store.ListOpenRuns(ctx, userID)
	if err != nil {
		return nil, err
	}
	allow := map[string]bool{}
	for _, th := range threads {
		allow[th.ID] = true
	}
	var out []store.OpenRun
	for _, row := range rows {
		if row.AgentID != agentID || !allow[row.ThreadID] {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Server) stopOne(ctx context.Context, user store.User, row store.Run) (string, bool) {
	if row.Status == "queued" {
		_, _, changed, err := s.store.StopRun(ctx, user.ID, row.ID, false)
		if err != nil {
			return chatUnstop, true
		}
		if changed {
			s.finishStop(ctx, user, row)
			return chatStopped, false
		}
		fresh, err := s.store.GetRun(ctx, user.ID, row.ID)
		if err != nil {
			return chatUnstop, true
		}
		if fresh.Status != "streaming" && fresh.Status != "waiting_approval" {
			return chatGone, true
		}
		row = fresh
	}
	if err := s.cancelGuest(ctx, user.ID, row); err != nil {
		return stillRunning, true
	}
	_, _, changed, err := s.store.StopRun(ctx, user.ID, row.ID, true)
	if err != nil {
		return chatUnstop, true
	}
	if !changed {
		return chatGone, true
	}
	s.finishStop(ctx, user, row)
	return chatStopped, false
}

func (s *Server) retryReplyNow(ctx context.Context, user store.User, caller store.Agent, query string) (string, bool) {
	threads, err := s.store.ListThreadsByAgent(ctx, user.ID, caller.ID)
	if err != nil {
		return chatThreads, true
	}
	chosen := threads
	if strings.TrimSpace(query) != "" {
		chosen = matchThreads(threads, query)
		if len(chosen) == 0 {
			return chatMiss, true
		}
		if len(chosen) > 1 {
			return threadChoice(chatOpenMany, chosen), true
		}
	}
	var hits []store.Thread
	var runs []store.Run
	for _, th := range chosen {
		list, err := s.store.ListThreadRuns(ctx, user.ID, th.ID)
		if err != nil {
			return chatThreads, true
		}
		var last *store.Run
		for i := range list {
			if list[i].AgentID == caller.ID && list[i].Status == "failed" {
				last = &list[i]
			}
		}
		if last != nil {
			hits = append(hits, th)
			runs = append(runs, *last)
		}
	}
	if len(runs) == 0 {
		return chatRetryNone, true
	}
	if len(runs) > 1 {
		return threadChoice(chatRetryMany, hits), true
	}
	return s.retryOne(ctx, user, runs[0])
}

func (s *Server) retryOne(ctx context.Context, user store.User, prev store.Run) (string, bool) {
	next, msg, err := s.store.RetryRun(ctx, user.ID, prev.ID)
	if errors.Is(err, store.ErrConflict) {
		return chatRetryWait, true
	}
	if err != nil {
		return chatRetryNo, true
	}
	sessionID := ""
	if th, err := s.store.GetThread(ctx, user.ID, prev.ThreadID); err == nil {
		sessionID = th.CastSessionID
	}
	s.runs.Start(runJob(user.ID, prev.ThreadID, prev.AgentID, next.ID, msg.ID, msg.Body, sessionID, s.priorTurns(ctx, user.ID, prev.ThreadID, msg.ID)))
	return chatRetryOK, false
}

func (s *Server) listThreadText(ctx context.Context, userID, agentID string) (string, bool) {
	threads, err := s.store.ListThreadsByAgent(ctx, userID, agentID)
	if err != nil {
		return chatThreads, true
	}
	if len(threads) == 0 {
		return chatEmpty, false
	}
	var b strings.Builder
	for _, th := range threads {
		line := threadLine(th)
		if b.Len() > 0 && b.Len()+1+len(line) > 8000 {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	return b.String(), false
}

func (s *Server) openNamedThread(ctx context.Context, userID, agentID, query string) (string, bool) {
	threads, err := s.store.ListThreadsByAgent(ctx, userID, agentID)
	if err != nil {
		return chatThreads, true
	}
	if len(threads) == 0 {
		return chatEmpty, true
	}
	chosen := threads
	if strings.TrimSpace(query) != "" {
		chosen = matchThreads(threads, query)
	} else if len(threads) > 1 {
		return chatOpenAsk, true
	}
	if len(chosen) == 0 {
		return chatMiss, true
	}
	if len(chosen) > 1 {
		return threadChoice(chatOpenMany, chosen), true
	}
	return s.openedThread(ctx, userID, chosen[0]), false
}

func (s *Server) openedThread(ctx context.Context, userID string, th store.Thread) string {
	var b strings.Builder
	b.WriteString("Opened " + threadDisplay(th) + ". " + threadKindLabel(th) + ".")
	msgs, err := s.store.ListMessages(ctx, userID, th.ID)
	if err != nil {
		return b.String()
	}
	start := 0
	if len(msgs) > 20 {
		start = len(msgs) - 20
	}
	for _, msg := range msgs[start:] {
		body := strings.TrimSpace(msg.Body)
		if body == "" {
			continue
		}
		line := msg.AuthorKind + ": " + clipRunes(body, 240)
		if b.Len()+1+len(line) > 8000 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}

func (s *Server) openChoice(ctx context.Context, userID, head string, rows []store.OpenRun) (string, bool) {
	var threads []store.Thread
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.ThreadID] {
			continue
		}
		seen[row.ThreadID] = true
		th, err := s.store.GetThread(ctx, userID, row.ThreadID)
		if err != nil {
			continue
		}
		threads = append(threads, th)
	}
	return threadChoice(head, threads), true
}

func threadChoice(head string, threads []store.Thread) string {
	var b strings.Builder
	b.WriteString(head)
	for _, th := range threads {
		line := threadLine(th)
		if b.Len()+1+len(line) > 8000 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}

func threadDisplay(th store.Thread) string {
	if th.Kind == "dm" {
		return "Chat"
	}
	if title := strings.TrimSpace(th.Title); title != "" {
		return title
	}
	return "Chat"
}

func threadKindLabel(th store.Thread) string {
	switch th.Kind {
	case "routine":
		return "Routine"
	case "cli":
		return "Cast CLI"
	case "agent_dm", "agent_chat":
		return "Agent chat"
	default:
		return "Chat"
	}
}

func threadLine(th store.Thread) string {
	return threadDisplay(th) + ". " + threadKindLabel(th) + ". " + th.ID
}

func matchThreads(threads []store.Thread, query string) []store.Thread {
	query = strings.TrimSpace(query)
	if query == "" {
		return threads
	}
	var exact []store.Thread
	for _, th := range threads {
		if strings.EqualFold(th.ID, query) {
			exact = append(exact, th)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	q := strings.ToLower(query)
	var titled []store.Thread
	for _, th := range threads {
		if strings.EqualFold(threadDisplay(th), query) || strings.EqualFold(strings.TrimSpace(th.Title), query) {
			titled = append(titled, th)
		}
	}
	if len(titled) > 0 {
		return titled
	}
	var loose []store.Thread
	for _, th := range threads {
		blob := strings.ToLower(threadDisplay(th) + " " + strings.TrimSpace(th.Title) + " " + threadKindLabel(th) + " " + th.ID)
		if strings.Contains(blob, q) {
			loose = append(loose, th)
		}
	}
	return loose
}
