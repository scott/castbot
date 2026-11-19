package store

import (
	"context"
	"strings"
	"time"
)

// SecretDelete is an approval to remove one named API key.
// It never stores the secret value.
type SecretDelete struct {
	ID          string
	WorkspaceID string
	UserID      string
	AgentID     string
	ThreadID    string
	RunID       string
	Name        string
	Reason      string
	Status      string
	ExpiresAt   string
	CreatedAt   string
	ResolvedAt  string
}

func (s *Store) migrateV33() error {
	if err := s.ensureColumn("secret_requests", "label", `ALTER TABLE secret_requests ADD COLUMN label TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS secret_deletes (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('pending', 'deleted', 'declined', 'expired')),
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  resolved_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_secret_del_pending ON secret_deletes(user_id, agent_id, name) WHERE status = 'pending'`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_secret_del_user ON secret_deletes(user_id, agent_id, created_at)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (33) ON CONFLICT(version) DO NOTHING`)
	return err
}

// CreateNamedSecretRequest opens a card for one environment secret, or
// returns the pending card for that name. The value is not stored here.
func (s *Store) CreateNamedSecretRequest(ctx context.Context, userID, agentID, threadID, runID, name, label, reason string, expires time.Time) (SecretRequest, bool, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if !SecretNameOK(name) {
		return SecretRequest{}, false, ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return SecretRequest{}, false, err
	}
	if pending, err := s.pendingSecret(ctx, userID, agentID, name); err == nil {
		if s.secretDue(pending) {
			if _, err := s.finishSecret(ctx, userID, pending.ID, "expired"); err != nil {
				return SecretRequest{}, false, err
			}
		} else {
			return pending, false, nil
		}
	} else if err != ErrNotFound {
		return SecretRequest{}, false, err
	}
	n, err := s.CountSecretRequests(ctx, userID, agentID, name, s.Now().Add(-time.Hour))
	if err != nil {
		return SecretRequest{}, false, err
	}
	if n >= SecretRequestLimit {
		_ = s.RecordConnectAudit(ctx, userID, "secret_request.limited", agentID, map[string]string{"connection": name})
		return SecretRequest{}, false, ErrConflict
	}
	id, err := newID()
	if err != nil {
		return SecretRequest{}, false, err
	}
	stamp := s.stamp(s.Now())
	row := SecretRequest{
		ID: id, WorkspaceID: agent.WorkspaceID, UserID: userID, AgentID: agentID,
		ThreadID: threadID, RunID: runID, Connection: name, Reason: reason, Label: label,
		Status: "pending", ExpiresAt: s.stamp(expires.UTC()), CreatedAt: stamp,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretRequest{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO secret_requests(id, workspace_id, user_id, agent_id, thread_id, run_id, connection, reason, label, status, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		row.ID, row.WorkspaceID, userID, agentID, threadID, runID, name, reason, label, row.ExpiresAt, stamp); err != nil {
		_ = tx.Rollback()
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			if pending, err2 := s.pendingSecret(ctx, userID, agentID, name); err2 == nil {
				return pending, false, nil
			}
		}
		return SecretRequest{}, false, err
	}
	if err := s.insertAudit(ctx, tx, agent.WorkspaceID, userID, "agent", agentID, "secret_request.created", "agent:"+agentID, auditConnect(name, "", "", "created")); err != nil {
		return SecretRequest{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return SecretRequest{}, false, err
	}
	return row, true, nil
}

// CreateSecretDelete opens one delete approval, or returns the pending one.
func (s *Store) CreateSecretDelete(ctx context.Context, userID, agentID, threadID, runID, name, reason string, expires time.Time) (SecretDelete, bool, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if !SecretNameOK(name) {
		return SecretDelete{}, false, ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return SecretDelete{}, false, err
	}
	if pending, err := s.pendingSecretDelete(ctx, userID, agentID, name); err == nil {
		if s.secretDeleteDue(pending) {
			if _, err := s.finishSecretDelete(ctx, userID, pending.ID, "expired"); err != nil {
				return SecretDelete{}, false, err
			}
		} else {
			return pending, false, nil
		}
	} else if err != ErrNotFound {
		return SecretDelete{}, false, err
	}
	n, err := s.CountSecretDeletes(ctx, userID, agentID, name, s.Now().Add(-time.Hour))
	if err != nil {
		return SecretDelete{}, false, err
	}
	if n >= SecretRequestLimit {
		_ = s.RecordConnectAudit(ctx, userID, "secret_delete.limited", agentID, map[string]string{"name": name})
		return SecretDelete{}, false, ErrConflict
	}
	id, err := newID()
	if err != nil {
		return SecretDelete{}, false, err
	}
	stamp := s.stamp(s.Now())
	row := SecretDelete{
		ID: id, WorkspaceID: agent.WorkspaceID, UserID: userID, AgentID: agentID,
		ThreadID: threadID, RunID: runID, Name: name, Reason: reason,
		Status: "pending", ExpiresAt: s.stamp(expires.UTC()), CreatedAt: stamp,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretDelete{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO secret_deletes(id, workspace_id, user_id, agent_id, thread_id, run_id, name, reason, status, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		row.ID, row.WorkspaceID, userID, agentID, threadID, runID, name, reason, row.ExpiresAt, stamp); err != nil {
		_ = tx.Rollback()
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			if pending, err2 := s.pendingSecretDelete(ctx, userID, agentID, name); err2 == nil {
				return pending, false, nil
			}
		}
		return SecretDelete{}, false, err
	}
	if err := s.insertAudit(ctx, tx, agent.WorkspaceID, userID, "agent", agentID, "secret_delete.created", "agent:"+agentID, auditConnect(name, "", "", "created")); err != nil {
		return SecretDelete{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return SecretDelete{}, false, err
	}
	return row, true, nil
}

// CountSecretDeletes counts delete cards opened since the given time.
func (s *Store) CountSecretDeletes(ctx context.Context, userID, agentID, name string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secret_deletes WHERE user_id = ? AND agent_id = ? AND name = ? AND created_at >= ?`,
		userID, agentID, name, s.stamp(since.UTC())).Scan(&n)
	return n, err
}

func (s *Store) pendingSecretDelete(ctx context.Context, userID, agentID, name string) (SecretDelete, error) {
	return scanSecretDelete(s.db.QueryRowContext(ctx, secretDeleteSelect+` WHERE user_id = ? AND agent_id = ? AND name = ? AND status = 'pending'`, userID, agentID, name))
}

// GetSecretDelete returns one delete card owned by the caller.
func (s *Store) GetSecretDelete(ctx context.Context, userID, id string) (SecretDelete, error) {
	return scanSecretDelete(s.db.QueryRowContext(ctx, secretDeleteSelect+` WHERE user_id = ? AND id = ?`, userID, id))
}

// ListSecretDeletes returns recent delete cards for one agent, oldest first.
func (s *Store) ListSecretDeletes(ctx context.Context, userID, agentID string, since time.Time) ([]SecretDelete, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, secretDeleteSelect+` WHERE user_id = ? AND agent_id = ? AND created_at >= ? ORDER BY created_at, id`, userID, agentID, s.stamp(since.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretDelete
	for rows.Next() {
		row, err := scanSecretDelete(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ResolveSecretDelete moves a pending card to deleted or declined.
func (s *Store) ResolveSecretDelete(ctx context.Context, userID, id, status string) (SecretDelete, error) {
	if status != "deleted" && status != "declined" {
		return SecretDelete{}, ErrInvalid
	}
	return s.finishSecretDelete(ctx, userID, id, status)
}

// ExpireSecretDelete marks a pending card expired when its time has passed.
func (s *Store) ExpireSecretDelete(ctx context.Context, userID, id string) (SecretDelete, error) {
	row, err := s.GetSecretDelete(ctx, userID, id)
	if err != nil {
		return SecretDelete{}, err
	}
	if row.Status != "pending" {
		return row, nil
	}
	if !s.secretDeleteDue(row) {
		return row, nil
	}
	return s.finishSecretDelete(ctx, userID, id, "expired")
}

func (s *Store) secretDeleteDue(row SecretDelete) bool {
	exp, err := parseTime(row.ExpiresAt)
	if err != nil || exp.IsZero() {
		return true
	}
	return !s.Now().Before(exp)
}

func (s *Store) finishSecretDelete(ctx context.Context, userID, id, status string) (SecretDelete, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretDelete{}, err
	}
	defer tx.Rollback()
	row, err := scanSecretDelete(tx.QueryRowContext(ctx, secretDeleteSelect+` WHERE user_id = ? AND id = ?`, userID, id))
	if err != nil {
		return SecretDelete{}, err
	}
	if row.Status != "pending" {
		return row, tx.Commit()
	}
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE secret_deletes SET status = ?, resolved_at = ? WHERE id = ? AND user_id = ? AND status = 'pending'`, status, stamp, id, userID)
	if err != nil {
		return SecretDelete{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return SecretDelete{}, err
	}
	if n == 1 {
		actorKind, actorID := "user", userID
		if status == "expired" {
			actorKind, actorID = "system", "connect"
		}
		if err := s.insertAudit(ctx, tx, row.WorkspaceID, userID, actorKind, actorID, "secret_delete."+status, "agent:"+row.AgentID, auditConnect(row.Name, "", "", status)); err != nil {
			return SecretDelete{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SecretDelete{}, err
	}
	return s.GetSecretDelete(ctx, userID, id)
}

const secretDeleteSelect = `SELECT id, workspace_id, user_id, agent_id, thread_id, run_id, name, reason, status, expires_at, created_at, COALESCE(resolved_at, '') FROM secret_deletes`

func scanSecretDelete(row rowScanner) (SecretDelete, error) {
	var out SecretDelete
	err := row.Scan(&out.ID, &out.WorkspaceID, &out.UserID, &out.AgentID, &out.ThreadID, &out.RunID, &out.Name, &out.Reason, &out.Status, &out.ExpiresAt, &out.CreatedAt, &out.ResolvedAt)
	if err != nil {
		return SecretDelete{}, scanErr(err)
	}
	return out, nil
}
