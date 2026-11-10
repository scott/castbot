package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/scott/castbot/internal/auth"
)

const (
	scopeEmail = "email"
	scopeIP    = "ip"
)

// Attempt is the result of a password or code check.
// Status is "ok", "fail", "locked", or "recovery".
type Attempt struct {
	Status      string
	User        User
	Email       string
	TriesLeft   int
	LockedUntil time.Time
}

// Session is a browser sign-in.
type Session struct {
	ID          string
	WorkspaceID string
	UserID      string
	CSRF        string
	UserAgent   string
	Remember    bool
	ExpiresAt   time.Time
	CreatedAt   time.Time
}

// Challenge is the short step between a correct password and the authenticator code.
type Challenge struct {
	ID          string
	WorkspaceID string
	UserID      string
	Email       string
	Remember    bool
	ExpiresAt   time.Time
}

// CheckPassword checks email and password for ip. Unknown emails get the same failure shape as a wrong password.
func (s *Store) CheckPassword(ctx context.Context, email, password, ip string) (Attempt, error) {
	email = normalizeEmail(email)
	if len(email) > 254 {
		email = email[:254]
	}
	if email == "" {
		email = "invalid"
	}
	locked, until, left, err := s.lockState(ctx, email, ip)
	if err != nil {
		return Attempt{}, err
	}
	if locked {
		return Attempt{Status: "locked", Email: email, TriesLeft: 0, LockedUntil: until}, nil
	}
	user, hash, _, err := s.userByEmail(ctx, email)
	if err != nil && err != ErrNotFound {
		return Attempt{}, err
	}
	compare := hash
	known := err == nil && !user.Disabled && hash != ""
	if !known {
		compare = s.dummy
	}
	ok := auth.Verify(password, compare) && known
	if !ok {
		return s.fail(ctx, email, ip, user)
	}
	return Attempt{Status: "ok", User: user, Email: email, TriesLeft: left}, nil
}

// BeginChallenge starts the authenticator step. Password must already have succeeded.
func (s *Store) BeginChallenge(ctx context.Context, user User, remember bool) (Challenge, error) {
	id, err := newID()
	if err != nil {
		return Challenge{}, err
	}
	now := s.Now()
	exp := now.Add(10 * time.Minute)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Challenge{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE user_id = ?`, user.ID); err != nil {
		return Challenge{}, err
	}
	rememberN := 0
	if remember {
		rememberN = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO login_challenges(id, workspace_id, user_id, email, remember, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, user.WorkspaceID, user.ID, user.Email, rememberN, s.stamp(exp), s.stamp(now)); err != nil {
		return Challenge{}, err
	}
	if err := tx.Commit(); err != nil {
		return Challenge{}, err
	}
	return Challenge{ID: id, WorkspaceID: user.WorkspaceID, UserID: user.ID, Email: user.Email, Remember: remember, ExpiresAt: exp}, nil
}

// GetChallenge loads a pending sign-in step.
func (s *Store) GetChallenge(ctx context.Context, id string) (Challenge, error) {
	var c Challenge
	var remember int
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, email, remember, expires_at FROM login_challenges WHERE id = ?`, id).
		Scan(&c.ID, &c.WorkspaceID, &c.UserID, &c.Email, &remember, &exp)
	if err != nil {
		return Challenge{}, scanErr(err)
	}
	c.Remember = remember == 1
	c.ExpiresAt, _ = parseTime(exp)
	if !c.ExpiresAt.After(s.Now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM login_challenges WHERE id = ?`, id)
		return Challenge{}, ErrNotFound
	}
	return c, nil
}

// DeleteChallenge removes a pending sign-in step owned by the challenge id.
func (s *Store) DeleteChallenge(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_challenges WHERE id = ?`, id)
	return err
}

// CheckCode checks an authenticator code for a challenge.
// recovery set means the person asked to use a recovery code, which this version does not issue.
func (s *Store) CheckCode(ctx context.Context, challengeID, code, recovery, ip string) (Attempt, error) {
	ch, err := s.GetChallenge(ctx, challengeID)
	if err != nil {
		return Attempt{}, err
	}
	user, err := s.GetUser(ctx, ch.UserID)
	if err != nil {
		return Attempt{}, err
	}
	locked, until, _, err := s.lockState(ctx, ch.Email, ip)
	if err != nil {
		return Attempt{}, err
	}
	if locked {
		_ = s.DeleteChallenge(ctx, ch.ID)
		return Attempt{Status: "locked", User: user, Email: ch.Email, LockedUntil: until}, nil
	}
	if recovery != "" {
		return Attempt{Status: "recovery", User: user, Email: ch.Email}, nil
	}
	accepted, err := s.acceptCode(ctx, user, ch, code, ip)
	if err != nil {
		return Attempt{}, err
	}
	if accepted {
		return Attempt{Status: "ok", User: user, Email: ch.Email}, nil
	}
	att, ferr := s.fail(ctx, ch.Email, ip, user)
	if ferr != nil {
		return Attempt{}, ferr
	}
	if att.Status == "locked" {
		_ = s.DeleteChallenge(ctx, ch.ID)
	}
	att.User = user
	return att, nil
}

// acceptCode records the matched TOTP step in the same transaction that accepts it.
// A step at or below the last accepted step is rejected.
func (s *Store) acceptCode(ctx context.Context, user User, ch Challenge, code, ip string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var secret string
	var last sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT data, last_step FROM credentials WHERE user_id = ? AND kind = 'totp'`, user.ID).Scan(&secret, &last)
	if err != nil {
		return false, scanErr(err)
	}
	step, ok := auth.TOTPMatch(secret, code, s.Now())
	if !ok || (last.Valid && step <= last.Int64) {
		return false, nil
	}
	res, err := tx.ExecContext(ctx, `UPDATE credentials SET last_step = ? WHERE user_id = ? AND kind = 'totp' AND (last_step IS NULL OR last_step < ?)`, step, user.ID, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_limits WHERE (scope = ? AND subject = ?) OR (scope = ? AND subject = ?)`,
		scopeEmail, ch.Email, scopeIP, ip); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_challenges WHERE id = ? AND user_id = ?`, ch.ID, user.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// SignOutOthers removes every other session and every trusted device except the one named by deviceToken.
func (s *Store) SignOutOthers(ctx context.Context, userID, keepSessionID, deviceToken string) (int, error) {
	keepDevice, _, _ := splitToken(deviceToken)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepSessionID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	q := `DELETE FROM trusted_devices WHERE user_id = ?`
	args := []any{userID}
	if keepDevice != "" {
		q = `DELETE FROM trusted_devices WHERE user_id = ? AND id <> ?`
		args = append(args, keepDevice)
	}
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// TrustedDevice reports whether cookie token belongs to userID and is still inside 30 days.
func (s *Store) TrustedDevice(ctx context.Context, token, userID string) (bool, error) {
	id, secret, ok := splitToken(token)
	if !ok {
		return false, nil
	}
	var hash, exp, owner string
	err := s.db.QueryRowContext(ctx, `SELECT secret_hash, expires_at, user_id FROM trusted_devices WHERE id = ?`, id).Scan(&hash, &exp, &owner)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256([]byte(secret))
	got := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(got), []byte(hash)) != 1 || owner != userID {
		return false, nil
	}
	until, err := parseTime(exp)
	if err != nil || !until.After(s.Now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM trusted_devices WHERE id = ? AND user_id = ?`, id, userID)
		return false, nil
	}
	return true, nil
}

// TrustDevice remembers this browser for 30 days and returns the cookie token.
func (s *Store) TrustDevice(ctx context.Context, userID, userAgent string) (string, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	secret, err := newID()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(secret))
	now := s.Now()
	exp := now.Add(30 * 24 * time.Hour)
	_, err = s.db.ExecContext(ctx, `INSERT INTO trusted_devices(id, workspace_id, user_id, secret_hash, user_agent, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, user.WorkspaceID, user.ID, hex.EncodeToString(sum[:]), userAgent, s.stamp(exp), s.stamp(now))
	if err != nil {
		return "", err
	}
	return id + "." + secret, nil
}

// CreateSession opens a session for the caller.
func (s *Store) CreateSession(ctx context.Context, userID, userAgent string, remember bool) (Session, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return Session{}, err
	}
	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	csrf, err := newID()
	if err != nil {
		return Session{}, err
	}
	now := s.Now()
	exp := now.Add(12 * time.Hour)
	if remember {
		exp = now.Add(30 * 24 * time.Hour)
	}
	rememberN := 0
	if remember {
		rememberN = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions(id, workspace_id, user_id, expires_at, csrf, user_agent, remember, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, user.WorkspaceID, user.ID, s.stamp(exp), csrf, userAgent, rememberN, s.stamp(now))
	if err != nil {
		return Session{}, err
	}
	return Session{ID: id, WorkspaceID: user.WorkspaceID, UserID: user.ID, CSRF: csrf, UserAgent: userAgent, Remember: remember, ExpiresAt: exp, CreatedAt: now}, nil
}

// GetSession loads a session by its id. The cookie layer checks the HMAC before this is called.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	var sess Session
	var exp, created string
	var remember int
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, expires_at, csrf, user_agent, remember, created_at FROM sessions WHERE id = ?`, id).
		Scan(&sess.ID, &sess.WorkspaceID, &sess.UserID, &exp, &sess.CSRF, &sess.UserAgent, &remember, &created)
	if err != nil {
		return Session{}, scanErr(err)
	}
	sess.Remember = remember == 1
	sess.ExpiresAt, _ = parseTime(exp)
	sess.CreatedAt, _ = parseTime(created)
	if !sess.ExpiresAt.After(s.Now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
		return Session{}, ErrNotFound
	}
	return sess, nil
}

// DeleteSession removes one of the caller's sessions.
func (s *Store) DeleteSession(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// DeleteOtherSessions removes every session for the caller except keepID.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID, keepID string) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// ListSessions returns the caller's sessions, newest first.
func (s *Store) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, expires_at, csrf, user_agent, remember, created_at FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY created_at DESC`, userID, s.stamp(s.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		var exp, created string
		var remember int
		if err := rows.Scan(&sess.ID, &sess.WorkspaceID, &sess.UserID, &exp, &sess.CSRF, &sess.UserAgent, &remember, &created); err != nil {
			return nil, err
		}
		sess.Remember = remember == 1
		sess.ExpiresAt, _ = parseTime(exp)
		sess.CreatedAt, _ = parseTime(created)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// PasswordHash returns the caller's password hash for a change-password check.
func (s *Store) PasswordHash(ctx context.Context, userID string) (string, error) {
	return s.credential(ctx, userID, "password")
}

func (s *Store) fail(ctx context.Context, email, ip string, user User) (Attempt, error) {
	left, until, locked, err := s.bump(ctx, email, ip, user)
	if err != nil {
		return Attempt{}, err
	}
	att := Attempt{Status: "fail", Email: email, TriesLeft: left, User: user}
	if locked {
		att.Status = "locked"
		att.LockedUntil = until
		att.TriesLeft = 0
	}
	return att, nil
}

type limitRow struct {
	failures int
	window   time.Time
	locked   time.Time
	present  bool
}

type limitQuery interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readLimitQ(ctx context.Context, q limitQuery, scope, subject string) (limitRow, error) {
	var failures int
	var window, locked sql.NullString
	err := q.QueryRowContext(ctx, `SELECT failures, window_start, locked_until FROM login_limits WHERE scope = ? AND subject = ?`, scope, subject).
		Scan(&failures, &window, &locked)
	if err == sql.ErrNoRows {
		return limitRow{}, nil
	}
	if err != nil {
		return limitRow{}, err
	}
	row := limitRow{failures: failures, present: true}
	if window.Valid {
		row.window, _ = parseTime(window.String)
	}
	if locked.Valid {
		row.locked, _ = parseTime(locked.String)
	}
	return row, nil
}

func (s *Store) interpret(row limitRow, now time.Time) (left int, locked bool, until time.Time) {
	if !row.present {
		return lockFailures, false, time.Time{}
	}
	if row.locked.After(now) {
		return 0, true, row.locked
	}
	if now.Sub(row.window) > lockWindow {
		return lockFailures, false, time.Time{}
	}
	left = lockFailures - row.failures
	if left < 0 {
		left = 0
	}
	return left, false, time.Time{}
}

func (s *Store) readLimit(ctx context.Context, scope, subject string) (limitRow, error) {
	return readLimitQ(ctx, s.db, scope, subject)
}

func (s *Store) lockState(ctx context.Context, email, ip string) (bool, time.Time, int, error) {
	er, err := s.readLimit(ctx, scopeEmail, email)
	if err != nil {
		return false, time.Time{}, 0, err
	}
	ir, err := s.readLimit(ctx, scopeIP, ip)
	if err != nil {
		return false, time.Time{}, 0, err
	}
	now := s.Now()
	el, eLock, eUntil := s.interpret(er, now)
	il, iLock, iUntil := s.interpret(ir, now)
	left := el
	if il < left {
		left = il
	}
	until := eUntil
	if iUntil.After(until) {
		until = iUntil
	}
	return eLock || iLock, until, left, nil
}

func (s *Store) bump(ctx context.Context, email, ip string, user User) (int, time.Time, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	defer tx.Rollback()
	now := s.Now()
	eLock, eUntil, eLeft, err := s.bumpOne(ctx, tx, scopeEmail, email, user.WorkspaceID, user.ID, now)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	iLock, iUntil, iLeft, err := s.bumpOne(ctx, tx, scopeIP, ip, "", "", now)
	if err != nil {
		return 0, time.Time{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, time.Time{}, false, err
	}
	left := eLeft
	if iLeft < left {
		left = iLeft
	}
	until := eUntil
	if iUntil.After(until) {
		until = iUntil
	}
	return left, until, eLock || iLock, nil
}

func (s *Store) bumpOne(ctx context.Context, tx *sql.Tx, scope, subject, workspaceID, userID string, now time.Time) (bool, time.Time, int, error) {
	row, err := readLimitQ(ctx, tx, scope, subject)
	if err != nil {
		return false, time.Time{}, 0, err
	}
	failures := 0
	window := now
	if row.present && row.locked.After(now) {
		return true, row.locked, 0, nil
	}
	if row.present && now.Sub(row.window) <= lockWindow && !row.locked.After(now) && row.locked.IsZero() {
		failures = row.failures
		window = row.window
	}
	if row.present && row.locked.IsZero() == false && !row.locked.After(now) {
		failures = 0
		window = now
	}
	failures++
	var until time.Time
	var untilVal any
	locked := false
	if failures >= lockFailures {
		locked = true
		until = now.Add(lockWindow)
		untilVal = s.stamp(until)
		failures = lockFailures
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO login_limits(scope, subject, workspace_id, user_id, failures, window_start, locked_until)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, subject) DO UPDATE SET
		  workspace_id = excluded.workspace_id,
		  user_id = excluded.user_id,
		  failures = excluded.failures,
		  window_start = excluded.window_start,
		  locked_until = excluded.locked_until`,
		scope, subject, workspaceID, userID, failures, s.stamp(window), untilVal)
	if err != nil {
		return false, time.Time{}, 0, err
	}
	left := lockFailures - failures
	if left < 0 {
		left = 0
	}
	return locked, until, left, nil
}

func (s *Store) clearLimits(ctx context.Context, email, ip string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_limits WHERE (scope = ? AND subject = ?) OR (scope = ? AND subject = ?)`,
		scopeEmail, email, scopeIP, ip)
	return err
}

func splitToken(token string) (string, string, bool) {
	id, secret, ok := cut(token, ".")
	if !ok || len(id) < 8 || len(secret) < 8 {
		return "", "", false
	}
	return id, secret, true
}

func cut(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return "", "", false
}
