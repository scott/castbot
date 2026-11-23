package store

import (
	"context"
	"database/sql"
	"strings"
)

// MaxSpecialistTurns is the number of specialist runs one owner prompt may start.
const MaxSpecialistTurns = 8

// AgentJob is one owner prompt that a chief may hand to other agents.
type AgentJob struct {
	ID              string
	WorkspaceID     string
	UserID          string
	OwnerThreadID   string
	OwnerMessageID  string
	ChiefAgentID    string
	SpecialistTurns int
}

// AgentBatch is one handoff. Delivery is claimed once so two specialists
// finishing cannot start two follow-ups.
type AgentBatch struct {
	ID                string
	WorkspaceID       string
	UserID            string
	JobID             string
	OwnerThreadID     string
	ChiefAgentID      string
	Prompt            string
	LinksMD           string
	DeliveryMessageID string
	Delivered         bool
}

// AgentDispatch is one specialist run, or a specialist that did not run.
type AgentDispatch struct {
	ID          string
	BatchID     string
	JobID       string
	ThreadID    string
	AgentID     string
	RunID       string
	Status      string
	Task        string
	Note        string
	UserID      string
	WorkspaceID string
}

// EnsureAgentJob returns the job for one owner message, creating it when needed.
// The thread must be the caller's direct message with the chief.
func (s *Store) EnsureAgentJob(ctx context.Context, userID, ownerThreadID, ownerMessageID, chiefID string) (AgentJob, error) {
	th, err := s.GetThread(ctx, userID, ownerThreadID)
	if err != nil {
		return AgentJob{}, err
	}
	if th.Kind != "dm" || th.AgentID != chiefID {
		return AgentJob{}, ErrInvalid
	}
	msg, err := s.GetMessage(ctx, userID, ownerMessageID)
	if err != nil {
		return AgentJob{}, err
	}
	if msg.ThreadID != th.ID {
		return AgentJob{}, ErrNotFound
	}
	if _, err := s.GetAgent(ctx, userID, chiefID); err != nil {
		return AgentJob{}, err
	}
	id, err := newID()
	if err != nil {
		return AgentJob{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_jobs(id, workspace_id, user_id, owner_thread_id, owner_message_id, chief_agent_id, specialist_turns, created_at)
VALUES (?, ?, ?, ?, ?, ?, 0, ?)
ON CONFLICT(user_id, owner_message_id) DO NOTHING`,
		id, th.WorkspaceID, userID, th.ID, msg.ID, chiefID, s.stamp(s.Now()))
	if err != nil {
		return AgentJob{}, err
	}
	return s.JobByMessage(ctx, userID, ownerMessageID)
}

// JobByMessage returns the job for one owner message.
func (s *Store) JobByMessage(ctx context.Context, userID, messageID string) (AgentJob, error) {
	return s.scanJob(ctx, `SELECT id, workspace_id, user_id, owner_thread_id, owner_message_id, chief_agent_id, specialist_turns FROM agent_jobs WHERE user_id = ? AND owner_message_id = ?`, userID, messageID)
}

// JobByID returns one of the caller's jobs.
func (s *Store) JobByID(ctx context.Context, userID, id string) (AgentJob, error) {
	return s.scanJob(ctx, `SELECT id, workspace_id, user_id, owner_thread_id, owner_message_id, chief_agent_id, specialist_turns FROM agent_jobs WHERE user_id = ? AND id = ?`, userID, id)
}

// JobForThread returns the job linked from an agent thread.
func (s *Store) JobForThread(ctx context.Context, userID, threadID string) (AgentJob, error) {
	th, err := s.GetThread(ctx, userID, threadID)
	if err != nil {
		return AgentJob{}, err
	}
	if th.Kind != "agent_dm" && th.Kind != "agent_chat" {
		return AgentJob{}, ErrNotFound
	}
	return s.scanJob(ctx, `SELECT j.id, j.workspace_id, j.user_id, j.owner_thread_id, j.owner_message_id, j.chief_agent_id, j.specialist_turns
FROM agent_jobs j
JOIN threads t ON t.job_id = j.id AND t.user_id = j.user_id
WHERE t.user_id = ? AND t.id = ?`, userID, threadID)
}

func (s *Store) scanJob(ctx context.Context, q string, args ...any) (AgentJob, error) {
	var job AgentJob
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&job.ID, &job.WorkspaceID, &job.UserID, &job.OwnerThreadID, &job.OwnerMessageID, &job.ChiefAgentID, &job.SpecialistTurns)
	if err != nil {
		return AgentJob{}, scanErr(err)
	}
	return job, nil
}

// ClaimSpecialistTurns adds n specialist runs when the job stays within the cap.
// Zero rows means the whole send is rejected. A partial overrun is not stored.
func (s *Store) ClaimSpecialistTurns(ctx context.Context, userID, jobID string, n int) error {
	if n < 1 {
		return ErrInvalid
	}
	if _, err := s.JobByID(ctx, userID, jobID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_jobs SET specialist_turns = specialist_turns + ? WHERE id = ? AND user_id = ? AND specialist_turns + ? <= ?`,
		n, jobID, userID, n, MaxSpecialistTurns)
	if err != nil {
		return err
	}
	got, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if got == 0 {
		return ErrRejected
	}
	return nil
}

// CreateAgentThread inserts an agent thread whose agent_id is the chief.
// Every participant must be one of the caller's agents.
func (s *Store) CreateAgentThread(ctx context.Context, userID, kind, chiefID, title, jobID string, agents []string) (Thread, error) {
	if kind != "agent_dm" && kind != "agent_chat" {
		return Thread{}, ErrInvalid
	}
	if strings.TrimSpace(chiefID) == "" || len(agents) < 2 {
		return Thread{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return Thread{}, err
	}
	if _, err := s.GetAgent(ctx, userID, chiefID); err != nil {
		return Thread{}, err
	}
	seen := map[string]bool{}
	var ids []string
	hasChief := false
	for _, id := range agents {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return Thread{}, ErrInvalid
		}
		if _, err := s.GetAgent(ctx, userID, id); err != nil {
			return Thread{}, err
		}
		seen[id] = true
		if id == chiefID {
			hasChief = true
		}
		ids = append(ids, id)
	}
	if !hasChief {
		return Thread{}, ErrInvalid
	}
	if kind == "agent_dm" && len(ids) != 2 {
		return Thread{}, ErrInvalid
	}
	id, err := newID()
	if err != nil {
		return Thread{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Thread{}, err
	}
	defer tx.Rollback()
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO threads(id, workspace_id, user_id, kind, agent_id, title, job_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, user.WorkspaceID, userID, kind, chiefID, strings.TrimSpace(title), jobID, stamp); err != nil {
		return Thread{}, err
	}
	for _, agentID := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO thread_participants(thread_id, agent_id, user_id, workspace_id) VALUES (?, ?, ?, ?)`,
			id, agentID, userID, user.WorkspaceID); err != nil {
			return Thread{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Thread{}, err
	}
	return s.GetThread(ctx, userID, id)
}

// FindAgentDM returns the direct message for this job and these two agents.
func (s *Store) FindAgentDM(ctx context.Context, userID, jobID, a, b string) (Thread, error) {
	if strings.TrimSpace(jobID) == "" || a == "" || b == "" || a == b {
		return Thread{}, ErrNotFound
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT t.id FROM threads t
WHERE t.user_id = ? AND t.kind = 'agent_dm' AND t.job_id = ?
AND EXISTS (SELECT 1 FROM thread_participants p WHERE p.thread_id = t.id AND p.user_id = t.user_id AND p.agent_id = ?)
AND EXISTS (SELECT 1 FROM thread_participants p WHERE p.thread_id = t.id AND p.user_id = t.user_id AND p.agent_id = ?)
AND (SELECT COUNT(*) FROM thread_participants p WHERE p.thread_id = t.id AND p.user_id = t.user_id) = 2
ORDER BY t.created_at LIMIT 1`, userID, jobID, a, b).Scan(&id)
	if err != nil {
		return Thread{}, scanErr(err)
	}
	return s.GetThread(ctx, userID, id)
}

// IsParticipant reports whether the agent is on one of the caller's threads.
func (s *Store) IsParticipant(ctx context.Context, userID, threadID, agentID string) (bool, error) {
	if _, err := s.GetThread(ctx, userID, threadID); err != nil {
		return false, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_participants WHERE user_id = ? AND thread_id = ? AND agent_id = ?`, userID, threadID, agentID).Scan(&n)
	return n > 0, err
}

// Participants lists agent ids on one of the caller's threads.
func (s *Store) Participants(ctx context.Context, userID, threadID string) ([]string, error) {
	if _, err := s.GetThread(ctx, userID, threadID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM thread_participants WHERE user_id = ? AND thread_id = ? ORDER BY agent_id`, userID, threadID)
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
		out = append(out, id)
	}
	return out, rows.Err()
}

// CreateBatch opens one handoff batch.
func (s *Store) CreateBatch(ctx context.Context, userID, jobID string) (AgentBatch, error) {
	job, err := s.JobByID(ctx, userID, jobID)
	if err != nil {
		return AgentBatch{}, err
	}
	id, err := newID()
	if err != nil {
		return AgentBatch{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_batches(id, workspace_id, user_id, job_id, owner_thread_id, chief_agent_id, prompt, links_md, delivered, delivery_message_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, '', '', 0, '', ?)`,
		id, job.WorkspaceID, userID, job.ID, job.OwnerThreadID, job.ChiefAgentID, s.stamp(s.Now()))
	if err != nil {
		return AgentBatch{}, err
	}
	return s.GetBatch(ctx, userID, id)
}

// GetBatch returns one of the caller's handoff batches.
func (s *Store) GetBatch(ctx context.Context, userID, id string) (AgentBatch, error) {
	var b AgentBatch
	var delivered int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, job_id, owner_thread_id, chief_agent_id, prompt, links_md, delivery_message_id, delivered FROM agent_batches WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&b.ID, &b.WorkspaceID, &b.UserID, &b.JobID, &b.OwnerThreadID, &b.ChiefAgentID, &b.Prompt, &b.LinksMD, &b.DeliveryMessageID, &delivered)
	if err != nil {
		return AgentBatch{}, scanErr(err)
	}
	b.Delivered = delivered != 0
	return b, nil
}

// SetBatchLinks stores the markdown links for the owner-facing answer.
func (s *Store) SetBatchLinks(ctx context.Context, userID, batchID, links string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_batches SET links_md = ? WHERE id = ? AND user_id = ?`, links, batchID, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// SetBatchPrompt stores the follow-up text sent to the chief.
func (s *Store) SetBatchPrompt(ctx context.Context, userID, batchID, prompt string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_batches SET prompt = ? WHERE id = ? AND user_id = ?`, prompt, batchID, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// SetBatchDeliveryMessage records the owner-thread note for this follow-up.
func (s *Store) SetBatchDeliveryMessage(ctx context.Context, userID, batchID, messageID string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_batches SET delivery_message_id = ? WHERE id = ? AND user_id = ?`, messageID, batchID, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// ClaimBatchDelivery marks the batch delivered once.
// The second caller gets ok false.
func (s *Store) ClaimBatchDelivery(ctx context.Context, userID, batchID string) (AgentBatch, bool, error) {
	if _, err := s.GetBatch(ctx, userID, batchID); err != nil {
		return AgentBatch{}, false, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_batches SET delivered = 1 WHERE id = ? AND user_id = ? AND delivered = 0`, batchID, userID)
	if err != nil {
		return AgentBatch{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return AgentBatch{}, false, err
	}
	batch, err := s.GetBatch(ctx, userID, batchID)
	if err != nil {
		return AgentBatch{}, false, err
	}
	return batch, n == 1, nil
}

// BatchByDelivery returns the batch whose follow-up note is this message.
func (s *Store) BatchByDelivery(ctx context.Context, userID, messageID string) (AgentBatch, error) {
	if strings.TrimSpace(messageID) == "" {
		return AgentBatch{}, ErrNotFound
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM agent_batches WHERE user_id = ? AND delivery_message_id = ?`, userID, messageID).Scan(&id)
	if err != nil {
		return AgentBatch{}, scanErr(err)
	}
	return s.GetBatch(ctx, userID, id)
}

// AddDispatch records one specialist on a batch.
func (s *Store) AddDispatch(ctx context.Context, userID, batchID, threadID, agentID, runID, status, task, note string) (AgentDispatch, error) {
	switch status {
	case "running", "done", "blocked", "rejected":
	default:
		return AgentDispatch{}, ErrInvalid
	}
	batch, err := s.GetBatch(ctx, userID, batchID)
	if err != nil {
		return AgentDispatch{}, err
	}
	if _, err := s.GetThread(ctx, userID, threadID); err != nil {
		return AgentDispatch{}, err
	}
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return AgentDispatch{}, err
	}
	id, err := newID()
	if err != nil {
		return AgentDispatch{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_dispatches(id, batch_id, job_id, thread_id, agent_id, run_id, status, task, note, user_id, workspace_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, batch.ID, batch.JobID, threadID, agentID, runID, status, task, note, userID, batch.WorkspaceID, s.stamp(s.Now()))
	if err != nil {
		return AgentDispatch{}, err
	}
	return s.dispatchByID(ctx, userID, id)
}

// DispatchByRun returns the dispatch for one run.
func (s *Store) DispatchByRun(ctx context.Context, userID, runID string) (AgentDispatch, error) {
	if strings.TrimSpace(runID) == "" {
		return AgentDispatch{}, ErrNotFound
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM agent_dispatches WHERE user_id = ? AND run_id = ?`, userID, runID).Scan(&id)
	if err != nil {
		return AgentDispatch{}, scanErr(err)
	}
	return s.dispatchByID(ctx, userID, id)
}

// ListDispatches returns the dispatches on one of the caller's batches.
func (s *Store) ListDispatches(ctx context.Context, userID, batchID string) ([]AgentDispatch, error) {
	if _, err := s.GetBatch(ctx, userID, batchID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM agent_dispatches WHERE user_id = ? AND batch_id = ? ORDER BY created_at, id`, userID, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []AgentDispatch
	for _, id := range ids {
		row, err := s.dispatchByID(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

// MarkDispatch moves a running dispatch to a terminal status.
// ok is false when this run was already finished.
func (s *Store) MarkDispatch(ctx context.Context, userID, runID, status, note string) (bool, error) {
	switch status {
	case "done", "blocked", "rejected":
	default:
		return false, ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_dispatches SET status = ?, note = ? WHERE user_id = ? AND run_id = ? AND status = 'running'`,
		status, note, userID, runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// CountRunning counts dispatches on a batch that have not finished.
func (s *Store) CountRunning(ctx context.Context, userID, batchID string) (int, error) {
	if _, err := s.GetBatch(ctx, userID, batchID); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_dispatches WHERE user_id = ? AND batch_id = ? AND status = 'running'`, userID, batchID).Scan(&n)
	return n, err
}

// AgentOnJob reports whether this agent was handed work for the job.
func (s *Store) AgentOnJob(ctx context.Context, userID, jobID, agentID string) (bool, error) {
	if _, err := s.JobByID(ctx, userID, jobID); err != nil {
		return false, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_dispatches WHERE user_id = ? AND job_id = ? AND agent_id = ?`, userID, jobID, agentID).Scan(&n)
	return n > 0, err
}

func (s *Store) dispatchByID(ctx context.Context, userID, id string) (AgentDispatch, error) {
	var d AgentDispatch
	err := s.db.QueryRowContext(ctx, `SELECT id, batch_id, job_id, thread_id, agent_id, run_id, status, task, note, user_id, workspace_id FROM agent_dispatches WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&d.ID, &d.BatchID, &d.JobID, &d.ThreadID, &d.AgentID, &d.RunID, &d.Status, &d.Task, &d.Note, &d.UserID, &d.WorkspaceID)
	if err != nil {
		return AgentDispatch{}, scanErr(err)
	}
	return d, nil
}

// LatestAgentReply returns the newest agent message stored for a run.
func (s *Store) LatestAgentReply(ctx context.Context, userID, runID string) (Message, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return Message{}, err
	}
	var m Message
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, steps_md, origin, journal_seq, created_at
FROM messages WHERE user_id = ? AND run_id = ? AND author_kind = 'agent'
ORDER BY created_at DESC, rowid DESC LIMIT 1`, userID, runID).
		Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.ThreadID, &m.AuthorKind, &m.AuthorID, &m.Body, &m.Steps, &m.Origin, &seq, &m.CreatedAt)
	if seq.Valid {
		m.JournalSeq = int(seq.Int64)
	}
	if err != nil {
		return Message{}, scanErr(err)
	}
	return m, nil
}

// LinkedMessage returns the message that started a run.
// A user message wins. Otherwise the oldest linked row is the prompt.
func (s *Store) LinkedMessage(ctx context.Context, userID, runID string) (Message, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return Message{}, err
	}
	var m Message
	var seq sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, origin, journal_seq, created_at
FROM messages WHERE user_id = ? AND run_id = ?
ORDER BY CASE author_kind WHEN 'user' THEN 0 WHEN 'system' THEN 1 ELSE 2 END, created_at, rowid
LIMIT 1`, userID, runID).
		Scan(&m.ID, &m.WorkspaceID, &m.UserID, &m.ThreadID, &m.AuthorKind, &m.AuthorID, &m.Body, &m.Origin, &seq, &m.CreatedAt)
	if seq.Valid {
		m.JournalSeq = int(seq.Int64)
	}
	if err != nil {
		return Message{}, scanErr(err)
	}
	return m, nil
}
