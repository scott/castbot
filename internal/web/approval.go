package web

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const (
	decisionTimeout = 45 * time.Second
	denyTimeout     = 15 * time.Second
	confirmGrace    = 30 * time.Second
	// journalPullLimit is how many failed journal pulls recovery allows
	// before it marks the approval inactive and stops retrying.
	journalPullLimit = 5
	// lostApprovalNotice is the run failure when the prompt is gone.
	lostApprovalNotice = "The approval request was lost. Send the message again."
)

var errMachinePaused = errors.New("machine is paused")

var errNoJournalDecision = errors.New("no journaled decision")

var errNoMachine = errors.New("no live machine")

// errPauseHold means the post is waiting for the machine to be observed running.
// It is not a failure and it does not schedule a backoff retry.
var errPauseHold = errors.New("waiting for the machine to run")

func approvalWaitFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CASTBOT_APPROVAL_TIMEOUT"))
	if raw == "" {
		return run.DefaultApprovalWait
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < time.Second || d > 24*time.Hour {
		return run.DefaultApprovalWait
	}
	return d
}

// SetApprovalTimeout sets how long a pending approval stays open.
// The wait is enforced on the server. Closing the browser does not deny.
func (s *Server) SetApprovalTimeout(d time.Duration) {
	if d <= 0 {
		return
	}
	s.mu.Lock()
	s.approvalFor = d
	s.mu.Unlock()
	if s.runs != nil {
		s.runs.SetApprovalTimeout(d)
	}
}

func (s *Server) approvalTimeout() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.approvalFor <= 0 {
		return run.DefaultApprovalWait
	}
	return s.approvalFor
}

func (s *Server) expireLoop() {
	s.recoverDecisions(context.Background(), 0)
	_, _ = s.store.SweepOrphanCLIDecisions(context.Background())
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.base.Done():
			return
		case <-tick.C:
			if s.sweepPause.Load() {
				continue
			}
			// Mark expirations even while a post is still running, so a slow
			// or unreachable guest cannot hold the next deadline back.
			_, _ = s.store.ExpireDue(s.base)
			_, _ = s.store.SweepOrphanCLIDecisions(s.base)
			_ = s.store.DropStaleCreateDrafts(s.base)
			s.kickApprovalFollowUp()
		}
	}
}

// kickApprovalFollowUp posts at most one denial and one recovery at a time.
// The ticker keeps marking rows due while that post is in flight.
func (s *Server) kickApprovalFollowUp() {
	s.sweepMu.Lock()
	if s.sweeping {
		s.sweepMu.Unlock()
		return
	}
	s.sweeping = true
	s.sweepMu.Unlock()
	go func() {
		defer func() {
			s.sweepMu.Lock()
			s.sweeping = false
			s.sweepMu.Unlock()
		}()
		s.ExpireDue(context.Background())
		s.recoverDecisions(context.Background(), confirmGrace)
	}()
}

// ExpireDue posts Deny for approvals whose wait has elapsed.
// A second call does not post twice. A paused machine is not woken.
// Each post has its own timeout, and a failure backs off.
func (s *Server) ExpireDue(ctx context.Context) {
	rows, err := s.store.ExpireDue(ctx)
	if err != nil {
		return
	}
	s.postDenials(ctx, rows, true)
	left, err := s.store.ListRestartDenials(ctx)
	if err != nil {
		return
	}
	s.postDenials(ctx, left, false)
}

func (s *Server) postDenials(ctx context.Context, rows []store.Approval, resume bool) {
	for _, row := range rows {
		if ctx.Err() != nil || s.base.Err() != nil {
			return
		}
		if skip, err := s.releasePaused(ctx, row); err != nil || skip {
			if err != nil {
				return
			}
			continue
		}
		if !s.denyReady(row.ID) || !s.markBusy(row.ID) {
			continue
		}
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), denyTimeout)
		err := s.deliverDenial(callCtx, row, resume)
		cancel()
		s.clearBusy(row.ID)
		s.finishFollowUp(row.ID, err)
		// One post per call. The next tick takes the next row, so a slow
		// guest cannot hold every other denial behind it.
		return
	}
}

func (s *Server) deliverDenial(ctx context.Context, row store.Approval, resume bool) error {
	runRow, err := s.store.GetRun(ctx, row.UserID, row.RunID)
	if err != nil {
		return err
	}
	user, err := s.store.GetUser(ctx, row.UserID)
	if err != nil {
		return err
	}
	postErr := s.postPermission(ctx, user, runRow, row.RequestID, false, false)
	if errors.Is(postErr, errMachinePaused) {
		s.notePaused(ctx, row)
		return errPauseHold
	}
	if errors.Is(postErr, errNoMachine) {
		_, err := s.store.MarkNoMachine(ctx, row.UserID, row.ID)
		return err
	}
	if postErr != nil && !lostPermission(postErr) {
		return postErr
	}
	if err := s.store.MarkSent(ctx, row.UserID, row.ID); err != nil {
		return err
	}
	if resume && !runTerminal(runRow.Status) {
		_ = s.store.ResumeAfterApproval(ctx, row.UserID, row.RunID)
	}
	s.runs.Hub.Notify(row.RunID)
	return nil
}

// recoverDecisions re-posts claims that never confirmed, and writes an audit
// row when the post landed but the record did not. grace is how long a fresh
// claim is left to the click that owns it. Startup uses zero.
func (s *Server) recoverDecisions(ctx context.Context, grace time.Duration) {
	rows, err := s.store.ListUnconfirmed(ctx, s.store.Now().Add(-grace))
	if err != nil {
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil || s.base.Err() != nil {
			return
		}
		if skip, err := s.releasePaused(ctx, row); err != nil || skip {
			if err != nil {
				return
			}
			continue
		}
		if !s.denyReady(row.ID) || !s.markBusy(row.ID) {
			continue
		}
		callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), denyTimeout)
		err := s.confirmClaim(callCtx, row)
		cancel()
		s.clearBusy(row.ID)
		s.finishFollowUp(row.ID, err)
		return
	}
}

func (s *Server) confirmClaim(ctx context.Context, row store.Approval) error {
	runRow, err := s.store.GetRun(ctx, row.UserID, row.RunID)
	if err != nil {
		return err
	}
	user, err := s.store.GetUser(ctx, row.UserID)
	if err != nil {
		return err
	}
	allow := row.Status == "approved"
	// A failed or finished run must not be approved. Deny if the prompt is
	// still live, then mark the row inactive with no approved audit.
	terminalApprove := runTerminal(runRow.Status) && allow
	if !row.Sent {
		// castlet journals every permission/respond before it forwards it,
		// including the re-post below. So note what the journal held for this
		// request before posting, and decide only from that if the prompt is
		// gone. A journal row read back after the post may be our own.
		// A failed pull is retried. Recovery does not decide from a partial read.
		if err := s.pullJournalBestEffort(ctx, user.ID, runRow.AgentID); err != nil {
			n := s.noteJournalPull(row.ID)
			if n >= journalPullLimit {
				s.logger().Warn("approval journal pull gave up", "approval_id", row.ID, "attempts", n)
				s.clearJournalPull(row.ID)
				if _, err := s.store.MarkInactive(ctx, row.UserID, row.ID, "system", "system:journal"); err != nil {
					return err
				}
				s.runs.Hub.Notify(row.RunID)
				return nil
			}
			return err
		}
		s.clearJournalPull(row.ID)
		seq, err := s.recoverSnapshot(ctx, row)
		if err != nil {
			return err
		}
		prior, priorFound, err := s.store.JournalDecisionBefore(ctx, row.UserID, runRow.AgentID, row.RequestID, seq)
		if err != nil {
			return err
		}
		// castlet journals this post before it forwards it. Remember the cursor
		// now so that row, which is above it, is not treated as an earlier one.
		preSeq, err := s.store.JournalCursor(ctx, user.ID, runRow.AgentID)
		if err != nil {
			return err
		}
		// Always re-post. A journal row alone does not prove the runtime got
		// the decision. A second post that finds no pending prompt is harmless.
		postErr := s.postPermission(ctx, user, runRow, row.RequestID, allow && !terminalApprove, false)
		if errors.Is(postErr, errMachinePaused) {
			s.notePaused(ctx, row)
			return errPauseHold
		}
		if errors.Is(postErr, errNoMachine) {
			_, err := s.store.MarkNoMachine(ctx, row.UserID, row.ID)
			return err
		}
		if lostPermission(postErr) {
			// A run that failed or stopped is never audited as approved, even
			// if an earlier approve was journaled. It ends inactive.
			if terminalApprove && runRow.Status != "done" {
				return s.settleLostClaim(ctx, user, runRow, row, false)
			}
			// On a finished run recovery posts Deny, so only an approve that
			// was journaled earlier, by the click, counts as delivered.
			if terminalApprove && prior != "approved" {
				return s.settleLostClaim(ctx, user, runRow, row, false)
			}
			// The prompt is gone. A decision journaled before this post means
			// an earlier post was delivered: audit it. Otherwise it is lost.
			if priorFound {
				if err := s.confirmJournaled(ctx, runRow, row, prior); err == nil {
					return nil
				} else if !errors.Is(err, errNoJournalDecision) {
					return err
				}
			}
			// Pull the row castlet wrote while forwarding. It is this attempt's
			// own row when its sequence is above preSeq, and it does not prove
			// the runtime ran the tool. A row from an earlier attempt sits in
			// (recover_seq, preSeq]: the outcome of that post is unknown, so
			// the run stays as it is.
			if err := s.pullJournalBestEffort(ctx, user.ID, runRow.AgentID); err != nil {
				return err
			}
			_, found, err := s.store.JournalDecisionBetween(ctx, row.UserID, runRow.AgentID, row.RequestID, seq, preSeq)
			if err != nil {
				return err
			}
			if found {
				if _, err := s.store.MarkInactive(ctx, row.UserID, row.ID, "user", row.DecidedBy); err != nil {
					return err
				}
				s.runs.Hub.Notify(row.RunID)
				return nil
			}
			return s.settleLostClaim(ctx, user, runRow, row, false)
		}
		if postErr != nil {
			return postErr
		}
		if terminalApprove {
			return s.settleLostClaim(ctx, user, runRow, row, false)
		}
	} else if terminalApprove {
		return s.settleLostClaim(ctx, user, runRow, row, false)
	}
	action := "approval.denied"
	if allow {
		action = "approval.approved"
	}
	if err := s.store.WriteDecisionAudit(ctx, row.UserID, row.ID, action, "user", row.DecidedBy); err != nil {
		return err
	}
	if err := s.store.MarkSent(ctx, row.UserID, row.ID); err != nil {
		return err
	}
	if !runTerminal(runRow.Status) {
		_ = s.store.ResumeAfterApproval(ctx, row.UserID, row.RunID)
	}
	s.runs.Hub.Notify(row.RunID)
	return nil
}

// settleLostClaim matches the click path when the prompt is gone or the run
// has already failed. deny posts Deny first when the guest may still be waiting.
func (s *Server) settleLostClaim(ctx context.Context, user store.User, runRow store.Run, row store.Approval, deny bool) error {
	if deny && !row.Sent {
		postErr := s.postPermission(ctx, user, runRow, row.RequestID, false, false)
		if errors.Is(postErr, errMachinePaused) {
			s.notePaused(ctx, row)
			return errPauseHold
		}
		if errors.Is(postErr, errNoMachine) {
			_, err := s.store.MarkNoMachine(ctx, row.UserID, row.ID)
			return err
		}
		if postErr != nil && !lostPermission(postErr) {
			return postErr
		}
	}
	if !runTerminal(runRow.Status) {
		if _, err := s.store.FailRun(ctx, row.UserID, runRow.ID, lostApprovalNotice, runRow.StreamOffset); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	if _, err := s.store.MarkInactive(ctx, row.UserID, row.ID, "user", row.DecidedBy); err != nil {
		return err
	}
	s.runs.Hub.Notify(row.RunID)
	return nil
}

// recoverSnapshot is the journal max sequence stored before the first
// recovery post. Later posts from this process are above it and are not
// treated as a decision the runtime already accepted.
func (s *Server) recoverSnapshot(ctx context.Context, row store.Approval) (int, error) {
	if row.RecoverSet {
		return row.RecoverSeq, nil
	}
	current, err := s.store.GetApproval(ctx, row.UserID, row.ID)
	if err != nil {
		return 0, err
	}
	if current.RecoverSet {
		return current.RecoverSeq, nil
	}
	seq, err := s.store.JournalCursor(ctx, row.UserID, row.AgentID)
	if err != nil {
		return 0, err
	}
	if err := s.store.SnapshotRecoverSeq(ctx, row.UserID, row.ID, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// pullJournalBestEffort refreshes the stored castlet journal when this agent
// has a machine. A missing machine leaves the rows already on disk.
// A pull that fails returns the error so recovery can retry it.
func (s *Server) pullJournalBestEffort(ctx context.Context, userID, agentID string) error {
	endpoint, token, err := s.store.VMCredentials(ctx, userID, agentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if endpoint == "" || token == "" {
		return nil
	}
	return s.pullJournal(ctx, userID, agentID, endpoint, token)
}

// confirmJournaled writes the audit row for a claim whose re-post found no
// pending prompt but whose request the castlet journal already showed as
// decided before that re-post. decision is that earlier journal decision.
// errNoJournalDecision means it was not approved or denied, so the caller
// marks the row inactive. The audit always follows the row's own status. A
// journal that disagrees (for example a CLI answer to the same request) is
// logged and does not overwrite the row.
func (s *Server) confirmJournaled(ctx context.Context, runRow store.Run, row store.Approval, decision string) error {
	if decision != "approved" && decision != "denied" {
		return errNoJournalDecision
	}
	action := "approval.denied"
	if row.Status == "approved" {
		action = "approval.approved"
	}
	if decision != row.Status {
		s.logger().Warn("approval journal mismatch", "approval_id", row.ID, "run_id", row.RunID, "row_status", row.Status, "journal_decision", decision)
	}
	if err := s.store.WriteDecisionAudit(ctx, row.UserID, row.ID, action, "user", row.DecidedBy); err != nil {
		return err
	}
	if err := s.store.MarkSent(ctx, row.UserID, row.ID); err != nil {
		return err
	}
	if !runTerminal(runRow.Status) {
		_ = s.store.ResumeAfterApproval(ctx, row.UserID, runRow.ID)
	}
	s.runs.Hub.Notify(row.RunID)
	return nil
}

func (s *Server) approveRun(w http.ResponseWriter, r *http.Request) {
	s.decide(w, r, true)
}

func (s *Server) denyRun(w http.ResponseWriter, r *http.Request) {
	s.decide(w, r, false)
}

func (s *Server) getApproval(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if r.PathValue("id") == "live" {
		s.approvalsLive(w, r)
		return
	}
	row, err := s.store.GetApproval(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(row.AgentID)+"?panel=approvals#appr-"+row.ID, http.StatusFound)
}

func (s *Server) decide(w http.ResponseWriter, r *http.Request, allow bool) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	status := "denied"
	action := "approval.denied"
	if allow {
		status = "approved"
		action = "approval.approved"
	}
	// The run id is checked before the claim. A mismatched URL must not
	// stick the row at approved with nothing posted.
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
	if current.Status != "pending" {
		s.writeCard(w, user, current, from(r).CSRF)
		return
	}
	if runTerminal(runRow.Status) {
		next, changed, merr := s.store.MarkPendingInactive(r.Context(), user.ID, current.ID, "user", user.ID)
		if merr != nil {
			s.writeCard(w, user, current, from(r).CSRF)
			return
		}
		if changed {
			s.runs.Hub.Notify(runRow.ID)
		}
		s.writeCard(w, user, next, from(r).CSRF)
		return
	}
	row, claimed, err := s.store.ClaimDecision(r.Context(), user.ID, current.ID, runRow.ID, status, user.ID)
	if s.missing(w, err) {
		return
	}
	if !claimed {
		s.writeCard(w, user, row, from(r).CSRF)
		return
	}
	// The post, the release, and the audit use a context the tab cannot cancel.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), decisionTimeout)
	defer cancel()
	if !s.markBusy(row.ID) {
		s.writeCard(w, user, row, from(r).CSRF)
		return
	}
	defer s.clearBusy(row.ID)
	if err := s.postPermission(ctx, user, runRow, row.RequestID, allow, true); err != nil {
		if errors.Is(err, errNoMachine) {
			next, merr := s.store.MarkNoMachine(ctx, user.ID, row.ID)
			if merr != nil {
				s.writeCard(w, user, row, from(r).CSRF)
				return
			}
			s.runs.Hub.Notify(runRow.ID)
			s.writeCard(w, user, next, from(r).CSRF)
			return
		}
		if lostPermission(err) {
			next, merr := s.store.MarkInactive(ctx, user.ID, row.ID, "user", user.ID)
			if merr != nil {
				s.writeCard(w, user, row, from(r).CSRF)
				return
			}
			s.runs.Hub.Notify(runRow.ID)
			s.writeCard(w, user, next, from(r).CSRF)
			return
		}
		_ = s.store.ReleaseDecision(ctx, user.ID, row.ID, status, user.ID)
		writeText(w, http.StatusBadGateway, "The agent did not take that decision.\n")
		return
	}
	if err := s.store.WriteDecisionAudit(ctx, user.ID, row.ID, action, "user", user.ID); err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	if err := s.store.MarkSent(ctx, user.ID, row.ID); err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	_ = s.store.ResumeAfterApproval(ctx, user.ID, runRow.ID)
	go s.afterSecretRun(run.Job{UserID: user.ID, AgentID: runRow.AgentID, RunID: runRow.ID})
	next, err := s.store.GetApproval(ctx, user.ID, row.ID)
	if err != nil {
		s.runs.Hub.Notify(runRow.ID)
		return
	}
	s.runs.Hub.Notify(runRow.ID)
	s.writeCard(w, user, next, from(r).CSRF)
}

func (s *Server) writeCard(w http.ResponseWriter, user store.User, row store.Approval, csrf string) {
	view := s.presentApproval(row, user, csrf)
	body, err := execTemplate("approvalCard", view)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	// The chat card and the pane card share an id, and the form target
	// resolves to the first match. This copy settles every match, so the
	// in-chat card updates when the click was in the pane.
	view.OOB = true
	if extra, err := execTemplate("approvalCard", view); err == nil {
		body += extra
	}
	if chrome, cerr := s.approvalChrome(context.Background(), user, row.AgentID, row.RunID); cerr == nil {
		body += chrome
	}
	if sync, serr := s.approvalsSyncHTML(context.Background(), user, row.AgentID, csrf); serr == nil {
		body += sync
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func lostPermission(err error) bool {
	return errors.Is(err, host.ErrNoRequest) || errors.Is(err, runtime.ErrNoRequest)
}

type permissionResponder interface {
	Respond(ctx context.Context, requestID string, allow bool) error
}

func (s *Server) postPermission(ctx context.Context, user store.User, runRow store.Run, requestID string, allow, wake bool) error {
	s.mu.Lock()
	h := s.machine
	client := s.client
	s.mu.Unlock()
	if h != nil {
		vm, err := s.store.LiveVM(ctx, user.ID, runRow.AgentID)
		if err == nil {
			return s.postMachine(ctx, h, user.ID, runRow.AgentID, vm, requestID, allow, wake)
		}
		if errors.Is(err, store.ErrNotFound) {
			return errNoMachine
		}
		return err
	}
	responder, ok := client.(permissionResponder)
	if !ok || responder == nil {
		return errors.New("no runtime")
	}
	return responder.Respond(ctx, requestID, allow)
}

func (s *Server) postMachine(ctx context.Context, h host.AgentHost, userID, agentID string, vm store.VM, requestID string, allow, wake bool) error {
	state, _ := s.observedMachine(ctx, userID, agentID, vm)
	endpoint, token, err := s.store.VMCredentials(ctx, userID, agentID)
	if err != nil {
		return err
	}
	if state == "paused" && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
		if !wake {
			return errMachinePaused
		}
		s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeRequested, 0)
		wakeAt := time.Now()
		got, werr := h.Wake(ctx, vm.MicroVMID)
		if werr != nil {
			return werr
		}
		if err := s.store.ApplyVMState(ctx, userID, agentID, got.State, got.Endpoint, ""); err != nil {
			return err
		}
		if got.State == "running" {
			s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeRunning, time.Since(wakeAt))
		}
		if got.Endpoint != "" {
			endpoint = got.Endpoint
		}
		if token != "" && endpoint != "" {
			if perr := h.Probe(ctx, endpoint, token); perr == nil {
				s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeReady, time.Since(wakeAt))
			}
		}
	}
	if endpoint == "" || token == "" {
		return errors.New("machine is not ready")
	}
	return h.Approve(ctx, endpoint, token, requestID, allow)
}

func (s *Server) presentApproval(row store.Approval, user store.User, csrf string) cardView {
	loc := userLocation(user.Timezone)
	view := cardView{
		ID: row.ID, RunID: row.RunID, Tool: row.Tool, CSRF: csrf,
		Args:       approvalArgs(row.Arguments, row.Redacted),
		When:       clockLabel(row.CreatedAt, loc),
		ApproveURL: "/runs/" + url.PathEscape(row.RunID) + "/approvals/" + url.PathEscape(row.ID) + "/approve",
		DenyURL:    "/runs/" + url.PathEscape(row.RunID) + "/approvals/" + url.PathEscape(row.ID) + "/deny",
	}
	if view.Tool == "" {
		view.Tool = "Approval"
	}
	switch row.Status {
	case "pending":
		if row.Origin == "web" {
			view.Pending = true
		} else {
			view.ReadOnly = true
			view.Line = "Answer it in the Cast CLI."
		}
	case "approved":
		view.Approved = true
		when := clockLabel(row.DecidedAt, loc)
		if when == "" {
			view.Line = "Approved by you"
		} else {
			view.Line = "Approved by you at " + when
			view.When = when
		}
	case "denied":
		view.Line = "Denied"
		if row.DecidedBy == "routine" {
			view.Line = "Denied by the routine setting"
		}
		if when := clockLabel(row.DecidedAt, loc); when != "" {
			view.When = when
		}
	case "expired":
		view.Line = "Expired"
		if when := clockLabel(row.DecidedAt, loc); when != "" {
			view.When = when
		}
	default:
		view.Line = "This request is no longer active."
		if when := clockLabel(row.DecidedAt, loc); when != "" {
			view.When = when
		}
	}
	view.At = row.CreatedAt
	view.Extra, view.From = s.routineCardExtra(row, user)
	return view
}

func approvalArgs(raw, redacted string) []argView {
	var out []argView
	dec := json.NewDecoder(strings.NewReader(raw))
	tok, err := dec.Token()
	if err == nil {
		if d, ok := tok.(json.Delim); ok && d == '{' {
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					break
				}
				key, _ := keyTok.(string)
				var val json.RawMessage
				if err := dec.Decode(&val); err != nil {
					break
				}
				out = append(out, argText(key, strings.TrimSpace(string(val))))
			}
		}
	}
	var hidden []string
	if json.Unmarshal([]byte(redacted), &hidden) == nil {
		for _, key := range hidden {
			out = append(out, argView{Key: key, Hidden: true})
		}
	}
	return out
}

func argText(key, raw string) argView {
	value := raw
	if len(raw) >= 2 && raw[0] == '"' {
		var s string
		if json.Unmarshal([]byte(raw), &s) == nil {
			value = s
		}
	}
	view := argView{Key: key, Value: value}
	if utf8.RuneCountInString(value) > 80 {
		view.Long = true
		view.Short = clipRunes(value, 80) + "..."
	}
	return view
}

func clipRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for k := range s {
		if i == n {
			return s[:k]
		}
		i++
	}
	return s
}

func (s *Server) cardsFor(ctx context.Context, user store.User, runID, csrf string) ([]cardView, error) {
	rows, err := s.store.ListRunApprovals(ctx, user.ID, runID)
	if err != nil {
		return nil, err
	}
	var out []cardView
	for _, row := range rows {
		out = append(out, s.presentApproval(row, user, csrf))
	}
	return out, nil
}

func (s *Server) cardsHTML(ctx context.Context, user store.User, runID, csrf string) (string, error) {
	cards, err := s.cardsFor(ctx, user, runID, csrf)
	if err != nil {
		return "", err
	}
	var secrets []cardView
	if runRow, err := s.store.GetRun(ctx, user.ID, runID); err == nil {
		secrets = s.secretCardViews(ctx, user, runRow.AgentID, csrf)
		cards = append(cards, s.connectCardViews(ctx, user, runRow.AgentID, csrf)...)
	}
	if len(cards) == 0 && len(secrets) == 0 {
		return "", nil
	}
	body, err := execTemplate("liveCards", page{Cards: cards, SecretCards: secrets})
	if err != nil {
		return "", err
	}
	chrome, err := s.approvalChrome(ctx, user, "", runID)
	if err != nil {
		return body, nil
	}
	return body + chrome, nil
}

func (s *Server) approvalChrome(ctx context.Context, user store.User, agentID, runID string) (string, error) {
	if agentID == "" && runID != "" {
		runRow, err := s.store.GetRun(ctx, user.ID, runID)
		if err != nil {
			return "", err
		}
		agentID = runRow.AgentID
	}
	agent, err := s.store.GetAgent(ctx, user.ID, agentID)
	if err != nil {
		return "", err
	}
	total, err := s.store.PendingApprovalCount(ctx, user.ID)
	if err != nil {
		return "", err
	}
	act := ""
	if runID != "" {
		if runRow, err := s.store.GetRun(ctx, user.ID, runID); err == nil && !runTerminal(runRow.Status) {
			events, _ := s.store.ListRunEvents(ctx, user.ID, runID, 0)
			text, _, _, fact, _ := foldRun(events)
			pending := false
			if rows, err := s.store.ListRunApprovals(ctx, user.ID, runID); err == nil {
				for _, row := range rows {
					if row.Status == "pending" {
						pending = true
						break
					}
				}
			}
			out := s.activityFor(ctx, user.ID, runRow, text, fact, pending)
			elapsed := ""
			if started, perr := time.Parse(time.RFC3339, runRow.StartedAt); perr == nil {
				elapsed, _ = formatElapsed(s.store.Now().Sub(started))
			}
			slot := actSlotHTML(runID, actRowHTML(out, elapsed), out.HideThread)
			act = strings.Replace(slot, `class="act-hold"`, `class="act-hold" hx-swap-oob="outerHTML"`, 1)
		}
	}
	return execTemplate("approvalChrome", chromeView{
		AgentID: agent.ID, RunID: runID, AgentApprovals: agent.Approvals,
		Total: total, Unread: agent.Unread, Act: template.HTML(act),
	})
}

func (s *Server) markBusy(id string) bool {
	s.busyMu.Lock()
	defer s.busyMu.Unlock()
	if s.busy == nil {
		s.busy = map[string]struct{}{}
	}
	if _, ok := s.busy[id]; ok {
		return false
	}
	s.busy[id] = struct{}{}
	return true
}

func (s *Server) clearBusy(id string) {
	s.busyMu.Lock()
	defer s.busyMu.Unlock()
	delete(s.busy, id)
}

func (s *Server) denyReady(id string) bool {
	s.denyMu.Lock()
	defer s.denyMu.Unlock()
	next, ok := s.denyNext[id]
	return !ok || !time.Now().Before(next)
}

func (s *Server) noteDenyFail(id string) {
	s.denyMu.Lock()
	defer s.denyMu.Unlock()
	if s.denyFails == nil {
		s.denyFails = map[string]int{}
		s.denyNext = map[string]time.Time{}
	}
	s.denyFails[id]++
	shift := s.denyFails[id]
	if shift > 5 {
		shift = 5
	}
	wait := time.Second << shift
	if wait > time.Minute {
		wait = time.Minute
	}
	s.denyNext[id] = time.Now().Add(wait)
}

func (s *Server) clearDeny(id string) {
	s.denyMu.Lock()
	defer s.denyMu.Unlock()
	delete(s.denyFails, id)
	delete(s.denyNext, id)
}

func (s *Server) finishFollowUp(id string, err error) {
	if errors.Is(err, errPauseHold) {
		s.clearDeny(id)
		return
	}
	if err != nil {
		s.noteDenyFail(id)
		return
	}
	s.clearDeny(id)
	s.clearPaused(id)
	s.clearJournalPull(id)
}

// releasePaused reports whether this approval is waiting for a paused
// machine to be observed running. A machine that is running again is
// released for one post. A missing machine row is marked inactive.
func (s *Server) releasePaused(ctx context.Context, row store.Approval) (bool, error) {
	gen, held := s.pausedGen(row.ID)
	if !held {
		return false, nil
	}
	vm, err := s.store.LiveVM(ctx, row.UserID, row.AgentID)
	if errors.Is(err, store.ErrNotFound) {
		s.clearPaused(row.ID)
		if _, merr := s.store.MarkNoMachine(ctx, row.UserID, row.ID); merr != nil {
			return false, merr
		}
		return true, nil
	}
	if err != nil {
		return true, nil
	}
	if vm.State == "running" && vm.StateGen != gen {
		s.clearPaused(row.ID)
		return false, nil
	}
	return true, nil
}

func (s *Server) notePaused(ctx context.Context, row store.Approval) {
	vm, err := s.store.LiveVM(ctx, row.UserID, row.AgentID)
	gen := int64(0)
	if err == nil {
		gen = vm.StateGen
	}
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	if s.pauseGen == nil {
		s.pauseGen = map[string]int64{}
	}
	s.pauseGen[row.ID] = gen
}

func (s *Server) pausedGen(id string) (int64, bool) {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	gen, ok := s.pauseGen[id]
	return gen, ok
}

func (s *Server) clearPaused(id string) {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	delete(s.pauseGen, id)
}

func (s *Server) noteJournalPull(id string) int {
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	if s.journalFail == nil {
		s.journalFail = map[string]int{}
	}
	s.journalFail[id]++
	return s.journalFail[id]
}

func (s *Server) clearJournalPull(id string) {
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	delete(s.journalFail, id)
}

func cliCard(body, id string) (cardView, bool) {
	body = strings.TrimSpace(body)
	tool := ""
	approved := false
	switch {
	case strings.HasPrefix(body, "An approval was asked"):
		tool = "Approval"
		rest := strings.TrimPrefix(body, "An approval was asked for ")
		if rest != body {
			tool = strings.TrimSuffix(rest, ". Answer it in the Cast CLI.")
		}
	case strings.HasPrefix(body, "An approval for ") && strings.Contains(body, " was "):
		rest := strings.TrimPrefix(body, "An approval for ")
		tool = rest
		if i := strings.Index(rest, " was "); i >= 0 {
			tool = rest[:i]
		}
		approved = strings.Contains(body, "was approved")
	case strings.HasPrefix(body, "An approval was "):
		tool = "Approval"
		approved = strings.Contains(body, "was approved")
	default:
		return cardView{}, false
	}
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "Approval"
	}
	return cardView{
		ID: id, Tool: tool, Line: body, ReadOnly: true, Approved: approved,
	}, true
}
