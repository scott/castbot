package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// UsageSpan is machine time that overlaps a window.
// Kind is compute while running and disk while paused.
// Seconds is the overlap only. An open interval is measured to the read clock.
type UsageSpan struct {
	AgentID string
	Size    string
	Kind    string
	Seconds int64
}

// migrateV24 records machine time. It does not bill a provider.
// It is safe to run more than once.
func (s *Store) migrateV24() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS vm_usage (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  agent_vm_id TEXT NOT NULL,
  size TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('compute', 'disk')),
  started_at TEXT NOT NULL,
  ended_at TEXT NOT NULL DEFAULT ''
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_vm_usage_owner ON vm_usage(user_id, agent_id, started_at)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vm_usage_open ON vm_usage(agent_vm_id) WHERE ended_at = ''`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (24) ON CONFLICT(version) DO NOTHING`)
	return err
}

func usageKind(state string) string {
	switch state {
	case "running":
		return "compute"
	case "paused":
		return "disk"
	default:
		return ""
	}
}

func usageSize(size string) string {
	size = strings.TrimSpace(size)
	if size == "" {
		return "1:2048"
	}
	return size
}

// noteUsageTx opens or closes the interval for one machine row.
// An empty kind only closes. The same open kind and size is a no-op,
// so a repeated running write does not move started_at.
func (s *Store) noteUsageTx(ctx context.Context, tx *sql.Tx, workspaceID, userID, agentID, agentVMID, size, state string) error {
	if strings.TrimSpace(agentVMID) == "" || userID == "" || workspaceID == "" {
		return nil
	}
	kind := usageKind(state)
	size = usageSize(size)
	stamp := s.stamp(s.Now())
	var id int64
	var openKind, openSize string
	err := tx.QueryRowContext(ctx, `SELECT id, kind, size FROM vm_usage
WHERE user_id = ? AND workspace_id = ? AND agent_vm_id = ? AND ended_at = ''`,
		userID, workspaceID, agentVMID).Scan(&id, &openKind, &openSize)
	if err == sql.ErrNoRows {
		if kind == "" {
			return nil
		}
		return insertUsage(ctx, tx, workspaceID, userID, agentID, agentVMID, size, kind, stamp)
	}
	if err != nil {
		return err
	}
	if kind == openKind && size == openSize {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vm_usage SET ended_at = ? WHERE id = ? AND user_id = ? AND ended_at = ''`, stamp, id, userID); err != nil {
		return err
	}
	if kind == "" {
		return nil
	}
	return insertUsage(ctx, tx, workspaceID, userID, agentID, agentVMID, size, kind, stamp)
}

func insertUsage(ctx context.Context, tx *sql.Tx, workspaceID, userID, agentID, agentVMID, size, kind, stamp string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO vm_usage(workspace_id, user_id, agent_id, agent_vm_id, size, kind, started_at, ended_at)
VALUES (?, ?, ?, ?, ?, ?, ?, '')`, workspaceID, userID, agentID, agentVMID, size, kind, stamp)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return nil
	}
	return err
}

func closeUsageVMTx(ctx context.Context, tx *sql.Tx, userID, agentVMID, stamp string) error {
	if agentVMID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE vm_usage SET ended_at = ? WHERE user_id = ? AND agent_vm_id = ? AND ended_at = ''`, stamp, userID, agentVMID)
	return err
}

func closeUsageAgentTx(ctx context.Context, tx *sql.Tx, workspaceID, userID, agentID, stamp string) error {
	_, err := tx.ExecContext(ctx, `UPDATE vm_usage SET ended_at = ? WHERE user_id = ? AND workspace_id = ? AND agent_id = ? AND ended_at = ''`,
		stamp, userID, workspaceID, agentID)
	return err
}

// EnsureUsage opens an interval when a live running or paused machine has none.
// It does not invent time before this call, and it does not call a guest.
func (s *Store) EnsureUsage(ctx context.Context, userID, agentID string) error {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	vm, err := s.LiveVM(ctx, userID, agentID)
	if err != nil {
		if err == ErrNotFound {
			return nil
		}
		return err
	}
	kind := usageKind(vm.State)
	if kind == "" {
		return nil
	}
	size := usageSize(agent.Size)
	var openKind, openSize string
	err = s.db.QueryRowContext(ctx, `SELECT kind, size FROM vm_usage
WHERE user_id = ? AND workspace_id = ? AND agent_vm_id = ? AND ended_at = ''`,
		userID, agent.WorkspaceID, vm.ID).Scan(&openKind, &openSize)
	if err == nil && openKind == kind && openSize == size {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.noteUsageTx(ctx, tx, agent.WorkspaceID, userID, agentID, vm.ID, agent.Size, vm.State); err != nil {
		return err
	}
	return tx.Commit()
}

// UsageIn returns intervals for the caller that overlap [from, to).
// A non-empty agentID is checked with GetAgent, so another user gets ErrNotFound.
// An empty agentID is the whole account. Every row is filtered by user and workspace.
func (s *Store) UsageIn(ctx context.Context, userID, agentID string, from, to time.Time) ([]UsageSpan, error) {
	if !to.After(from) {
		return nil, nil
	}
	var workspace string
	if agentID != "" {
		agent, err := s.GetAgent(ctx, userID, agentID)
		if err != nil {
			return nil, err
		}
		workspace = agent.WorkspaceID
	} else {
		user, err := s.GetUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		workspace = user.WorkspaceID
	}
	q := `SELECT agent_id, size, kind, started_at, ended_at FROM vm_usage
WHERE user_id = ? AND workspace_id = ? AND started_at < ? AND (ended_at = '' OR ended_at > ?)`
	args := []any{userID, workspace, s.stamp(to), s.stamp(from)}
	if agentID != "" {
		q += ` AND agent_id = ?`
		args = append(args, agentID)
	}
	q += ` ORDER BY started_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.Now()
	var out []UsageSpan
	for rows.Next() {
		var span UsageSpan
		var started, ended string
		if err := rows.Scan(&span.AgentID, &span.Size, &span.Kind, &started, &ended); err != nil {
			return nil, err
		}
		start, err := parseTime(started)
		if err != nil {
			return nil, err
		}
		end := now
		if ended != "" {
			end, err = parseTime(ended)
			if err != nil {
				return nil, err
			}
		}
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			continue
		}
		span.Seconds = int64(end.Sub(start) / time.Second)
		if span.Seconds <= 0 {
			continue
		}
		out = append(out, span)
	}
	return out, rows.Err()
}
