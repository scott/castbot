package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/scott/castbot/internal/connect"
)

const (
	connectKind = "connection"
	// SecretRequestLimit is how many cards one agent may open per connection each hour.
	SecretRequestLimit = 5
)

// ConnectionGrant is a proxy capability. It is not an environment grant.
type ConnectionGrant struct {
	ID         string
	Connection string
	SecretID   string
	Enabled    bool
	CreatedAt  string
	RevokedAt  string
}

// SecretRequest is one chat card. It never stores the submitted value.
type SecretRequest struct {
	ID          string
	WorkspaceID string
	UserID      string
	AgentID     string
	ThreadID    string
	RunID       string
	Connection  string
	Reason      string
	Status      string
	ExpiresAt   string
	CreatedAt   string
	ResolvedAt  string
}

// ConnectApproval is a host check for a mutating proxy call.
type ConnectApproval struct {
	ID          string
	WorkspaceID string
	UserID      string
	AgentID     string
	ThreadID    string
	Connection  string
	Action      string
	Method      string
	Path        string
	Status      string
	ExpiresAt   string
	CreatedAt   string
	DecidedAt   string
}

func (s *Store) migrateV22() error {
	if err := s.ensureColumn("secrets", "kind", `ALTER TABLE secrets ADD COLUMN kind TEXT NOT NULL DEFAULT 'env'`); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_bundles", "extras_json", `ALTER TABLE agent_bundles ADD COLUMN extras_json TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS connection_grants (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  connection TEXT NOT NULL,
  secret_id TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  revoked_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_conn_grant_open ON connection_grants(user_id, agent_id, connection) WHERE revoked_at IS NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_conn_grant_user ON connection_grants(user_id, agent_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS secret_requests (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  run_id TEXT NOT NULL DEFAULT '',
  connection TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN ('pending', 'saved', 'declined', 'expired')),
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  resolved_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_secret_req_pending ON secret_requests(user_id, agent_id, connection) WHERE status = 'pending'`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_secret_req_user ON secret_requests(user_id, agent_id, created_at)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS connect_approvals (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  thread_id TEXT NOT NULL DEFAULT '',
  connection TEXT NOT NULL,
  action TEXT NOT NULL,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'consumed')),
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  decided_at TEXT,
  consumed_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_connect_appr_user ON connect_approvals(user_id, agent_id, status)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS approval_uses (
  approval_id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  created_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (22) ON CONFLICT(version) DO NOTHING`)
	return err
}

// SaveConnectionSecret writes a connection token into the vault.
// It does not create an environment grant.
func (s *Store) SaveConnectionSecret(ctx context.Context, userID, connection, value string) (Secret, error) {
	name := connect.SecretName(connection)
	if name == "" || !secretValueOK(value) {
		return Secret{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return Secret{}, err
	}
	cur, err := s.SecretByName(ctx, userID, name)
	if err == nil {
		var kind string
		if err := s.db.QueryRowContext(ctx, `SELECT kind FROM secrets WHERE id = ? AND user_id = ?`, cur.ID, userID).Scan(&kind); err != nil {
			return Secret{}, err
		}
		if kind != connectKind {
			return Secret{}, ErrConflict
		}
		return s.ReplaceSecret(ctx, userID, cur.ID, value)
	}
	if err != ErrNotFound {
		return Secret{}, err
	}
	ct, nonce, err := s.seal([]byte(value))
	if err != nil {
		return Secret{}, err
	}
	id, err := newID()
	if err != nil {
		return Secret{}, err
	}
	stamp := s.stamp(s.Now())
	label := "Connection"
	if e, ok := connect.Lookup(connection); ok {
		label = e.Title + " connection"
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO secrets(id, workspace_id, user_id, name, label, provider, ciphertext, nonce, created_at, kind)
VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?)`, id, user.WorkspaceID, userID, name, label, ct, nonce, stamp, connectKind)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Secret{}, ErrConflict
		}
		return Secret{}, err
	}
	return Secret{ID: id, WorkspaceID: user.WorkspaceID, UserID: userID, Name: name, Label: label, CreatedAt: stamp}, nil
}

// ConnectionSecret returns the vault row for a connection, without the value.
func (s *Store) ConnectionSecret(ctx context.Context, userID, connection string) (Secret, error) {
	name := connect.SecretName(connection)
	if name == "" {
		return Secret{}, ErrInvalid
	}
	sec, err := s.SecretByName(ctx, userID, name)
	if err != nil {
		return Secret{}, err
	}
	var kind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM secrets WHERE id = ? AND user_id = ?`, sec.ID, userID).Scan(&kind); err != nil {
		return Secret{}, err
	}
	if kind != connectKind {
		return Secret{}, ErrNotFound
	}
	return sec, nil
}

// ConnectionValue decrypts a connection token for the proxy. Do not log it.
func (s *Store) ConnectionValue(ctx context.Context, userID, connection string) (string, error) {
	sec, err := s.ConnectionSecret(ctx, userID, connection)
	if err != nil {
		return "", err
	}
	return s.DecryptSecret(ctx, userID, sec.ID)
}

// EnableConnection records a proxy grant for one agent. The secret must already exist.
func (s *Store) EnableConnection(ctx context.Context, userID, agentID, connection string) error {
	if _, ok := connect.Lookup(connection); !ok {
		return ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	sec, err := s.ConnectionSecret(ctx, userID, connection)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id, revoked string
	err = tx.QueryRowContext(ctx, `SELECT id, COALESCE(revoked_at, '') FROM connection_grants WHERE user_id = ? AND agent_id = ? AND connection = ? AND revoked_at IS NULL`, userID, agentID, connection).Scan(&id, &revoked)
	stamp := s.stamp(s.Now())
	if err == sql.ErrNoRows {
		id, err = newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO connection_grants(id, workspace_id, user_id, agent_id, connection, secret_id, enabled, created_at) VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
			id, agent.WorkspaceID, userID, agentID, connection, sec.ID, stamp); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if _, err := tx.ExecContext(ctx, `UPDATE connection_grants SET enabled = 1, secret_id = ?, revoked_at = NULL WHERE id = ? AND user_id = ?`, sec.ID, id, userID); err != nil {
		return err
	}
	if err := s.insertAudit(ctx, tx, agent.WorkspaceID, userID, "user", userID, "connection.enabled", "agent:"+agentID, auditConnect(connection, "", "", "enabled")); err != nil {
		return err
	}
	return tx.Commit()
}

// RevokeConnection stops proxy use immediately.
func (s *Store) RevokeConnection(ctx context.Context, userID, agentID, connection string) error {
	if _, ok := connect.Lookup(connection); !ok {
		return ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE connection_grants SET enabled = 0, revoked_at = ? WHERE user_id = ? AND agent_id = ? AND connection = ? AND revoked_at IS NULL`,
		stamp, userID, agentID, connection)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := s.insertAudit(ctx, tx, agent.WorkspaceID, userID, "user", userID, "connection.revoked", "agent:"+agentID, auditConnect(connection, "", "", "revoked")); err != nil {
		return err
	}
	return tx.Commit()
}

// ConnectionEnabled reports a live grant.
func (s *Store) ConnectionEnabled(ctx context.Context, userID, agentID, connection string) (bool, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return false, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM connection_grants WHERE user_id = ? AND agent_id = ? AND connection = ? AND enabled = 1 AND revoked_at IS NULL`, userID, agentID, connection).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// EnabledConnections lists live grants for one agent.
func (s *Store) EnabledConnections(ctx context.Context, userID, agentID string) ([]string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT connection FROM connection_grants WHERE user_id = ? AND agent_id = ? AND enabled = 1 AND revoked_at IS NULL ORDER BY connection`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// CreateSecretRequest opens one card, or returns the pending one.
// created is false when an open card was reused.
func (s *Store) CreateSecretRequest(ctx context.Context, userID, agentID, threadID, runID, connection, reason string, expires time.Time) (SecretRequest, bool, error) {
	if _, ok := connect.Lookup(connection); !ok {
		return SecretRequest{}, false, ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return SecretRequest{}, false, err
	}
	if pending, err := s.pendingSecret(ctx, userID, agentID, connection); err == nil {
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
	n, err := s.CountSecretRequests(ctx, userID, agentID, connection, s.Now().Add(-time.Hour))
	if err != nil {
		return SecretRequest{}, false, err
	}
	if n >= SecretRequestLimit {
		_ = s.RecordConnectAudit(ctx, userID, "secret_request.limited", agentID, map[string]string{"connection": connection})
		return SecretRequest{}, false, ErrConflict
	}
	id, err := newID()
	if err != nil {
		return SecretRequest{}, false, err
	}
	stamp := s.stamp(s.Now())
	row := SecretRequest{
		ID: id, WorkspaceID: agent.WorkspaceID, UserID: userID, AgentID: agentID,
		ThreadID: threadID, RunID: runID, Connection: connection, Reason: reason,
		Status: "pending", ExpiresAt: s.stamp(expires.UTC()), CreatedAt: stamp,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretRequest{}, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO secret_requests(id, workspace_id, user_id, agent_id, thread_id, run_id, connection, reason, status, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		row.ID, row.WorkspaceID, userID, agentID, threadID, runID, connection, reason, row.ExpiresAt, stamp); err != nil {
		_ = tx.Rollback()
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			if pending, err2 := s.pendingSecret(ctx, userID, agentID, connection); err2 == nil {
				return pending, false, nil
			}
		}
		return SecretRequest{}, false, err
	}
	if err := s.insertAudit(ctx, tx, agent.WorkspaceID, userID, "agent", agentID, "secret_request.created", "agent:"+agentID, auditConnect(connection, "", "", "created")); err != nil {
		return SecretRequest{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return SecretRequest{}, false, err
	}
	return row, true, nil
}

// CountSecretRequests counts cards opened since the given time.
func (s *Store) CountSecretRequests(ctx context.Context, userID, agentID, connection string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secret_requests WHERE user_id = ? AND agent_id = ? AND connection = ? AND created_at >= ?`,
		userID, agentID, connection, s.stamp(since.UTC())).Scan(&n)
	return n, err
}

func (s *Store) pendingSecret(ctx context.Context, userID, agentID, connection string) (SecretRequest, error) {
	row := s.db.QueryRowContext(ctx, secretSelect+` WHERE user_id = ? AND agent_id = ? AND connection = ? AND status = 'pending'`, userID, agentID, connection)
	return scanSecretRequest(row)
}

// GetSecretRequest returns one card owned by the caller.
func (s *Store) GetSecretRequest(ctx context.Context, userID, id string) (SecretRequest, error) {
	return scanSecretRequest(s.db.QueryRowContext(ctx, secretSelect+` WHERE user_id = ? AND id = ?`, userID, id))
}

// ListSecretRequests returns recent cards for one agent, oldest first.
func (s *Store) ListSecretRequests(ctx context.Context, userID, agentID string, since time.Time) ([]SecretRequest, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, secretSelect+` WHERE user_id = ? AND agent_id = ? AND created_at >= ? ORDER BY created_at, id`, userID, agentID, s.stamp(since.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretRequest
	for rows.Next() {
		row, err := scanSecretRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ResolveSecretRequest moves a pending card to saved or declined.
func (s *Store) ResolveSecretRequest(ctx context.Context, userID, id, status string) (SecretRequest, error) {
	if status != "saved" && status != "declined" {
		return SecretRequest{}, ErrInvalid
	}
	return s.finishSecret(ctx, userID, id, status)
}

// ExpireSecretRequest marks a pending card expired when its time has passed.
func (s *Store) ExpireSecretRequest(ctx context.Context, userID, id string) (SecretRequest, error) {
	row, err := s.GetSecretRequest(ctx, userID, id)
	if err != nil {
		return SecretRequest{}, err
	}
	if row.Status != "pending" {
		return row, nil
	}
	if !s.secretDue(row) {
		return row, nil
	}
	return s.finishSecret(ctx, userID, id, "expired")
}

func (s *Store) secretDue(row SecretRequest) bool {
	exp, err := parseTime(row.ExpiresAt)
	if err != nil || exp.IsZero() {
		return true
	}
	return !s.Now().Before(exp)
}

func (s *Store) finishSecret(ctx context.Context, userID, id, status string) (SecretRequest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SecretRequest{}, err
	}
	defer tx.Rollback()
	row, err := scanSecretRequest(tx.QueryRowContext(ctx, secretSelect+` WHERE user_id = ? AND id = ?`, userID, id))
	if err != nil {
		return SecretRequest{}, err
	}
	if row.Status != "pending" {
		return row, tx.Commit()
	}
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE secret_requests SET status = ?, resolved_at = ? WHERE id = ? AND user_id = ? AND status = 'pending'`, status, stamp, id, userID)
	if err != nil {
		return SecretRequest{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return SecretRequest{}, err
	}
	if n == 1 {
		actorKind, actorID := "user", userID
		if status == "expired" {
			actorKind, actorID = "system", "connect"
		}
		if err := s.insertAudit(ctx, tx, row.WorkspaceID, userID, actorKind, actorID, "secret_request."+status, "agent:"+row.AgentID, auditConnect(row.Connection, "", "", status)); err != nil {
			return SecretRequest{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return SecretRequest{}, err
	}
	return s.GetSecretRequest(ctx, userID, id)
}

// CreateConnectApproval opens a host approval card for a mutating call.
func (s *Store) CreateConnectApproval(ctx context.Context, userID, agentID, threadID, connection, action, method, path string, expires time.Time) (ConnectApproval, error) {
	if _, ok := connect.Lookup(connection); !ok || action == "" || method == "" {
		return ConnectApproval{}, ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return ConnectApproval{}, err
	}
	id, err := newID()
	if err != nil {
		return ConnectApproval{}, err
	}
	stamp := s.stamp(s.Now())
	path = clipPath(path)
	row := ConnectApproval{
		ID: id, WorkspaceID: agent.WorkspaceID, UserID: userID, AgentID: agentID, ThreadID: threadID,
		Connection: connection, Action: clipPath(action), Method: method, Path: path,
		Status: "pending", ExpiresAt: s.stamp(expires.UTC()), CreatedAt: stamp,
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO connect_approvals(id, workspace_id, user_id, agent_id, thread_id, connection, action, method, path, status, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		row.ID, row.WorkspaceID, userID, agentID, threadID, connection, row.Action, method, path, row.ExpiresAt, stamp)
	if err != nil {
		return ConnectApproval{}, err
	}
	return row, nil
}

// GetConnectApproval returns one host approval.
func (s *Store) GetConnectApproval(ctx context.Context, userID, id string) (ConnectApproval, error) {
	return scanConnectApproval(s.db.QueryRowContext(ctx, connectApprSelect+` WHERE user_id = ? AND id = ?`, userID, id))
}

// ListConnectApprovals returns cards still useful to show.
func (s *Store) ListConnectApprovals(ctx context.Context, userID, agentID string, since time.Time) ([]ConnectApproval, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, connectApprSelect+` WHERE user_id = ? AND agent_id = ? AND created_at >= ? ORDER BY created_at, id`, userID, agentID, s.stamp(since.UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectApproval
	for rows.Next() {
		row, err := scanConnectApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// DecideConnectApproval approves or denies a pending host card.
func (s *Store) DecideConnectApproval(ctx context.Context, userID, id, status string) (ConnectApproval, bool, error) {
	if status != "approved" && status != "denied" {
		return ConnectApproval{}, false, ErrInvalid
	}
	stamp := s.stamp(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE connect_approvals SET status = ?, decided_at = ? WHERE id = ? AND user_id = ? AND status = 'pending'`, status, stamp, id, userID)
	if err != nil {
		return ConnectApproval{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ConnectApproval{}, false, err
	}
	row, err := s.GetConnectApproval(ctx, userID, id)
	if err != nil {
		return ConnectApproval{}, false, err
	}
	if n == 1 {
		_ = s.RecordConnectAudit(ctx, userID, "connection."+status, row.AgentID, map[string]string{
			"connection": row.Connection, "method": row.Method, "path": row.Path, "outcome": status,
		})
	}
	return row, n == 1, nil
}

// ConsumeConnectApproval marks an approved card used, once.
func (s *Store) ConsumeConnectApproval(ctx context.Context, userID, id string) (bool, error) {
	stamp := s.stamp(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE connect_approvals SET status = 'consumed', consumed_at = ? WHERE id = ? AND user_id = ? AND status = 'approved'`, stamp, id, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ExpireConnectApproval marks a pending card expired when its time has passed.
func (s *Store) ExpireConnectApproval(ctx context.Context, userID, id string) (ConnectApproval, error) {
	row, err := s.GetConnectApproval(ctx, userID, id)
	if err != nil {
		return ConnectApproval{}, err
	}
	if row.Status != "pending" {
		return row, nil
	}
	exp, err := parseTime(row.ExpiresAt)
	if err != nil || s.Now().Before(exp) {
		return row, nil
	}
	stamp := s.stamp(s.Now())
	_, err = s.db.ExecContext(ctx, `UPDATE connect_approvals SET status = 'expired', decided_at = ? WHERE id = ? AND user_id = ? AND status = 'pending'`, stamp, id, userID)
	if err != nil {
		return ConnectApproval{}, err
	}
	return s.GetConnectApproval(ctx, userID, id)
}

// ConsumeToolApproval uses one approved Cast card for a tool name, once.
func (s *Store) ConsumeToolApproval(ctx context.Context, userID, agentID, tool string) (bool, error) {
	tool = strings.TrimSpace(tool)
	if tool == "" || len(tool) > 80 {
		return false, ErrInvalid
	}
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return false, err
	}
	since := s.stamp(s.Now().Add(-15 * time.Minute))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id, workspace string
	err = tx.QueryRowContext(ctx, `SELECT a.id, a.workspace_id FROM approvals a
JOIN runs r ON r.id = a.run_id AND r.user_id = a.user_id
WHERE a.user_id = ? AND r.agent_id = ? AND a.tool = ? AND a.status = 'approved' AND a.origin = 'web'
AND COALESCE(a.decided_at, '') >= ?
AND a.id NOT IN (SELECT approval_id FROM approval_uses WHERE user_id = ?)
ORDER BY a.decided_at DESC LIMIT 1`, userID, agentID, tool, since, userID).Scan(&id, &workspace)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO approval_uses(approval_id, workspace_id, user_id, agent_id, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, workspace, userID, agentID, s.stamp(s.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	return true, tx.Commit()
}

// OpenRun returns the newest streaming or waiting run for an agent.
func (s *Store) OpenRun(ctx context.Context, userID, agentID string) (string, string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", "", err
	}
	var id, thread string
	err := s.db.QueryRowContext(ctx, `SELECT id, thread_id FROM runs WHERE user_id = ? AND agent_id = ? AND status IN ('queued', 'streaming', 'waiting_approval') ORDER BY started_at DESC LIMIT 1`, userID, agentID).Scan(&id, &thread)
	if err != nil {
		return "", "", scanErr(err)
	}
	return id, thread, nil
}

// RecordConnectAudit writes one audit row. detail must not contain a token.
func (s *Store) RecordConnectAudit(ctx context.Context, userID, action, agentID string, detail any) error {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.insertAudit(ctx, tx, user.WorkspaceID, userID, "system", "connect", action, "agent:"+agentID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

func auditConnect(connection, method, path, outcome string) string {
	raw, err := json.Marshal(map[string]string{
		"connection": connection,
		"method":     method,
		"path":       clipPath(path),
		"outcome":    outcome,
	})
	if err != nil {
		return `{"outcome":"bad"}`
	}
	return string(raw)
}

func clipPath(path string) string {
	path = strings.ReplaceAll(path, "\n", "")
	path = strings.ReplaceAll(path, "\r", "")
	if len(path) > 180 {
		path = path[:180]
	}
	return path
}

const secretSelect = `SELECT id, workspace_id, user_id, agent_id, thread_id, run_id, connection, reason, status, expires_at, created_at, COALESCE(resolved_at, '') FROM secret_requests`

func scanSecretRequest(row rowScanner) (SecretRequest, error) {
	var out SecretRequest
	err := row.Scan(&out.ID, &out.WorkspaceID, &out.UserID, &out.AgentID, &out.ThreadID, &out.RunID, &out.Connection, &out.Reason, &out.Status, &out.ExpiresAt, &out.CreatedAt, &out.ResolvedAt)
	if err != nil {
		return SecretRequest{}, scanErr(err)
	}
	return out, nil
}

const connectApprSelect = `SELECT id, workspace_id, user_id, agent_id, thread_id, connection, action, method, path, status, expires_at, created_at, COALESCE(decided_at, '') FROM connect_approvals`

func scanConnectApproval(row rowScanner) (ConnectApproval, error) {
	var out ConnectApproval
	err := row.Scan(&out.ID, &out.WorkspaceID, &out.UserID, &out.AgentID, &out.ThreadID, &out.Connection, &out.Action, &out.Method, &out.Path, &out.Status, &out.ExpiresAt, &out.CreatedAt, &out.DecidedAt)
	if err != nil {
		return ConnectApproval{}, scanErr(err)
	}
	return out, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}
