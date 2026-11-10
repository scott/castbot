package store

import (
	"context"
	"time"
)

// VM event kinds. Durations are milliseconds from the related request.
const (
	EventCreateRequested = "create_requested"
	EventCreateAccepted  = "create_accepted"
	EventRunning         = "running"
	EventGuestReady      = "guest_ready"
	EventPauseRequested  = "pause_requested"
	EventPauseObserved   = "pause_observed"
	EventWakeRequested   = "wake_requested"
	EventWakeRunning     = "wake_running"
	EventWakeReady       = "wake_ready"
	EventDelete          = "delete"
	EventEdgeResume      = "edge_resume"
)

// VMEvent is one durable machine timing row.
type VMEvent struct {
	ID         int64
	AgentVMID  string
	UserID     string
	AgentID    string
	Kind       string
	At         string
	DurationMS int64
}

// AddVMEvent stores one timing for the caller's live machine.
// The kind and the duration are not a message body or a token.
func (s *Store) AddVMEvent(ctx context.Context, userID, agentID, kind string, duration time.Duration) error {
	vm, err := s.LiveVM(ctx, userID, agentID)
	if err != nil {
		return err
	}
	if duration < 0 {
		duration = 0
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO vm_events(agent_vm_id, workspace_id, user_id, agent_id, kind, at, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		vm.ID, vm.Workspace, vm.UserID, vm.AgentID, kind, s.stamp(s.Now()), duration.Milliseconds())
	return err
}

// ListVMEvents returns timings for one of the caller's agents, oldest first.
// Rows stay after the machine is deleted.
func (s *Store) ListVMEvents(ctx context.Context, userID, agentID string) ([]VMEvent, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND user_id = ?`, agentID, userID).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, agent_vm_id, user_id, agent_id, kind, at, duration_ms
FROM vm_events WHERE user_id = ? AND agent_id = ? ORDER BY id`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VMEvent
	for rows.Next() {
		var ev VMEvent
		if err := rows.Scan(&ev.ID, &ev.AgentVMID, &ev.UserID, &ev.AgentID, &ev.Kind, &ev.At, &ev.DurationMS); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// RunningVMs returns machines this database still calls running.
// The reconciler uses it. It does not return tokens.
func (s *Store) RunningVMs(ctx context.Context) ([]VM, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, agent_id, microvm_id, name, endpoint, image_ref, state, failure, COALESCE(last_wake_at, ''), created_at, COALESCE(created_running_at, '')
FROM agent_vms WHERE deleted_at IS NULL AND state = 'running' AND microvm_id != '' AND microvm_id != 'pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VM
	for rows.Next() {
		var vm VM
		if err := rows.Scan(&vm.ID, &vm.Workspace, &vm.UserID, &vm.AgentID, &vm.MicroVMID, &vm.Name, &vm.Endpoint, &vm.Image, &vm.State, &vm.Failure, &vm.LastWake, &vm.CreatedAt, &vm.CreatedRunning); err != nil {
			return nil, err
		}
		out = append(out, vm)
	}
	return out, rows.Err()
}
