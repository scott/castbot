package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// RollbackWindow is how long a previous runtime stays offered after a move
// that copied memory. A repair on the same image does not reset it.
const RollbackWindow = 7 * 24 * time.Hour

// RuntimeRollback is the previous runtime image an owner can return to.
// Image is the full reference the replace path boots. The page shows the tag.
type RuntimeRollback struct {
	ID        string
	UserID    string
	AgentID   string
	Image     string
	ExpiresAt string
}

func (s *Store) migrateV28() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS runtime_rollbacks (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  replacement_id TEXT NOT NULL,
  image TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runtime_rollbacks_user ON runtime_rollbacks(user_id, agent_id, created_at)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (28) ON CONFLICT(version) DO NOTHING`)
	return err
}

// settleRollbackTx records the image a move just left, when memory was copied.
// A no-copy move retires any open offer and does not save a new one.
// The same image does not change the offer.
func (s *Store) settleRollbackTx(ctx context.Context, tx *sql.Tx, workspace, userID, agentID, replID, oldImage, newImage string, noCopy bool) error {
	oldImage = strings.TrimSpace(oldImage)
	newImage = strings.TrimSpace(newImage)
	if oldImage == "" || oldImage == newImage {
		return nil
	}
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE runtime_rollbacks SET used_at = ? WHERE user_id = ? AND agent_id = ? AND used_at IS NULL`,
		stamp, userID, agentID); err != nil {
		return err
	}
	if noCopy {
		return nil
	}
	id, err := newID()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_rollbacks(
  id, workspace_id, user_id, agent_id, replacement_id, image, created_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspace, userID, agentID, replID, oldImage, stamp, s.stamp(s.Now().Add(RollbackWindow)))
	return err
}

// NoteRuntimeRollback saves a previous runtime for tests and for a move that
// already committed. The image must differ from the live machine.
func (s *Store) NoteRuntimeRollback(ctx context.Context, userID, agentID, replacementID, image string) error {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	vm, err := s.LiveVM(ctx, userID, agentID)
	if err != nil {
		return err
	}
	replacementID = strings.TrimSpace(replacementID)
	image = strings.TrimSpace(image)
	if replacementID == "" || image == "" || image == vm.Image {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.settleRollbackTx(ctx, tx, agent.WorkspaceID, userID, agentID, replacementID, image, vm.Image, false); err != nil {
		return err
	}
	return tx.Commit()
}

// ActiveRollback returns the owner's open offer for one agent.
// An expired offer is not returned. Another user's agent is not found.
func (s *Store) ActiveRollback(ctx context.Context, userID, agentID string) (RuntimeRollback, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return RuntimeRollback{}, err
	}
	var out RuntimeRollback
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, agent_id, image, expires_at
FROM runtime_rollbacks
WHERE user_id = ? AND agent_id = ? AND used_at IS NULL AND expires_at > ?
ORDER BY created_at DESC LIMIT 1`, userID, agentID, s.stamp(s.Now())).Scan(
		&out.ID, &out.UserID, &out.AgentID, &out.Image, &out.ExpiresAt)
	if err != nil {
		return RuntimeRollback{}, scanErr(err)
	}
	return out, nil
}
