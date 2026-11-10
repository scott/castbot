package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func (s *Server) openRun(ctx context.Context, job run.Job, req runtime.Send) (runtime.Stream, error) {
	s.mu.Lock()
	h := s.machine
	client := s.client
	s.mu.Unlock()
	if h != nil {
		endpoint, token, err := s.store.VMCredentials(ctx, job.UserID, job.AgentID)
		if err == nil && endpoint != "" && token != "" {
			return h.Stream(ctx, endpoint, token, req)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	if client == nil {
		return nil, errors.New("no runtime")
	}
	return client.Open(ctx, req)
}

func (s *Server) provisionMachine(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	h, image, publicURL := s.hostConfig()
	if h == nil || image == "" || publicURL == "" {
		s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusBadRequest, "No machine host is configured.")
		return
	}
	if _, err := s.store.LiveVM(r.Context(), user.ID, agent.ID); err == nil {
		s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusConflict, "This agent already has a machine.")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if _, err := s.store.BeginVM(r.Context(), user.ID, agent.ID, host.MachineName(agent.ID), image); err != nil {
		if errors.Is(err, store.ErrQuota) {
			s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusConflict, "This account has reached its machine limit.")
			return
		}
		if s.missing(w, err) {
			return
		}
		return
	}
	boot, err := s.store.IssueBootstrap(r.Context(), user.ID, agent.ID)
	if err != nil {
		_ = s.store.ReleaseVM(r.Context(), user.ID, agent.ID)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	created, err := h.Create(ctx, host.CreateSpec{
		Name: host.MachineName(agent.ID), AgentID: agent.ID, Image: image,
		Region: host.DefaultRegion, CPU: host.DefaultCPU, MemoryMiB: host.DefaultMemoryMiB,
		Idle: time.Duration(agent.IdleTimeout) * time.Second, HTTPPort: host.DefaultHTTPPort,
		ControlURL: publicURL, Bootstrap: boot,
	})
	if err != nil || created.State != "running" || created.ID == "" {
		if created.ID != "" {
			_ = h.Delete(context.Background(), created.ID)
		}
		_ = s.store.ReleaseVM(r.Context(), user.ID, agent.ID)
		s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusBadGateway, "The machine did not start. You can try again.")
		return
	}
	if err := s.store.FinishVM(r.Context(), user.ID, agent.ID, created.ID, created.Endpoint, created.State, ""); err != nil {
		_ = h.Delete(context.Background(), created.ID)
		_ = s.store.ReleaseVM(r.Context(), user.ID, agent.ID)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	endpoint, token, _ := s.store.VMCredentials(r.Context(), user.ID, agent.ID)
	if endpoint != "" && token != "" {
		pctx, pcancel := context.WithTimeout(r.Context(), 90*time.Second)
		_ = h.Probe(pctx, endpoint, token)
		pcancel()
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"/machine", http.StatusSeeOther)
}

func (s *Server) pauseMachine(w http.ResponseWriter, r *http.Request) {
	s.machineAction(w, r, "pause")
}

func (s *Server) wakeMachine(w http.ResponseWriter, r *http.Request) {
	s.machineAction(w, r, "wake")
}

func (s *Server) machineAction(w http.ResponseWriter, r *http.Request, kind string) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	vm, err := s.store.LiveVM(r.Context(), user.ID, agent.ID)
	if s.missing(w, err) {
		return
	}
	h, _, _ := s.hostConfig()
	if h == nil || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusBadRequest, "This agent has no machine yet.")
		return
	}
	var got host.Machine
	switch kind {
	case "pause":
		got, err = h.Pause(r.Context(), vm.MicroVMID)
	default:
		got, err = h.Wake(r.Context(), vm.MicroVMID)
	}
	if err != nil {
		s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusBadGateway, "The machine did not change.")
		return
	}
	if err := s.store.ApplyVMState(r.Context(), user.ID, agent.ID, got.State, got.Endpoint, ""); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"/machine", http.StatusSeeOther)
}

func (s *Server) deleteMachine(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if err := host.ConfirmName(r.FormValue("confirm"), agent.Name); err != nil {
		s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusBadRequest, "Type the agent name to delete this machine.")
		return
	}
	if err := s.removeMachine(r.Context(), user.ID, agent); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusNotFound, "This agent has no machine.")
			return
		}
		s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusBadGateway, "The machine could not be deleted.")
		return
	}
	if err := s.store.DeleteAgent(r.Context(), user.ID, agent.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) removeMachine(ctx context.Context, userID string, agent store.Agent) error {
	vm, err := s.store.LiveVM(ctx, userID, agent.ID)
	if err != nil {
		return err
	}
	h, _, _ := s.hostConfig()
	if h != nil && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
		if err := h.Delete(ctx, vm.MicroVMID); err != nil {
			return err
		}
	}
	return s.store.MarkVMDeleted(ctx, userID, agent.ID)
}

func (s *Server) syncMachine(ctx context.Context, userID string, agent *store.Agent) machineView {
	view := machineView{Lead: "No machine is attached."}
	h, image, publicURL := s.hostConfig()
	vm, err := s.store.LiveVM(ctx, userID, agent.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		view.Lead = "The machine record could not be read."
		return view
	}
	has := err == nil
	if has && h != nil && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		got, serr := h.Status(cctx, vm.MicroVMID)
		cancel()
		if serr == nil {
			_ = s.store.ApplyVMState(ctx, userID, agent.ID, got.State, got.Endpoint, "")
			if fresh, ferr := s.store.GetAgent(ctx, userID, agent.ID); ferr == nil {
				*agent = fresh
			}
			if fresh, ferr := s.store.LiveVM(ctx, userID, agent.ID); ferr == nil {
				vm = fresh
			}
		}
	}
	switch {
	case !has && h != nil && image != "" && publicURL != "":
		view.Lead = "This agent has no machine yet."
		view.CanStart = agent.Status == "draft" || agent.Status == "failed"
		view.CanRetry = agent.Status == "failed"
	case !has:
		view.Lead = "No machine is attached."
	default:
		view.CanDelete = true
		view.LastWake = vm.LastWake
		switch vm.State {
		case "running":
			view.Lead = "The machine is running. It pauses after it is idle."
			view.CanPause = h != nil
		case "paused":
			view.Lead = "The machine is paused. A chat message wakes it."
			view.CanWake = h != nil
		case "creating":
			view.Lead = "The machine is waking."
		case "failed":
			view.Lead = "The machine did not start. You can try again."
			view.CanRetry = h != nil
		default:
			view.Lead = "The machine record is saved. A check does not wake it."
		}
	}
	return view
}
