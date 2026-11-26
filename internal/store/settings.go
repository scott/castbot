package store

import (
	"context"
	"database/sql"
	"errors"
)

// migrateV38 stores settings for this install. The rows are instance state,
// not a person's data, so the table has no user_id and no workspace_id.
func (s *Store) migrateV38() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS instance_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (38) ON CONFLICT(version) DO NOTHING`)
	return err
}

// migrateV39 stores hosted pages. The link token is a separate column from
// the id so a public route never accepts the id. Grants stay in the page
// owner's workspace. Cards record which reply introduced a revision.
func (s *Store) migrateV39() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS pages (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  title TEXT NOT NULL,
  token TEXT NOT NULL,
  visibility TEXT NOT NULL CHECK (visibility IN ('private', 'workspace', 'invited', 'link')),
  body TEXT NOT NULL,
  revision INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_pages_token ON pages(token)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_pages_agent ON pages(user_id, agent_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_pages_workspace ON pages(workspace_id, visibility)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS page_grants (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  page_id TEXT NOT NULL,
  grantee_kind TEXT NOT NULL CHECK (grantee_kind IN ('user', 'agent')),
  grantee_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (page_id, grantee_kind, grantee_id)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_page_grants_grantee ON page_grants(workspace_id, grantee_kind, grantee_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS page_cards (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  page_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  UNIQUE (message_id, page_id)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_page_cards_message ON page_cards(workspace_id, message_id)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (39) ON CONFLICT(version) DO NOTHING`)
	return err
}

func instanceSettingKeyOK(key string) bool {
	if key == "" || len(key) > 32 {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func instanceSettingValueOK(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// InstanceSetting returns a stored value. A missing key is an empty string.
func (s *Store) InstanceSetting(ctx context.Context, key string) (string, error) {
	if !instanceSettingKeyOK(key) {
		return "", ErrInvalid
	}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM instance_settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// SetInstanceSetting inserts or replaces one instance setting.
func (s *Store) SetInstanceSetting(ctx context.Context, key, value string) error {
	if !instanceSettingKeyOK(key) || !instanceSettingValueOK(value) {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO instance_settings(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
