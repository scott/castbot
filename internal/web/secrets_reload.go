package web

import (
	"context"
	"time"

	"github.com/scott/castbot/internal/run"
)

// vaultNote is the last reload result for one agent. It holds no secret value.
type vaultNote struct {
	SecretID string
	Status   string
	At       time.Time
}

func (s *Server) noteReload(userID, agentID, secretID, status string) {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	if s.vaultReload == nil {
		s.vaultReload = map[string]vaultNote{}
	}
	key := userID + "\x00" + agentID
	prev := s.vaultReload[key]
	if secretID == "" {
		secretID = prev.SecretID
	}
	s.vaultReload[key] = vaultNote{SecretID: secretID, Status: status, At: time.Now()}
}

func (s *Server) reloadNote(userID, agentID string) (vaultNote, bool) {
	s.vaultMu.Lock()
	defer s.vaultMu.Unlock()
	note, ok := s.vaultReload[userID+"\x00"+agentID]
	return note, ok
}

// reloadBlocked reports a machine replace in progress for any of these agents.
func (s *Server) reloadBlocked(ctx context.Context, userID string, agentIDs []string) bool {
	for _, id := range agentIDs {
		if s.replacing(ctx, userID, id) {
			return true
		}
	}
	return false
}

// reloadOne reloads one running agent, or records why it did not.
// A paused agent is not woken. An agent that is replying or waiting on an
// approval is left until that finishes.
func (s *Server) reloadOne(ctx context.Context, userID, secretID, agentID string) string {
	agent, err := s.store.GetAgent(ctx, userID, agentID)
	if err != nil {
		s.noteReload(userID, agentID, secretID, "failed")
		return "failed"
	}
	vm, verr := s.store.LiveVM(ctx, userID, agentID)
	if agent.Status == "paused" || (verr == nil && vm.State == "paused") {
		s.noteReload(userID, agentID, secretID, "wake")
		return "wake"
	}
	wait, err := s.guestRestart(ctx, userID, agentID, "")
	if err != nil {
		s.noteReload(userID, agentID, secretID, "failed")
		return "failed"
	}
	if wait {
		s.noteReload(userID, agentID, secretID, "deferred")
		return "deferred"
	}
	h, _, _ := s.hostConfig()
	endpoint, token, credErr := s.store.VMCredentials(ctx, userID, agentID)
	if h == nil || credErr != nil || endpoint == "" || token == "" || verr != nil || vm.State != "running" {
		s.noteReload(userID, agentID, secretID, "idle")
		return "idle"
	}
	if err := s.refreshSecrets(ctx, userID, agent, endpoint, token); err != nil {
		s.logger().Info("vm", "event", "secrets_reload_failed", "agent_id", agentID)
		s.noteReload(userID, agentID, secretID, "failed")
		return "failed"
	}
	s.noteReload(userID, agentID, secretID, "reloaded")
	return "reloaded"
}

func (s *Server) reloadAgents(ctx context.Context, userID, secretID string, agentIDs []string) {
	for _, id := range agentIDs {
		s.reloadOne(ctx, userID, secretID, id)
	}
}

// afterSecretRun reloads when a reply has finished and a new secret set is waiting.
// It does not start a new run and it does not wake a paused machine.
func (s *Server) afterSecretRun(job run.Job) {
	if job.UserID == "" || job.AgentID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pending, err := s.restartPending(ctx, job.UserID, job.AgentID)
	if err != nil || !pending {
		return
	}
	s.reloadOne(ctx, job.UserID, "", job.AgentID)
}
