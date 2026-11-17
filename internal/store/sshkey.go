package store

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"

	"github.com/scott/castbot/internal/connect"
	"golang.org/x/crypto/ssh"
)

// DigitalOceanSSHSecret is the vault name of the account SSH seed.
// It is a connection secret. Guest grants reject it here so the running
// image does not need a new block list.
const DigitalOceanSSHSecret = "DIGITALOCEAN_SSH_KEY"

// ConnectionSSHKey is the public half of an account SSH key.
// The private seed is not a field.
type ConnectionSSHKey struct {
	ID          string
	WorkspaceID string
	UserID      string
	Connection  string
	PublicKey   string
	Fingerprint string
	RemoteID    string
	CreatedAt   string
}

var sshFingerprintRE = regexp.MustCompile(`^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){15}$`)

// BlocksSSHSeed reports the vault name that must not become an environment variable.
func BlocksSSHSeed(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), DigitalOceanSSHSecret)
}

// SSHFingerprintOK reports an OpenSSH MD5 fingerprint.
func SSHFingerprintOK(fp string) bool {
	return sshFingerprintRE.MatchString(fp)
}

func (s *Store) migrateV32() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS connection_ssh_keys (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  connection TEXT NOT NULL,
  public_key TEXT NOT NULL,
  fingerprint TEXT NOT NULL DEFAULT '',
  remote_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  UNIQUE(user_id, connection)
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_connection_ssh_user ON connection_ssh_keys(user_id, workspace_id, connection)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (32) ON CONFLICT(version) DO NOTHING`)
	return err
}

// SaveConnectionSSHMaterial stores a new public key and its private seed.
// The seed is a connection secret. An existing public key is kept.
func (s *Store) SaveConnectionSSHMaterial(ctx context.Context, userID, connection, publicKey, seed string) error {
	if connection != connect.DigitalOcean || !sshPublicLineOK(publicKey) || !sshSeedOK(seed) {
		return ErrInvalid
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
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT public_key FROM connection_ssh_keys WHERE user_id = ? AND workspace_id = ? AND connection = ?`, userID, user.WorkspaceID, connection).Scan(&existing)
	if err == nil {
		if existing != publicKey {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := s.upsertSSHSeedTx(ctx, tx, user, seed); err != nil {
		return err
	}
	id, err := newID()
	if err != nil {
		return err
	}
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO connection_ssh_keys(id, workspace_id, user_id, connection, public_key, fingerprint, remote_id, created_at) VALUES (?, ?, ?, ?, ?, '', '', ?)`,
		id, user.WorkspaceID, userID, connection, publicKey, stamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrConflict
		}
		return err
	}
	return tx.Commit()
}

// SetConnectionSSHRemote records the fingerprint and id from the account.
// A fingerprint that is already stored is left in place.
func (s *Store) SetConnectionSSHRemote(ctx context.Context, userID, connection, fingerprint, remoteID string) error {
	if connection != connect.DigitalOcean || !SSHFingerprintOK(fingerprint) || !sshRemoteIDOK(remoteID) {
		return ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	var current string
	err = s.db.QueryRowContext(ctx, `SELECT fingerprint FROM connection_ssh_keys WHERE user_id = ? AND workspace_id = ? AND connection = ?`, userID, user.WorkspaceID, connection).Scan(&current)
	if err != nil {
		return scanErr(err)
	}
	if current != "" && current != fingerprint {
		return ErrConflict
	}
	res, err := s.db.ExecContext(ctx, `UPDATE connection_ssh_keys SET fingerprint = ?, remote_id = ? WHERE user_id = ? AND workspace_id = ? AND connection = ?`,
		fingerprint, remoteID, userID, user.WorkspaceID, connection)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// ConnectionSSHKey returns the public key for this account.
// Another user's id is not found.
func (s *Store) ConnectionSSHKey(ctx context.Context, userID, connection string) (ConnectionSSHKey, error) {
	if connection != connect.DigitalOcean {
		return ConnectionSSHKey{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return ConnectionSSHKey{}, err
	}
	var row ConnectionSSHKey
	err = s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, connection, public_key, fingerprint, remote_id, created_at FROM connection_ssh_keys WHERE user_id = ? AND workspace_id = ? AND connection = ?`,
		userID, user.WorkspaceID, connection).Scan(&row.ID, &row.WorkspaceID, &row.UserID, &row.Connection, &row.PublicKey, &row.Fingerprint, &row.RemoteID, &row.CreatedAt)
	if err != nil {
		return ConnectionSSHKey{}, scanErr(err)
	}
	return row, nil
}

// SSHKeySeed decrypts the private seed for this account.
// Do not log it, render it, or grant it. Another user's id is not found.
func (s *Store) SSHKeySeed(ctx context.Context, userID, connection string) (string, error) {
	if _, err := s.ConnectionSSHKey(ctx, userID, connection); err != nil {
		return "", err
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	sec, err := s.SecretByName(ctx, userID, DigitalOceanSSHSecret)
	if err != nil {
		return "", err
	}
	if sec.WorkspaceID != user.WorkspaceID {
		return "", ErrNotFound
	}
	var kind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM secrets WHERE id = ? AND user_id = ?`, sec.ID, userID).Scan(&kind); err != nil {
		return "", scanErr(err)
	}
	if kind != connectKind {
		return "", ErrNotFound
	}
	return s.DecryptSecret(ctx, userID, sec.ID)
}

func (s *Store) upsertSSHSeedTx(ctx context.Context, tx *sql.Tx, user User, seed string) error {
	var id, kind string
	err := tx.QueryRowContext(ctx, `SELECT id, kind FROM secrets WHERE user_id = ? AND name = ?`, user.ID, DigitalOceanSSHSecret).Scan(&id, &kind)
	ct, nonce, sealErr := s.seal([]byte(seed))
	if sealErr != nil {
		return sealErr
	}
	stamp := s.stamp(s.Now())
	if errors.Is(err, sql.ErrNoRows) {
		newID, idErr := newID()
		if idErr != nil {
			return idErr
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO secrets(id, workspace_id, user_id, name, label, provider, ciphertext, nonce, created_at, kind) VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?)`,
			newID, user.WorkspaceID, user.ID, DigitalOceanSSHSecret, "DigitalOcean SSH key", ct, nonce, stamp, connectKind)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrConflict
		}
		return err
	}
	if err != nil {
		return err
	}
	if kind != connectKind {
		return ErrConflict
	}
	res, err := tx.ExecContext(ctx, `UPDATE secrets SET ciphertext = ?, nonce = ?, rotated_at = ? WHERE id = ? AND user_id = ? AND kind = ?`,
		ct, nonce, stamp, id, user.ID, connectKind)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func sshPublicLineOK(line string) bool {
	if line == "" || len(line) > 400 || strings.ContainsAny(line, "\x00\n\r") {
		return false
	}
	pub, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || pub == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return false
	}
	return pub.Type() == ssh.KeyAlgoED25519
}

func sshSeedOK(seed string) bool {
	if !secretValueOK(seed) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(seed)
	if err != nil || len(raw) != ed25519.SeedSize {
		return false
	}
	return true
}

func sshRemoteIDOK(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > 20 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
