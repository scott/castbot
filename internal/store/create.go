package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// CreateDraft is one unfinished new-agent chat. It belongs to one user.
type CreateDraft struct {
	ID          string
	UserID      string
	WorkspaceID string
	Payload     string
	CreatedAt   string
	UpdatedAt   string
}

// migrateV19 adds greeted_at and the create draft table.
// Existing agents are marked greeted so this slice does not say hello to them.
// The backfill runs only when the column is first added, so a later reopen
// does not greet an agent that is still waiting to say hello.
func (s *Store) migrateV19() error {
	missing, err := s.columnMissing("agents", "greeted_at")
	if err != nil {
		return err
	}
	if err := s.ensureColumn("agents", "greeted_at", `ALTER TABLE agents ADD COLUMN greeted_at TEXT`); err != nil {
		return err
	}
	if missing {
		if _, err := s.db.Exec(`UPDATE agents SET greeted_at = created_at WHERE greeted_at IS NULL OR greeted_at = ''`); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS create_drafts (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  payload TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_create_drafts_user ON create_drafts(user_id)`); err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (19) ON CONFLICT(version) DO NOTHING`)
	return err
}

// AgentGreeted returns the greeted_at stamp. Empty means the greeting has not run.
func (s *Store) AgentGreeted(ctx context.Context, userID, agentID string) (string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", err
	}
	var greeted sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT greeted_at FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&greeted)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(greeted.String), nil
}

// GreetWrite is the greeting stored with the run that says hello.
type GreetWrite struct {
	ThreadID string
	Body     string
	AskJob   bool
}

// BeginGreet marks the agent greeted and inserts the greet run in one transaction.
// A second call returns started=false and does not insert another run.
// The greeting message is stored in the same transaction so a crash cannot
// start a second hello.
func (s *Store) BeginGreet(ctx context.Context, userID, agentID string, in GreetWrite) (Run, bool, error) {
	body := strings.TrimSpace(in.Body)
	if body == "" || len(body) > 8000 || in.ThreadID == "" {
		return Run{}, false, ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return Run{}, false, err
	}
	th, err := s.GetThread(ctx, userID, in.ThreadID)
	if err != nil {
		return Run{}, false, err
	}
	if th.AgentID != agentID {
		return Run{}, false, ErrNotFound
	}
	runID, err := newID()
	if err != nil {
		return Run{}, false, err
	}
	msgID, err := newID()
	if err != nil {
		return Run{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback()
	var greeted sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT greeted_at FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&greeted)
	if err != nil {
		return Run{}, false, scanErr(err)
	}
	if strings.TrimSpace(greeted.String) != "" {
		if err := tx.Commit(); err != nil {
			return Run{}, false, err
		}
		return Run{}, false, nil
	}
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE agents SET greeted_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND (greeted_at IS NULL OR greeted_at = '')`, stamp, agentID, userID)
	if err != nil {
		return Run{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Run{}, false, err
	}
	if n == 0 {
		if err := tx.Commit(); err != nil {
			return Run{}, false, err
		}
		return Run{}, false, nil
	}
	if in.AskJob {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET activity = 'waiting for a job' WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID); err != nil {
			return Run{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runs(id, workspace_id, thread_id, agent_id, user_id, status, stream_offset, started_at, error) VALUES (?, ?, ?, ?, ?, 'queued', 0, ?, '')`,
		runID, agent.WorkspaceID, th.ID, agentID, userID, stamp); err != nil {
		return Run{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO messages(id, workspace_id, user_id, thread_id, author_kind, author_id, body_md, origin, run_id, created_at) VALUES (?, ?, ?, ?, 'agent', ?, ?, 'greet', ?, ?)`,
		msgID, agent.WorkspaceID, userID, th.ID, agentID, body, runID, stamp); err != nil {
		return Run{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE messages SET origin = 'web' WHERE user_id = ? AND thread_id = ? AND origin = 'waiting'`, userID, th.ID); err != nil {
		return Run{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, false, err
	}
	run, err := s.GetRun(ctx, userID, runID)
	return run, true, err
}

// SettleGreetRun marks a greet run done without adding another message.
// The greeting text is already stored. A failed stream must not look like a second hello.
func (s *Store) SettleGreetRun(ctx context.Context, userID, runID string) error {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return err
	}
	stamp := s.stamp(s.Now())
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET status = 'done', error = '', finished_at = COALESCE(NULLIF(finished_at, ''), ?) WHERE id = ? AND user_id = ? AND status != 'done'`, stamp, runID, userID)
	return err
}

// SaveCreateDraft upserts the caller's unfinished create chat.
func (s *Store) SaveCreateDraft(ctx context.Context, userID, payload string) (CreateDraft, error) {
	payload = strings.TrimSpace(payload)
	if payload == "" || len(payload) > 200000 {
		return CreateDraft{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return CreateDraft{}, err
	}
	stamp := s.stamp(s.Now())
	var id string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM create_drafts WHERE user_id = ?`, userID).Scan(&id)
	if err == sql.ErrNoRows {
		id, err = newID()
		if err != nil {
			return CreateDraft{}, err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO create_drafts(id, user_id, workspace_id, payload, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
			id, userID, user.WorkspaceID, payload, stamp, stamp)
	} else if err != nil {
		return CreateDraft{}, err
	} else {
		_, err = s.db.ExecContext(ctx, `UPDATE create_drafts SET payload = ?, updated_at = ? WHERE id = ? AND user_id = ?`, payload, stamp, id, userID)
	}
	if err != nil {
		return CreateDraft{}, err
	}
	return s.CreateDraftByID(ctx, userID, id)
}

// LoadCreateDraft returns the caller's draft. A draft older than 24 hours is dropped.
func (s *Store) LoadCreateDraft(ctx context.Context, userID string) (CreateDraft, error) {
	if err := s.DropStaleCreateDrafts(ctx); err != nil {
		return CreateDraft{}, err
	}
	var d CreateDraft
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, workspace_id, payload, created_at, updated_at FROM create_drafts WHERE user_id = ?`, userID).
		Scan(&d.ID, &d.UserID, &d.WorkspaceID, &d.Payload, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return CreateDraft{}, scanErr(err)
	}
	return d, nil
}

// CreateDraftByID returns one draft owned by the caller.
// Another user's id is ErrNotFound.
func (s *Store) CreateDraftByID(ctx context.Context, userID, id string) (CreateDraft, error) {
	var d CreateDraft
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, workspace_id, payload, created_at, updated_at FROM create_drafts WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&d.ID, &d.UserID, &d.WorkspaceID, &d.Payload, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return CreateDraft{}, scanErr(err)
	}
	return d, nil
}

// DeleteCreateDraft removes the caller's draft.
func (s *Store) DeleteCreateDraft(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM create_drafts WHERE user_id = ?`, userID)
	return err
}

// DropStaleCreateDrafts removes drafts that have not been updated in 24 hours.
func (s *Store) DropStaleCreateDrafts(ctx context.Context) error {
	cutoff := s.stamp(s.Now().Add(-24 * time.Hour))
	_, err := s.db.ExecContext(ctx, `DELETE FROM create_drafts WHERE updated_at < ?`, cutoff)
	return err
}

// DMThread returns the caller's direct-message thread for an agent.
func (s *Store) DMThread(ctx context.Context, userID, agentID string) (Thread, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return Thread{}, err
	}
	var th Thread
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, kind, COALESCE(agent_id, ''), title, cast_session_id, created_at FROM threads WHERE user_id = ? AND agent_id = ? AND kind = 'dm' ORDER BY created_at LIMIT 1`, userID, agentID).
		Scan(&th.ID, &th.WorkspaceID, &th.UserID, &th.Kind, &th.AgentID, &th.Title, &th.CastSessionID, &th.CreatedAt)
	if err != nil {
		return Thread{}, scanErr(err)
	}
	return th, nil
}
