package web

import (
	"context"
	"net/http"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

// statusJob is one machine whose saved state should be checked off the request path.
type statusJob struct {
	userID  string
	agentID string
}

// RunStatusRefresh checks queued machines on a timer.
// It calls Status only. It does not wake a guest and it does not call a guest.
// Page renders enqueue work here so they can paint from the database first.
func (s *Server) RunStatusRefresh(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 2 * time.Second
	}
	go s.statusRefreshLoop(ctx, every)
}

func (s *Server) statusRefreshLoop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.drainStatus(ctx)
		}
	}
}

func (s *Server) noteStatusRefresh(userID, agentID string) {
	if userID == "" || agentID == "" {
		return
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if s.statusWant == nil {
		s.statusWant = map[string]statusJob{}
	}
	s.statusWant[agentID] = statusJob{userID: userID, agentID: agentID}
}

func (s *Server) drainStatus(ctx context.Context) {
	s.statusMu.Lock()
	jobs := s.statusWant
	s.statusWant = nil
	s.statusMu.Unlock()
	for _, job := range jobs {
		_ = s.refreshMachineStatus(ctx, job.userID, job.agentID, false)
	}
}

// refreshMachineStatus reads Status and copies it onto the saved row.
// bypass drops a warm cache first so an explicit refresh talks to the API.
// It does not call Wake, Pause, or the guest.
func (s *Server) refreshMachineStatus(ctx context.Context, userID, agentID string, bypass bool) error {
	h, _, _ := s.hostConfig()
	if h == nil {
		return nil
	}
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if err != nil {
		return err
	}
	if vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return nil
	}
	if bypass {
		if drop, ok := h.(interface{ ForgetStatus(string) }); ok {
			drop.ForgetStatus(vm.MicroVMID)
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	got, err := h.Status(cctx, vm.MicroVMID)
	cancel()
	if err != nil {
		return err
	}
	if got.State == "" {
		return nil
	}
	agent, err := s.store.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	if got.State != vm.State || (got.Endpoint != "" && got.Endpoint != vm.Endpoint) {
		vm = s.observeMachine(ctx, userID, &agent, vm, got)
	}
	if vm.State == "running" {
		s.kickIfPending(ctx, userID, agentID)
	}
	return nil
}

func (s *Server) observeMachine(ctx context.Context, userID string, agent *store.Agent, vm store.VM, got host.Machine) store.VM {
	s.noteIfEdgeResume(ctx, userID, agent.ID, vm.MicroVMID, vm.State, got.State)
	if err := s.store.ApplyVMState(ctx, userID, agent.ID, got.State, got.Endpoint, ""); err != nil {
		return vm
	}
	if fresh, err := s.store.GetAgent(ctx, userID, agent.ID); err == nil {
		*agent = fresh
	}
	if fresh, err := s.store.LiveVM(ctx, userID, agent.ID); err == nil {
		return fresh
	}
	return vm
}

func (s *Server) kickIfPending(ctx context.Context, userID, agentID string) {
	pending, err := s.store.PendingBundle(ctx, userID, agentID)
	if err != nil || pending == "" {
		return
	}
	s.kickReload(userID, agentID)
}

func cachedMachine(h host.AgentHost, id string) (host.Machine, bool) {
	if h == nil || id == "" {
		return host.Machine{}, false
	}
	c, ok := h.(interface {
		CachedStatus(string) (host.Machine, bool)
	})
	if !ok {
		return host.Machine{}, false
	}
	got, ok := c.CachedStatus(id)
	if !ok || got.State == "" {
		return host.Machine{}, false
	}
	return got, true
}

// refreshMachinePanel is the machine panel's explicit status check.
// The chat and panel renders do not call it. It does not wake the guest.
func (s *Server) refreshMachinePanel(w http.ResponseWriter, r *http.Request) {
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
	msg := ""
	code := http.StatusOK
	if err := s.refreshMachineStatus(r.Context(), user.ID, agent.ID, true); err != nil {
		msg = "The machine could not be checked. It was not woken."
		code = http.StatusBadGateway
	}
	if r.Header.Get("HX-Request") == "true" {
		s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusOK, msg)
		return
	}
	if msg != "" {
		s.renderAgent(w, r, user, agent.ID, false, "machine", code, msg)
		return
	}
	s.redirectMachine(w, r, agent.ID)
}

// machineSync returns chrome for the saved machine state.
// It does not call the machine API. An unchanged chip is an empty response
// so the page does not swap nodes that are already right.
func (s *Server) machineSync(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if s.replacing(r.Context(), user.ID, agent.ID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	vm, err := s.store.LiveVM(r.Context(), user.ID, agent.ID)
	if err != nil || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	machine := s.machineFromStore(r.Context(), user.ID, &agent, false)
	view := s.paintAgent(r.Context(), user.ID, agent, true, userLocation(user.Timezone))
	if chromeSame(r, view) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	p := page{Agent: view, Machine: machine}
	s.render(w, http.StatusOK, "machineSync", p)
}

func chromeSame(r *http.Request, v agentView) bool {
	q := r.URL.Query()
	if _, ok := q["dot"]; !ok {
		return false
	}
	return q.Get("dot") == v.Dot &&
		q.Get("chip") == v.ChipClass &&
		q.Get("label") == v.ChipLabel &&
		q.Get("sub") == v.Sub &&
		q.Get("subc") == v.SubClass
}
