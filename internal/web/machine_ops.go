package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const stalePending = 10 * time.Minute

func (s *Server) openRun(ctx context.Context, job run.Job, req runtime.Send) (runtime.Stream, error) {
	if s.replacing(ctx, job.UserID, job.AgentID) {
		return nil, &run.Notice{Text: replaceSentence}
	}
	s.mu.Lock()
	h := s.machine
	client := s.client
	echoOn := s.devEcho
	explicit := s.explicitRuntime
	s.mu.Unlock()
	if h != nil {
		vm, liveErr := s.store.LiveVM(ctx, job.UserID, job.AgentID)
		if liveErr == nil {
			endpoint, token, err := s.store.VMCredentials(ctx, job.UserID, job.AgentID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			agent, err := s.store.GetAgent(ctx, job.UserID, job.AgentID)
			if err != nil {
				return nil, err
			}
			if _, _, missing := s.effectiveModelSecret(ctx, job.UserID, agent.Model); missing != "" {
				return nil, &run.Notice{Text: missing}
			}
			state, endpoint := s.observedMachine(ctx, job.UserID, job.AgentID, vm)
			routineID := ""
			if row, rerr := s.store.GetRun(ctx, job.UserID, job.RunID); rerr == nil {
				routineID = row.RoutineRunID
			}
			facts, werr := s.wakeForWork(ctx, h, job.UserID, job.AgentID, vm, endpoint, token, state, routineID != "")
			if werr != nil {
				if routineID != "" {
					return nil, werr
				}
				var note *run.Notice
				if errors.As(werr, &note) {
					return nil, note
				}
				return nil, &run.Notice{Text: "The machine is paused."}
			}
			if facts.State != "" {
				state = facts.State
			}
			if facts.Endpoint != "" {
				endpoint = facts.Endpoint
			}
			if routineID != "" {
				pausedMS, wakeMS := -1, -1
				if facts.WasPaused {
					pausedMS = int(facts.PausedFor / time.Millisecond)
					wakeMS = int(facts.WakeFor / time.Millisecond)
				}
				_ = s.store.NoteRoutineWake(ctx, job.UserID, routineID, pausedMS, wakeMS)
			}
			deferRestart, err := s.guestRestart(ctx, job.UserID, job.AgentID, job.RunID)
			if err != nil {
				return nil, err
			}
			if !deferRestart {
				if err := s.runReloadExcept(ctx, job.UserID, job.AgentID, job.RunID); err != nil {
					return nil, err
				}
			}
			endpoint, token, err = s.store.VMCredentials(ctx, job.UserID, job.AgentID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			if state == "running" && err == nil && endpoint != "" && token != "" {
				if !deferRestart {
					if rerr := s.refreshSecrets(ctx, job.UserID, agent, endpoint, token); rerr != nil {
						return nil, rerr
					}
					s.clearGuestDeferIfIdle(ctx, job.UserID, job.AgentID)
				}
				_ = s.pullJournal(ctx, job.UserID, job.AgentID, endpoint, token)
				return h.Stream(ctx, endpoint, token, req)
			}
			if endpoint == "" && state != "creating" && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
				return nil, &run.Notice{Text: "The machine address was not accepted."}
			}
			return nil, &run.Notice{Text: "The machine is starting."}
		}
		if liveErr != nil && !errors.Is(liveErr, store.ErrNotFound) {
			return nil, liveErr
		}
	}
	if client != nil && (echoOn || explicit) {
		return client.Open(ctx, req)
	}
	return nil, &run.Notice{Text: "No machine yet. Start it to chat."}
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
	if s.refuseReplacing(w, r, user.ID, agent.ID) {
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
	s.noteVM(r.Context(), user.ID, agent.ID, "", store.EventCreateRequested, 0)
	s.prov.Add(1)
	go func() {
		defer s.prov.Done()
		s.finishProvision(s.base, h, user.ID, agent, image, publicURL)
	}()
	target := "/agents/" + url.PathEscape(agent.ID) + "/machine"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
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
	started := time.Now()
	created, err := h.Create(ctx, host.CreateSpec{
		Name: host.MachineName(agent.ID), AgentID: agent.ID, Image: image,
		Region: host.DefaultRegion, CPU: host.DefaultCPU, MemoryMiB: host.DefaultMemoryMiB,
		Idle: idle, HTTPPort: host.DefaultHTTPPort,
		ControlURL: publicURL, Bootstrap: boot,
		Tags: []string{host.InstanceTag(inst)},
		Accepted: func(m host.Machine) {
			if m.ID == "" {
				return
			}
			s.noteVM(ctx, userID, agent.ID, m.ID, store.EventCreateAccepted, time.Since(started))
		},
	})
	if err != nil || created.State != "running" || created.ID == "" || created.Endpoint == "" {
		if created.ID != "" {
			s.deleteQuiet(h, created.ID)
		}
		if err == nil && created.AddressRejected {
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
	if created.State == "running" {
		s.noteVM(detach(ctx), userID, agent.ID, created.ID, store.EventRunning, time.Since(started))
	}
	endpoint, token, err := s.store.VMCredentials(detach(ctx), userID, agent.ID)
	if err == nil && endpoint != "" && token != "" {
		pctx, pcancel := context.WithTimeout(detach(ctx), 90*time.Second)
		if perr := h.Probe(pctx, endpoint, token); perr == nil {
			s.noteGuestReady(detach(ctx), userID, agent.ID, created.ID, time.Since(started))
		}
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
// share the token. A copied database has the same instance id, so a machine
// is deleted only when its agent tag names a pending, failed, or deleted row
// in this file. Demo names, castbot-s00, and castbot-live stay as well.
func (s *Server) SweepMachines(ctx context.Context) {
	if n, err := s.store.SweepExpiredDrafts(ctx); err != nil {
		log.Printf("expired draft sweep failed")
	} else if n > 0 {
		log.Printf("removed %d expired import drafts", n)
	}
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
		// A copied database shares the instance id. Delete only when the
		// agent tag names a pending, failed, or deleted row in this file.
		// An agent id this database does not know is left alone.
		agentID := agentTag(vm)
		if agentID == "" {
			continue
		}
		recorded, recErr := s.store.OrphanCandidate(ctx, agentID)
		if recErr != nil || !recorded {
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

func agentTag(vm host.Machine) string {
	for _, tag := range vm.Tags {
		id, ok := strings.CutPrefix(tag, "agent:")
		if ok && id != "" && !strings.Contains(id, ":") {
			return id
		}
	}
	return ""
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
	if s.refuseReplacing(w, r, user.ID, agent.ID) {
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
	actionAt := time.Now()
	switch kind {
	case "pause":
		s.noteVM(r.Context(), user.ID, agent.ID, vm.MicroVMID, store.EventPauseRequested, 0)
		got, err = h.Pause(r.Context(), vm.MicroVMID)
	default:
		s.noteVM(r.Context(), user.ID, agent.ID, vm.MicroVMID, store.EventWakeRequested, 0)
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
	if kind == "pause" {
		s.noteVM(r.Context(), user.ID, agent.ID, vm.MicroVMID, store.EventPauseObserved, time.Since(actionAt))
	}
	if kind != "pause" && got.State == "running" {
		s.noteVM(r.Context(), user.ID, agent.ID, vm.MicroVMID, store.EventWakeRunning, time.Since(actionAt))
		endpoint, token, credErr := s.store.VMCredentials(r.Context(), user.ID, agent.ID)
		if credErr == nil && endpoint != "" && token != "" {
			if perr := h.Probe(r.Context(), endpoint, token); perr == nil {
				s.noteVM(r.Context(), user.ID, agent.ID, vm.MicroVMID, store.EventWakeReady, time.Since(actionAt))
			}
		}
	}
	if kind != "pause" && got.State == "running" {
		if err := s.runReload(r.Context(), user.ID, agent.ID); err != nil {
			s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusBadGateway, "The machine woke, but the saved agent could not be reloaded.")
			return
		}
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
	if s.refuseReplacing(w, r, user.ID, agent.ID) {
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
	delAt := time.Now()
	if h != nil && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
		if err := h.Delete(ctx, vm.MicroVMID); err != nil {
			return err
		}
	} else {
		log.Printf("machine row removed without a remote delete")
	}
	s.noteVM(ctx, userID, agent.ID, vm.MicroVMID, store.EventDelete, time.Since(delAt))
	return s.store.MarkVMDeleted(ctx, userID, agent.ID)
}

// observedMachine asks the machine API for the state. Status does not wake
// the guest. The database row is updated when the API answers.
func (s *Server) observedMachine(ctx context.Context, userID, agentID string, vm store.VM) (state, endpoint string) {
	state = vm.State
	endpoint = vm.Endpoint
	h, _, _ := s.hostConfig()
	if h == nil || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return state, endpoint
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	got, err := h.Status(cctx, vm.MicroVMID)
	cancel()
	if err != nil {
		return state, endpoint
	}
	s.noteIfEdgeResume(ctx, userID, agentID, vm.MicroVMID, state, got.State)
	_ = s.store.ApplyVMState(ctx, userID, agentID, got.State, got.Endpoint, "")
	if got.Endpoint != "" {
		endpoint = got.Endpoint
	}
	return got.State, endpoint
}

// applyPending reloads a bundle that was saved while the machine was not
// running. It does nothing unless the machine is running, and it clears the
// mark only after the reload succeeds.
func (s *Server) applyPending(ctx context.Context, userID, agentID string) error {
	return s.applyPendingExcept(ctx, userID, agentID, "")
}

func (s *Server) applyPendingExcept(ctx context.Context, userID, agentID, exceptRun string) error {
	deferRestart, err := s.guestRestart(ctx, userID, agentID, exceptRun)
	if err != nil || deferRestart {
		return err
	}
	version, err := s.store.PendingBundle(ctx, userID, agentID)
	if err != nil || version == "" {
		return err
	}
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if err != nil || vm.State != "running" {
		return err
	}
	endpoint, token, err := s.store.VMCredentials(ctx, userID, agentID)
	if err != nil || endpoint == "" || token == "" {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	h, _, _ := s.hostConfig()
	if h == nil {
		return nil
	}
	if vm.MicroVMID != "" && vm.MicroVMID != "pending" {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		got, serr := h.Status(cctx, vm.MicroVMID)
		cancel()
		if serr != nil {
			return serr
		}
		_ = s.store.ApplyVMState(ctx, userID, agentID, got.State, got.Endpoint, "")
		if got.State != "running" {
			return nil
		}
		if got.Endpoint != "" {
			endpoint = got.Endpoint
		}
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
	defer cancel()
	if err := h.Reload(rctx, endpoint, token); err != nil {
		return err
	}
	_ = s.pullJournal(rctx, userID, agentID, endpoint, token)
	if err := s.store.ClearPendingBundle(ctx, userID, agentID, version); err != nil {
		return err
	}
	s.clearGuestDeferIfIdle(ctx, userID, agentID)
	return nil
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
			s.noteIfEdgeResume(ctx, userID, agent.ID, vm.MicroVMID, vm.State, got.State)
			_ = s.store.ApplyVMState(ctx, userID, agent.ID, got.State, got.Endpoint, "")
			if fresh, ferr := s.store.GetAgent(ctx, userID, agent.ID); ferr == nil {
				*agent = fresh
			}
			if fresh, ferr := s.store.LiveVM(ctx, userID, agent.ID); ferr == nil {
				vm = fresh
			}
			if vm.State == "running" {
				if pending, perr := s.store.PendingBundle(ctx, userID, agent.ID); perr == nil && pending != "" {
					s.kickReload(userID, agent.ID)
				}
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
		view.ShowFirst = true
		view.FirstRunning = vm.CreatedRunning
		switch vm.State {
		case "running":
			_, token, credErr := s.store.VMCredentials(ctx, userID, agent.ID)
			if credErr == nil && token == "" {
				view.Lead = "The machine is starting."
			} else {
				view.Lead = "The machine is running. It pauses after it is idle."
			}
			view.CanPause = h != nil
		case "paused":
			view.Lead = "The machine is paused. A chat message wakes it."
			view.CanWake = h != nil
		case "creating":
			if agent.Activity == "Waking" {
				view.Lead = "The machine is waking."
			} else {
				view.Lead = "The machine is starting."
			}
		case "failed":
			view.Lead = failedLead(*agent)
			view.CanRetry = h != nil
		default:
			view.Lead = "The machine record is saved. A check does not wake it."
		}
	}
	return view
}

// guestRestart reports whether a wanted bundle or secret reload should wait.
// The wait clock starts only when a reload is actually pending. Nothing is
// logged when the guest is already on the current bundle and secrets.
func (s *Server) guestRestart(ctx context.Context, userID, agentID, exceptRun string) (bool, error) {
	wanted, err := s.restartPending(ctx, userID, agentID)
	if err != nil {
		return false, err
	}
	if wanted {
		s.store.NoteRestartWanted(userID, agentID)
	} else {
		s.clearGuestDefer(userID, agentID)
	}
	wait, reason, err := s.store.DeferGuestRestart(ctx, userID, agentID, exceptRun)
	if err != nil {
		return false, err
	}
	if wanted && reason != "" {
		s.noteGuestDefer(ctx, userID, agentID, reason)
	}
	return wait, nil
}

// restartPending is true when a bundle is waiting to reload or the secret
// fingerprint does not match the set the guest last started with.
func (s *Server) restartPending(ctx context.Context, userID, agentID string) (bool, error) {
	version, err := s.store.PendingBundle(ctx, userID, agentID)
	if err != nil {
		return false, err
	}
	if version != "" {
		return true, nil
	}
	agent, err := s.store.GetAgent(ctx, userID, agentID)
	if err != nil {
		return false, err
	}
	fp, err := s.secretFingerprint(ctx, userID, agent)
	if err != nil {
		return false, err
	}
	stored, err := s.store.SecretFP(ctx, userID, agentID)
	if err != nil {
		return false, err
	}
	return stored != fp, nil
}

func (s *Server) clearGuestDeferIfIdle(ctx context.Context, userID, agentID string) {
	pending, err := s.restartPending(ctx, userID, agentID)
	if err == nil && !pending {
		s.clearGuestDefer(userID, agentID)
	}
}

func (s *Server) clearGuestDefer(userID, agentID string) {
	s.store.ClearRestartWanted(userID, agentID)
	s.deferMu.Lock()
	delete(s.deferLogged, agentID)
	s.deferMu.Unlock()
}

// noteGuestDefer logs a reload that is waiting, or one the time bound released.
// It logs only when a restart is actually pending, and only when that state
// changes. A forced reload is logged once until the wait is cleared.
// Fingerprints are hashes. Only a short prefix is logged, never a secret.
func (s *Server) noteGuestDefer(ctx context.Context, userID, agentID, reason string) {
	pending, err := s.restartPending(ctx, userID, agentID)
	if err != nil || !pending {
		return
	}
	event := "guest_restart_deferred"
	if strings.HasPrefix(reason, "bound:") {
		event = "guest_restart_forced"
	}
	state := event + "\x00" + reason
	s.deferMu.Lock()
	if s.deferLogged == nil {
		s.deferLogged = map[string]string{}
	}
	if s.deferLogged[agentID] == state {
		s.deferMu.Unlock()
		return
	}
	s.deferLogged[agentID] = state
	s.deferMu.Unlock()
	oldFP, _ := s.store.SecretFP(ctx, userID, agentID)
	newFP := ""
	if agent, err := s.store.GetAgent(ctx, userID, agentID); err == nil {
		newFP, _ = s.secretFingerprint(ctx, userID, agent)
	}
	s.logger().Info("vm", "event", event, "agent_id", agentID, "reason", reason, "old_fp", fpPrefix(oldFP), "new_fp", fpPrefix(newFP))
}

func fpPrefix(fp string) string {
	if len(fp) > 8 {
		return fp[:8]
	}
	return fp
}

// refreshSecrets reloads Cast when the granted secrets or the provider key
// differ from the set the guest last started with. An empty stored value
// reloads once. The fingerprint is a hash. Values are not logged.
func (s *Server) refreshSecrets(ctx context.Context, userID string, agent store.Agent, endpoint, token string) error {
	fp, err := s.secretFingerprint(ctx, userID, agent)
	if err != nil {
		return err
	}
	stored, err := s.store.SecretFP(ctx, userID, agent.ID)
	if err != nil {
		return err
	}
	if stored != "" && stored == fp {
		return nil
	}
	if vm, verr := s.store.LiveVM(ctx, userID, agent.ID); verr == nil && vm.State == "paused" {
		return nil
	}
	h, _, _ := s.hostConfig()
	if h == nil || endpoint == "" || token == "" {
		return nil
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Second)
	defer cancel()
	if err := h.Reload(rctx, endpoint, token); err != nil {
		s.logger().Info("vm", "event", "secrets_reload_failed", "agent_id", agent.ID)
		return &run.Notice{Text: "The machine could not reload."}
	}
	return s.store.SetSecretFP(ctx, userID, agent.ID, fp)
}

type secretPair struct {
	name  string
	value string
}

func (s *Server) secretPairs(ctx context.Context, userID string, agent store.Agent) ([]secretPair, error) {
	secrets, err := s.store.GrantedSecrets(ctx, userID, agent.ID)
	if err != nil {
		return nil, err
	}
	pairs := make([]secretPair, 0, len(secrets)+1)
	for _, sec := range secrets {
		pairs = append(pairs, secretPair{name: sec.Name, value: sec.Value})
	}
	if name, value, missing := s.effectiveModelSecret(ctx, userID, agent.Model); missing == "" && name != "" && value != "" {
		pairs = append(pairs, secretPair{name: name, value: value})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].name < pairs[j].name })
	return pairs, nil
}

// secretFPKey is an HMAC key derived from the session key. The session key
// itself is not the fingerprint key.
func (s *Server) secretFPKey() []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("castbot-secret-fp-v1"))
	return mac.Sum(nil)
}

func hashSecretPairs(h hash.Hash, pairs []secretPair) string {
	for _, p := range pairs {
		h.Write([]byte(p.name))
		h.Write([]byte{0})
		h.Write([]byte(p.value))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) secretFingerprint(ctx context.Context, userID string, agent store.Agent) (string, error) {
	pairs, err := s.secretPairs(ctx, userID, agent)
	if err != nil {
		return "", err
	}
	return hashSecretPairs(hmac.New(sha256.New, s.secretFPKey()), pairs), nil
}

// plainSecretFP is the pre-S07c sha256 fingerprint. Startup rewrites a stored
// value that still matches it so the switch does not reload Cast.
func (s *Server) plainSecretFP(ctx context.Context, userID string, agent store.Agent) (string, error) {
	pairs, err := s.secretPairs(ctx, userID, agent)
	if err != nil {
		return "", err
	}
	return hashSecretPairs(sha256.New(), pairs), nil
}

// RewriteLegacySecretFP replaces a stored plain sha256 with the HMAC of the
// same secret set. A value that already differs is left for the normal reload.
func (s *Server) RewriteLegacySecretFP(ctx context.Context) error {
	rows, err := s.store.ListSecretFPs(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.FP == "" {
			continue
		}
		agent, err := s.store.GetAgent(ctx, row.UserID, row.AgentID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return err
		}
		plain, err := s.plainSecretFP(ctx, row.UserID, agent)
		if err != nil {
			return err
		}
		if row.FP != plain {
			continue
		}
		next, err := s.secretFingerprint(ctx, row.UserID, agent)
		if err != nil {
			return err
		}
		if next == row.FP {
			continue
		}
		if err := s.store.SetSecretFP(ctx, row.UserID, row.AgentID, next); err != nil {
			return err
		}
	}
	return nil
}
