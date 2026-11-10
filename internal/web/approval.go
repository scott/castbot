package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

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
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.base.Done():
			return
		case <-tick.C:
			s.ExpireDue(context.Background())
		}
	}
}

// ExpireDue posts Deny for approvals whose wait has elapsed.
// A second call does not post twice.
func (s *Server) ExpireDue(ctx context.Context) {
	s.expireMu.Lock()
	defer s.expireMu.Unlock()
	rows, err := s.store.ExpireDue(ctx)
	if err != nil {
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil || s.base.Err() != nil {
			return
		}
		runRow, err := s.store.GetRun(ctx, row.UserID, row.RunID)
		if err != nil {
			continue
		}
		user, err := s.store.GetUser(ctx, row.UserID)
		if err != nil {
			continue
		}
		var postErr error
		if !runTerminal(runRow.Status) {
			postErr = s.postPermission(ctx, user, runRow, row.RequestID, false)
		}
		if postErr != nil && !lostPermission(postErr) {
			continue
		}
		if err := s.store.MarkDenialSent(ctx, row.UserID, row.ID); err != nil {
			continue
		}
		s.runs.Hub.Notify(row.RunID)
	}
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
	row, err := s.store.GetApproval(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	body, err := execTemplate("approvalCard", s.presentApproval(row, user, from(r).CSRF))
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
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
	runRow, err := s.store.GetRun(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	row, claimed, err := s.store.ClaimDecision(r.Context(), user.ID, r.PathValue("approval"), status, user.ID)
	if s.missing(w, err) {
		return
	}
	if row.RunID != runRow.ID {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	if !claimed {
		s.writeCard(w, user, row, from(r).CSRF)
		return
	}
	if runTerminal(runRow.Status) {
		next, err := s.store.MarkInactive(r.Context(), user.ID, row.ID, "user", user.ID)
		if err != nil {
			s.writeCard(w, user, row, from(r).CSRF)
			return
		}
		s.runs.Hub.Notify(runRow.ID)
		s.writeCard(w, user, next, from(r).CSRF)
		return
	}
	if err := s.postPermission(r.Context(), user, runRow, row.RequestID, allow); err != nil {
		if lostPermission(err) {
			next, merr := s.store.MarkInactive(r.Context(), user.ID, row.ID, "user", user.ID)
			if merr != nil {
				s.writeCard(w, user, row, from(r).CSRF)
				return
			}
			s.runs.Hub.Notify(runRow.ID)
			s.writeCard(w, user, next, from(r).CSRF)
			return
		}
		_ = s.store.ReleaseDecision(r.Context(), user.ID, row.ID, status, user.ID)
		writeText(w, http.StatusBadGateway, "The agent did not take that decision.\n")
		return
	}
	if err := s.store.WriteDecisionAudit(r.Context(), user.ID, row.ID, action, "user", user.ID); err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	next, err := s.store.GetApproval(r.Context(), user.ID, row.ID)
	if s.missing(w, err) {
		return
	}
	s.runs.Hub.Notify(runRow.ID)
	s.writeCard(w, user, next, from(r).CSRF)
}

func (s *Server) writeCard(w http.ResponseWriter, user store.User, row store.Approval, csrf string) {
	body, err := execTemplate("approvalCard", s.presentApproval(row, user, csrf))
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
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

func (s *Server) postPermission(ctx context.Context, user store.User, runRow store.Run, requestID string, allow bool) error {
	s.mu.Lock()
	h := s.machine
	client := s.client
	s.mu.Unlock()
	if h != nil {
		vm, err := s.store.LiveVM(ctx, user.ID, runRow.AgentID)
		if err == nil {
			return s.postMachine(ctx, h, user.ID, runRow.AgentID, vm, requestID, allow)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	responder, ok := client.(permissionResponder)
	if !ok || responder == nil {
		return errors.New("no runtime")
	}
	return responder.Respond(ctx, requestID, allow)
}

func (s *Server) postMachine(ctx context.Context, h host.AgentHost, userID, agentID string, vm store.VM, requestID string, allow bool) error {
	state, _ := s.observedMachine(ctx, userID, agentID, vm)
	endpoint, token, err := s.store.VMCredentials(ctx, userID, agentID)
	if err != nil {
		return err
	}
	if state == "paused" && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
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
		Args: approvalArgs(row.Arguments, row.Redacted),
		When: clockLabel(row.CreatedAt, loc),
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
	if len(value) > 80 {
		view.Long = true
		view.Short = value[:80] + "..."
	}
	return view
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
	if len(cards) == 0 {
		return "", nil
	}
	return execTemplate("approvalCards", page{Cards: cards})
}

func cliCard(body, id string) (cardView, bool) {
	body = strings.TrimSpace(body)
	tool := ""
	approved := false
	switch {
	case strings.HasPrefix(body, "An approval was asked for ") && strings.Contains(body, "Cast CLI"):
		tool = strings.TrimSuffix(strings.TrimPrefix(body, "An approval was asked for "), ". Answer it in the Cast CLI.")
	case strings.HasPrefix(body, "An approval for ") && strings.Contains(body, " was "):
		rest := strings.TrimPrefix(body, "An approval for ")
		tool = rest
		if i := strings.Index(rest, " was "); i >= 0 {
			tool = rest[:i]
		}
		approved = strings.Contains(body, "was approved")
	case strings.Contains(body, "Cast CLI") && strings.Contains(body, "approval"):
		tool = "Approval"
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
