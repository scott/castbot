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
