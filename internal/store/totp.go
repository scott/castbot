package store

import (
	"context"
	"strings"

	"github.com/scott/castbot/internal/auth"
)

func (s *Store) migrateV34() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS totp_pending (
  user_id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  secret TEXT NOT NULL,
  created_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (34) ON CONFLICT(version) DO NOTHING`)
	return err
}

// HasTOTP reports whether this user has an authenticator credential.
func (s *Store) HasTOTP(ctx context.Context, userID string) (bool, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return false, err
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM credentials WHERE user_id = ? AND workspace_id = ? AND kind = 'totp'`, user.ID, user.WorkspaceID).Scan(&n)
	return n > 0, err
}

// BeginTOTP stores a pending authenticator secret for this user.
// A user who already has two-factor on gets ErrConflict. The pending row is replaced.
func (s *Store) BeginTOTP(ctx context.Context, userID, secret string) error {
	secret = strings.TrimSpace(secret)
	if userID == "" || secret == "" || len(secret) > 128 || strings.ContainsAny(secret, " \t\r\n") {
		return ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if user.Disabled {
		return ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM credentials WHERE user_id = ? AND workspace_id = ? AND kind = 'totp'`, user.ID, user.WorkspaceID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE totp_pending SET secret = ?, created_at = ? WHERE user_id = ? AND workspace_id = ?`, secret, stamp, user.ID, user.WorkspaceID)
	if err != nil {
		return err
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if updated == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO totp_pending(user_id, workspace_id, secret, created_at) VALUES (?, ?, ?, ?)`, user.ID, user.WorkspaceID, secret, stamp); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return ErrNotFound
			}
			return err
		}
	}
	return tx.Commit()
}

// PendingTOTP returns the unconfirmed authenticator secret for this user.
func (s *Store) PendingTOTP(ctx context.Context, userID string) (string, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	var secret string
	err = s.db.QueryRowContext(ctx, `SELECT secret FROM totp_pending WHERE user_id = ? AND workspace_id = ?`, user.ID, user.WorkspaceID).Scan(&secret)
	if err != nil {
		return "", scanErr(err)
	}
	return secret, nil
}

// ConfirmTOTP checks the current code, stores the authenticator credential, and drops the pending secret.
// A code that does not match is ErrInvalid and the pending secret stays.
// The matched step is stored so that same code cannot be used at the next sign-in.
func (s *Store) ConfirmTOTP(ctx context.Context, userID, code string) error {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if user.Disabled {
		return ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM credentials WHERE user_id = ? AND workspace_id = ? AND kind = 'totp'`, user.ID, user.WorkspaceID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}
	var secret string
	err = tx.QueryRowContext(ctx, `SELECT secret FROM totp_pending WHERE user_id = ? AND workspace_id = ?`, user.ID, user.WorkspaceID).Scan(&secret)
	if err != nil {
		return scanErr(err)
	}
	step, ok := auth.TOTPMatch(secret, strings.TrimSpace(code), s.Now())
	if !ok {
		return ErrInvalid
	}
	cid, err := newID()
	if err != nil {
		return err
	}
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO credentials(id, workspace_id, user_id, kind, data, last_step, created_at) VALUES (?, ?, ?, 'totp', ?, ?, ?)`,
		cid, user.WorkspaceID, user.ID, secret, step, stamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrConflict
		}
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM totp_pending WHERE user_id = ? AND workspace_id = ?`, user.ID, user.WorkspaceID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	return tx.Commit()
}
