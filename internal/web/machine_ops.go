package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const stalePending = 10 * time.Minute

func (s *Server) openRun(ctx context.Context, job run.Job, req runtime.Send) (runtime.Stream, error) {
	s.mu.Lock()
	h := s.machine
	client := s.client
	s.mu.Unlock()
	if h != nil {
		vm, liveErr := s.store.LiveVM(ctx, job.UserID, job.AgentID)
		if liveErr == nil {
			endpoint, token, err := s.store.VMCredentials(ctx, job.UserID, job.AgentID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			if err == nil && endpoint != "" && token != "" {
				return h.Stream(ctx, endpoint, token, req)
			}
			if endpoint == "" && vm.State != "creating" && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
				return nil, &run.Notice{Text: "The machine address was not accepted."}
			}
			return nil, &run.Notice{Text: "The machine is starting."}
		}
		if liveErr != nil && !errors.Is(liveErr, store.ErrNotFound) {
			return nil, liveErr
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
		if errors.Is(err, store.ErrConflict) {
			s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusConflict, "This agent already has a machine.")
			return
		}
		if s.missing(w, err) {
			return
		}
		return
	}
	s.prov.Add(1)
	go func() {
		defer s.prov.Done()
		s.finishProvision(s.base, h, user.ID, agent, image, publicURL)
	}()
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"/machine", http.StatusSeeOther)
}

func (s *Server) finishProvision(ctx context.Context, h host.AgentHost, userID string, agent store.Agent, image, publicURL string) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	boot, err := s.store.IssueBootstrap(ctx, userID, agent.ID)
	if err != nil {
		s.releaseQuiet(userID, agent.ID)
		return
	}
	idle := time.Duration(agent.IdleTimeout) * time.Second
	if idle <= 0 {
		idle = host.DefaultIdle
	}
	inst, err := s.store.InstanceID(ctx)
	if err != nil {
		s.releaseQuiet(userID, agent.ID)
		return
	}
	created, err := h.Create(ctx, host.CreateSpec{
		Name: host.MachineName(agent.ID), AgentID: agent.ID, Image: image,
		Region: host.DefaultRegion, CPU: host.DefaultCPU, MemoryMiB: host.DefaultMemoryMiB,
		Idle: idle, HTTPPort: host.DefaultHTTPPort,
		ControlURL: publicURL, Bootstrap: boot,
		Tags: []string{host.InstanceTag(inst)},
	})
	if err != nil || created.State != "running" || created.ID == "" || created.Endpoint == "" {
		if created.ID != "" {
			s.deleteQuiet(h, created.ID)
		}
		if err == nil && created.ID != "" && created.Endpoint == "" {
			s.releaseNoted(userID, agent.ID, "The machine address was not accepted. You can try again.")
			return
		}
		s.releaseQuiet(userID, agent.ID)
		return
	}
	if err := s.store.FinishVM(detach(ctx), userID, agent.ID, created.ID, created.Endpoint, created.State, ""); err != nil {
		s.deleteQuiet(h, created.ID)
		s.releaseQuiet(userID, agent.ID)
		return
	}
	endpoint, token, err := s.store.VMCredentials(detach(ctx), userID, agent.ID)
	if err == nil && endpoint != "" && token != "" {
		pctx, pcancel := context.WithTimeout(detach(ctx), 90*time.Second)
		_ = h.Probe(pctx, endpoint, token)
		pcancel()
	}
}

func detach(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func (s *Server) releaseQuiet(userID, agentID string) {
	s.releaseNoted(userID, agentID, "")
}

func (s *Server) releaseNoted(userID, agentID, note string) {
	cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = s.store.ReleaseVM(cctx, userID, agentID)
	if note != "" {
		_ = s.store.NoteAgent(cctx, userID, agentID, note)
	}
}

func (s *Server) deleteQuiet(h host.AgentHost, id string) {
	if h == nil || id == "" || id == "pending" {
		return
	}
	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = h.Delete(cctx, id)
}

// SweepMachines drops stale pending reservations and deletes machines this
// database created that have no live row. A machine without this database's
// castbot-inst tag is left alone, including other castbot processes that
// share the token. Demo names, castbot-s00, and castbot-live stay as well.
func (s *Server) SweepMachines(ctx context.Context) {
	n, err := s.store.ReleaseStalePending(ctx, stalePending)
	if err != nil {
		log.Printf("stale machine sweep failed")
	} else if n > 0 {
		log.Printf("released %d stale machine reservations", n)
	}
	s.mu.Lock()
	h := s.machine
	s.mu.Unlock()
	lister, ok := h.(interface {
		List(context.Context) ([]host.Machine, error)
	})
	if !ok || h == nil {
		return
	}
	inst, err := s.store.InstanceID(ctx)
	if err != nil || inst == "" {
		log.Printf("instance id unavailable")
		return
	}
	tag := host.InstanceTag(inst)
	vms, err := lister.List(ctx)
	if err != nil {
		log.Printf("machine list failed")
		return
	}
	known, err := s.store.LiveMicroIDs(ctx)
	if err != nil {
		log.Printf("machine id list failed")
		return
	}
	for _, vm := range vms {
		if vm.ID == "" || !hasTag(vm, tag) {
			continue
		}
		if hasTag(vm, "castbot-live") {
			continue
		}
		if err := host.GuardDelete(vm); err != nil {
			continue
		}
		if _, ok := known[vm.ID]; ok {
			continue
		}
		if err := h.Delete(ctx, vm.ID); err != nil {
			log.Printf("orphan machine was not deleted")
			continue
		}
		log.Printf("deleted orphan machine")
	}
}

func failedLead(agent store.Agent) string {
	if strings.TrimSpace(agent.Activity) != "" {
		return agent.Activity
	}
	return "The machine did not start. You can try again."
}

// paintAgent keeps a machine that is not ready to chat off the green ready dot.
func (s *Server) paintAgent(ctx context.Context, userID string, a store.Agent, on bool, loc *time.Location) agentView {
	v := presentAgent(a, on, loc)
	if v.Dot != "run" {
		return v
	}
	s.mu.Lock()
	h := s.machine
	s.mu.Unlock()
	if h == nil {
		return v
	}
	vm, err := s.store.LiveVM(ctx, userID, a.ID)
	if err != nil {
		return v
	}
	endpoint, token, credErr := s.store.VMCredentials(ctx, userID, a.ID)
	if credErr == nil && endpoint != "" && token != "" {
		return v
	}
	if endpoint == "" && vm.State != "creating" && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
		v.Dot = "err"
		v.ChipClass = "err"
		v.ChipLabel = "Failed"
		v.Sub, v.SubClass = joinDot(a.Title, "Address rejected"), "err"
		return v
	}
	v.Dot = "start"
	v.ChipClass = "start"
	v.ChipLabel = "Starting"
	v.Sub, v.SubClass = joinDot(a.Title, "Starting"), "start"
	return v
}

func hasTag(vm host.Machine, tag string) bool {
	for _, item := range vm.Tags {
		if item == tag {
			return true
		}
	}
	return false
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
	} else {
		log.Printf("machine row removed without a remote delete")
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
		view.CanStart = agent.Status == "draft" || agent.Status == "failed"
		view.CanRetry = agent.Status == "failed"
		if agent.Status == "failed" {
			view.Lead = failedLead(*agent)
		} else {
			view.Lead = "This agent has no machine yet."
		}
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
			view.Lead = failedLead(*agent)
			view.CanRetry = h != nil
		default:
			view.Lead = "The machine record is saved. A check does not wake it."
		}
	}
	return view
}
