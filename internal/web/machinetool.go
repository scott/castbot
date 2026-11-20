package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/scott/castbot/internal/machinetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const (
	machineLocked  = "This machine is being replaced. Pause, Wake, Delete and Add to Cast CLI are off until it is done."
	refreshChecked = "Checks the machine with the cloud API. This does not wake it."
	retryNotFailed = "The machine is not waiting on a failed start."
	retryUseAgain  = "The last start failed. Try again starts a new machine."
	machineUnread  = "The machine record could not be read."
	machineNoStart = "The machine did not start."
)

// machinePower runs one machine tool for the agent in the path.
// user_id and agent_id arguments are ignored. The call cannot name another agent.
func (s *Server) machinePower(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
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
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &struct{}{}); err != nil {
			writeAgentResult(w, "That request was rejected.", true)
			return
		}
	}
	text, failed := s.runMachineTool(r.Context(), user, agent, name, "")
	writeAgentResult(w, text, failed)
}

// maybeMachine reads one castbot-machine fence from the latest reply.
// A start that already began in the last two minutes is not started again.
// That covers a tool call and a fence for the same ask.
func (s *Server) maybeMachine(job run.Job) {
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
	visible, req, ok := machinetool.Parse(reply.Body)
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
	if (req.Action == machinetool.ToolStart || req.Action == machinetool.ToolRetry) && s.recentStart(ctx, job.UserID, job.AgentID) {
		body := strings.TrimSpace(visible)
		if body == "" {
			body = s.machineSentence(ctx, user, agent)
		}
		s.replaceReply(ctx, job.UserID, reply.ID, body)
		s.noteMachineChrome(ctx, job.UserID, job.RunID)
		return
	}
	text, failed := s.runMachineTool(ctx, user, agent, req.Action, job.RunID)
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func (s *Server) recentStart(ctx context.Context, userID, agentID string) bool {
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if err != nil {
		return false
	}
	if vm.State != "creating" && vm.State != "running" {
		return false
	}
	at, err := time.Parse(time.RFC3339, vm.CreatedAt)
	if err != nil {
		return false
	}
	return !at.Before(s.store.Now().Add(-recentOrganizeWindow))
}

func (s *Server) runMachineTool(ctx context.Context, user store.User, agent store.Agent, name, runID string) (string, bool) {
	if !machinetool.Known(name) {
		return "That request was rejected.", true
	}
	if s.replacing(ctx, user.ID, agent.ID) {
		return machineLocked, true
	}
	var text string
	var failed bool
	switch name {
	case machinetool.ToolPause:
		text, failed = s.pauseForTool(ctx, user, agent)
	case machinetool.ToolWake:
		text, failed = s.wakeForTool(ctx, user, agent)
	case machinetool.ToolStart:
		text, failed = s.startForTool(ctx, user, agent)
	case machinetool.ToolRetry:
		text, failed = s.retryForTool(ctx, user, agent)
	case machinetool.ToolRefresh:
		text, failed = s.refreshForTool(ctx, user, agent)
	default:
		return "That request was rejected.", true
	}
	if !failed {
		s.noteMachineChrome(ctx, user.ID, runID)
	}
	return text, failed
}

func (s *Server) pauseForTool(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	vm, err := s.store.LiveVM(ctx, user.ID, agent.ID)
	if errors.Is(err, store.ErrNotFound) {
		return wakeNoMachine, true
	}
	if err != nil {
		return machineUnread, true
	}
	h, _, _ := s.hostConfig()
	if h == nil || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return wakeNoMachine, true
	}
	if vm.State == "paused" {
		return s.machineSentence(ctx, user, agent), false
	}
	if vm.State != "running" {
		return s.machineSentence(ctx, user, agent), true
	}
	s.noteVM(ctx, user.ID, agent.ID, vm.MicroVMID, store.EventPauseRequested, 0)
	actionAt := time.Now()
	got, err := h.Pause(ctx, vm.MicroVMID)
	if err != nil {
		return wakeUnchanged, true
	}
	if err := s.store.ApplyVMState(ctx, user.ID, agent.ID, got.State, got.Endpoint, ""); err != nil {
		return wakeUnchanged, true
	}
	s.noteVM(ctx, user.ID, agent.ID, vm.MicroVMID, store.EventPauseObserved, time.Since(actionAt))
	return s.machineSentence(ctx, user, agent), false
}

func (s *Server) wakeForTool(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	vm, err := s.store.LiveVM(ctx, user.ID, agent.ID)
	if errors.Is(err, store.ErrNotFound) {
		return wakeNoMachine, true
	}
	if err != nil {
		return machineUnread, true
	}
	h, _, _ := s.hostConfig()
	if h == nil || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return wakeNoMachine, true
	}
	if vm.State != "paused" {
		return s.machineSentence(ctx, user, agent), vm.State != "running"
	}
	out := s.wakeIfPaused(ctx, user.ID, agent.ID, false)
	if out.DBErr {
		return wakeUnchanged, true
	}
	if out.Message == wakeReloaded {
		return wakeReloaded, true
	}
	if out.Code >= 400 {
		if out.Message == "" {
			return wakeUnchanged, true
		}
		return out.Message, true
	}
	return s.machineSentence(ctx, user, agent), false
}

func (s *Server) startForTool(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	vm, err := s.store.LiveVM(ctx, user.ID, agent.ID)
	if err == nil {
		if vm.State == "failed" {
			return retryUseAgain, true
		}
		return "This agent already has a machine.", true
	}
	if !errors.Is(err, store.ErrNotFound) {
		return machineUnread, true
	}
	if err := s.launchNewAgent(ctx, user.ID, agent); err != nil {
		return startErr(err), true
	}
	return s.machineSentence(ctx, user, agent), false
}

func (s *Server) retryForTool(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	vm, err := s.store.LiveVM(ctx, user.ID, agent.ID)
	has := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return machineUnread, true
	}
	fresh, err := s.store.GetAgent(ctx, user.ID, agent.ID)
	if err != nil {
		return machineUnread, true
	}
	if has && vm.State != "failed" {
		return retryNotFailed, true
	}
	if !has && fresh.Status != "failed" {
		return retryNotFailed, true
	}
	if has {
		h, _, _ := s.hostConfig()
		if h != nil && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
			s.deleteQuiet(h, vm.MicroVMID)
		}
		if err := s.store.ReleaseVM(ctx, user.ID, agent.ID); err != nil {
			return machineNoStart, true
		}
		fresh, err = s.store.GetAgent(ctx, user.ID, agent.ID)
		if err != nil {
			return machineUnread, true
		}
	}
	if err := s.launchNewAgent(ctx, user.ID, fresh); err != nil {
		return startErr(err), true
	}
	return s.machineSentence(ctx, user, fresh), false
}

func (s *Server) refreshForTool(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	vm, err := s.store.LiveVM(ctx, user.ID, agent.ID)
	if errors.Is(err, store.ErrNotFound) {
		return wakeNoMachine, true
	}
	if err != nil {
		return machineUnread, true
	}
	if vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return s.machineSentence(ctx, user, agent), false
	}
	if err := s.refreshMachineStatus(ctx, user.ID, agent.ID, true); err != nil {
		return "The machine could not be checked. It was not woken.", true
	}
	lead := s.machineSentence(ctx, user, agent)
	if lead == "" {
		return refreshChecked, false
	}
	return lead + "\n" + refreshChecked, false
}

func startErr(err error) string {
	switch {
	case errors.Is(err, errNoHost):
		return "No machine host is configured."
	case errors.Is(err, store.ErrQuota):
		return "This account has reached its machine limit."
	case errors.Is(err, store.ErrConflict):
		return "This agent already has a machine."
	default:
		return machineNoStart
	}
}

func (s *Server) machineSentence(ctx context.Context, user store.User, agent store.Agent) string {
	fresh, err := s.store.GetAgent(ctx, user.ID, agent.ID)
	if err != nil {
		return machineUnread
	}
	view := s.machineFromStore(ctx, user.ID, &fresh, false)
	if strings.TrimSpace(view.Lead) == "" {
		return machineUnread
	}
	return view.Lead
}

func (s *Server) noteMachineChrome(ctx context.Context, userID, runID string) {
	if runID != "" {
		s.markChrome(runID)
		return
	}
	rows, err := s.store.ListOpenRuns(ctx, userID)
	if err != nil {
		return
	}
	for _, row := range rows {
		s.markChrome(row.ID)
	}
}

func (s *Server) markChrome(runID string) {
	if runID == "" {
		return
	}
	s.sideMu.Lock()
	defer s.sideMu.Unlock()
	if s.chromeRefresh == nil {
		s.chromeRefresh = map[string]struct{}{}
	}
	s.chromeRefresh[runID] = struct{}{}
}

func (s *Server) takeChrome(runID string) bool {
	s.sideMu.Lock()
	defer s.sideMu.Unlock()
	if _, ok := s.chromeRefresh[runID]; !ok {
		return false
	}
	delete(s.chromeRefresh, runID)
	return true
}

// machineChromeFrame is the machine chip and lead, ready to swap into the open page.
// It is empty when this run has not changed the machine since the last frame.
func (s *Server) machineChromeFrame(ctx context.Context, user store.User, runRow store.Run) string {
	if !s.takeChrome(runRow.ID) {
		return ""
	}
	agent, err := s.store.GetAgent(ctx, user.ID, runRow.AgentID)
	if err != nil {
		s.markChrome(runRow.ID)
		return ""
	}
	view := s.paintAgent(ctx, user.ID, agent, true, userLocation(user.Timezone))
	machine := s.machineFromStore(ctx, user.ID, &agent, false)
	html, err := execTemplate("machineSync", page{Agent: view, Machine: machine})
	if err != nil || html == "" {
		s.markChrome(runRow.ID)
		return ""
	}
	return html
}
