package store

import (
	"context"
	"database/sql"
	"strings"
)

// User is an account.
type User struct {
	ID                string
	WorkspaceID       string
	WorkspaceName     string
	Email             string
	DisplayName       string
	Role              string
	Theme             string
	Timezone          string
	PasswordChangedAt string
	CreatedAt         string
	Disabled          bool
}

// NewUser is the bootstrap input for CreateUser.
type NewUser struct {
	Email        string
	DisplayName  string
	Workspace    string
	Role         string
	PasswordHash string
	TOTPSecret   string
	Theme        string
	Timezone     string
}

// CreateUser inserts a workspace, the user, password and TOTP credentials, and the Pinned and Agents sections.
// It does not take a caller id because it is the bootstrap path used by the first-user command and tests.
func (s *Store) CreateUser(ctx context.Context, in NewUser) (User, error) {
	email := normalizeEmail(in.Email)
	name := strings.TrimSpace(in.DisplayName)
	ws := strings.TrimSpace(in.Workspace)
	if email == "" || !strings.Contains(email, "@") || len(email) > 254 || name == "" || len(name) > 80 || ws == "" || in.PasswordHash == "" || in.TOTPSecret == "" {
		return User{}, ErrInvalid
	}
	role := in.Role
	if role == "" {
		role = "owner"
	}
	if role != "owner" && role != "member" {
		return User{}, ErrInvalid
	}
	theme := in.Theme
	if theme == "" {
		theme = "system"
	}
	if theme != "system" && theme != "dark" && theme != "light" {
		return User{}, ErrInvalid
	}
	tz := strings.TrimSpace(in.Timezone)
	if tz == "" {
		tz = "America/Denver"
	}
	if len(tz) > 64 || strings.ContainsAny(tz, " \t\r\n") {
		return User{}, ErrInvalid
	}
	stamp := s.stamp(s.Now())
	uid, err := newID()
	if err != nil {
		return User{}, err
	}
	wid, err := newID()
	if err != nil {
		return User{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO workspaces(id, name, created_at) VALUES (?, ?, ?)`, wid, ws, stamp); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, workspace_id, email, display_name, role, theme, timezone, password_changed_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		uid, wid, email, name, role, theme, tz, stamp, stamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, ErrConflict
		}
		return User{}, err
	}
	for _, kind := range []struct{ k, data string }{{"password", in.PasswordHash}, {"totp", in.TOTPSecret}} {
		cid, err := newID()
		if err != nil {
			return User{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO credentials(id, workspace_id, user_id, kind, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			cid, wid, uid, kind.k, kind.data, stamp); err != nil {
			return User{}, err
		}
	}
	if _, err := insertSection(ctx, tx, wid, uid, "Pinned", "pinned", 0, stamp); err != nil {
		return User{}, err
	}
	if _, err := insertSection(ctx, tx, wid, uid, "Agents", "agents", 1, stamp); err != nil {
		return User{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_quotas(user_id, workspace_id, max_agents) VALUES (?, ?, ?)`, uid, wid, DefaultMaxAgents); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, uid)
}

// CountUsers returns how many accounts exist. The first-user command uses it.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// FindUserByEmail is the bootstrap lookup used by the seed command.
// Handlers do not call it; they already have the session user id.
func (s *Store) FindUserByEmail(ctx context.Context, email string) (User, error) {
	u, _, _, err := s.userByEmail(ctx, normalizeEmail(email))
	return u, err
}

// GetUser returns the caller.
func (s *Store) GetUser(ctx context.Context, userID string) (User, error) {
	var u User
	var disabled sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.workspace_id, w.name, u.email, u.display_name, u.role, u.theme, u.timezone, u.password_changed_at, u.created_at, u.disabled_at
		FROM users u JOIN workspaces w ON w.id = u.workspace_id
		WHERE u.id = ?`, userID).Scan(
		&u.ID, &u.WorkspaceID, &u.WorkspaceName, &u.Email, &u.DisplayName, &u.Role, &u.Theme, &u.Timezone, &u.PasswordChangedAt, &u.CreatedAt, &disabled)
	if err != nil {
		return User{}, scanErr(err)
	}
	u.Disabled = disabled.Valid && disabled.String != ""
	if u.Timezone == "" {
		u.Timezone = "America/Denver"
	}
	return u, nil
}

// UpdateName changes the caller's display name.
func (s *Store) UpdateName(ctx context.Context, userID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET display_name = ? WHERE id = ? AND disabled_at IS NULL`, name, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// UpdateTheme saves the caller's appearance.
func (s *Store) UpdateTheme(ctx context.Context, userID, theme string) error {
	if theme != "system" && theme != "dark" && theme != "light" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET theme = ? WHERE id = ? AND disabled_at IS NULL`, theme, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// ChangePassword replaces the caller's password, drops every other session and every trusted device, and rotates keepSessionID.
func (s *Store) ChangePassword(ctx context.Context, userID, keepSessionID, hash string) (Session, error) {
	if hash == "" || userID == "" || keepSessionID == "" {
		return Session{}, ErrInvalid
	}
	nextID, err := newID()
	if err != nil {
		return Session{}, err
	}
	csrf, err := newID()
	if err != nil {
		return Session{}, err
	}
	stamp := s.stamp(s.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE credentials SET data = ?, created_at = ? WHERE user_id = ? AND kind = 'password'`, hash, stamp, userID)
	if err != nil {
		return Session{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Session{}, err
	}
	res, err = tx.ExecContext(ctx, `UPDATE users SET password_changed_at = ? WHERE id = ?`, stamp, userID)
	if err != nil {
		return Session{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepSessionID); err != nil {
		return Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM trusted_devices WHERE user_id = ?`, userID); err != nil {
		return Session{}, err
	}
	res, err = tx.ExecContext(ctx, `UPDATE sessions SET id = ?, csrf = ? WHERE id = ? AND user_id = ?`, nextID, csrf, keepSessionID, userID)
	if err != nil {
		return Session{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return s.GetSession(ctx, nextID)
}

func rowsAffected(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *Store) credential(ctx context.Context, userID, kind string) (string, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM credentials WHERE user_id = ? AND kind = ?`, userID, kind).Scan(&data)
	return data, scanErr(err)
}

func (s *Store) userByEmail(ctx context.Context, email string) (User, string, string, error) {
	var u User
	var disabled sql.NullString
	var pass, totp string
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.workspace_id, w.name, u.email, u.display_name, u.role, u.theme, u.timezone, u.password_changed_at, u.created_at, u.disabled_at,
		       COALESCE(p.data, ''), COALESCE(t.data, '')
		FROM users u
		JOIN workspaces w ON w.id = u.workspace_id
		LEFT JOIN credentials p ON p.user_id = u.id AND p.kind = 'password'
		LEFT JOIN credentials t ON t.user_id = u.id AND t.kind = 'totp'
		WHERE u.email = ?`, email).Scan(
		&u.ID, &u.WorkspaceID, &u.WorkspaceName, &u.Email, &u.DisplayName, &u.Role, &u.Theme, &u.Timezone, &u.PasswordChangedAt, &u.CreatedAt, &disabled, &pass, &totp)
	if err != nil {
		return User{}, "", "", scanErr(err)
	}
	u.Disabled = disabled.Valid && disabled.String != ""
	if u.Timezone == "" {
		u.Timezone = "America/Denver"
	}
	return u, pass, totp, nil
}
