package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// DefaultMaxAgents is the machine cap seeded for an account.
// It stays under the team memory limit and is not a usage bill.
const DefaultMaxAgents = 4

// VM is one machine row. Token is the per-agent secret when one has been
// exchanged. Callers that render pages use LiveVM, which leaves Token empty.
type VM struct {
	ID             string
	UserID         string
	AgentID        string
	MicroVMID      string
	Name           string
	Endpoint       string
	Image          string
	State          string
	Failure        string
	Token          string
	LastWake       string
	CreatedAt      string
	CreatedRunning string
	Workspace      string
	// StateGen bumps on every machine state write. Pause compares it so two
	// wakes in the same second cannot be mistaken for one.
	StateGen int64
}

// BeginVM reserves a machine slot for one of the caller's agents.
// The row counts toward the quota until it is released.
func (s *Store) BeginVM(ctx context.Context, userID, agentID, name, image string) (VM, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return VM{}, err
	}
	name = strings.TrimSpace(name)
	image = strings.TrimSpace(image)
	if name == "" || image == "" {
		return VM{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VM{}, err
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).Scan(&existing); err != nil {
		return VM{}, err
	}
	if existing > 0 {
		return VM{}, ErrConflict
	}
	var used, max int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_vms WHERE user_id = ? AND deleted_at IS NULL`, userID).Scan(&used); err != nil {
		return VM{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT max_agents FROM user_quotas WHERE user_id = ?`, userID).Scan(&max)
	if err == sql.ErrNoRows {
		max = DefaultMaxAgents
	} else if err != nil {
		return VM{}, err
	}
	if used >= max {
		return VM{}, ErrQuota
	}
	id, err := newID()
	if err != nil {
		return VM{}, err
	}
	stamp := s.stamp(s.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_vms(id, workspace_id, user_id, agent_id, microvm_id, name, endpoint, image_ref, state, failure, created_at, observe_boot, state_gen)
VALUES (?, ?, ?, ?, 'pending', ?, '', ?, 'creating', '', ?, 1, 1)`, id, agent.WorkspaceID, userID, agentID, name, image, stamp); err != nil {
		return VM{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = 'provisioning', activity = 'Starting' WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID); err != nil {
		return VM{}, err
	}
	if err := tx.Commit(); err != nil {
		return VM{}, err
	}
	return s.LiveVM(ctx, userID, agentID)
}

// FinishVM stores the API id and state after a create returns.
func (s *Store) FinishVM(ctx context.Context, userID, agentID, microID, endpoint, state, failure string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	if strings.TrimSpace(microID) == "" || microID == "pending" {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_vms SET microvm_id = ?, endpoint = ?, state = ?, failure = ?, state_gen = state_gen + 1 WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`,
		microID, endpoint, state, failure, userID, agentID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	return s.ApplyVMState(ctx, userID, agentID, state, endpoint, failure)
}

// MarkPausedIfUnchanged sets paused only when the row is still running and
// its state generation is the one the caller observed. A wake in between
// bumps that generation, including two wakes in the same second, so a stale
// list does not overwrite it.
func (s *Store) MarkPausedIfUnchanged(ctx context.Context, userID, agentID, microID string, stateGen int64, endpoint string) (bool, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return false, err
	}
	status, activity, ok := agentStatusFor("paused", agent.Status)
	if !ok {
		return false, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE agent_vms SET state = 'paused', state_gen = state_gen + 1,
endpoint = CASE WHEN ? != '' THEN ? ELSE endpoint END
WHERE user_id = ? AND agent_id = ? AND microvm_id = ? AND state = 'running'
AND state_gen = ? AND deleted_at IS NULL`,
		endpoint, endpoint, userID, agentID, microID, stateGen)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = ?, activity = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, status, activity, agentID, userID); err != nil {
		return false, err
	}
	var vmID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_vms WHERE user_id = ? AND agent_id = ? AND microvm_id = ? AND deleted_at IS NULL`, userID, agentID, microID).Scan(&vmID); err != nil {
		return false, err
	}
	if err := s.noteUsageTx(ctx, tx, agent.WorkspaceID, userID, agentID, vmID, agent.Size, "paused"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// SecretFP is the last secrets fingerprint sent to this agent. Empty means
// the guest has not been reloaded for the current set.
func (s *Store) SecretFP(ctx context.Context, userID, agentID string) (string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", err
	}
	var fp string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(secret_fp, '') FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&fp)
	return fp, err
}

// SecretFPRow is one stored fingerprint. Startup uses it to rewrite the
// plain sha256 form. It is not a user handler.
type SecretFPRow struct {
	UserID  string
	AgentID string
	FP      string
}

// ListSecretFPs returns stored fingerprints for agents that still exist.
func (s *Store) ListSecretFPs(ctx context.Context) ([]SecretFPRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, id, COALESCE(secret_fp, '') FROM agents WHERE deleted_at IS NULL AND COALESCE(secret_fp, '') != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecretFPRow
	for rows.Next() {
		var row SecretFPRow
		if err := rows.Scan(&row.UserID, &row.AgentID, &row.FP); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetSecretFP records a secrets fingerprint. The value is a hash, not a secret.
func (s *Store) SetSecretFP(ctx context.Context, userID, agentID, fp string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET secret_fp = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, fp, agentID, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// ApplyVMState copies a machine API state onto the caller's row.
// It does not contact the guest.
func (s *Store) ApplyVMState(ctx context.Context, userID, agentID, state, endpoint, failure string) error {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	status, activity, ok := agentStatusFor(state, agent.Status)
	if !ok {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	wake := ""
	runningAt := ""
	if state == "running" {
		wake = s.stamp(s.Now())
		runningAt = wake
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_vms SET state = ?, failure = ?, state_gen = state_gen + 1,
endpoint = CASE WHEN ? != '' THEN ? ELSE endpoint END,
last_wake_at = CASE WHEN ? != '' THEN ? ELSE last_wake_at END,
created_running_at = CASE WHEN ? = 'running' AND observe_boot = 1 AND COALESCE(created_running_at, '') = '' THEN ? ELSE created_running_at END
WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`,
		state, failure, endpoint, endpoint, wake, wake, state, runningAt, userID, agentID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = ?, activity = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, status, activity, agentID, userID); err != nil {
		return err
	}
	var vmID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).Scan(&vmID); err != nil {
		return err
	}
	if err := s.noteUsageTx(ctx, tx, agent.WorkspaceID, userID, agentID, vmID, agent.Size, state); err != nil {
		return err
	}
	return tx.Commit()
}

// ReleaseVM drops a reservation and marks the agent failed so the owner can retry.
// An open usage interval for that machine is closed in the same write.
func (s *Store) ReleaseVM(ctx context.Context, userID, agentID string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := s.stamp(s.Now())
	var vmID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).Scan(&vmID)
	if err == sql.ErrNoRows {
		vmID = ""
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_vms SET state = 'failed', deleted_at = ?, state_gen = state_gen + 1 WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, stamp, userID, agentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = 'failed', activity = '' WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID); err != nil {
		return err
	}
	if err := closeUsageVMTx(ctx, tx, userID, vmID, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

// NoteAgent stores a short status line on the caller's agent.
func (s *Store) NoteAgent(ctx context.Context, userID, agentID, activity string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	activity = strings.TrimSpace(activity)
	if len(activity) > 180 {
		activity = activity[:180]
	}
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET activity = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, activity, agentID, userID)
	return err
}

// ReleaseStalePending drops creating rows that never received a machine id
// and are older than the cutoff. A process that died during create leaves
// the quota slot taken. This runs at startup and is not a user handler.
func (s *Store) ReleaseStalePending(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		return 0, ErrInvalid
	}
	cutoff := s.stamp(s.Now().Add(-olderThan))
	stamp := s.stamp(s.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = 'failed', activity = ''
WHERE deleted_at IS NULL AND id IN (
	SELECT agent_id FROM agent_vms
	WHERE deleted_at IS NULL AND state = 'creating' AND microvm_id = 'pending' AND created_at < ?
)`, cutoff); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_vms SET state = 'failed', deleted_at = ?, state_gen = state_gen + 1
WHERE deleted_at IS NULL AND state = 'creating' AND microvm_id = 'pending' AND created_at < ?`, stamp, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(n), nil
}

// OrphanCandidate reports whether this database has a pending, failed, or
// deleted machine row for agentID. A live row does not count. The startup
// sweep uses it so a copied database does not delete a machine whose agent
// id is unknown here. It is not a user handler.
func (s *Store) OrphanCandidate(ctx context.Context, agentID string) (bool, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false, nil
	}
	var live int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_vms WHERE agent_id = ? AND deleted_at IS NULL AND state NOT IN ('failed', 'deleted')`, agentID).Scan(&live)
	if err != nil {
		return false, err
	}
	if live > 0 {
		return false, nil
	}
	var n int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_vms WHERE agent_id = ? AND (
		state IN ('failed', 'deleted')
		OR deleted_at IS NOT NULL
		OR (state = 'creating' AND microvm_id = 'pending')
	)`, agentID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// LiveMicroIDs returns machine ids that still have a live row.
// Startup orphan cleanup uses it. It is not a user handler and it returns
// no tokens, names, or owners.
func (s *Store) LiveMicroIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT microvm_id FROM agent_vms WHERE deleted_at IS NULL AND microvm_id != '' AND microvm_id != 'pending'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// MarkVMDeleted records a confirmed delete and revokes that machine's agent token.
func (s *Store) MarkVMDeleted(ctx context.Context, userID, agentID string) error {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var vmID, hash string
	err = tx.QueryRowContext(ctx, `SELECT id, COALESCE(agent_token_hash, '') FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).Scan(&vmID, &hash)
	if err != nil {
		return scanErr(err)
	}
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE agent_vms SET state = 'deleted', deleted_at = ?, state_gen = state_gen + 1 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, stamp, vmID, userID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	if err := revokeAgentTokens(ctx, tx, userID, agentID, "", "", stamp); err != nil {
		return err
	}
	if err := closeUsageVMTx(ctx, tx, userID, vmID, stamp); err != nil {
		return err
	}
	_ = hash
	return tx.Commit()
}

// LiveVM returns the caller's current machine without the agent token.
func (s *Store) LiveVM(ctx context.Context, userID, agentID string) (VM, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return VM{}, err
	}
	var vm VM
	var wake sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, agent_id, microvm_id, name, endpoint, image_ref, state, failure, COALESCE(last_wake_at, ''), created_at, COALESCE(created_running_at, ''), state_gen
FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).
		Scan(&vm.ID, &vm.Workspace, &vm.UserID, &vm.AgentID, &vm.MicroVMID, &vm.Name, &vm.Endpoint, &vm.Image, &vm.State, &vm.Failure, &wake, &vm.CreatedAt, &vm.CreatedRunning, &vm.StateGen)
	if err != nil {
		return VM{}, scanErr(err)
	}
	vm.LastWake = wake.String
	return vm, nil
}

// VMCredentials returns the endpoint and agent token for one of the caller's machines.
func (s *Store) VMCredentials(ctx context.Context, userID, agentID string) (endpoint, token string, err error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return "", "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT endpoint, agent_token FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).Scan(&endpoint, &token)
	if err != nil {
		return "", "", scanErr(err)
	}
	return endpoint, token, nil
}

// SaveVMToken stores the per-agent token on the machine the bootstrap was bound to.
// A token bound to a replacement row is staged there and does not touch the live row.
// A machine that has not been reserved yet is ignored so a local boot still works.
func (s *Store) SaveVMToken(ctx context.Context, agentID, plain string) error {
	_, err := s.PlaceVMToken(ctx, agentID, plain)
	return err
}

// PlaceVMToken stores a token the way SaveVMToken does.
// The bool is true when the live machine row was updated.
func (s *Store) PlaceVMToken(ctx context.Context, agentID, plain string) (bool, error) {
	plain = strings.TrimSpace(plain)
	agentID = strings.TrimSpace(agentID)
	if plain == "" || agentID == "" {
		return false, ErrInvalid
	}
	var userID, vmID string
	err := s.db.QueryRowContext(ctx, `SELECT user_id, COALESCE(agent_vm_id, '') FROM agent_tokens
WHERE token_hash = ? AND agent_id = ? AND kind = 'agent' AND revoked_at IS NULL`, tokenHash(plain), agentID).Scan(&userID, &vmID)
	if err != nil {
		return false, scanErr(err)
	}
	if vmID != "" {
		res, err := s.db.ExecContext(ctx, `UPDATE agent_vms SET agent_token = ?, agent_token_hash = ? WHERE id = ? AND user_id = ? AND agent_id = ? AND deleted_at IS NULL`,
			plain, tokenHash(plain), vmID, userID, agentID)
		if err != nil {
			return false, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
		if err := s.stageReplacementToken(ctx, userID, agentID, vmID, plain); err != nil {
			return false, err
		}
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE agent_vms SET agent_token = ?, agent_token_hash = ? WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`,
		plain, tokenHash(plain), userID, agentID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// SetQuota sets the caller's machine cap.
func (s *Store) SetQuota(ctx context.Context, userID string, max int) error {
	if max < 0 {
		return ErrInvalid
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE user_quotas SET max_agents = ? WHERE user_id = ?`, max, user.ID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	return nil
}

// Quota returns how many machines the caller has and the cap.
func (s *Store) Quota(ctx context.Context, userID string) (used, max int, err error) {
	if _, err = s.GetUser(ctx, userID); err != nil {
		return 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_vms WHERE user_id = ? AND deleted_at IS NULL`, userID).Scan(&used); err != nil {
		return 0, 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT max_agents FROM user_quotas WHERE user_id = ?`, userID).Scan(&max)
	if err == sql.ErrNoRows {
		return used, DefaultMaxAgents, nil
	}
	return used, max, err
}

func agentStatusFor(state, prev string) (status, activity string, ok bool) {
	switch state {
	case "running":
		return "ready", "", true
	case "paused":
		return "paused", "", true
	case "creating":
		if prev == "paused" {
			return "provisioning", "Waking", true
		}
		return "provisioning", "Starting", true
	case "failed", "terminated":
		return "failed", "", true
	default:
		return "", "", false
	}
}
