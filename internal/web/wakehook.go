package web

import "context"

// SetAfterRun registers a callback for a successful wake or a finished run
// that left the machine running. Callbacks run in registration order.
// A later slice can register its own hook without replacing this one.
func (s *Server) SetAfterRun(fn func(context.Context, string, string)) {
	if fn == nil {
		return
	}
	s.fileMu.Lock()
	s.fileHooks = append(s.fileHooks, fn)
	s.fileMu.Unlock()
}

func (s *Server) ensureFileHooks() {
	s.fileHookOn.Do(func() {
		s.SetAfterRun(s.copyHeldUploads)
	})
}

// fireAfterRun runs the wake hooks. Held uploads copy here, onto the machine
// that is current after a replace finishes.
func (s *Server) fireAfterRun(ctx context.Context, userID, agentID string) {
	s.ensureFileHooks()
	s.fileMu.Lock()
	hooks := append([]func(context.Context, string, string){}, s.fileHooks...)
	s.fileMu.Unlock()
	for _, fn := range hooks {
		fn(ctx, userID, agentID)
	}
}
