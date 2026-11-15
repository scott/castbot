package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// StoppedLine is the thread note after the owner stops a reply.
const StoppedLine = "You stopped the reply."

// StopRun marks one open run stopped and keeps the text already written.
// started is false while the run is still queued, and true once the guest
// has been asked to stop. The bool is false when the status no longer
// matches, and nothing is written. Text is the partial reply kept in the
// thread. It is empty when none was written.
func (s *Store) StopRun(ctx context.Context, userID, runID string, started bool) (Run, string, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, "", false, err
	}
	defer tx.Rollback()
	run, err := s.lockRun(ctx, tx, userID, runID)
	if err != nil {
		return Run{}, "", false, err
	}
	if !stopStatus(run.Status, started) {
		return run, "", false, nil
	}
	text, err := s.runTextTx(ctx, tx, runID)
	if err != nil {
		return Run{}, "", false, err
	}
	stamp := s.stamp(s.Now())
	if strings.TrimSpace(text) != "" {
		if err := s.insertRunMessage(ctx, tx, run, "agent", run.AgentID, text, "web", stamp); err != nil {
			return Run{}, "", false, err
		}
	}
	if err := s.insertRunMessage(ctx, tx, run, "system", run.AgentID, StoppedLine, "notice", stamp); err != nil {
		return Run{}, "", false, err
	}
	clause := `status = 'queued'`
	if started {
		clause = `status IN ('streaming', 'waiting_approval')`
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'stopped', finished_at = ?, error = '' WHERE id = ? AND user_id = ? AND `+clause,
		stamp, runID, userID)
	if err != nil {
		return Run{}, "", false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Run{}, "", false, err
	}
	if n != 1 {
		return run, "", false, nil
	}
	if err := s.settleStopApprovals(ctx, tx, userID, runID, stamp); err != nil {
		return Run{}, "", false, err
	}
	if runID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE connect_approvals SET status = 'expired', decided_at = ? WHERE user_id = ? AND run_id = ? AND status IN ('pending', 'approved')`,
			stamp, userID, runID); err != nil {
			return Run{}, "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Run{}, "", false, err
	}
	fresh, err := s.GetRun(ctx, userID, runID)
	if err != nil {
		run.Status = "stopped"
		run.FinishedAt = stamp
		run.Error = ""
		return run, text, true, nil
	}
	return fresh, text, true, nil
}

// RunText returns the partial reply stored on one of the caller's runs.
func (s *Store) RunText(ctx context.Context, userID, runID string) (string, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return "", err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload_json FROM run_events WHERE run_id = ? AND kind = 'text' ORDER BY seq`, runID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	return joinRunText(rows)
}

func stopStatus(status string, started bool) bool {
	if started {
		return status == "streaming" || status == "waiting_approval"
	}
	return status == "queued"
}

func (s *Store) runTextTx(ctx context.Context, tx *sql.Tx, runID string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM run_events WHERE run_id = ? AND kind = 'text' ORDER BY seq`, runID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	return joinRunText(rows)
}

func joinRunText(rows *sql.Rows) (string, error) {
	var b strings.Builder
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return "", err
		}
		var body struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(payload), &body) != nil {
			continue
		}
		b.WriteString(body.Text)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return clipReply(b.String()), nil
}

func clipReply(body string) string {
	if len(body) <= replyLimit {
		return body
	}
	cut := replyLimit
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return body[:cut]
}

func (s *Store) insertRunMessage(ctx context.Context, tx *sql.Tx, run Run, authorKind, authorID, body, origin, stamp string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, run_id, origin, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, run.WorkspaceID, run.UserID, run.ThreadID, authorKind, authorID, body, run.ID, origin, stamp)
	return err
}

// settleStopApprovals marks this run's open web approvals inactive.
// sent is true so a restart does not post Deny, and the audit is not an approval.
func (s *Store) settleStopApprovals(ctx context.Context, tx *sql.Tx, userID, runID, stamp string) error {
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.workspace_id, r.agent_id, a.tool, a.request_id, a.arguments_json, a.redacted_json FROM approvals a JOIN runs r ON r.id = a.run_id AND r.user_id = a.user_id WHERE a.user_id = ? AND a.run_id = ? AND a.status = 'pending' AND a.origin = 'web'`, userID, runID)
	if err != nil {
		return err
	}
	type left struct {
		id, workspace, agent, tool, request, args, redacted string
	}
	var found []left
	for rows.Next() {
		var row left
		if err := rows.Scan(&row.id, &row.workspace, &row.agent, &row.tool, &row.request, &row.args, &row.redacted); err != nil {
			rows.Close()
			return err
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	seen := map[string]bool{}
	for _, row := range found {
		res, err := tx.ExecContext(ctx, `UPDATE approvals SET status = 'inactive', decided_by = 'system:stop', decided_at = ?, sent = 1 WHERE id = ? AND user_id = ? AND status = 'pending'`, stamp, row.id, userID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			continue
		}
		if err := s.insertAudit(ctx, tx, row.workspace, userID, "system", "system:stop", "approval.inactive", "approval:"+row.id, auditDetail(row.tool, row.request, json.RawMessage(row.args), decodeList(row.redacted))); err != nil {
			return err
		}
		seen[row.agent] = true
	}
	for agentID := range seen {
		if err := s.syncApprovalCount(ctx, tx, userID, agentID); err != nil {
			return err
		}
	}
	return nil
}
