package web

import (
	"context"
	"sync"
	"time"
)

// reloadGate is the single in-flight reload for one agent.
// Page loads start it in the background. A save waits for it, then applies
// a newer pending version if one landed during the flight.
type reloadGate struct {
	mu      sync.Mutex
	running bool
	done    chan struct{}
	next    time.Time
	fails   int
}

func (s *Server) gate(agentID string) *reloadGate {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if s.reloads == nil {
		s.reloads = map[string]*reloadGate{}
	}
	g, ok := s.reloads[agentID]
	if !ok {
		g = &reloadGate{}
		s.reloads[agentID] = g
	}
	return g
}

// kickReload starts a pending reload without waiting. A failure backs off
// so the next page view does not call the edge again immediately.
func (s *Server) kickReload(userID, agentID string) {
	g := s.gate(agentID)
	g.mu.Lock()
	if g.running || time.Now().Before(g.next) {
		g.mu.Unlock()
		return
	}
	g.running = true
	done := make(chan struct{})
	g.done = done
	g.mu.Unlock()
	parent := s.base
	if parent == nil {
		parent = context.Background()
	}
	s.reloadWG.Add(1)
	go func() {
		defer s.reloadWG.Done()
		ctx, cancel := context.WithTimeout(parent, 35*time.Second)
		defer cancel()
		err := s.applyPending(ctx, userID, agentID)
		s.finishReload(g, done, err)
	}()
}

// runReload applies the pending bundle on the caller's goroutine.
// It waits for an in-flight reload, then runs once so a newer version is not lost.
func (s *Server) runReload(ctx context.Context, userID, agentID string) error {
	return s.runReloadExcept(ctx, userID, agentID, "")
}

func (s *Server) runReloadExcept(ctx context.Context, userID, agentID, exceptRun string) error {
	g := s.gate(agentID)
	g.mu.Lock()
	for g.running {
		done := g.done
		g.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
		g.mu.Lock()
	}
	g.running = true
	done := make(chan struct{})
	g.done = done
	g.mu.Unlock()
	err := s.applyPendingExcept(ctx, userID, agentID, exceptRun)
	s.finishReload(g, done, err)
	return err
}

func (s *Server) finishReload(g *reloadGate, done chan struct{}, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.fails++
		shift := g.fails
		if shift > 5 {
			shift = 5
		}
		g.next = time.Now().Add(time.Second << shift)
	} else {
		g.fails = 0
		g.next = time.Time{}
	}
	g.running = false
	close(done)
}
