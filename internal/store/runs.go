package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Run is one agent reply. UserID is the owner.
type Run struct {
	ID           string
	WorkspaceID  string
	ThreadID     string
	AgentID      string
	UserID       string
	RoutineRunID string
	Status       string
	StreamOffset int
	StartedAt    string
	FinishedAt   string
	Error        string
}

// RunEvent is one durable event for a run. Seq is monotonic per run.
// The row has no user id of its own. Callers reach it through a run
// that was loaded with the caller's user id.
type RunEvent struct {
	RunID     string
	Seq       int
	Kind      string
	Payload   string
	CreatedAt string
}

const replyLimit = 200000

// CreateRun inserts a queued run for one of the caller's threads.
func (s *Store) CreateRun(ctx context.Context, userID, threadID, agentID string) (Run, error) {
	th, err := s.GetThread(ctx, userID, threadID)
	if err != nil {
		return Run{}, err
	}
	if th.AgentID != agentID {
		return Run{}, ErrNotFound
	}
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return Run{}, err
	}
	id, err := newID()
	if err != nil {
		return Run{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id, workspace_id, thread_id, agent_id, user_id, status, stream_offset, started_at, error) VALUES (?, ?, ?, ?, ?, 'queued', 0, ?, '')`,
		id, th.WorkspaceID, threadID, agentID, userID, s.stamp(s.Now()))
	if err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, userID, id)
}

// GetRun returns one of the caller's runs.
func (s *Store) GetRun(ctx context.Context, userID, id string) (Run, error) {
	var run Run
	var routine, finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, thread_id, agent_id, user_id, routine_run_id, status, stream_offset, started_at, finished_at, error FROM runs WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&run.ID, &run.WorkspaceID, &run.ThreadID, &run.AgentID, &run.UserID, &routine, &run.Status, &run.StreamOffset, &run.StartedAt, &finished, &run.Error)
	if err != nil {
		return Run{}, scanErr(err)
	}
	run.RoutineRunID = routine.String
	run.FinishedAt = finished.String
	return run, nil
}

// ListThreadRuns returns the caller's runs in a thread, oldest first.
func (s *Store) ListThreadRuns(ctx context.Context, userID, threadID string) ([]Run, error) {
	if _, err := s.GetThread(ctx, userID, threadID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, thread_id, agent_id, user_id, routine_run_id, status, stream_offset, started_at, finished_at, error FROM runs WHERE user_id = ? AND thread_id = ? ORDER BY started_at, rowid`, userID, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var run Run
		var routine, finished sql.NullString
		if err := rows.Scan(&run.ID, &run.WorkspaceID, &run.ThreadID, &run.AgentID, &run.UserID, &routine, &run.Status, &run.StreamOffset, &run.StartedAt, &finished, &run.Error); err != nil {
			return nil, err
		}
		run.RoutineRunID = routine.String
		run.FinishedAt = finished.String
		out = append(out, run)
	}
	return out, rows.Err()
}

// MarkStreaming moves a queued run to streaming.
func (s *Store) MarkStreaming(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET status = 'streaming' WHERE id = ? AND user_id = ? AND status = 'queued'`, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// AppendRunEvent stores the next event and the runtime stream offset.
// Seq starts at 1 and increases by one for each event on the run.
func (s *Store) AppendRunEvent(ctx context.Context, userID, runID, kind, payload string, offset int) (RunEvent, error) {
	switch kind {
	case "text", "permission", "tool", "error", "done":
	default:
		return RunEvent{}, ErrInvalid
	}
	if payload == "" || len(payload) > replyLimit {
		return RunEvent{}, ErrInvalid
	}
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return RunEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunEvent{}, err
	}
	defer tx.Rollback()
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = ?`, runID).Scan(&seq); err != nil {
		return RunEvent{}, err
	}
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_events(run_id, seq, kind, payload_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		runID, seq, kind, payload, stamp); err != nil {
		return RunEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET stream_offset = ? WHERE id = ? AND user_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')`,
		offset, runID, userID); err != nil {
		return RunEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunEvent{}, err
	}
	return RunEvent{RunID: runID, Seq: seq, Kind: kind, Payload: payload, CreatedAt: stamp}, nil
}

// ListRunEvents returns events with seq greater than after for one of the caller's runs.
func (s *Store) ListRunEvents(ctx context.Context, userID, runID string, after int) ([]RunEvent, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, seq, kind, payload_json, created_at FROM run_events WHERE run_id = ? AND seq > ? ORDER BY seq`, runID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunEvent
	for rows.Next() {
		var ev RunEvent
		if err := rows.Scan(&ev.RunID, &ev.Seq, &ev.Kind, &ev.Payload, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// FinishRun stores the reply and marks the run done in one transaction.
func (s *Store) FinishRun(ctx context.Context, userID, runID, body string, offset int) (Message, RunEvent, error) {
	if strings.TrimSpace(body) == "" || len(body) > replyLimit {
		return Message{}, RunEvent{}, ErrInvalid
	}
	run, err := s.GetRun(ctx, userID, runID)
	if err != nil {
		return Message{}, RunEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, RunEvent{}, err
	}
	defer tx.Rollback()
	stamp := s.stamp(s.Now())
	var msgID string
	err = tx.QueryRowContext(ctx, `SELECT m.id FROM messages m
JOIN journal_entries j ON j.user_id = m.user_id AND j.agent_id = ? AND j.seq = m.journal_seq AND j.kind = 'reply' AND j.stream_id = ?
WHERE m.user_id = ? AND m.thread_id = ? AND m.author_kind = 'agent' AND m.journal_seq IS NOT NULL AND m.run_id IS NULL
ORDER BY m.journal_seq LIMIT 1`,
		run.AgentID, run.ID, userID, run.ThreadID).Scan(&msgID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Message{}, RunEvent{}, err
	}
	if msgID != "" {
		res, err := tx.ExecContext(ctx, `UPDATE messages SET run_id = ? WHERE id = ? AND user_id = ? AND run_id IS NULL`, runID, msgID, userID)
		if err != nil {
			return Message{}, RunEvent{}, err
		}
		if err := rowsAffected(res); err != nil {
			return Message{}, RunEvent{}, err
		}
	} else {
		msgID, err = newID()
		if err != nil {
			return Message{}, RunEvent{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, run_id, created_at) VALUES (?, ?, ?, ?, 'agent', ?, ?, ?, ?)`,
			msgID, run.WorkspaceID, userID, run.ThreadID, run.AgentID, body, runID, stamp); err != nil {
			return Message{}, RunEvent{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET journal_seq = (
  SELECT j.seq FROM journal_entries j
  WHERE j.user_id = ? AND j.agent_id = ? AND j.kind = 'reply' AND j.stream_id = ?
    AND NOT EXISTS (SELECT 1 FROM messages other WHERE other.user_id = j.user_id AND other.thread_id = ? AND other.journal_seq = j.seq)
  ORDER BY j.seq LIMIT 1
) WHERE id = ? AND user_id = ? AND journal_seq IS NULL AND EXISTS (
  SELECT 1 FROM journal_entries j
  WHERE j.user_id = ? AND j.agent_id = ? AND j.kind = 'reply' AND j.stream_id = ?
    AND NOT EXISTS (SELECT 1 FROM messages other WHERE other.user_id = j.user_id AND other.thread_id = ? AND other.journal_seq = j.seq)
)`, userID, run.AgentID, run.ID, run.ThreadID, msgID, userID, userID, run.AgentID, run.ID, run.ThreadID); err != nil {
		return Message{}, RunEvent{}, err
	}
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = ?`, runID).Scan(&seq); err != nil {
		return Message{}, RunEvent{}, err
	}
	payload, err := jsonString(map[string]string{"messageId": msgID})
	if err != nil {
		return Message{}, RunEvent{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_events(run_id, seq, kind, payload_json, created_at) VALUES (?, ?, 'done', ?, ?)`,
		runID, seq, payload, stamp); err != nil {
		return Message{}, RunEvent{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'done', stream_offset = ?, finished_at = ?, error = '' WHERE id = ? AND user_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')`,
		offset, stamp, runID, userID)
	if err != nil {
		return Message{}, RunEvent{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Message{}, RunEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return Message{}, RunEvent{}, err
	}
	msg, err := s.GetMessage(ctx, userID, msgID)
	if err != nil {
		return Message{}, RunEvent{}, err
	}
	return msg, RunEvent{RunID: runID, Seq: seq, Kind: "done", Payload: payload, CreatedAt: stamp}, nil
}

// FailRun records an error event and marks the run failed.
func (s *Store) FailRun(ctx context.Context, userID, runID, message string, offset int) (RunEvent, error) {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 400 {
		return RunEvent{}, ErrInvalid
	}
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return RunEvent{}, err
	}
	payload, err := jsonString(map[string]string{"message": message})
	if err != nil {
		return RunEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunEvent{}, err
	}
	defer tx.Rollback()
	var seq int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = ?`, runID).Scan(&seq); err != nil {
		return RunEvent{}, err
	}
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_events(run_id, seq, kind, payload_json, created_at) VALUES (?, ?, 'error', ?, ?)`,
		runID, seq, payload, stamp); err != nil {
		return RunEvent{}, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'failed', stream_offset = ?, finished_at = ?, error = ? WHERE id = ? AND user_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')`,
		offset, stamp, message, runID, userID)
	if err != nil {
		return RunEvent{}, err
	}
	if err := rowsAffected(res); err != nil {
		return RunEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunEvent{}, err
	}
	return RunEvent{RunID: runID, Seq: seq, Kind: "error", Payload: payload, CreatedAt: stamp}, nil
}

// RetryRun claims a failed run and creates its replacement in one transaction.
// A second caller sees no failed row and gets ErrConflict.
func (s *Store) RetryRun(ctx context.Context, userID, runID string) (Run, Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, Message{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'retried' WHERE id = ? AND user_id = ? AND status = 'failed'`, runID, userID)
	if err != nil {
		return Run{}, Message{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Run{}, Message{}, err
	}
	if n != 1 {
		return Run{}, Message{}, ErrConflict
	}
	var prev Run
	var routine, finished sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, workspace_id, thread_id, agent_id, user_id, routine_run_id, status, stream_offset, started_at, finished_at, error FROM runs WHERE id = ? AND user_id = ?`, runID, userID).
		Scan(&prev.ID, &prev.WorkspaceID, &prev.ThreadID, &prev.AgentID, &prev.UserID, &routine, &prev.Status, &prev.StreamOffset, &prev.StartedAt, &finished, &prev.Error)
	if err != nil {
		return Run{}, Message{}, scanErr(err)
	}
	var msg Message
	err = tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, created_at FROM messages WHERE user_id = ? AND run_id = ? AND author_kind = 'user' ORDER BY created_at, rowid LIMIT 1`, userID, runID).
		Scan(&msg.ID, &msg.WorkspaceID, &msg.UserID, &msg.ThreadID, &msg.AuthorKind, &msg.AuthorID, &msg.Body, &msg.CreatedAt)
	if err != nil {
		return Run{}, Message{}, scanErr(err)
	}
	id, err := newID()
	if err != nil {
		return Run{}, Message{}, err
	}
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id, workspace_id, thread_id, agent_id, user_id, status, stream_offset, started_at, error) VALUES (?, ?, ?, ?, ?, 'queued', 0, ?, '')`,
		id, prev.WorkspaceID, prev.ThreadID, prev.AgentID, userID, stamp); err != nil {
		return Run{}, Message{}, err
	}
	moved, err := tx.ExecContext(ctx, `UPDATE messages SET run_id = ? WHERE id = ? AND user_id = ?`, id, msg.ID, userID)
	if err != nil {
		return Run{}, Message{}, err
	}
	if err := rowsAffected(moved); err != nil {
		return Run{}, Message{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, Message{}, err
	}
	next, err := s.GetRun(ctx, userID, id)
	if err != nil {
		return Run{}, Message{}, err
	}
	return next, msg, nil
}

// FailAbandoned marks every queued, streaming, and waiting_approval run failed
// and writes the error event in the same transaction. Startup uses it so a
// restarted process does not leave a bubble on Replying.
func (s *Store) FailAbandoned(ctx context.Context, message string) error {
	message = strings.TrimSpace(message)
	if message == "" || len(message) > 400 {
		return ErrInvalid
	}
	payload, err := jsonString(map[string]string{"message": message})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id, user_id, stream_offset FROM runs WHERE status IN ('queued', 'streaming', 'waiting_approval')`)
	if err != nil {
		return err
	}
	type left struct {
		id     string
		userID string
		offset int
	}
	var pending []left
	for rows.Next() {
		var row left
		if err := rows.Scan(&row.id, &row.userID, &row.offset); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	stamp := s.stamp(s.Now())
	for _, row := range pending {
		var seq int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM run_events WHERE run_id = ?`, row.id).Scan(&seq); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO run_events(run_id, seq, kind, payload_json, created_at) VALUES (?, ?, 'error', ?, ?)`,
			row.id, seq, payload, stamp); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'failed', stream_offset = ?, finished_at = ?, error = ? WHERE id = ? AND user_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')`,
			row.offset, stamp, message, row.id, row.userID)
		if err != nil {
			return err
		}
		if err := rowsAffected(res); err != nil {
			return err
		}
		if err := s.abandonApprovals(ctx, tx, row.userID, row.id, stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// abandonApprovals marks a failed run's open web approvals inactive.
// sent stays false so the server can still post Deny when the machine is live.
func (s *Store) abandonApprovals(ctx context.Context, tx *sql.Tx, userID, runID, stamp string) error {
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
		res, err := tx.ExecContext(ctx, `UPDATE approvals SET status = 'inactive', decided_by = 'system:restart', decided_at = ?, sent = 0 WHERE id = ? AND user_id = ? AND status = 'pending'`, stamp, row.id, userID)
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
		if err := s.insertAudit(ctx, tx, row.workspace, userID, "system", "system:restart", "approval.inactive", "approval:"+row.id, auditDetail(row.tool, row.request, json.RawMessage(row.args), decodeList(row.redacted))); err != nil {
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

// DeferRestartLimit is how long a bundle or secret reload will wait for an
// open run or a pending web approval. Past that, the restart proceeds so
// two threads cannot hold a key rotation or a bundle update open.
const DeferRestartLimit = 15 * time.Minute

// DeferGuestRestart reports that a bundle or secret reload should wait.
// A pending web approval, another open run, or a resume of runID would
// restart the guest and drop the prompt. A brand new run with no offset
// does not defer, so the next message can apply the change first.
// reason is why it is waiting, or "bound:" plus that reason when the wait
// has passed DeferRestartLimit and the restart should proceed.
func (s *Store) DeferGuestRestart(ctx context.Context, userID, agentID, runID string) (bool, string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return false, "", err
	}
	reason, oldest, err := s.deferReason(ctx, userID, agentID, runID)
	if err != nil || reason == "" {
		return false, "", err
	}
	if !oldest.IsZero() && !s.Now().Before(oldest.Add(DeferRestartLimit)) {
		return false, "bound:" + reason, nil
	}
	return true, reason, nil
}

func (s *Store) deferReason(ctx context.Context, userID, agentID, runID string) (string, time.Time, error) {
	var reason string
	var oldest time.Time
	consider := func(why, stamp string) error {
		if stamp == "" {
			return nil
		}
		t, err := parseTime(stamp)
		if err != nil {
			return err
		}
		if reason == "" {
			reason = why
		}
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
		return nil
	}
	var created string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(a.created_at), '') FROM approvals a JOIN runs r ON r.id = a.run_id AND r.user_id = a.user_id WHERE a.user_id = ? AND r.agent_id = ? AND a.status = 'pending' AND a.origin = 'web'`, userID, agentID).Scan(&created); err != nil {
		return "", time.Time{}, err
	}
	if err := consider("pending approval", created); err != nil {
		return "", time.Time{}, err
	}
	q := `SELECT COALESCE(MIN(started_at), '') FROM runs WHERE user_id = ? AND agent_id = ? AND status IN ('streaming', 'waiting_approval')`
	args := []any{userID, agentID}
	if runID != "" {
		q += ` AND id != ?`
		args = append(args, runID)
	}
	var started string
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&started); err != nil {
		return "", time.Time{}, err
	}
	if err := consider("another run is open", started); err != nil {
		return "", time.Time{}, err
	}
	if runID != "" {
		var status, runStarted string
		var offset int
		err := s.db.QueryRowContext(ctx, `SELECT status, stream_offset, started_at FROM runs WHERE id = ? AND user_id = ?`, runID, userID).Scan(&status, &offset, &runStarted)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", time.Time{}, err
		}
		if err == nil && (status == "waiting_approval" || offset > 0) {
			if err := consider("this run has already started", runStarted); err != nil {
				return "", time.Time{}, err
			}
		}
	}
	return reason, oldest, nil
}

// SetMessageRun links one of the caller's messages to a run they own.
func (s *Store) SetMessageRun(ctx context.Context, userID, messageID, runID string) error {
	msg, err := s.GetMessage(ctx, userID, messageID)
	if err != nil {
		return err
	}
	run, err := s.GetRun(ctx, userID, runID)
	if err != nil {
		return err
	}
	if msg.ThreadID != run.ThreadID {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET run_id = ? WHERE id = ? AND user_id = ?`, runID, messageID, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// UserMessageForRun returns the user message linked to one of the caller's runs.
func (s *Store) UserMessageForRun(ctx context.Context, userID, runID string) (Message, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return Message{}, err
	}
	var m Message
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, created_at FROM messages WHERE user_id = ? AND run_id = ? AND author_kind = 'user' ORDER BY created_at, rowid LIMIT 1`, userID, runID).
		Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.ThreadID, &m.AuthorKind, &m.AuthorID, &m.Body, &m.CreatedAt)
	if err != nil {
		return Message{}, scanErr(err)
	}
	return m, nil
}
