package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"golang.org/x/crypto/nacl/secretbox"
)

// SecretValueMax is the vault cap, in bytes.
const SecretValueMax = 8192

// Secret is one vault row. Value is set only after a decrypt and must not be logged.
type Secret struct {
	ID          string
	WorkspaceID string
	UserID      string
	Name        string
	Label       string
	Provider    string
	CreatedAt   string
	RotatedAt   string
	Value       string
}

// SecretInput is a new vault row. Provider is empty for an agent secret.
// Label is the name the user chose for a model key. It is not key material.
type SecretInput struct {
	Name     string
	Label    string
	Provider string
	Value    string
}

// AuditRow is one audit_log line. Detail has names and ids, never a value.
type AuditRow struct {
	Action    string
	ActorID   string
	Detail    string
	CreatedAt string
}

var secretNameRE = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// SecretNameOK reports whether name can be stored and pulled by the guest.
// The guest rejects a wider set than the user-facing rule, and a saved name
// must not fail at pull.
func SecretNameOK(name string) bool {
	if !secretNameRE.MatchString(name) {
		return false
	}
	return !secretNameBlocked(name)
}

func secretNameBlocked(name string) bool {
	switch name {
	case "PATH", "HOME", "HOSTNAME", "CASTBOT_BOOTSTRAP_TOKEN", "CASTBOT_CA_FILE", "CASTLET_SECRET_COUNT":
		return true
	default:
		return strings.HasPrefix(name, "CASTLET_") || strings.HasPrefix(name, "CASTBOT_")
	}
}

func secretValueOK(value string) bool {
	if value == "" || len(value) > SecretValueMax {
		return false
	}
	return !strings.ContainsAny(value, "\x00\n\r")
}

func (s *Store) migrateSecrets() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS secrets (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  name TEXT NOT NULL,
  label TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL DEFAULT '',
  ciphertext BLOB NOT NULL,
  nonce BLOB NOT NULL,
  created_at TEXT NOT NULL,
  rotated_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_secrets_user_name ON secrets(user_id, name)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_vault_user ON secrets(user_id, provider)`); err != nil {
		return err
	}
	var ver int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 16`).Scan(&ver)
	if err != nil {
		return err
	}
	if ver == 0 {
		if err := s.encryptFixtures(); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES (16)`); err != nil {
		return err
	}
	// A crash can leave plaintext behind. The vault already has the copy.
	res, err := s.db.Exec(`DELETE FROM secret_fixtures`)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	// Free pages and the WAL can still hold the old plaintext. One connection
	// is open, so the checkpoint and VACUUM are safe here.
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return err
	}
	return nil
}

func (s *Store) encryptFixtures() error {
	rows, err := s.db.Query(`SELECT id, workspace_id, user_id, name, value, created_at FROM secret_fixtures`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type row struct {
		id, workspace, user, name, value, created string
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.workspace, &r.user, &r.name, &r.value, &r.created); err != nil {
			return err
		}
		batch = append(batch, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(batch) == 0 {
		return nil
	}
	if s.boxKey() == nil {
		return errors.New("secrets key is not configured")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range batch {
		ct, nonce, err := s.seal([]byte(r.value))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO secrets(id, workspace_id, user_id, name, label, provider, ciphertext, nonce, created_at)
VALUES (?, ?, ?, ?, '', '', ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			r.id, r.workspace, r.user, r.name, ct, nonce, r.created); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetSecretsKey installs the 32-byte vault key. It refuses the wrong length
// and an all-zero key. The session key must not be reused here.
func (s *Store) SetSecretsKey(key []byte) error {
	if len(key) != 32 {
		return errors.New("secrets key must be 32 bytes")
	}
	zero := true
	for _, b := range key {
		if b != 0 {
			zero = false
			break
		}
	}
	if zero {
		return errors.New("secrets key must not be all zeros")
	}
	var k [32]byte
	copy(k[:], key)
	s.boxMu.Lock()
	s.box = &k
	s.boxMu.Unlock()
	return nil
}

func (s *Store) boxKey() *[32]byte {
	s.boxMu.RLock()
	defer s.boxMu.RUnlock()
	return s.box
}

func (s *Store) seal(plain []byte) (ciphertext, nonce []byte, err error) {
	key := s.boxKey()
	if key == nil {
		return nil, nil, errors.New("secrets key is not configured")
	}
	var n [24]byte
	if _, err := rand.Read(n[:]); err != nil {
		return nil, nil, err
	}
	return secretbox.Seal(nil, plain, &n, key), append([]byte(nil), n[:]...), nil
}

func (s *Store) openBox(ciphertext, nonce []byte) (string, error) {
	key := s.boxKey()
	if key == nil {
		return "", errors.New("secrets key is not configured")
	}
	if len(nonce) != 24 {
		return "", ErrInvalid
	}
	var n [24]byte
	copy(n[:], nonce)
	out, ok := secretbox.Open(nil, ciphertext, &n, key)
	if !ok {
		return "", ErrInvalid
	}
	return string(out), nil
}

// CreateSecret encrypts a new row. Names are unique per user.
func (s *Store) CreateSecret(ctx context.Context, userID string, in SecretInput) (Secret, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Label = strings.TrimSpace(in.Label)
	in.Provider = strings.TrimSpace(in.Provider)
	if !SecretNameOK(in.Name) || !secretValueOK(in.Value) {
		return Secret{}, ErrInvalid
	}
	if len(in.Label) > 80 || len(in.Provider) > 64 || strings.ContainsAny(in.Provider, " \t") {
		return Secret{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return Secret{}, err
	}
	ct, nonce, err := s.seal([]byte(in.Value))
	if err != nil {
		return Secret{}, err
	}
	id, err := newID()
	if err != nil {
		return Secret{}, err
	}
	stamp := s.stamp(s.Now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO secrets(id, workspace_id, user_id, name, label, provider, ciphertext, nonce, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, user.WorkspaceID, userID, in.Name, in.Label, in.Provider, ct, nonce, stamp)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Secret{}, ErrConflict
		}
		return Secret{}, err
	}
	return Secret{ID: id, WorkspaceID: user.WorkspaceID, UserID: userID, Name: in.Name, Label: in.Label, Provider: in.Provider, CreatedAt: stamp}, nil
}

// ReplaceSecret overwrites the ciphertext and sets rotated_at. A fresh nonce is used.
func (s *Store) ReplaceSecret(ctx context.Context, userID, id, value string) (Secret, error) {
	if !secretValueOK(value) {
		return Secret{}, ErrInvalid
	}
	cur, err := s.SecretMeta(ctx, userID, id)
	if err != nil {
		return Secret{}, err
	}
	ct, nonce, err := s.seal([]byte(value))
	if err != nil {
		return Secret{}, err
	}
	stamp := s.stamp(s.Now())
	res, err := s.db.ExecContext(ctx, `UPDATE secrets SET ciphertext = ?, nonce = ?, rotated_at = ? WHERE id = ? AND user_id = ?`,
		ct, nonce, stamp, id, userID)
	if err != nil {
		return Secret{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Secret{}, err
	}
	cur.RotatedAt = stamp
	return cur, nil
}

// DeleteSecret removes one secret and its grants.
func (s *Store) DeleteSecret(ctx context.Context, userID, id string) error {
	if _, err := s.SecretMeta(ctx, userID, id); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_secret_grants WHERE secret_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	return tx.Commit()
}

// SecretMeta returns one secret without the value.
func (s *Store) SecretMeta(ctx context.Context, userID, id string) (Secret, error) {
	var sec Secret
	var rotated sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, name, label, provider, created_at, rotated_at
FROM secrets WHERE id = ? AND user_id = ?`, id, userID).Scan(
		&sec.ID, &sec.WorkspaceID, &sec.UserID, &sec.Name, &sec.Label, &sec.Provider, &sec.CreatedAt, &rotated)
	if err != nil {
		return Secret{}, scanErr(err)
	}
	sec.RotatedAt = rotated.String
	return sec, nil
}

// SecretByName returns one secret without the value.
func (s *Store) SecretByName(ctx context.Context, userID, name string) (Secret, error) {
	var sec Secret
	var rotated sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, name, label, provider, created_at, rotated_at
FROM secrets WHERE user_id = ? AND name = ?`, userID, name).Scan(
		&sec.ID, &sec.WorkspaceID, &sec.UserID, &sec.Name, &sec.Label, &sec.Provider, &sec.CreatedAt, &rotated)
	if err != nil {
		return Secret{}, scanErr(err)
	}
	sec.RotatedAt = rotated.String
	return sec, nil
}

// DecryptSecret returns the value for one of the caller's secrets.
func (s *Store) DecryptSecret(ctx context.Context, userID, id string) (string, error) {
	var ct, nonce []byte
	err := s.db.QueryRowContext(ctx, `SELECT ciphertext, nonce FROM secrets WHERE id = ? AND user_id = ?`, id, userID).Scan(&ct, &nonce)
	if err != nil {
		return "", scanErr(err)
	}
	return s.openBox(ct, nonce)
}

// ListSecrets returns the caller's rows without values. providerFilter empty means every row.
// Pass "*" for agent secrets only (empty provider).
func (s *Store) ListSecrets(ctx context.Context, userID, providerFilter string) ([]Secret, error) {
	q := `SELECT id, workspace_id, user_id, name, label, provider, created_at, rotated_at FROM secrets WHERE user_id = ?`
	args := []any{userID}
	switch providerFilter {
	case "*":
		q += ` AND provider = ''`
	case "":
	default:
		q += ` AND provider = ?`
		args = append(args, providerFilter)
	}
	q += ` ORDER BY created_at, name`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		var sec Secret
		var rotated sql.NullString
		if err := rows.Scan(&sec.ID, &sec.WorkspaceID, &sec.UserID, &sec.Name, &sec.Label, &sec.Provider, &sec.CreatedAt, &rotated); err != nil {
			return nil, err
		}
		sec.RotatedAt = rotated.String
		out = append(out, sec)
	}
	return out, rows.Err()
}

// ProviderSecrets returns model keys whose provider is one of ids, newest first.
// The first row is the one agents use. Values are decrypted.
func (s *Store) ProviderSecrets(ctx context.Context, userID string, ids []string) ([]Secret, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	q := `SELECT id, workspace_id, user_id, name, label, provider, ciphertext, nonce, created_at, rotated_at
FROM secrets WHERE user_id = ? AND provider IN (`
	args := []any{userID}
	for i, id := range ids {
		if i > 0 {
			q += ", "
		}
		q += "?"
		args = append(args, id)
	}
	q += `) ORDER BY created_at DESC, rowid DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		var sec Secret
		var ct, nonce []byte
		var rotated sql.NullString
		if err := rows.Scan(&sec.ID, &sec.WorkspaceID, &sec.UserID, &sec.Name, &sec.Label, &sec.Provider, &ct, &nonce, &sec.CreatedAt, &rotated); err != nil {
			return nil, err
		}
		sec.RotatedAt = rotated.String
		val, err := s.openBox(ct, nonce)
		if err != nil {
			return nil, err
		}
		sec.Value = val
		out = append(out, sec)
	}
	return out, rows.Err()
}

func (s *Store) grantSecret(ctx context.Context, userID, agentID, secretID string) error {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	sec, err := s.SecretMeta(ctx, userID, secretID)
	if err != nil {
		return err
	}
	if sec.Provider != "" || sec.WorkspaceID != agent.WorkspaceID {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_secret_grants(agent_id, secret_id, workspace_id, user_id) VALUES (?, ?, ?, ?)
ON CONFLICT(agent_id, secret_id) DO NOTHING`, agentID, secretID, sec.WorkspaceID, userID)
	return err
}

// RevokeSecret removes one grant. The secret stays in the vault.
func (s *Store) RevokeSecret(ctx context.Context, userID, agentID, secretID string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	if _, err := s.SecretMeta(ctx, userID, secretID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_secret_grants WHERE user_id = ? AND agent_id = ? AND secret_id = ?`, userID, agentID, secretID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// GrantAgentIDs lists agents that hold one secret.
func (s *Store) GrantAgentIDs(ctx context.Context, userID, secretID string) ([]string, error) {
	if _, err := s.SecretMeta(ctx, userID, secretID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM agent_secret_grants WHERE user_id = ? AND secret_id = ? ORDER BY agent_id`, userID, secretID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// GrantedMeta lists secrets granted to one agent, without values.
func (s *Store) GrantedMeta(ctx context.Context, userID, agentID string) ([]Secret, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.workspace_id, f.user_id, f.name, f.label, f.provider, f.created_at, f.rotated_at
FROM secrets f
JOIN agent_secret_grants g ON g.secret_id = f.id AND g.user_id = f.user_id
WHERE g.user_id = ? AND g.agent_id = ? AND f.provider = '' ORDER BY f.name`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Secret
	for rows.Next() {
		var sec Secret
		var rotated sql.NullString
		if err := rows.Scan(&sec.ID, &sec.WorkspaceID, &sec.UserID, &sec.Name, &sec.Label, &sec.Provider, &sec.CreatedAt, &rotated); err != nil {
			return nil, err
		}
		sec.RotatedAt = rotated.String
		out = append(out, sec)
	}
	return out, rows.Err()
}

func (s *Store) grantedSecrets(ctx context.Context, userID, agentID string) ([]SecretFixture, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.name, f.ciphertext, f.nonce FROM secrets f
JOIN agent_secret_grants g ON g.secret_id = f.id AND g.user_id = f.user_id
WHERE g.user_id = ? AND g.agent_id = ? AND f.provider = '' ORDER BY f.name`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretFixture
	for rows.Next() {
		var sec SecretFixture
		var ct, nonce []byte
		if err := rows.Scan(&sec.ID, &sec.Name, &ct, &nonce); err != nil {
			return nil, err
		}
		val, err := s.openBox(ct, nonce)
		if err != nil {
			return nil, err
		}
		sec.Value = val
		out = append(out, sec)
	}
	return out, rows.Err()
}

// RecordSecretAudit writes one audit row. detail must not contain a value or ciphertext.
func (s *Store) RecordSecretAudit(ctx context.Context, userID, action, secretID string, detail any) error {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.insertAudit(ctx, tx, user.WorkspaceID, userID, "user", userID, action, "secret:"+secretID, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
}

// SecretAudit lists audit rows for one secret, newest first.
func (s *Store) SecretAudit(ctx context.Context, userID, secretID string) ([]AuditRow, error) {
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT action, actor_id, detail_json, created_at FROM audit_log
WHERE user_id = ? AND target = ? ORDER BY id DESC`, userID, "secret:"+secretID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var row AuditRow
		if err := rows.Scan(&row.Action, &row.ActorID, &row.Detail, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SchemaHas reports whether a schema version row exists.
func (s *Store) SchemaHas(ctx context.Context, version int) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n)
	return n > 0, err
}
