package store

import "context"

// agentUseLayout is a fixed-width UTC timestamp so text order matches time order.
const agentUseLayout = "2006-01-02T15:04:05.000000000Z"

// migrateV30 records when this account last opened an agent or sent it a message.
// A blank value means this account has not used that agent yet.
func (s *Store) migrateV30() error {
	if err := s.ensureColumn("agents", "last_used_at", `ALTER TABLE agents ADD COLUMN last_used_at TEXT`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (30) ON CONFLICT(version) DO NOTHING`)
	return err
}

// TouchAgentUse marks that this account opened the agent or sent a message.
// An agent the caller does not own is ErrNotFound and is left unchanged.
func (s *Store) TouchAgentUse(ctx context.Context, userID, agentID string) error {
	stamp := s.Now().UTC().Format(agentUseLayout)
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET last_used_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, stamp, agentID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var have int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&have); err != nil {
		return err
	}
	if have == 0 {
		return ErrNotFound
	}
	return nil
}

// PreferredAgent is the live agent this account should open.
// Used agents rank by last_used_at. When none have been used, the newest
// created_at wins, then the highest id. No live agent is ErrNotFound.
func (s *Store) PreferredAgent(ctx context.Context, userID string) (Agent, error) {
	var a Agent
	err := s.db.QueryRowContext(ctx, agentSelect+` WHERE a.user_id = ? AND a.deleted_at IS NULL ORDER BY a.last_used_at DESC, a.created_at DESC, a.id DESC LIMIT 1`, userID).Scan(agentScan(&a)...)
	if err != nil {
		return Agent{}, scanErr(err)
	}
	return a, nil
}
