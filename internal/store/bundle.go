package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

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

// SecretFixture is a plaintext secret granted to an agent.
// S08 replaces this with the encrypted vault. Do not log Value.
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
	return s.LatestBundle(ctx, userID, agentID)
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

// PutSecret stores a plaintext fixture secret for the caller.
func (s *Store) PutSecret(ctx context.Context, userID, name, value string) (SecretFixture, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || value == "" || len(value) > 4000 {
		return SecretFixture{}, ErrInvalid
	}
	if strings.ContainsAny(value, "\x00\n\r") {
		return SecretFixture{}, ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return SecretFixture{}, err
	}
	id, err := newID()
	if err != nil {
		return SecretFixture{}, err
	}
	stamp := s.stamp(s.Now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO secret_fixtures(id, workspace_id, user_id, name, value, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, user.WorkspaceID, userID, name, value, stamp)
	if err != nil {
		return SecretFixture{}, err
	}
	return SecretFixture{ID: id, Name: name, Value: value}, nil
}

// GrantSecret lets one of the caller's agents pull one of the caller's secrets.
func (s *Store) GrantSecret(ctx context.Context, userID, agentID, secretID string) error {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	var workspace string
	err = s.db.QueryRowContext(ctx, `SELECT workspace_id FROM secret_fixtures WHERE id = ? AND user_id = ?`, secretID, userID).Scan(&workspace)
	if err != nil {
		return scanErr(err)
	}
	if workspace != agent.WorkspaceID {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_secret_grants(agent_id, secret_id, workspace_id, user_id) VALUES (?, ?, ?, ?)
ON CONFLICT(agent_id, secret_id) DO NOTHING`, agentID, secretID, workspace, userID)
	return err
}

// GrantedSecrets returns the secrets granted to one of the caller's agents.
func (s *Store) GrantedSecrets(ctx context.Context, userID, agentID string) ([]SecretFixture, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.name, f.value FROM secret_fixtures f
JOIN agent_secret_grants g ON g.secret_id = f.id AND g.user_id = f.user_id
WHERE g.user_id = ? AND g.agent_id = ? ORDER BY f.name`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretFixture
	for rows.Next() {
		var sec SecretFixture
		if err := rows.Scan(&sec.ID, &sec.Name, &sec.Value); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

// IssueBootstrap mints a one-time token for one of the caller's agents.
// The plaintext is returned once and only the hash is stored.
func (s *Store) IssueBootstrap(ctx context.Context, userID, agentID string) (string, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	plain, err := newToken()
	if err != nil {
		return "", err
	}
	stamp := s.stamp(s.Now())
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_tokens(token_hash, workspace_id, user_id, agent_id, kind, created_at) VALUES (?, ?, ?, ?, 'bootstrap', ?)`,
		tokenHash(plain), agent.WorkspaceID, userID, agentID, stamp)
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
	var userID, workspace string
	err = tx.QueryRowContext(ctx, `SELECT user_id, workspace_id FROM agent_tokens WHERE token_hash = ? AND agent_id = ? AND kind = 'bootstrap' AND used_at IS NULL`,
		tokenHash(plain), agentID).Scan(&userID, &workspace)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tokens(token_hash, workspace_id, user_id, agent_id, kind, created_at) VALUES (?, ?, ?, ?, 'agent', ?)`,
		tokenHash(next), workspace, userID, agentID, stamp); err != nil {
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
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM agent_tokens WHERE token_hash = ? AND agent_id = ? AND kind = 'agent' AND used_at IS NULL`,
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
