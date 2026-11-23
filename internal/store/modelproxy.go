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

// ModelProxy reports whether this machine has called models through the host.
// vmID is the calling machine. An empty id reads the live row, which is the
// token that was not bound to one machine.
func (s *Store) ModelProxy(ctx context.Context, userID, agentID, vmID string) (bool, error) {
	q := `SELECT model_proxy FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`
	args := []any{userID, agentID}
	if vmID != "" {
		q += ` AND id = ?`
		args = append(args, vmID)
	}
	var n int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n != 0, nil
}

// MarkModelProxy records that the calling machine calls models through the host.
// vmID limits the update to that row. Another machine, including one a failed
// replacement did not replace, is left unchanged. The bool is false when no
// row was updated.
func (s *Store) MarkModelProxy(ctx context.Context, userID, agentID, vmID string) (bool, error) {
	q := `UPDATE agent_vms SET model_proxy = 1 WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`
	args := []any{userID, agentID}
	if vmID != "" {
		q += ` AND id = ?`
		args = append(args, vmID)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
