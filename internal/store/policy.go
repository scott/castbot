package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/scott/castbot/internal/connect"
)

func (s *Store) migrateV23() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS connection_settings (
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  connection TEXT NOT NULL,
  connected INTEGER NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (user_id, connection)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_conn_settings_user ON connection_settings(user_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS connection_policies (
  user_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  connection TEXT NOT NULL,
  tool TEXT NOT NULL,
  level TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (user_id, connection, tool)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_conn_policy_user ON connection_policies(user_id, connection)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (23) ON CONFLICT(version) DO NOTHING`)
	return err
}

// ConnectionSwitchedOn reports the account switch.
// A missing Shell Actions row is on. A missing proxy row is off.
func (s *Store) ConnectionSwitchedOn(ctx context.Context, userID, connection string) (bool, error) {
	if _, ok := connect.PanelLookup(connection); !ok {
		return false, ErrInvalid
	}
	var on int
	err := s.db.QueryRowContext(ctx, `SELECT connected FROM connection_settings WHERE user_id = ? AND connection = ?`, userID, connection).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return connect.DefaultOn(connection), nil
	}
	if err != nil {
		return false, err
	}
	return on == 1, nil
}

// SetConnectionSwitch stores the account switch for one connection.
func (s *Store) SetConnectionSwitch(ctx context.Context, userID, connection string, on bool) error {
	if _, ok := connect.PanelLookup(connection); !ok {
		return ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	flag := 0
	if on {
		flag = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO connection_settings(user_id, workspace_id, connection, connected, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(user_id, connection) DO UPDATE SET connected = excluded.connected, workspace_id = excluded.workspace_id, updated_at = excluded.updated_at`,
		userID, user.WorkspaceID, connection, flag, s.stamp(s.Now()))
	return err
}

// ConnectionLevels returns stored tool levels for one connection.
// Missing tools are not filled with defaults.
func (s *Store) ConnectionLevels(ctx context.Context, userID, connection string) (map[string]string, error) {
	if _, ok := connect.PanelLookup(connection); !ok {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tool, level FROM connection_policies WHERE user_id = ? AND connection = ? ORDER BY tool`, userID, connection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var tool, level string
		if err := rows.Scan(&tool, &level); err != nil {
			return nil, err
		}
		out[tool] = level
	}
	return out, rows.Err()
}

// SaveConnectionLevels replaces the stored levels for one connection.
// Every tool must belong to that connection, and every level must be
// ask, always-allow, or deny.
func (s *Store) SaveConnectionLevels(ctx context.Context, userID, connection string, levels map[string]string) error {
	panel, ok := connect.PanelLookup(connection)
	if !ok {
		return ErrInvalid
	}
	allowed := map[string]bool{}
	for _, tool := range panel.Tools {
		allowed[tool.Name] = true
	}
	for tool, level := range levels {
		if !allowed[tool] || !policyLevelOK(level) {
			return ErrInvalid
		}
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM connection_policies WHERE user_id = ? AND connection = ?`, userID, connection); err != nil {
		return err
	}
	stamp := s.stamp(s.Now())
	for tool, level := range levels {
		if _, err := tx.ExecContext(ctx, `INSERT INTO connection_policies(user_id, workspace_id, connection, tool, level, updated_at)
VALUES (?, ?, ?, ?, ?, ?)`, userID, user.WorkspaceID, connection, tool, level, stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func policyLevelOK(level string) bool {
	switch level {
	case "ask", "always-allow", "deny":
		return true
	default:
		return false
	}
}
