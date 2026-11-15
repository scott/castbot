package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const (
	// GrantVia marks a card decided by Always approve for one prompt.
	GrantVia = "prompt"
	// GrantLine is the chat line on a card approved for this prompt.
	GrantLine = "Approved for this prompt"
	// GrantNote is the audit detail for that decision. It has no secret values.
	GrantNote = "Always approve for this prompt"
)

// PromptGrant is one Always approve grant for a user message.
// It is active while EndedAt is empty. A second click does not add a row.
type PromptGrant struct {
	ID          string
	WorkspaceID string
	UserID      string
	MessageID   string
	CreatedAt   string
	EndedAt     string
}

func (s *Store) migrateV27() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS prompt_grants (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  ended_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_grants_message ON prompt_grants(user_id, message_id)`); err != nil {
		return err
	}
	if err := s.ensureColumn("approvals", "via", `ALTER TABLE approvals ADD COLUMN via TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("connect_approvals", "via", `ALTER TABLE connect_approvals ADD COLUMN via TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (27) ON CONFLICT(version) DO NOTHING`)
	return err
}

// OpenPromptGrant starts or reopens the grant for one of the caller's user messages.
// created is false when that grant is already active. A second row is not inserted.
func (s *Store) OpenPromptGrant(ctx context.Context, userID, messageID string) (PromptGrant, bool, error) {
	msg, err := s.GetMessage(ctx, userID, messageID)
	if err != nil {
		return PromptGrant{}, false, err
	}
	if msg.AuthorKind != "user" {
		return PromptGrant{}, false, ErrInvalid
	}
	id, err := newID()
	if err != nil {
		return PromptGrant{}, false, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO prompt_grants(id, workspace_id, user_id, message_id, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id, message_id) DO UPDATE SET ended_at = NULL, created_at = excluded.created_at
WHERE prompt_grants.ended_at IS NOT NULL`,
		id, msg.WorkspaceID, userID, messageID, s.stamp(s.Now()))
	if err != nil {
		return PromptGrant{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return PromptGrant{}, false, err
	}
	row, err := s.GetPromptGrant(ctx, userID, messageID)
	if err != nil {
		return PromptGrant{}, false, err
	}
	return row, n == 1, nil
}

// GetPromptGrant returns the caller's grant for one message.
func (s *Store) GetPromptGrant(ctx context.Context, userID, messageID string) (PromptGrant, error) {
	var row PromptGrant
	var ended sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, message_id, created_at, ended_at FROM prompt_grants WHERE user_id = ? AND message_id = ?`, userID, messageID).
		Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.MessageID, &row.CreatedAt, &ended)
	if err != nil {
		return PromptGrant{}, scanErr(err)
	}
	if ended.Valid {
		row.EndedAt = ended.String
	}
	return row, nil
}

// PromptGrantActive reports whether the caller's grant for this message is still open.
// A grant that has ended stays inactive even if a later run of the same message is open.
func (s *Store) PromptGrantActive(ctx context.Context, userID, messageID string) (bool, error) {
	row, err := s.GetPromptGrant(ctx, userID, messageID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return row.EndedAt == "", nil
}

// EndPromptGrant closes an active grant. A second call changes nothing.
func (s *Store) EndPromptGrant(ctx context.Context, userID, messageID string) error {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE prompt_grants SET ended_at = ? WHERE user_id = ? AND message_id = ? AND ended_at IS NULL`,
		s.stamp(s.Now()), userID, messageID)
	return err
}

// PromptMessageID is the user message a run belongs to.
// A specialist run uses the job's owner message. Any other run uses its own user message.
func (s *Store) PromptMessageID(ctx context.Context, userID, runID string) (string, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return "", err
	}
	dispatch, err := s.DispatchByRun(ctx, userID, runID)
	if err == nil {
		job, jerr := s.JobByID(ctx, userID, dispatch.JobID)
		if jerr != nil {
			return "", jerr
		}
		return job.OwnerMessageID, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	msg, err := s.UserMessageForRun(ctx, userID, runID)
	if err != nil {
		return "", err
	}
	return msg.ID, nil
}

// PromptRunIDs lists the chief run for a user message and specialist runs whose job points at it.
// An empty run id is left out.
func (s *Store) PromptRunIDs(ctx context.Context, userID, messageID string) ([]string, error) {
	if _, err := s.GetMessage(ctx, userID, messageID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id FROM messages WHERE user_id = ? AND id = ? AND run_id != ''
UNION
SELECT d.run_id FROM agent_dispatches d
JOIN agent_jobs j ON j.id = d.job_id AND j.user_id = d.user_id
WHERE d.user_id = ? AND j.owner_message_id = ? AND d.run_id != ''`,
		userID, messageID, userID, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// PromptRunsOpen reports whether any run covered by this message is still queued, streaming, or waiting.
func (s *Store) PromptRunsOpen(ctx context.Context, userID, messageID string) (bool, error) {
	ids, err := s.PromptRunIDs(ctx, userID, messageID)
	if err != nil {
		return false, err
	}
	if len(ids) == 0 {
		return false, nil
	}
	q, args := inClause(`SELECT COUNT(*) FROM runs WHERE user_id = ? AND status IN ('queued', 'streaming', 'waiting_approval') AND id IN (`, userID, ids)
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListPendingApprovalsForRuns returns the caller's pending web approvals on those runs.
func (s *Store) ListPendingApprovalsForRuns(ctx context.Context, userID string, runIDs []string) ([]Approval, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	if len(runIDs) == 0 {
		return nil, nil
	}
	q, args := inClause(approvalSelect+` WHERE a.user_id = ? AND a.status = 'pending' AND a.origin = 'web' AND a.run_id IN (`, userID, runIDs)
	q += ` ORDER BY a.created_at, a.id`
	return s.listApprovals(ctx, q, args...)
}

// ListPendingConnectForRuns returns the caller's pending connection cards on those runs.
// A card with an empty run id is not included.
func (s *Store) ListPendingConnectForRuns(ctx context.Context, userID string, runIDs []string) ([]ConnectApproval, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	if len(runIDs) == 0 {
		return nil, nil
	}
	q, args := inClause(connectApprSelect+` WHERE user_id = ? AND status = 'pending' AND run_id != '' AND run_id IN (`, userID, runIDs)
	q += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
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

func inClause(prefix, userID string, ids []string) (string, []any) {
	var b strings.Builder
	b.WriteString(prefix)
	args := []any{userID}
	for i, id := range ids {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("?")
		args = append(args, id)
	}
	b.WriteString(")")
	return b.String(), args
}

// MarkApprovalVia records how a decided approval was granted.
func (s *Store) MarkApprovalVia(ctx context.Context, userID, id, via string) error {
	if via != GrantVia && via != "" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE approvals SET via = ? WHERE id = ? AND user_id = ?`, via, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// WriteGrantAudit records one approval.approved row for Always approve.
// The detail names the prompt grant and does not include argument values.
// A second call for the same approval does not add a row.
func (s *Store) WriteGrantAudit(ctx context.Context, userID, id, actorKind, actorID string) error {
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
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE user_id = ? AND target = ? AND action = 'approval.approved'`, userID, "approval:"+id).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if err := s.insertAudit(ctx, tx, row.WorkspaceID, userID, actorKind, actorID, "approval.approved", "approval:"+id, grantAuditDetail(row.Tool, row.RequestID)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func grantAuditDetail(tool, requestID string) string {
	raw, err := json.Marshal(map[string]string{
		"tool":       tool,
		"request_id": requestID,
		"via":        GrantVia,
		"note":       GrantNote,
	})
	if err != nil {
		return `{"via":"prompt","note":"Always approve for this prompt"}`
	}
	return string(raw)
}

// GrantConnectApproval approves one pending connection card for this prompt.
// claimed is false when the card was already decided. The audit has no secret values.
func (s *Store) GrantConnectApproval(ctx context.Context, userID, id string) (ConnectApproval, bool, error) {
	stamp := s.stamp(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE connect_approvals SET status = 'approved', decided_at = ?, via = ? WHERE id = ? AND user_id = ? AND status = 'pending'`,
		stamp, GrantVia, id, userID)
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
		if err := s.RecordConnectAudit(ctx, userID, "connection.approved", row.AgentID, map[string]string{
			"connection": row.Connection,
			"method":     row.Method,
			"path":       row.Path,
			"outcome":    "approved",
			"via":        GrantVia,
			"note":       GrantNote,
		}); err != nil {
			return ConnectApproval{}, false, err
		}
	}
	return row, n == 1, nil
}
