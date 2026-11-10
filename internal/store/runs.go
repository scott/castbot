package store

import (
	"context"
	"database/sql"
	"strings"
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
	msgID, err := newID()
	if err != nil {
		return Message{}, RunEvent{}, err
	}
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, run_id, created_at) VALUES (?, ?, ?, ?, 'agent', ?, ?, ?, ?)`,
		msgID, run.WorkspaceID, userID, run.ThreadID, run.AgentID, body, runID, stamp); err != nil {
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
