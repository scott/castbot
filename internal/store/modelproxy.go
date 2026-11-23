package store

import (
	"context"
	"database/sql"
)

// migrateV36 records that a machine calls models through the host.
// Existing rows and a newly created machine stay off.
func (s *Store) migrateV36() error {
	if err := s.ensureColumn("agent_vms", "model_proxy", `ALTER TABLE agent_vms ADD COLUMN model_proxy INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (36) ON CONFLICT(version) DO NOTHING`)
	return err
}

// ModelProxy reports whether this user's machine has called models through the host.
func (s *Store) ModelProxy(ctx context.Context, userID, agentID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT model_proxy FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n != 0, nil
}

// MarkModelProxy records that this user's live machine calls models through the host.
// Another user's row is left unchanged. The bool is false when no row was updated.
func (s *Store) MarkModelProxy(ctx context.Context, userID, agentID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE agent_vms SET model_proxy = 1 WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
