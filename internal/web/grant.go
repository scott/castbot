package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func (s *Server) alwaysApproveRun(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	runRow, err := s.store.GetRun(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if s.refuseReplacing(w, r, user.ID, runRow.AgentID) {
		return
	}
	current, err := s.store.GetApproval(r.Context(), user.ID, r.PathValue("approval"))
	if s.missing(w, err) {
		return
	}
	if current.RunID != runRow.ID {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	if current.Status != "pending" || current.Origin != "web" {
		s.writeCard(w, user, current, from(r).CSRF)
		return
	}
	if runTerminal(runRow.Status) {
		next, _, merr := s.store.MarkPendingInactive(r.Context(), user.ID, current.ID, "user", user.ID)
		if merr != nil {
			s.writeCard(w, user, current, from(r).CSRF)
			return
		}
		s.runs.Hub.Notify(runRow.ID)
		s.writeCard(w, user, next, from(r).CSRF)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), decisionTimeout)
	defer cancel()
	row, claimed, err := s.store.ClaimDecision(ctx, user.ID, current.ID, runRow.ID, "approved", user.ID)
	if s.missing(w, err) {
		return
	}
	if !claimed {
		s.writeCard(w, user, row, from(r).CSRF)
		return
	}
	if !s.markBusy(row.ID) {
		s.writeCard(w, user, row, from(r).CSRF)
		return
	}
	defer s.clearBusy(row.ID)
	msgID, err := s.store.PromptMessageID(ctx, user.ID, runRow.ID)
	if err != nil {
		_ = s.store.ReleaseDecision(ctx, user.ID, row.ID, "approved", user.ID)
		writeText(w, http.StatusBadGateway, "The agent did not take that decision.\n")
		return
	}
	// Open the grant before the guest resumes, so the next permission is covered.
	_, created, err := s.store.OpenPromptGrant(ctx, user.ID, msgID)
	if err != nil {
		_ = s.store.ReleaseDecision(ctx, user.ID, row.ID, "approved", user.ID)
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	next, posted := s.postGrantCard(ctx, user, runRow, row)
	if !posted {
		if created {
			_ = s.store.EndPromptGrant(ctx, user.ID, msgID)
		}
		fresh, ferr := s.store.GetApproval(ctx, user.ID, row.ID)
		if ferr != nil {
			fresh = row
		}
		if fresh.Status == "pending" {
			writeText(w, http.StatusBadGateway, "The agent did not take that decision.\n")
			return
		}
		s.runs.Hub.Notify(runRow.ID)
		s.writeCard(w, user, fresh, from(r).CSRF)
		return
	}
	s.runs.Hub.Notify(runRow.ID)
	extras, agents := s.sweepGrant(ctx, user, msgID, row.ID, "")
	s.writeGrantCards(w, user, s.presentApproval(next, user, from(r).CSRF), extras, runRow.AgentID, agents, runRow.ID, from(r).CSRF)
}

func (s *Server) alwaysApproveConnect(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	if _, err := s.store.GetAgent(r.Context(), user.ID, agentID); s.missing(w, err) {
		return
	}
	current, err := s.store.GetConnectApproval(r.Context(), user.ID, r.PathValue("approval"))
	if s.missing(w, err) {
		return
	}
	if current.AgentID != agentID {
		s.missing(w, store.ErrNotFound)
		return
	}
	if current.Status != "pending" {
		s.renderGrantConnect(w, r, user, current)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), decisionTimeout)
	defer cancel()
	msgID := ""
	if current.RunID != "" {
		msgID, err = s.store.PromptMessageID(ctx, user.ID, current.RunID)
		if err != nil {
			msgID = ""
		}
	}
	if msgID == "" {
		s.decideConnectCard(w, r, "approved")
		return
	}
	row, claimed, err := s.store.GrantConnectApproval(ctx, user.ID, current.ID)
	if s.missing(w, err) {
		return
	}
	if !claimed {
		s.renderGrantConnect(w, r, user, row)
		return
	}
	if _, _, err := s.store.OpenPromptGrant(ctx, user.ID, msgID); err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	s.notifyAgent(ctx, user.ID, agentID)
	extras, agents := s.sweepGrant(ctx, user, msgID, "", row.ID)
	primary := s.presentConnect(user, row, from(r).CSRF)
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/agents/"+url.PathEscape(agentID), http.StatusSeeOther)
		return
	}
	s.writeGrantCards(w, user, primary, extras, agentID, agents, row.RunID, from(r).CSRF)
}

// grantPermission approves a card that arrives while the prompt grant is active.
// It runs before the live page is told, so the card does not sit on the pending bar.
func (s *Server) grantPermission(job run.Job, appr store.Approval) {
	if appr.Origin != "web" || appr.Status != "pending" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), decisionTimeout)
	defer cancel()
	msgID, err := s.store.PromptMessageID(ctx, job.UserID, job.RunID)
	if err != nil || msgID == "" {
		return
	}
	active, err := s.store.PromptGrantActive(ctx, job.UserID, msgID)
	if err != nil || !active {
		return
	}
	user, err := s.store.GetUser(ctx, job.UserID)
	if err != nil {
		return
	}
	runRow, err := s.store.GetRun(ctx, job.UserID, job.RunID)
	if err != nil || runTerminal(runRow.Status) {
		return
	}
	if _, ok := s.approveGranted(ctx, user, runRow, appr); ok {
		s.runs.Hub.Notify(runRow.ID)
	}
}

// settleGrant ends the prompt grant once every covered run has finished, failed, or stopped.
// A specialist that is still open keeps the grant.
func (s *Server) settleGrant(job run.Job, status string) {
	switch status {
	case "done", "failed", "stopped", "retried":
	default:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msgID, err := s.store.PromptMessageID(ctx, job.UserID, job.RunID)
	if err != nil || msgID == "" {
		return
	}
	active, err := s.store.PromptGrantActive(ctx, job.UserID, msgID)
	if err != nil || !active {
		return
	}
	open, err := s.store.PromptRunsOpen(ctx, job.UserID, msgID)
	if err != nil || open {
		return
	}
	_ = s.store.EndPromptGrant(ctx, job.UserID, msgID)
}

// approveGranted claims one still-pending web card and posts Allow.
// A card that Deny already decided is left denied.
func (s *Server) approveGranted(ctx context.Context, user store.User, runRow store.Run, row store.Approval) (store.Approval, bool) {
	if row.Origin != "web" || row.Status != "pending" || runTerminal(runRow.Status) {
		return row, false
	}
	claimed, ok, err := s.store.ClaimDecision(ctx, user.ID, row.ID, runRow.ID, "approved", user.ID)
	if err != nil || !ok {
		return row, false
	}
	if !s.markBusy(claimed.ID) {
		return claimed, false
	}
	defer s.clearBusy(claimed.ID)
	next, posted := s.postGrantCard(ctx, user, runRow, claimed)
	if !posted {
		return next, false
	}
	return next, true
}

// postGrantCard tells the guest and writes the prompt audit.
// posted is false when the guest did not accept the decision.
func (s *Server) postGrantCard(ctx context.Context, user store.User, runRow store.Run, row store.Approval) (store.Approval, bool) {
	if err := s.postPermission(ctx, user, runRow, row.RequestID, true, true); err != nil {
		if errors.Is(err, errNoMachine) {
			next, merr := s.store.MarkNoMachine(ctx, user.ID, row.ID)
			if merr != nil {
				return row, false
			}
			return next, false
		}
		if lostPermission(err) {
			next, merr := s.store.MarkInactive(ctx, user.ID, row.ID, "user", user.ID)
			if merr != nil {
				return row, false
			}
			return next, false
		}
		_ = s.store.ReleaseDecision(ctx, user.ID, row.ID, "approved", user.ID)
		return row, false
	}
	if err := s.store.MarkApprovalVia(ctx, user.ID, row.ID, store.GrantVia); err != nil {
		return row, false
	}
	if err := s.store.WriteGrantAudit(ctx, user.ID, row.ID, "user", user.ID); err != nil {
		return row, false
	}
	if err := s.store.MarkSent(ctx, user.ID, row.ID); err != nil {
		return row, false
	}
	_ = s.store.ResumeAfterApproval(ctx, user.ID, runRow.ID)
	next, err := s.store.GetApproval(ctx, user.ID, row.ID)
	if err != nil {
		return row, true
	}
	return next, true
}

// sweepGrant approves every other still-pending card for the prompt.
// A card Deny already won is left as it is.
func (s *Server) sweepGrant(ctx context.Context, user store.User, messageID, skipApproval, skipConnect string) ([]cardView, []string) {
	ids, err := s.store.PromptRunIDs(ctx, user.ID, messageID)
	if err != nil {
		return nil, nil
	}
	var cards []cardView
	var agents []string
	pending, err := s.store.ListPendingApprovalsForRuns(ctx, user.ID, ids)
	if err == nil {
		for _, row := range pending {
			if row.ID == skipApproval {
				continue
			}
			runRow, rerr := s.store.GetRun(ctx, user.ID, row.RunID)
			if rerr != nil {
				continue
			}
			next, ok := s.approveGranted(ctx, user, runRow, row)
			if !ok {
				continue
			}
			s.runs.Hub.Notify(runRow.ID)
			cards = append(cards, s.presentApproval(next, user, ""))
			agents = append(agents, runRow.AgentID)
		}
	}
	connects, err := s.store.ListPendingConnectForRuns(ctx, user.ID, ids)
	if err == nil {
		for _, row := range connects {
			if row.ID == skipConnect || row.RunID == "" {
				continue
			}
			decided, ok, derr := s.store.GrantConnectApproval(ctx, user.ID, row.ID)
			if derr != nil || !ok {
				continue
			}
			s.notifyAgent(ctx, user.ID, decided.AgentID)
			cards = append(cards, s.presentConnect(user, decided, ""))
			agents = append(agents, decided.AgentID)
		}
	}
	return cards, agents
}

func (s *Server) renderGrantConnect(w http.ResponseWriter, r *http.Request, user store.User, row store.ConnectApproval) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/agents/"+url.PathEscape(row.AgentID), http.StatusSeeOther)
		return
	}
	s.writeCardView(w, s.presentConnect(user, row, from(r).CSRF))
}

func (s *Server) writeCardView(w http.ResponseWriter, view cardView) {
	body, err := execTemplate("approvalCard", view)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (s *Server) writeGrantCards(w http.ResponseWriter, user store.User, primary cardView, extras []cardView, pageAgent string, also []string, runID, csrf string) {
	primary.CSRF = csrf
	body, err := execTemplate("approvalCard", primary)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	primary.OOB = true
	if extra, err := execTemplate("approvalCard", primary); err == nil {
		body += extra
	}
	for _, card := range extras {
		card.CSRF = csrf
		card.OOB = true
		if chunk, err := execTemplate("approvalCard", card); err == nil {
			body += chunk
		}
	}
	// The pending bar and the Approvals pane belong to the page the click
	// came from. Another agent's pane uses the same id, so swapping it
	// would replace the list the person is looking at.
	if pageAgent != "" {
		if chrome, cerr := s.approvalChrome(context.Background(), user, pageAgent, runID, csrf); cerr == nil {
			body += chrome
		}
		if sync, serr := s.approvalsSyncHTML(context.Background(), user, pageAgent, csrf); serr == nil {
			body += sync
		}
	}
	seen := map[string]bool{pageAgent: true}
	for _, id := range also {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if badge, berr := s.agentBadgeHTML(context.Background(), user, id); berr == nil {
			body += badge
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// agentBadgeHTML refreshes one sidebar shield without touching the pending bar.
func (s *Server) agentBadgeHTML(ctx context.Context, user store.User, agentID string) (string, error) {
	agent, err := s.store.GetAgent(ctx, user.ID, agentID)
	if err != nil {
		return "", err
	}
	return execTemplate("agentApprBadge", chromeView{
		AgentID: agent.ID, AgentApprovals: agent.Approvals, Unread: agent.Unread,
	})
}
