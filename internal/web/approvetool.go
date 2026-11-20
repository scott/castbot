package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/scott/castbot/internal/approvetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const (
	approvalNone    = "Nothing is waiting."
	approvalMany    = "More than one approval is waiting. Say which one."
	approvalMiss    = "No waiting approval matches that."
	approvalGone    = "That approval is no longer waiting."
	approvalCLI     = "Answer it in the Cast CLI."
	approvalClosed  = "This request is no longer active."
	approvalReject  = "The agent did not take that decision."
	approvalUnread  = "Approvals could not be read."
	approvalUnsaved = "The approval could not be saved."
)

// applyApproval runs one approval decision for the agent in the path.
// user_id and agent_id arguments are ignored. The call cannot name another agent.
func (s *Server) applyApproval(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	agent, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	card, ok := approvalCardArg(raw)
	if !ok || !approvetool.Known(name) {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	text, failed := s.decideThisCard(r.Context(), user, agent, name, card, "")
	writeAgentResult(w, text, failed)
}

// maybeApprove reads one castbot-approval fence from the latest reply.
func (s *Server) maybeApprove(job run.Job) {
	ctx := context.Background()
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	unlock := s.lockCreate(job.RunID)
	defer unlock()
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, fields, ok := approvetool.Parse(reply.Body)
	if !ok {
		return
	}
	user, err := s.store.GetUser(ctx, job.UserID)
	if err != nil {
		return
	}
	agent, err := s.store.GetAgent(ctx, job.UserID, job.AgentID)
	if err != nil {
		return
	}
	text, failed := s.decideThisCard(ctx, user, agent, fields.Action, fields.Card, job.RunID)
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func approvalCardArg(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var fields struct {
		Card *string `json:"card"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", false
	}
	if fields.Card == nil {
		return "", true
	}
	return strings.TrimSpace(*fields.Card), true
}

type waitingCard struct {
	kind     string
	id       string
	tool     string
	sentence string
	approval store.Approval
	connect  store.ConnectApproval
}

func (s *Server) decideThisCard(ctx context.Context, user store.User, agent store.Agent, action, query, runID string) (string, bool) {
	if !approvetool.Known(action) {
		return "That request was rejected.", true
	}
	if s.replacing(ctx, user.ID, agent.ID) {
		return replaceSentence, true
	}
	cards, err := s.waitingCards(ctx, user, agent)
	if err != nil {
		return approvalUnread, true
	}
	picked, many, msg := pickWaiting(cards, query)
	if msg != "" {
		if len(many) > 0 {
			return approvalList(msg, many), true
		}
		return msg, true
	}
	switch picked.kind {
	case "cli":
		return approvalCLI, true
	case "connect":
		return s.decideConnectTool(ctx, user, agent, action, picked, runID)
	default:
		return s.decideRunTool(ctx, user, action, picked, runID)
	}
}

func (s *Server) waitingCards(ctx context.Context, user store.User, agent store.Agent) ([]waitingCard, error) {
	rows, err := s.store.ListAgentApprovals(ctx, user.ID, agent.ID)
	if err != nil {
		return nil, err
	}
	var out []waitingCard
	for _, row := range rows {
		if row.Status != "pending" {
			continue
		}
		view := s.presentApproval(row, user, "")
		kind := "run"
		if row.Origin != "web" {
			kind = "cli"
		}
		out = append(out, waitingCard{
			kind: kind, id: row.ID, tool: view.Tool, sentence: approvalSentence(view), approval: row,
		})
	}
	connects, err := s.store.ListConnectApprovals(ctx, user.ID, agent.ID, time.Unix(0, 0))
	if err != nil {
		return nil, err
	}
	for _, row := range connects {
		if row.Status == "pending" {
			row, _ = s.store.ExpireConnectApproval(ctx, user.ID, row.ID)
		}
		if row.Status != "pending" {
			continue
		}
		view := s.presentConnect(user, row, "")
		out = append(out, waitingCard{
			kind: "connect", id: row.ID, tool: view.Tool, sentence: approvalSentence(view), connect: row,
		})
	}
	return out, nil
}

func pickWaiting(cards []waitingCard, query string) (waitingCard, []waitingCard, string) {
	query = strings.TrimSpace(query)
	if len(cards) == 0 {
		return waitingCard{}, nil, approvalNone
	}
	if query == "" {
		if len(cards) == 1 {
			return cards[0], nil, ""
		}
		return waitingCard{}, cards, approvalMany
	}
	var exact, loose []waitingCard
	q := strings.ToLower(query)
	for _, card := range cards {
		if strings.EqualFold(card.id, query) {
			exact = append(exact, card)
			continue
		}
		if strings.Contains(strings.ToLower(card.tool), q) || strings.Contains(strings.ToLower(card.sentence), q) || strings.Contains(strings.ToLower(card.id), q) {
			loose = append(loose, card)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil, ""
	}
	if len(exact) > 1 {
		return waitingCard{}, exact, approvalMany
	}
	if len(loose) == 1 {
		return loose[0], nil, ""
	}
	if len(loose) == 0 {
		return waitingCard{}, nil, approvalMiss
	}
	return waitingCard{}, loose, approvalMany
}

func approvalList(lead string, cards []waitingCard) string {
	var b strings.Builder
	b.WriteString(lead)
	for _, card := range cards {
		line := strings.TrimSpace(card.sentence)
		if line == "" {
			line = card.tool
		}
		if b.Len()+1+len(line) > 4000 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}

func approvalDone(action, tool string, also []string) string {
	lead := "Approved"
	switch action {
	case approvetool.ToolDeny:
		lead = "Denied"
	case approvetool.ToolAlways:
		lead = "Approved for this prompt"
	}
	text := lead + "."
	if strings.TrimSpace(tool) != "" {
		text += " " + strings.TrimSpace(tool) + "."
	}
	if action == approvetool.ToolAlways && len(also) > 0 {
		text += " Also approved for this prompt: " + strings.Join(also, ", ") + "."
	}
	return text
}

func (s *Server) decideRunTool(ctx context.Context, user store.User, action string, card waitingCard, noteRun string) (string, bool) {
	row := card.approval
	runRow, err := s.store.GetRun(ctx, user.ID, row.RunID)
	if err != nil {
		return approvalGone, true
	}
	if row.Origin != "web" {
		return approvalCLI, true
	}
	if row.Status != "pending" {
		return approvalGone, true
	}
	if runTerminal(runRow.Status) {
		next, _, merr := s.store.MarkPendingInactive(ctx, user.ID, row.ID, "user", user.ID)
		if merr != nil {
			return approvalClosed, true
		}
		s.runs.Hub.Notify(runRow.ID)
		s.rememberApproval(ctx, user.ID, noteRun, []cardView{s.presentApproval(next, user, "")})
		return approvalClosed, true
	}
	if action == approvetool.ToolAlways {
		return s.alwaysRunTool(ctx, user, runRow, row, card.tool, noteRun)
	}
	allow := action == approvetool.ToolApprove
	status := "denied"
	audit := "approval.denied"
	if allow {
		status = "approved"
		audit = "approval.approved"
	}
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), decisionTimeout)
	defer cancel()
	claimed, ok, err := s.store.ClaimDecision(postCtx, user.ID, row.ID, runRow.ID, status, user.ID)
	if err != nil || !ok {
		return approvalGone, true
	}
	if !s.markBusy(claimed.ID) {
		return approvalGone, true
	}
	defer s.clearBusy(claimed.ID)
	if err := s.postPermission(postCtx, user, runRow, claimed.RequestID, allow, true); err != nil {
		if errors.Is(err, errNoMachine) {
			next, merr := s.store.MarkNoMachine(postCtx, user.ID, claimed.ID)
			if merr != nil {
				return approvalClosed, true
			}
			s.runs.Hub.Notify(runRow.ID)
			s.rememberApproval(ctx, user.ID, noteRun, []cardView{s.presentApproval(next, user, "")})
			return approvalClosed, true
		}
		if lostPermission(err) {
			next, merr := s.store.MarkInactive(postCtx, user.ID, claimed.ID, "user", user.ID)
			if merr != nil {
				return approvalClosed, true
			}
			s.runs.Hub.Notify(runRow.ID)
			s.rememberApproval(ctx, user.ID, noteRun, []cardView{s.presentApproval(next, user, "")})
			return approvalClosed, true
		}
		_ = s.store.ReleaseDecision(postCtx, user.ID, claimed.ID, status, user.ID)
		return approvalReject, true
	}
	if err := s.store.WriteDecisionAudit(postCtx, user.ID, claimed.ID, audit, "user", user.ID); err != nil {
		return approvalUnsaved, true
	}
	if err := s.store.MarkSent(postCtx, user.ID, claimed.ID); err != nil {
		return approvalUnsaved, true
	}
	_ = s.store.ResumeAfterApproval(postCtx, user.ID, runRow.ID)
	go s.afterSecretRun(run.Job{UserID: user.ID, AgentID: runRow.AgentID, RunID: runRow.ID})
	next, err := s.store.GetApproval(postCtx, user.ID, claimed.ID)
	if err != nil {
		next = claimed
	}
	s.runs.Hub.Notify(runRow.ID)
	s.rememberApproval(ctx, user.ID, noteRun, []cardView{s.presentApproval(next, user, "")})
	return approvalDone(action, card.tool, nil), false
}

func (s *Server) alwaysRunTool(ctx context.Context, user store.User, runRow store.Run, row store.Approval, tool, noteRun string) (string, bool) {
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), decisionTimeout)
	defer cancel()
	claimed, ok, err := s.store.ClaimDecision(postCtx, user.ID, row.ID, runRow.ID, "approved", user.ID)
	if err != nil || !ok {
		return approvalGone, true
	}
	if !s.markBusy(claimed.ID) {
		return approvalGone, true
	}
	defer s.clearBusy(claimed.ID)
	msgID, err := s.store.PromptMessageID(postCtx, user.ID, runRow.ID)
	if err != nil {
		_ = s.store.ReleaseDecision(postCtx, user.ID, claimed.ID, "approved", user.ID)
		return approvalReject, true
	}
	_, created, err := s.store.OpenPromptGrant(postCtx, user.ID, msgID)
	if err != nil {
		_ = s.store.ReleaseDecision(postCtx, user.ID, claimed.ID, "approved", user.ID)
		return approvalUnsaved, true
	}
	next, posted := s.postGrantCard(postCtx, user, runRow, claimed)
	if !posted {
		if created {
			_ = s.store.EndPromptGrant(postCtx, user.ID, msgID)
		}
		fresh, ferr := s.store.GetApproval(postCtx, user.ID, claimed.ID)
		if ferr != nil {
			fresh = next
		}
		if fresh.Status == "pending" {
			return approvalReject, true
		}
		s.runs.Hub.Notify(runRow.ID)
		s.rememberApproval(ctx, user.ID, noteRun, []cardView{s.presentApproval(fresh, user, "")})
		return approvalClosed, true
	}
	s.runs.Hub.Notify(runRow.ID)
	extras, _ := s.sweepGrant(postCtx, user, msgID, claimed.ID, "")
	views := []cardView{s.presentApproval(next, user, "")}
	views = append(views, extras...)
	s.rememberApproval(ctx, user.ID, noteRun, views)
	var also []string
	for _, extra := range extras {
		if name := strings.TrimSpace(extra.Tool); name != "" {
			also = append(also, name)
		}
	}
	return approvalDone(approvetool.ToolAlways, tool, also), false
}

func (s *Server) decideConnectTool(ctx context.Context, user store.User, agent store.Agent, action string, card waitingCard, noteRun string) (string, bool) {
	if card.connect.AgentID != agent.ID {
		return approvalGone, true
	}
	if action == approvetool.ToolAlways {
		return s.alwaysConnectTool(ctx, user, card, noteRun)
	}
	status := "denied"
	if action == approvetool.ToolApprove {
		status = "approved"
	}
	row, claimed, err := s.store.DecideConnectApproval(ctx, user.ID, card.id, status)
	if err != nil || !claimed || row.AgentID != agent.ID {
		return approvalGone, true
	}
	s.notifyAgent(ctx, user.ID, agent.ID)
	s.rememberApproval(ctx, user.ID, noteRun, []cardView{s.presentConnect(user, row, "")})
	return approvalDone(action, card.tool, nil), false
}

func (s *Server) alwaysConnectTool(ctx context.Context, user store.User, card waitingCard, noteRun string) (string, bool) {
	msgID := ""
	if card.connect.RunID != "" {
		id, err := s.store.PromptMessageID(ctx, user.ID, card.connect.RunID)
		if err == nil {
			msgID = id
		}
	}
	if msgID == "" {
		return s.decideConnectTool(ctx, user, store.Agent{ID: card.connect.AgentID}, approvetool.ToolApprove, card, noteRun)
	}
	row, claimed, err := s.store.GrantConnectApproval(ctx, user.ID, card.id)
	if err != nil || !claimed {
		return approvalGone, true
	}
	if _, _, err := s.store.OpenPromptGrant(ctx, user.ID, msgID); err != nil {
		return approvalUnsaved, true
	}
	s.notifyAgent(ctx, user.ID, row.AgentID)
	extras, _ := s.sweepGrant(ctx, user, msgID, "", row.ID)
	views := []cardView{s.presentConnect(user, row, "")}
	views = append(views, extras...)
	s.rememberApproval(ctx, user.ID, noteRun, views)
	var also []string
	for _, extra := range extras {
		if name := strings.TrimSpace(extra.Tool); name != "" {
			also = append(also, name)
		}
	}
	return approvalDone(approvetool.ToolAlways, card.tool, also), false
}

func (s *Server) rememberApproval(ctx context.Context, userID, runID string, views []cardView) {
	var b strings.Builder
	for _, view := range views {
		if view.ID == "" {
			continue
		}
		view.OOB = true
		chunk, err := execTemplate("approvalCard", view)
		if err != nil || chunk == "" {
			continue
		}
		b.WriteString(chunk)
	}
	if b.Len() == 0 {
		return
	}
	html := b.String()
	var ids []string
	if runID != "" {
		ids = []string{runID}
	} else if rows, err := s.store.ListOpenRuns(ctx, userID); err == nil {
		for _, row := range rows {
			if row.ID != "" {
				ids = append(ids, row.ID)
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	s.sideMu.Lock()
	defer s.sideMu.Unlock()
	if s.approvalHTML == nil {
		s.approvalHTML = map[string]string{}
	}
	for _, id := range ids {
		s.approvalHTML[id] += html
	}
}

func (s *Server) takeApprovalHTML(runID string) string {
	if runID == "" {
		return ""
	}
	s.sideMu.Lock()
	defer s.sideMu.Unlock()
	html := s.approvalHTML[runID]
	delete(s.approvalHTML, runID)
	return html
}
