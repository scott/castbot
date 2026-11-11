package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/scott/castbot/internal/castcfg"
)

// Approval is one permission prompt. UserID is the owner.
// Arguments is scrubbed JSON. Redacted lists secret keys and has no values.
type Approval struct {
	ID          string
	WorkspaceID string
	UserID      string
	RunID       string
	AgentID     string
	RequestID   string
	Tool        string
	Arguments   string
	Redacted    string
	Status      string
	Origin      string
	DecidedBy   string
	DecidedAt   string
	ExpiresAt   string
	CreatedAt   string
	Sent        bool
}

// Audit is one approval record. Detail has no secret values.
type Audit struct {
	ID          int64
	WorkspaceID string
	UserID      string
	ActorKind   string
	ActorID     string
	Action      string
	Target      string
	Detail      string
	CreatedAt   string
}

func (s *Store) migrateApprovals() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS approvals (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  tool TEXT NOT NULL,
  arguments_json TEXT NOT NULL DEFAULT '{}',
  redacted_json TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'denied', 'expired', 'inactive')),
  origin TEXT NOT NULL DEFAULT 'web' CHECK (origin IN ('web', 'cli')),
  decided_by TEXT NOT NULL DEFAULT '',
  decided_at TEXT,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  sent INTEGER NOT NULL DEFAULT 0
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_approvals_run_request ON approvals(run_id, request_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_approvals_user ON approvals(user_id, status, expires_at)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS audit_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  actor_kind TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT NOT NULL,
  detail_json TEXT NOT NULL,
  created_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_log(user_id, id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (13) ON CONFLICT(version) DO NOTHING`); err != nil {
		return err
	}
	return nil
}

// SetTimezone stores the caller's zone. Times on the page use this zone.
func (s *Store) SetTimezone(ctx context.Context, userID, zone string) error {
	zone = strings.TrimSpace(zone)
	if zone == "" {
		return ErrInvalid
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET timezone = ? WHERE id = ?`, zone, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// RecordPermission stores a pending approval for one of the caller's runs.
// Secret argument values are dropped before the row is written.
// A second copy of the same request id does not add another audit row.
func (s *Store) RecordPermission(ctx context.Context, userID, runID string, raw json.RawMessage, expires time.Time, offset int) (Approval, error) {
	requestID := castcfg.JSONString(raw, "id", "requestId", "request_id")
	tool := castcfg.JSONString(raw, "tool", "name", "toolName")
	if requestID == "" || tool == "" || len(requestID) > 128 || len(tool) > 80 {
		return Approval{}, ErrInvalid
	}
	if strings.ContainsAny(requestID, "\r\n") || strings.ContainsAny(tool, "\r\n") {
		return Approval{}, ErrInvalid
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	argsRaw := obj["arguments"]
	clean, redacted, err := castcfg.ScrubJSON(argsRaw)
	if err != nil {
		clean = json.RawMessage("{}")
		redacted = nil
	}
	if len(clean) > 32<<10 {
		clean = json.RawMessage("{}")
	}
	redactedJSON, err := json.Marshal(redacted)
	if err != nil {
		return Approval{}, err
	}
	if redacted == nil {
		redactedJSON = []byte("[]")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Approval{}, err
	}
	defer tx.Rollback()
	run, err := s.lockRun(ctx, tx, userID, runID)
	if err != nil {
		return Approval{}, err
	}
	switch run.Status {
	case "queued", "streaming", "waiting_approval":
	default:
		return Approval{}, ErrNotFound
	}
	stamp := s.stamp(s.Now())
	exp := s.stamp(expires.UTC())
	id, err := newID()
	if err != nil {
		return Approval{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO approvals(id, workspace_id, user_id, run_id, request_id, tool, arguments_json, redacted_json, status, origin, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', 'web', ?, ?) ON CONFLICT(run_id, request_id) DO NOTHING`,
		id, run.WorkspaceID, userID, runID, requestID, tool, string(clean), string(redactedJSON), exp, stamp)
	if err != nil {
		return Approval{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Approval{}, err
	}
	inserted := n == 1
	if !inserted {
		err = tx.QueryRowContext(ctx, `SELECT id FROM approvals WHERE run_id = ? AND request_id = ? AND user_id = ?`, runID, requestID, userID).Scan(&id)
		if err != nil {
			return Approval{}, scanErr(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'waiting_approval', stream_offset = ? WHERE id = ? AND user_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')`,
		offset, runID, userID); err != nil {
		return Approval{}, err
	}
	if inserted {
		payload, err := jsonString(map[string]any{
			"approvalId": id,
			"requestId":  requestID,
			"tool":       tool,
			"arguments":  json.RawMessage(clean),
			"redacted":   redacted,
		})
		if err != nil {
			return Approval{}, err
		}
		var seq int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = ?`, runID).Scan(&seq); err != nil {
			return Approval{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_events(run_id, seq, kind, payload_json, created_at) VALUES (?, ?, 'permission', ?, ?)`,
			runID, seq, payload, stamp); err != nil {
			return Approval{}, err
		}
		if err := s.insertAudit(ctx, tx, run.WorkspaceID, userID, "agent", run.AgentID, "approval.requested", "approval:"+id, auditDetail(tool, requestID, clean, redacted)); err != nil {
			return Approval{}, err
		}
	}
	if err := s.syncApprovalCount(ctx, tx, userID, run.AgentID); err != nil {
		return Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return Approval{}, err
	}
	return s.GetApproval(ctx, userID, id)
}

// GetApproval returns one of the caller's approvals.
func (s *Store) GetApproval(ctx context.Context, userID, id string) (Approval, error) {
	row := s.db.QueryRowContext(ctx, approvalSelect+` WHERE a.user_id = ? AND a.id = ?`, userID, id)
	return scanApproval(row)
}

// ListRunApprovals returns the caller's approvals for one run, oldest first.
func (s *Store) ListRunApprovals(ctx context.Context, userID, runID string) ([]Approval, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return nil, err
	}
	return s.listApprovals(ctx, approvalSelect+` WHERE a.user_id = ? AND a.run_id = ? ORDER BY a.created_at, a.id`, userID, runID)
}

// ListThreadApprovals returns the caller's approvals in one thread, oldest first.
func (s *Store) ListThreadApprovals(ctx context.Context, userID, threadID string) ([]Approval, error) {
	if _, err := s.GetThread(ctx, userID, threadID); err != nil {
		return nil, err
	}
	return s.listApprovals(ctx, approvalSelect+` WHERE a.user_id = ? AND r.thread_id = ? ORDER BY a.created_at, a.id`, userID, threadID)
}

// ListPendingApprovals returns the caller's web prompts that are still open.
func (s *Store) ListPendingApprovals(ctx context.Context, userID string) ([]Approval, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	return s.listApprovals(ctx, approvalSelect+` WHERE a.user_id = ? AND a.status = 'pending' AND a.origin = 'web' ORDER BY a.created_at, a.id`, userID)
}

// ClaimDecision moves a pending web approval to approved or denied.
// runID must be the approval's run. A mismatched run does not claim.
// claimed is false when another caller already decided it, or it is not pending.
// sent stays false until the agent accepts the decision. The audit row is
// written by the caller after that accept, on a context the browser does not own.
func (s *Store) ClaimDecision(ctx context.Context, userID, id, runID, status, actor string) (Approval, bool, error) {
	if status != "approved" && status != "denied" || runID == "" {
		return Approval{}, false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Approval{}, false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET status = ?, decided_by = ?, decided_at = ?, sent = 0 WHERE id = ? AND user_id = ? AND run_id = ? AND status = 'pending' AND origin = 'web'`,
		status, actor, s.stamp(s.Now()), id, userID, runID)
	if err != nil {
		return Approval{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Approval{}, false, err
	}
	row, err := scanApproval(tx.QueryRowContext(ctx, approvalSelect+` WHERE a.user_id = ? AND a.id = ?`, userID, id))
	if err != nil {
		return Approval{}, false, err
	}
	if n == 1 {
		if err := s.syncApprovalCount(ctx, tx, userID, row.AgentID); err != nil {
			return Approval{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Approval{}, false, err
	}
	return row, n == 1, nil
}

// ReleaseDecision puts a claimed approval back to pending when the agent call failed.
func (s *Store) ReleaseDecision(ctx context.Context, userID, id, status, actor string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET status = 'pending', decided_by = '', decided_at = NULL WHERE id = ? AND user_id = ? AND status = ? AND decided_by = ? AND origin = 'web'`,
		id, userID, status, actor)
	if err != nil {
		return err
	}
	if _, err := res.RowsAffected(); err != nil {
		return err
	}
	row, err := scanApproval(tx.QueryRowContext(ctx, approvalSelect+` WHERE a.user_id = ? AND a.id = ?`, userID, id))
	if err != nil {
		return err
	}
	if err := s.syncApprovalCount(ctx, tx, userID, row.AgentID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkPendingInactive records that a still-open prompt died with its run.
// A row that was already decided is left as it is.
func (s *Store) MarkPendingInactive(ctx context.Context, userID, id, actorKind, actorID string) (Approval, bool, error) {
	return s.markInactive(ctx, userID, id, actorKind, actorID, true)
}

// MarkInactive records that the agent no longer has this prompt.
// It writes one audit row when the status actually changes.
func (s *Store) MarkInactive(ctx context.Context, userID, id, actorKind, actorID string) (Approval, error) {
	row, _, err := s.markInactive(ctx, userID, id, actorKind, actorID, false)
	return row, err
}

func (s *Store) markInactive(ctx context.Context, userID, id, actorKind, actorID string, onlyPending bool) (Approval, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Approval{}, false, err
	}
	defer tx.Rollback()
	match := `status IN ('pending', 'approved', 'denied')`
	if onlyPending {
		match = `status = 'pending'`
	}
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET status = 'inactive', decided_by = ?, decided_at = ?, sent = 1 WHERE id = ? AND user_id = ? AND `+match+` AND origin = 'web'`,
		actorID, s.stamp(s.Now()), id, userID)
	if err != nil {
		return Approval{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Approval{}, false, err
	}
	row, err := scanApproval(tx.QueryRowContext(ctx, approvalSelect+` WHERE a.user_id = ? AND a.id = ?`, userID, id))
	if err != nil {
		return Approval{}, false, err
	}
	if n == 1 {
		if err := s.insertAudit(ctx, tx, row.WorkspaceID, userID, actorKind, actorID, "approval.inactive", "approval:"+row.ID, auditDetail(row.Tool, row.RequestID, json.RawMessage(row.Arguments), decodeList(row.Redacted))); err != nil {
			return Approval{}, false, err
		}
		if err := s.syncApprovalCount(ctx, tx, userID, row.AgentID); err != nil {
			return Approval{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Approval{}, false, err
	}
	return row, n == 1, nil
}

// WriteDecisionAudit records one approved or denied decision.
// A second call for the same approval and action does not add a row.
func (s *Store) WriteDecisionAudit(ctx context.Context, userID, id, action, actorKind, actorID string) error {
	switch action {
	case "approval.approved", "approval.denied", "approval.expired":
	default:
		return ErrInvalid
	}
	row, err := s.GetApproval(ctx, userID, id)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE user_id = ? AND target = ? AND action = ?`, userID, "approval:"+id, action).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if err := s.insertAudit(ctx, tx, row.WorkspaceID, userID, actorKind, actorID, action, "approval:"+id, auditDetail(row.Tool, row.RequestID, json.RawMessage(row.Arguments), decodeList(row.Redacted))); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ExpireDue marks pending approvals whose time has passed and returns
// every expired approval that still needs a Deny posted to the agent.
// The sweep is the control plane. Each update is bound to that row's user id.
func (s *Store) ExpireDue(ctx context.Context) ([]Approval, error) {
	now := s.stamp(s.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.user_id FROM approvals a WHERE a.status = 'pending' AND a.origin = 'web' AND a.expires_at <= ?`, now)
	if err != nil {
		return nil, err
	}
	type key struct{ id, user string }
	var due []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.id, &k.user); err != nil {
			rows.Close()
			return nil, err
		}
		due = append(due, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, k := range due {
		res, err := tx.ExecContext(ctx, `UPDATE approvals SET status = 'expired', decided_by = 'system:timeout', decided_at = ? WHERE id = ? AND user_id = ? AND status = 'pending'`,
			now, k.id, k.user)
		if err != nil {
			return nil, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n != 1 {
			continue
		}
		row, err := scanApproval(tx.QueryRowContext(ctx, approvalSelect+` WHERE a.user_id = ? AND a.id = ?`, k.user, k.id))
		if err != nil {
			return nil, err
		}
		if err := s.insertAudit(ctx, tx, row.WorkspaceID, k.user, "system", "system:timeout", "approval.expired", "approval:"+row.ID, auditDetail(row.Tool, row.RequestID, json.RawMessage(row.Arguments), decodeList(row.Redacted))); err != nil {
			return nil, err
		}
		if err := s.syncApprovalCount(ctx, tx, k.user, row.AgentID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.listApprovals(ctx, approvalSelect+` WHERE a.status = 'expired' AND a.sent = 0 AND a.origin = 'web' ORDER BY a.expires_at, a.id`)
}

// MarkDenialSent records that Deny was posted for an expired approval.
func (s *Store) MarkDenialSent(ctx context.Context, userID, id string) error {
	return s.MarkSent(ctx, userID, id)
}

// MarkSent records that the agent was told the decision.
// A second call is a no-op when the row is already sent.
func (s *Store) MarkSent(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET sent = 1 WHERE id = ? AND user_id = ? AND sent = 0`, id, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	row, err := s.GetApproval(ctx, userID, id)
	if err != nil {
		return err
	}
	if row.Sent {
		return nil
	}
	return ErrNotFound
}

// ResumeAfterApproval moves a run from waiting_approval back to streaming
// once the agent has the decision. A run that already moved on is left as it is.
func (s *Store) ResumeAfterApproval(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET status = 'streaming' WHERE id = ? AND user_id = ? AND status = 'waiting_approval'`, id, userID)
	return err
}

// ListUnconfirmed returns claimed web decisions that still need a post or an audit row.
// before is the newest decided_at to include, so an in-flight click is left alone.
func (s *Store) ListUnconfirmed(ctx context.Context, before time.Time) ([]Approval, error) {
	cut := s.stamp(before.UTC())
	return s.listApprovals(ctx, approvalSelect+` WHERE a.origin = 'web' AND a.status IN ('approved', 'denied') AND a.decided_at != '' AND a.decided_at <= ? AND (
  a.sent = 0 OR NOT EXISTS (
    SELECT 1 FROM audit_log g WHERE g.user_id = a.user_id AND g.target = 'approval:' || a.id
    AND g.action = CASE a.status WHEN 'approved' THEN 'approval.approved' ELSE 'approval.denied' END
  )
) ORDER BY a.decided_at, a.id`, cut)
}

// ListRestartDenials returns approvals a restart left inactive before Deny was posted.
func (s *Store) ListRestartDenials(ctx context.Context) ([]Approval, error) {
	return s.listApprovals(ctx, approvalSelect+` WHERE a.origin = 'web' AND a.status = 'inactive' AND a.sent = 0 AND a.decided_by = 'system:restart' ORDER BY a.decided_at, a.id`)
}

// PendingApprovalCount is the caller's open web approvals.
func (s *Store) PendingApprovalCount(ctx context.Context, userID string) (int, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE user_id = ? AND status = 'pending' AND origin = 'web'`, userID).Scan(&n)
	return n, err
}

// ListAudit returns the caller's audit rows, oldest first.
func (s *Store) ListAudit(ctx context.Context, userID string) ([]Audit, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, actor_kind, actor_id, action, target, detail_json, created_at FROM audit_log WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Audit
	for rows.Next() {
		var row Audit
		if err := rows.Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.ActorKind, &row.ActorID, &row.Action, &row.Target, &row.Detail, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const approvalSelect = `SELECT a.id, a.workspace_id, a.user_id, a.run_id, r.agent_id, a.request_id, a.tool, a.arguments_json, a.redacted_json, a.status, a.origin, a.decided_by, COALESCE(a.decided_at, ''), a.expires_at, a.created_at, a.sent FROM approvals a JOIN runs r ON r.id = a.run_id AND r.user_id = a.user_id`

func (s *Store) listApprovals(ctx context.Context, query string, args ...any) ([]Approval, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		row, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type scanRow interface {
	Scan(dest ...any) error
}

func scanApproval(row scanRow) (Approval, error) {
	var a Approval
	var sent int
	err := row.Scan(&a.ID, &a.WorkspaceID, &a.UserID, &a.RunID, &a.AgentID, &a.RequestID, &a.Tool, &a.Arguments, &a.Redacted, &a.Status, &a.Origin, &a.DecidedBy, &a.DecidedAt, &a.ExpiresAt, &a.CreatedAt, &sent)
	if err != nil {
		return Approval{}, scanErr(err)
	}
	a.Sent = sent == 1
	return a, nil
}

func (s *Store) lockRun(ctx context.Context, tx *sql.Tx, userID, runID string) (Run, error) {
	var run Run
	var routine, finished sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, thread_id, agent_id, user_id, routine_run_id, status, stream_offset, started_at, finished_at, error FROM runs WHERE id = ? AND user_id = ?`, runID, userID).
		Scan(&run.ID, &run.WorkspaceID, &run.ThreadID, &run.AgentID, &run.UserID, &routine, &run.Status, &run.StreamOffset, &run.StartedAt, &finished, &run.Error)
	if err != nil {
		return Run{}, scanErr(err)
	}
	run.RoutineRunID = routine.String
	run.FinishedAt = finished.String
	return run, nil
}

func (s *Store) syncApprovalCount(ctx context.Context, tx *sql.Tx, userID, agentID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE agents SET approvals = (
  SELECT COUNT(*) FROM approvals a
  JOIN runs r ON r.id = a.run_id AND r.user_id = a.user_id
  WHERE a.user_id = ? AND r.agent_id = ? AND a.status = 'pending' AND a.origin = 'web'
) WHERE id = ? AND user_id = ?`, userID, agentID, agentID, userID)
	return err
}

func (s *Store) insertAudit(ctx context.Context, tx *sql.Tx, workspaceID, userID, actorKind, actorID, action, target, detail string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(workspace_id, user_id, actor_kind, actor_id, action, target, detail_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, userID, actorKind, actorID, action, target, detail, s.stamp(s.Now()))
	return err
}

func auditDetail(tool, requestID string, args json.RawMessage, redacted []string) string {
	if len(bytesTrim(args)) == 0 {
		args = json.RawMessage("{}")
	}
	if redacted == nil {
		redacted = []string{}
	}
	raw, err := json.Marshal(map[string]any{
		"tool":       tool,
		"request_id": requestID,
		"arguments":  args,
		"redacted":   redacted,
	})
	if err != nil {
		raw, _ = json.Marshal(map[string]any{"tool": tool, "request_id": requestID, "redacted": redacted})
	}
	return string(raw)
}

func bytesTrim(raw json.RawMessage) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func decodeList(raw string) []string {
	var out []string
	if json.Unmarshal([]byte(raw), &out) != nil || out == nil {
		return []string{}
	}
	return out
}
