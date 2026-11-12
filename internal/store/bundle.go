package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/scott/castbot/internal/bundle"
)

// AgentBundle is one saved agent directory. The sha256 is of the tar
// castlet pulls. This table is the S03 fixture store, not an image.
type AgentBundle struct {
	AgentID      string
	Version      string
	WorkspaceID  string
	UserID       string
	SHA256       string
	YAML         string
	Instructions string
	CreatedAt    string
}

// SecretFixture is one secret granted to an agent.
// Value is decrypted in memory for the caller. Do not log it.
// The vault stores ciphertext. See secrets.go.
type SecretFixture struct {
	ID    string
	Name  string
	Value string
}

// PutBundle saves a bundle version for one of the caller's agents.
func (s *Store) PutBundle(ctx context.Context, userID, agentID, version, yaml, instructions string) (AgentBundle, error) {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 64 {
		return AgentBundle{}, ErrInvalid
	}
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return AgentBundle{}, err
	}
	raw, sum, err := bundle.Archive(yaml, instructions)
	if err != nil {
		return AgentBundle{}, ErrInvalid
	}
	_ = raw
	stamp := s.stamp(s.Now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_bundles(agent_id, version, workspace_id, user_id, sha256, agent_yaml, instructions_md, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(agent_id, version) DO UPDATE SET sha256 = excluded.sha256, agent_yaml = excluded.agent_yaml, instructions_md = excluded.instructions_md, created_at = excluded.created_at
WHERE agent_bundles.user_id = excluded.user_id`,
		agentID, version, agent.WorkspaceID, userID, sum, yaml, instructions, userID, stamp)
	if err != nil {
		return AgentBundle{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET bundle_version = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, version, agentID, userID); err != nil {
		return AgentBundle{}, err
	}
	return s.LatestBundle(ctx, userID, agentID)
}

// SetPendingBundle records a bundle version that still has to be reloaded
// onto a machine that was not running when it was saved.
func (s *Store) SetPendingBundle(ctx context.Context, userID, agentID, version string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 64 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET pending_bundle = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, version, agentID, userID)
	return err
}

// ClearPendingBundle forgets a pending reload after that version succeeded.
// A newer save that landed while the reload was in flight stays pending.
func (s *Store) ClearPendingBundle(ctx context.Context, userID, agentID, version string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET pending_bundle = '' WHERE id = ? AND user_id = ? AND pending_bundle = ? AND deleted_at IS NULL`, agentID, userID, version)
	return err
}

// KeptYAML is the unowned agent.yaml stored on the agent row.
func (s *Store) KeptYAML(ctx context.Context, userID, agentID string) (string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT kept_yaml FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&raw)
	if err != nil {
		return "", scanErr(err)
	}
	return raw, nil
}

// SetKeptYAML stores the unowned agent.yaml on the caller's agent.
func (s *Store) SetKeptYAML(ctx context.Context, userID, agentID, raw string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	if len(raw) > 64<<10 {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET kept_yaml = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, raw, agentID, userID)
	return err
}

// PutImportDraft stores a short-lived copy of the unowned agent.yaml.
// agentID is empty until the agent exists. The form posts the id only.
func (s *Store) PutImportDraft(ctx context.Context, userID, agentID, raw string) (string, error) {
	if len(raw) > 64<<10 {
		return "", ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return "", err
	}
	if agentID != "" {
		if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
			return "", err
		}
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	stamp := s.stamp(s.Now())
	expires := s.stamp(s.Now().Add(time.Hour))
	_, err = s.db.ExecContext(ctx, `INSERT INTO import_drafts(id, workspace_id, user_id, agent_id, kept_yaml, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, user.WorkspaceID, userID, agentID, raw, expires, stamp)
	if err != nil {
		return "", err
	}
	return id, nil
}

// ImportDraft returns one unexpired draft owned by the caller for this agent.
func (s *Store) ImportDraft(ctx context.Context, userID, agentID, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", ErrNotFound
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT kept_yaml FROM import_drafts WHERE id = ? AND user_id = ? AND agent_id = ? AND expires_at > ?`,
		id, userID, agentID, s.stamp(s.Now())).Scan(&raw)
	if err != nil {
		return "", scanErr(err)
	}
	return raw, nil
}

// DeleteImportDraft removes one of the caller's drafts.
func (s *Store) DeleteImportDraft(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM import_drafts WHERE id = ? AND user_id = ?`, id, userID)
	return err
}

// PendingBundle is the version waiting for the next time the machine is running.
func (s *Store) PendingBundle(ctx context.Context, userID, agentID string) (string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", err
	}
	var version string
	err := s.db.QueryRowContext(ctx, `SELECT pending_bundle FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&version)
	if err != nil {
		return "", scanErr(err)
	}
	return version, nil
}

// LatestBundle returns the newest bundle for one of the caller's agents.
func (s *Store) LatestBundle(ctx context.Context, userID, agentID string) (AgentBundle, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return AgentBundle{}, err
	}
	var b AgentBundle
	err := s.db.QueryRowContext(ctx, `SELECT agent_id, version, workspace_id, user_id, sha256, agent_yaml, instructions_md, created_at
FROM agent_bundles WHERE user_id = ? AND agent_id = ? ORDER BY created_at DESC, version DESC LIMIT 1`, userID, agentID).
		Scan(&b.AgentID, &b.Version, &b.WorkspaceID, &b.UserID, &b.SHA256, &b.YAML, &b.Instructions, &b.CreatedAt)
	if err != nil {
		return AgentBundle{}, scanErr(err)
	}
	return b, nil
}

// PutSecret stores an agent secret for the caller. The value is encrypted.
// The name must match the vault rule. The value is at most 8 KB.
func (s *Store) PutSecret(ctx context.Context, userID, name, value string) (SecretFixture, error) {
	sec, err := s.CreateSecret(ctx, userID, SecretInput{Name: name, Value: value})
	if err != nil {
		return SecretFixture{}, err
	}
	return SecretFixture{ID: sec.ID, Name: sec.Name, Value: value}, nil
}

// GrantSecret lets one of the caller's agents pull one of the caller's secrets.
func (s *Store) GrantSecret(ctx context.Context, userID, agentID, secretID string) error {
	return s.grantSecret(ctx, userID, agentID, secretID)
}

// GrantedSecrets returns the agent secrets granted to one of the caller's agents.
// Values are decrypted in memory. Model keys are not included.
func (s *Store) GrantedSecrets(ctx context.Context, userID, agentID string) ([]SecretFixture, error) {
	return s.grantedSecrets(ctx, userID, agentID)
}

// IssueBootstrap mints a one-time token for one of the caller's agents.
// The plaintext is returned once and only the hash is stored.
func (s *Store) IssueBootstrap(ctx context.Context, userID, agentID string) (string, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	return s.issueBootstrap(ctx, agent, "")
}

// IssueBootstrapFor mints a one-time token bound to a machine row id.
// Exchange writes the agent token onto that row only, so a replacement
// guest cannot overwrite the live machine's token.
func (s *Store) IssueBootstrapFor(ctx context.Context, userID, agentID, agentVMID string) (string, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	return s.issueBootstrap(ctx, agent, strings.TrimSpace(agentVMID))
}

func (s *Store) issueBootstrap(ctx context.Context, agent Agent, agentVMID string) (string, error) {
	plain, err := newToken()
	if err != nil {
		return "", err
	}
	stamp := s.stamp(s.Now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_tokens(token_hash, workspace_id, user_id, agent_id, kind, agent_vm_id, created_at) VALUES (?, ?, ?, ?, 'bootstrap', ?, ?)`,
		tokenHash(plain), agent.WorkspaceID, agent.UserID, agent.ID, agentVMID, stamp)
	if err != nil {
		return "", err
	}
	return plain, nil
}

// ExchangeBootstrap consumes a one-time bootstrap token and returns a new
// per-agent token. The plaintext is returned once. A second call with the
// same bootstrap token fails.
func (s *Store) ExchangeBootstrap(ctx context.Context, agentID, plain string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	plain = strings.TrimSpace(plain)
	if agentID == "" || plain == "" {
		return "", ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var userID, workspace, vmID string
	err = tx.QueryRowContext(ctx, `SELECT user_id, workspace_id, COALESCE(agent_vm_id, '') FROM agent_tokens
WHERE token_hash = ? AND agent_id = ? AND kind = 'bootstrap' AND used_at IS NULL AND revoked_at IS NULL`,
		tokenHash(plain), agentID).Scan(&userID, &workspace, &vmID)
	if err != nil {
		return "", scanErr(err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&n); err != nil {
		return "", err
	}
	if n != 1 {
		return "", ErrNotFound
	}
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`, stamp, tokenHash(plain))
	if err != nil {
		return "", err
	}
	if err := rowsAffected(res); err != nil {
		return "", err
	}
	next, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tokens(token_hash, workspace_id, user_id, agent_id, kind, agent_vm_id, created_at) VALUES (?, ?, ?, ?, 'agent', ?, ?)`,
		tokenHash(next), workspace, userID, agentID, vmID, stamp); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return next, nil
}

// UserForAgentToken resolves a per-agent token to its owner.
// A token for a different agent is not found.
func (s *Store) UserForAgentToken(ctx context.Context, agentID, plain string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	plain = strings.TrimSpace(plain)
	if agentID == "" || plain == "" {
		return "", ErrNotFound
	}
	var userID string
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM agent_tokens WHERE token_hash = ? AND agent_id = ? AND kind = 'agent' AND used_at IS NULL AND revoked_at IS NULL`,
		tokenHash(plain), agentID).Scan(&userID)
	if err != nil {
		return "", scanErr(err)
	}
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", err
	}
	return userID, nil
}

func tokenHash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "cbt_" + hex.EncodeToString(buf), nil
}
