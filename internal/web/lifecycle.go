package web

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

const reconcileMax = 15 * time.Minute

func (s *Server) logger() *slog.Logger {
	s.mu.Lock()
	l := s.log
	s.mu.Unlock()
	if l != nil {
		return l
	}
	return slog.Default()
}

// SetLogger records machine timings. Tests pass a handler they can read.
// The logger must not receive tokens or message bodies.
func (s *Server) SetLogger(l *slog.Logger) {
	s.mu.Lock()
	s.log = l
	s.mu.Unlock()
}

func (s *Server) noteGuestReady(ctx context.Context, userID, agentID, microID string, d time.Duration) {
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if err != nil {
		return
	}
	events, err := s.store.ListVMEvents(ctx, userID, agentID)
	if err != nil {
		return
	}
	for _, ev := range events {
		if ev.Kind == store.EventGuestReady && ev.AgentVMID == vm.ID {
			return
		}
	}
	s.noteVM(ctx, userID, agentID, microID, store.EventGuestReady, d)
}

// noteIfEdgeResume records a running observation that this process did not wake.
// A row that is still creating is the normal boot, not a resume.
func (s *Server) noteIfEdgeResume(ctx context.Context, userID, agentID, microID, prev, next string) {
	if next == "running" && prev != "running" && prev != "creating" && prev != "" {
		s.noteVM(ctx, userID, agentID, microID, store.EventEdgeResume, 0)
	}
}

// kickProbe asks the guest for castlet health after a token is saved.
// The bootstrap response is not held open for it.
func (s *Server) kickProbe(userID, agentID string) {
	parent := s.base
	if parent == nil {
		parent = context.Background()
	}
	go func() {
		ctx, cancel := context.WithTimeout(parent, 90*time.Second)
		defer cancel()
		vm, err := s.store.LiveVM(ctx, userID, agentID)
		if err != nil || vm.State != "running" || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
			return
		}
		endpoint, token, err := s.store.VMCredentials(ctx, userID, agentID)
		if err != nil || endpoint == "" || token == "" {
			return
		}
		h, _, _ := s.hostConfig()
		if h == nil {
			return
		}
		if err := h.Probe(ctx, endpoint, token); err != nil {
			return
		}
		d := time.Duration(0)
		if t, err := time.Parse(time.RFC3339, vm.CreatedRunning); err == nil && !t.IsZero() {
			d = time.Since(t)
		}
		s.noteGuestReady(ctx, userID, agentID, vm.MicroVMID, d)
	}()
}

func (s *Server) noteVM(ctx context.Context, userID, agentID, microID, kind string, d time.Duration) {
	if d < 0 {
		d = 0
	}
	rowID := ""
	if vm, err := s.store.LiveVM(ctx, userID, agentID); err == nil {
		rowID = vm.ID
		if microID == "" {
			microID = vm.MicroVMID
		}
		_ = s.store.AddVMEvent(ctx, userID, agentID, kind, d)
	}
	s.logger().Info("vm",
		"event", kind,
		"agent_id", agentID,
		"vm_id", rowID,
		"microvm_id", microID,
		"duration_ms", d.Milliseconds(),
	)
}

// RunReconciler polls the machine list for rows this database still calls
// running. It never wakes a guest and it never calls a guest. Errors back
// off and are logged once per error kind.
func (s *Server) RunReconciler(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = time.Minute
	}
	s.mu.Lock()
	s.recEvery = every
	s.mu.Unlock()
	go s.reconcileLoop(ctx, every)
}

func (s *Server) reconcileLoop(ctx context.Context, every time.Duration) {
	delay := every
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		err := s.reconcileOnce(ctx)
		if err != nil {
			delay = s.noteReconcileErr(every, err)
			continue
		}
		s.clearReconcileErr()
		delay = every
	}
}

func (s *Server) noteReconcileErr(every time.Duration, err error) time.Duration {
	kind := reconcileKind(err)
	s.recMu.Lock()
	defer s.recMu.Unlock()
	if kind != s.recKind {
		s.logger().Warn("machine reconcile", "err_kind", kind)
		s.recKind = kind
	}
	if s.recDelay < every {
		s.recDelay = every
	}
	s.recDelay *= 2
	if s.recDelay > reconcileMax {
		s.recDelay = reconcileMax
	}
	return s.recDelay
}

func (s *Server) clearReconcileErr() {
	s.recMu.Lock()
	s.recKind = ""
	s.recDelay = 0
	s.recMu.Unlock()
}

func reconcileKind(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const mark = "status "
	if i := strings.Index(msg, mark); i >= 0 {
		rest := msg[i+len(mark):]
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j > 0 {
			return "http_" + rest[:j]
		}
	}
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") || strings.Contains(msg, "network is unreachable") {
		return "network"
	}
	return "other"
}

// reconcileOnce copies a paused list result onto rows that still say running.
// One list call covers every row. It does not call Status, Wake, or the guest.
func (s *Server) reconcileOnce(ctx context.Context) error {
	s.mu.Lock()
	h := s.machine
	s.mu.Unlock()
	lister, ok := h.(interface {
		List(context.Context) ([]host.Machine, error)
	})
	if !ok || h == nil {
		return nil
	}
	rows, err := s.store.RunningVMs(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	listed, err := lister.List(ctx)
	if err != nil {
		return err
	}
	byID := map[string]host.Machine{}
	for _, vm := range listed {
		if vm.ID != "" {
			byID[vm.ID] = vm
		}
	}
	for _, row := range rows {
		got, ok := byID[row.MicroVMID]
		if !ok || got.State != "paused" {
			continue
		}
		changed, err := s.store.MarkPausedIfUnchanged(ctx, row.UserID, row.AgentID, row.MicroVMID, row.StateGen, got.Endpoint)
		if err != nil {
			return err
		}
		if changed {
			s.noteVM(ctx, row.UserID, row.AgentID, row.MicroVMID, store.EventPauseObserved, 0)
		}
	}
	for _, row := range rows {
		s.maybeGreet(ctx, row.UserID, row.AgentID)
	}
	return nil
}

// reconcileDelayForTest reports the current backoff. Tests use it.
func (s *Server) reconcileDelayForTest() time.Duration {
	s.recMu.Lock()
	defer s.recMu.Unlock()
	return s.recDelay
}
