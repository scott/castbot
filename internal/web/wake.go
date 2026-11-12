package web

import (
	"context"
	"errors"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

// wakeFacts is what a routine run shows about the wake. A chat reply
// keeps the old notice and does not read these.
type wakeFacts struct {
	WasPaused bool
	PausedFor time.Duration
	WakeFor   time.Duration
	State     string
	Endpoint  string
}

// wakeForWork wakes a paused machine before a reply.
// strict is set for a routine run: Wake and Probe use the 90s budget,
// and a failure fails the run before the prompt is sent.
// A chat reply keeps the run context (up to the run limit) and returns the paused notice.
func (s *Server) wakeForWork(ctx context.Context, h host.AgentHost, userID, agentID string, vm store.VM, endpoint, token, state string, strict bool) (wakeFacts, error) {
	facts := wakeFacts{State: state, Endpoint: endpoint}
	if vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return facts, nil
	}
	if state == "running" && strict {
		return s.probeReady(ctx, h, userID, agentID, vm, facts, token, time.Now())
	}
	if state != "paused" {
		return facts, nil
	}
	facts.WasPaused = true
	facts.PausedFor = s.pausedFor(ctx, userID, agentID)
	s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeRequested, 0)
	wakeAt := time.Now()
	wakeCtx := ctx
	if strict {
		var cancel context.CancelFunc
		wakeCtx, cancel = context.WithTimeout(ctx, s.wakeBudget())
		defer cancel()
	}
	got, werr := h.Wake(wakeCtx, vm.MicroVMID)
	facts.WakeFor = time.Since(wakeAt)
	if werr != nil {
		code := store.WakeProvider
		if errors.Is(werr, context.DeadlineExceeded) || errors.Is(wakeCtx.Err(), context.DeadlineExceeded) {
			code = store.WakeTimeout
		}
		s.recordWakeFailed(ctx, userID, agentID, code, facts.WakeFor)
		if strict {
			return facts, &run.Notice{Text: "The machine did not wake."}
		}
		return facts, &run.Notice{Text: "The machine is paused."}
	}
	if err := s.store.ApplyVMState(ctx, userID, agentID, got.State, got.Endpoint, ""); err != nil {
		return facts, err
	}
	if got.State == "running" {
		s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeRunning, facts.WakeFor)
	}
	facts.State = got.State
	if got.Endpoint != "" {
		facts.Endpoint = got.Endpoint
	}
	if got.State != "running" {
		s.recordWakeFailed(ctx, userID, agentID, store.WakeTimeout, facts.WakeFor)
		if strict {
			return facts, &run.Notice{Text: "The machine did not wake."}
		}
		return facts, nil
	}
	if !strict {
		if token != "" && facts.Endpoint != "" {
			if perr := h.Probe(ctx, facts.Endpoint, token); perr == nil {
				s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeReady, time.Since(wakeAt))
			}
		}
		return facts, nil
	}
	return s.probeReady(ctx, h, userID, agentID, vm, facts, token, wakeAt)
}

func (s *Server) probeReady(ctx context.Context, h host.AgentHost, userID, agentID string, vm store.VM, facts wakeFacts, token string, wakeAt time.Time) (wakeFacts, error) {
	if token == "" || facts.Endpoint == "" {
		s.recordWakeFailed(ctx, userID, agentID, store.WakeHealth, time.Since(wakeAt))
		return facts, &run.Notice{Text: "The machine did not wake."}
	}
	pctx, cancel := context.WithTimeout(ctx, s.wakeBudget())
	defer cancel()
	err := h.Probe(pctx, facts.Endpoint, token)
	if err == nil {
		s.noteVM(ctx, userID, agentID, vm.MicroVMID, store.EventWakeReady, time.Since(wakeAt))
		return facts, nil
	}
	code := store.WakeHealth
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(pctx.Err(), context.DeadlineExceeded) {
		code = store.WakeTimeout
	}
	s.recordWakeFailed(ctx, userID, agentID, code, time.Since(wakeAt))
	return facts, &run.Notice{Text: "The machine did not wake."}
}

func (s *Server) recordWakeFailed(ctx context.Context, userID, agentID, code string, d time.Duration) {
	_ = s.store.AddVMEventCode(ctx, userID, agentID, store.EventWakeFailed, code, d)
}

func (s *Server) pausedFor(ctx context.Context, userID, agentID string) time.Duration {
	events, err := s.store.ListVMEvents(ctx, userID, agentID)
	if err != nil {
		return 0
	}
	var latest time.Time
	for _, ev := range events {
		if ev.Kind != store.EventPauseObserved && ev.Kind != store.EventPauseRequested {
			continue
		}
		t, err := time.Parse(time.RFC3339, ev.At)
		if err != nil {
			continue
		}
		if t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return 0
	}
	d := s.store.Now().Sub(latest)
	if d < 0 {
		return 0
	}
	return d
}

func (s *Server) latestWakeCode(ctx context.Context, userID, agentID string) string {
	events, err := s.store.ListVMEvents(ctx, userID, agentID)
	if err != nil {
		return ""
	}
	code := ""
	for _, ev := range events {
		if ev.Kind == store.EventWakeFailed && ev.Code != "" {
			code = ev.Code
		}
	}
	return code
}
