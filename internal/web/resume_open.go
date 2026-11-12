package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/scott/castbot/internal/store"
)

// wakeOutcome is one pass through the shared resume lock.
// Called is true only when Wake was invoked. A machine that is already
// running or still starting does not call Wake.
type wakeOutcome struct {
	State   string
	Called  bool
	Code    int
	Message string
	DBErr   bool
}

const (
	wakeUnchanged = "The machine did not change."
	wakeReloaded  = "The machine woke, but the saved agent could not be reloaded."
	wakeNoMachine = "This agent has no machine yet."
)

// wakeIfPaused resumes a paused machine when onlyPaused is set.
// The manual button passes false and always calls Wake, matching the
// existing resume action. Two callers share one lock.
func (s *Server) wakeIfPaused(ctx context.Context, userID, agentID string, onlyPaused bool) wakeOutcome {
	out := wakeOutcome{Code: http.StatusOK}
	s.withWake(agentID, func() {
		out = s.wakeLocked(ctx, userID, agentID, onlyPaused)
	})
	return out
}

func (s *Server) wakeLocked(ctx context.Context, userID, agentID string, onlyPaused bool) wakeOutcome {
	out := wakeOutcome{Code: http.StatusOK}
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if errors.Is(err, store.ErrNotFound) {
		out.Code = http.StatusBadRequest
		out.Message = wakeNoMachine
		return out
	}
	if err != nil {
		out.DBErr = true
		out.Code = http.StatusInternalServerError
		return out
	}
	out.State = vm.State
	h, _, _ := s.hostConfig()
	if h == nil || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		out.Code = http.StatusBadRequest
		out.Message = wakeNoMachine
		return out
	}
	if onlyPaused && vm.State != "paused" {
		if vm.State == "creating" {
			out.State = s.refreshCreating(ctx, userID, agentID, vm)
		}
		return out
	}
	s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeRequested, 0)
	actionAt := time.Now()
	got, err := h.Wake(ctx, vm.MicroVMID)
	out.Called = true
	if err != nil {
		out.Code = http.StatusBadGateway
		out.Message = wakeUnchanged
		return out
	}
	if err := s.store.ApplyVMState(ctx, userID, agentID, got.State, got.Endpoint, ""); err != nil {
		out.DBErr = true
		out.Code = http.StatusInternalServerError
		return out
	}
	out.State = got.State
	if got.State != "running" {
		return out
	}
	s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeRunning, time.Since(actionAt))
	endpoint, token, credErr := s.store.VMCredentials(ctx, userID, agentID)
	if credErr == nil && endpoint != "" && token != "" {
		if perr := h.Probe(ctx, endpoint, token); perr == nil {
			s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeReady, time.Since(actionAt))
		}
	}
	s.fireAfterRun(ctx, userID, agentID)
	if err := s.runReload(ctx, userID, agentID); err != nil {
		out.Code = http.StatusBadGateway
		out.Message = wakeReloaded
	}
	return out
}

// refreshCreating asks Status only. A creating machine is already starting,
// so this must not call Wake. Applying "creating" again would clear the
// Waking activity, so the row is updated only when the state changes.
func (s *Server) refreshCreating(ctx context.Context, userID, agentID string, vm store.VM) string {
	h, _, _ := s.hostConfig()
	if h == nil || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return vm.State
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	got, err := h.Status(cctx, vm.MicroVMID)
	cancel()
	if err != nil || got.State == "" || got.State == "creating" {
		return "creating"
	}
	s.noteIfEdgeResume(ctx, userID, agentID, vm.MicroVMID, vm.State, got.State)
	if err := s.store.ApplyVMState(ctx, userID, agentID, got.State, got.Endpoint, ""); err != nil {
		return "creating"
	}
	return got.State
}

func (s *Server) finishWake(w http.ResponseWriter, r *http.Request, user store.User, agent store.Agent) {
	out := s.wakeIfPaused(r.Context(), user.ID, agent.ID, false)
	if out.DBErr {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if out.Code >= 400 {
		s.renderAgent(w, r, user, agent.ID, false, "machine", out.Code, out.Message)
		return
	}
	s.redirectMachine(w, r, agent.ID)
}

func (s *Server) resumeChat(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if s.refuseReplacing(w, r, user.ID, agent.ID) {
		return
	}
	out := s.wakeIfPaused(r.Context(), user.ID, agent.ID, true)
	hx := r.Header.Get("HX-Request") == "true"
	if out.DBErr {
		if hx {
			s.writeResume(w, r, user, agent.ID, wakeUnchanged, false)
			return
		}
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if out.Code >= 400 {
		if hx {
			s.writeResume(w, r, user, agent.ID, out.Message, false)
			return
		}
		s.renderChat(w, r, user, agent.ID, false, "chat", http.StatusOK, out.Message)
		return
	}
	if !hx {
		http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID), http.StatusSeeOther)
		return
	}
	s.writeResume(w, r, user, agent.ID, "", out.State == "creating")
}

func (s *Server) writeResume(w http.ResponseWriter, r *http.Request, user store.User, agentID, message string, poll bool) {
	agent, err := s.store.GetAgent(r.Context(), user.ID, agentID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p := page{Agent: s.paintAgent(r.Context(), user.ID, agent, true, userLocation(user.Timezone))}
	p.ResumeErr = message
	if message == "" && poll && agent.Activity == "Waking" {
		markWaking(&p.Agent)
		p.ResumePoll = true
		p.ResumeAct = actRowHTML(activityOut{Label: "Waking up"}, "")
	}
	_ = s.render(w, http.StatusOK, "resumeLive", p)
}

// armChatResume paints the waking indicator on the chat page and arms one
// POST. A plain GET never calls Wake. Panels, including Files, stay idle.
func (s *Server) armChatResume(p *page, agentID string, machine machineView) {
	if p.FormError != "" || p.Page != "chat" || p.Tab != "chat" || p.Panel != "" || p.Replace.Lock || !machine.CanWake {
		return
	}
	p.AutoResume = true
	markWaking(&p.Agent)
	for i := range p.Sections {
		for j := range p.Sections[i].Agents {
			if p.Sections[i].Agents[j].ID == agentID {
				markWaking(&p.Sections[i].Agents[j])
			}
		}
	}
	p.ResumeAct = actRowHTML(activityOut{Label: "Waking up"}, "")
}

func markWaking(v *agentView) {
	v.Dot = "start"
	v.ChipClass = "start"
	v.ChipLabel = "Waking"
	v.ChipMeta = ""
	v.Sub = "Waking up"
	v.SubClass = "act-sub"
	v.ActSub = sidebarActHTML(activityOut{Label: "Waking up"})
}
