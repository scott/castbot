package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Replacement is one in-progress or finished machine replace.
// It holds no token, bootstrap value, or file contents.
type Replacement struct {
	ID             string
	WorkspaceID    string
	UserID         string
	AgentID        string
	State          string
	Step           string
	Class          string
	OldVMID        string
	OldMicro       string
	OldImage       string
	OldState       string
	NewVMID        string
	NewMicro       string
	NewEndpoint    string
	NewTokenHash   string
	NewImage       string
	NewName        string
	Actor          string
	NoCopy         bool
	Woke           bool
	MemoryBytes    int64
	FilesDone      int
	FilesTotal     int
	FileBytes      int64
	DeletePending  bool
	CleanupPending bool
	CleanupSince   string
	CleanupAfter   string
	CleanupWaitMS  int
	CleanupAlerted bool
	CreatedAt      string
	UpdatedAt      string
	FinishedAt     string
}

// ReplaceBlock is a precondition the caller can show as a single line.
type ReplaceBlock struct {
	Line string
}

func (e *ReplaceBlock) Error() string {
	if e == nil {
		return "replace blocked"
	}
	return e.Line
}

const (
	replRunning   = "running"
	replFailed    = "failed"
	replSucceeded = "succeeded"
)

func (s *Store) migrateReplace() error {
	if err := s.ensureColumn("agent_tokens", "agent_vm_id", `ALTER TABLE agent_tokens ADD COLUMN agent_vm_id TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("agent_tokens", "revoked_at", `ALTER TABLE agent_tokens ADD COLUMN revoked_at TEXT`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS vm_replacements (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  state TEXT NOT NULL,
  step TEXT NOT NULL,
  error_class TEXT NOT NULL DEFAULT '',
  old_vm_id TEXT NOT NULL,
  old_microvm_id TEXT NOT NULL,
  old_image TEXT NOT NULL,
  old_state TEXT NOT NULL DEFAULT '',
  new_vm_id TEXT NOT NULL,
  new_microvm_id TEXT NOT NULL DEFAULT '',
  new_endpoint TEXT NOT NULL DEFAULT '',
  new_token_hash TEXT NOT NULL DEFAULT '',
  new_image TEXT NOT NULL,
  new_name TEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL,
  no_copy INTEGER NOT NULL DEFAULT 0,
  woke INTEGER NOT NULL DEFAULT 0,
  memory_bytes INTEGER NOT NULL DEFAULT 0,
  files_done INTEGER NOT NULL DEFAULT 0,
  files_total INTEGER NOT NULL DEFAULT 0,
  file_bytes INTEGER NOT NULL DEFAULT 0,
  delete_pending INTEGER NOT NULL DEFAULT 0,
  cleanup_pending INTEGER NOT NULL DEFAULT 0,
  cleanup_since TEXT NOT NULL DEFAULT '',
  cleanup_after TEXT NOT NULL DEFAULT '',
  cleanup_wait_ms INTEGER NOT NULL DEFAULT 0,
  cleanup_alerted INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  finished_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_vm_repl_active ON vm_replacements(agent_id) WHERE state = 'running'`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_vm_repl_user ON vm_replacements(user_id, agent_id, created_at)`); err != nil {
		return err
	}
	if err := s.ensureColumn("vm_replacements", "cleanup_pending", `ALTER TABLE vm_replacements ADD COLUMN cleanup_pending INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("vm_replacements", "cleanup_since", `ALTER TABLE vm_replacements ADD COLUMN cleanup_since TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("vm_replacements", "cleanup_after", `ALTER TABLE vm_replacements ADD COLUMN cleanup_after TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := s.ensureColumn("vm_replacements", "cleanup_wait_ms", `ALTER TABLE vm_replacements ADD COLUMN cleanup_wait_ms INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn("vm_replacements", "cleanup_alerted", `ALTER TABLE vm_replacements ADD COLUMN cleanup_alerted INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// The staged agent token is plaintext until the switch copies it onto agent_vms.
	// It is not a column of vm_replacements, and it is deleted at the switch.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS vm_replacement_tokens (
  replacement_id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  agent_vm_id TEXT NOT NULL,
  token TEXT NOT NULL
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (15) ON CONFLICT(version) DO NOTHING`); err != nil {
		return err
	}
	return nil
}

// BeginReplacement locks one agent for a replace after the preconditions pass.
// The new machine row id is reserved here and is not inserted into agent_vms.
func (s *Store) BeginReplacement(ctx context.Context, userID, agentID, actor, newImage string, noCopy bool) (Replacement, VM, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return Replacement{}, VM{}, err
	}
	actor = strings.TrimSpace(actor)
	newImage = strings.TrimSpace(newImage)
	if actor == "" || newImage == "" {
		return Replacement{}, VM{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Replacement{}, VM{}, err
	}
	defer tx.Rollback()
	vm, err := lockLiveVM(ctx, tx, userID, agentID)
	if err != nil {
		return Replacement{}, VM{}, err
	}
	if line := replaceBlock(ctx, tx, userID, agentID, vm); line != "" {
		return Replacement{}, VM{}, &ReplaceBlock{Line: line}
	}
	id, err := newID()
	if err != nil {
		return Replacement{}, VM{}, err
	}
	newVM, err := newID()
	if err != nil {
		return Replacement{}, VM{}, err
	}
	stamp := s.stamp(s.Now())
	no := 0
	if noCopy {
		no = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO vm_replacements(
  id, workspace_id, user_id, agent_id, state, step, old_vm_id, old_microvm_id, old_image, old_state,
  new_vm_id, new_image, actor, no_copy, created_at, updated_at)
VALUES (?, ?, ?, ?, 'running', 'create', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, agent.WorkspaceID, userID, agentID, vm.ID, vm.MicroVMID, vm.Image, vm.State, newVM, newImage, actor, no, stamp, stamp)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Replacement{}, VM{}, &ReplaceBlock{Line: "A replacement is already running."}
		}
		return Replacement{}, VM{}, err
	}
	if err := tx.Commit(); err != nil {
		return Replacement{}, VM{}, err
	}
	row, err := s.GetReplacement(ctx, userID, id)
	if err != nil {
		return Replacement{}, VM{}, err
	}
	return row, vm, nil
}

// ReplacePrecheck reads the live machine and the precondition line, if any.
// It does not insert a row and it does not call a machine API.
func (s *Store) ReplacePrecheck(ctx context.Context, userID, agentID string) (VM, string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return VM{}, "", err
	}
	vm, err := s.LiveVM(ctx, userID, agentID)
	if err != nil {
		if err == ErrNotFound {
			return VM{}, "This agent has no machine yet.", nil
		}
		return VM{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VM{}, "", err
	}
	defer tx.Rollback()
	line := replaceBlock(ctx, tx, userID, agentID, vm)
	if err := tx.Commit(); err != nil {
		return VM{}, "", err
	}
	return vm, line, nil
}

func lockLiveVM(ctx context.Context, tx *sql.Tx, userID, agentID string) (VM, error) {
	var vm VM
	var wake sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id, workspace_id, user_id, agent_id, microvm_id, name, endpoint, image_ref, state, failure, COALESCE(last_wake_at, ''), created_at, COALESCE(created_running_at, '')
FROM agent_vms WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL`, userID, agentID).
		Scan(&vm.ID, &vm.Workspace, &vm.UserID, &vm.AgentID, &vm.MicroVMID, &vm.Name, &vm.Endpoint, &vm.Image, &vm.State, &vm.Failure, &wake, &vm.CreatedAt, &vm.CreatedRunning)
	if err != nil {
		return VM{}, scanErr(err)
	}
	vm.LastWake = wake.String
	return vm, nil
}

func replaceBlock(ctx context.Context, tx *sql.Tx, userID, agentID string, vm VM) string {
	if vm.MicroVMID == "" || vm.MicroVMID == "pending" || (vm.State != "running" && vm.State != "paused") {
		if vm.State == "creating" {
			return "The machine is still starting."
		}
		if vm.State == "failed" {
			return "The machine did not start."
		}
		return "The machine is still starting."
	}
	var runs int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE user_id = ? AND agent_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')`, userID, agentID).Scan(&runs); err != nil {
		return "The machine record could not be read."
	}
	if runs > 0 {
		return "Finish or stop the reply in progress first."
	}
	var approvals int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals a
JOIN runs r ON r.id = a.run_id AND r.user_id = a.user_id
WHERE a.user_id = ? AND r.agent_id = ? AND (a.status = 'pending' OR (a.sent = 0 AND a.status IN ('approved', 'denied')))`, userID, agentID).Scan(&approvals)
	if err != nil {
		return "The machine record could not be read."
	}
	if approvals > 0 {
		return "Answer the waiting approval first."
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_replacements WHERE agent_id = ? AND state = 'running'`, agentID).Scan(&active); err != nil {
		return "The machine record could not be read."
	}
	if active > 0 {
		return "A replacement is already running."
	}
	return ""
}

func scanReplacement(row interface{ Scan(...any) error }) (Replacement, error) {
	var r Replacement
	var noCopy, woke, pending, cleanup, alerted int
	var finished sql.NullString
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.UserID, &r.AgentID, &r.State, &r.Step, &r.Class,
		&r.OldVMID, &r.OldMicro, &r.OldImage, &r.OldState, &r.NewVMID, &r.NewMicro, &r.NewEndpoint, &r.NewTokenHash,
		&r.NewImage, &r.NewName, &r.Actor, &noCopy, &woke, &r.MemoryBytes, &r.FilesDone, &r.FilesTotal, &r.FileBytes,
		&pending, &cleanup, &r.CleanupSince, &r.CleanupAfter, &r.CleanupWaitMS, &alerted, &r.CreatedAt, &r.UpdatedAt, &finished)
	if err != nil {
		return Replacement{}, scanErr(err)
	}
	r.NoCopy = noCopy != 0
	r.Woke = woke != 0
	r.DeletePending = pending != 0
	r.CleanupPending = cleanup != 0
	r.CleanupAlerted = alerted != 0
	r.FinishedAt = finished.String
	return r, nil
}

const replSelect = `SELECT id, workspace_id, user_id, agent_id, state, step, error_class,
  old_vm_id, old_microvm_id, old_image, old_state, new_vm_id, new_microvm_id, new_endpoint, new_token_hash,
  new_image, new_name, actor, no_copy, woke, memory_bytes, files_done, files_total,   file_bytes,
  delete_pending, cleanup_pending, cleanup_since, cleanup_after, cleanup_wait_ms, cleanup_alerted,
  created_at, updated_at, finished_at FROM vm_replacements`

// GetReplacement returns one replacement owned by the caller.
func (s *Store) GetReplacement(ctx context.Context, userID, id string) (Replacement, error) {
	return scanReplacement(s.db.QueryRowContext(ctx, replSelect+` WHERE id = ? AND user_id = ?`, id, userID))
}

// ActiveReplacement returns the caller's in-progress replace, if any.
func (s *Store) ActiveReplacement(ctx context.Context, userID, agentID string) (Replacement, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return Replacement{}, err
	}
	return scanReplacement(s.db.QueryRowContext(ctx, replSelect+` WHERE user_id = ? AND agent_id = ? AND state = 'running'`, userID, agentID))
}

// LatestReplacement returns the caller's newest replacement, including a finished one.
func (s *Store) LatestReplacement(ctx context.Context, userID, agentID string) (Replacement, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return Replacement{}, err
	}
	return scanReplacement(s.db.QueryRowContext(ctx, replSelect+` WHERE user_id = ? AND agent_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, userID, agentID))
}

// RunningReplacements lists every in-progress replace in this database.
// Startup recovery uses it. It is not a user handler and it returns no tokens.
func (s *Store) RunningReplacements(ctx context.Context) ([]Replacement, error) {
	rows, err := s.db.QueryContext(ctx, replSelect+` WHERE state = 'running' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Replacement
	for rows.Next() {
		row, err := scanReplacement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// PendingDeletes lists succeeded replaces whose old machine is still not deleted.
func (s *Store) PendingDeletes(ctx context.Context) ([]Replacement, error) {
	rows, err := s.db.QueryContext(ctx, replSelect+` WHERE state = 'succeeded' AND delete_pending = 1 ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Replacement
	for rows.Next() {
		row, err := scanReplacement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetReplacementStep records the current step. It does not store an error string.
func (s *Store) SetReplacementStep(ctx context.Context, userID, id, step string) error {
	step = strings.TrimSpace(step)
	if step == "" {
		return ErrInvalid
	}
	if s.stepUpdateBlocked(step) {
		return errors.New("database is locked")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE vm_replacements SET step = ?, updated_at = ? WHERE id = ? AND user_id = ? AND state = 'running'`,
		step, s.stamp(s.Now()), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// NoteReplacementMachine stores the new machine id and address. The token stays out of this row.
func (s *Store) NoteReplacementMachine(ctx context.Context, userID, id, micro, endpoint, name string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE vm_replacements SET new_microvm_id = ?, new_endpoint = ?, new_name = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND state = 'running'`,
		strings.TrimSpace(micro), strings.TrimSpace(endpoint), strings.TrimSpace(name), s.stamp(s.Now()), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// NoteReplacementCopy stores byte and file counts. It does not store contents.
func (s *Store) NoteReplacementCopy(ctx context.Context, userID, id string, woke bool, memory int64, done, total int, fileBytes int64) error {
	w := 0
	if woke {
		w = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE vm_replacements SET woke = ?, memory_bytes = ?, files_done = ?, files_total = ?, file_bytes = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND state = 'running'`,
		w, memory, done, total, fileBytes, s.stamp(s.Now()), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// FailReplacement marks a replace failed before the switch. The live machine row is left alone.
// Tokens bound to the new row are revoked. A switched row is left for the forward path.
func (s *Store) FailReplacement(ctx context.Context, userID, id, step, class string) error {
	step = strings.TrimSpace(step)
	class = strings.TrimSpace(class)
	if step == "" || class == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := scanReplacement(tx.QueryRowContext(ctx, replSelect+` WHERE id = ? AND user_id = ? AND state = 'running' AND step NOT IN ('switched', 'delete')`, id, userID))
	if err != nil {
		return err
	}
	stamp := s.stamp(s.Now())
	if err := revokeBound(ctx, tx, userID, row.AgentID, row.NewVMID, stamp); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE vm_replacements SET state = 'failed', step = ?, error_class = ?, cleanup_pending = 0, finished_at = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND state = 'running' AND step NOT IN ('switched', 'delete')`,
		step, class, stamp, stamp, id, userID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vm_replacement_tokens WHERE replacement_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.noteReplaceDone(userID, row.AgentID)
	return nil
}

// MarkCleanupPending records a failed replace whose new machine still needs to be deleted.
// The replace is failed, so the agent stays usable. The token bound to the new row is revoked.
func (s *Store) MarkCleanupPending(ctx context.Context, userID, id, step, class string) error {
	step = strings.TrimSpace(step)
	class = strings.TrimSpace(class)
	if step == "" || class == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := scanReplacement(tx.QueryRowContext(ctx, replSelect+` WHERE id = ? AND user_id = ? AND state = 'running' AND step NOT IN ('switched', 'delete')`, id, userID))
	if err != nil {
		return err
	}
	now := s.Now()
	stamp := s.stamp(now)
	after := s.stamp(now.Add(time.Minute))
	if err := revokeBound(ctx, tx, userID, row.AgentID, row.NewVMID, stamp); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vm_replacements SET state = 'failed', step = ?, error_class = ?,
cleanup_pending = 1, cleanup_since = ?, cleanup_after = ?, cleanup_wait_ms = ?, cleanup_alerted = 0,
finished_at = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND state = 'running'`,
		step, class, stamp, after, int(time.Minute/time.Millisecond), stamp, stamp, id, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vm_replacement_tokens WHERE replacement_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.noteReplaceDone(userID, row.AgentID)
	return nil
}

// ClearCleanup marks a leftover machine as gone.
func (s *Store) ClearCleanup(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE vm_replacements SET cleanup_pending = 0, updated_at = ? WHERE id = ? AND user_id = ? AND cleanup_pending = 1`,
		s.stamp(s.Now()), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// BumpCleanupWait doubles the wait after a failed delete, up to one hour.
func (s *Store) BumpCleanupWait(ctx context.Context, userID, id string) error {
	var wait int
	err := s.db.QueryRowContext(ctx, `SELECT cleanup_wait_ms FROM vm_replacements WHERE id = ? AND user_id = ? AND cleanup_pending = 1`, id, userID).Scan(&wait)
	if err != nil {
		return scanErr(err)
	}
	if wait < int(time.Minute/time.Millisecond) {
		wait = int(time.Minute / time.Millisecond)
	}
	wait *= 2
	if wait > int(time.Hour/time.Millisecond) {
		wait = int(time.Hour / time.Millisecond)
	}
	now := s.Now()
	res, err := s.db.ExecContext(ctx, `UPDATE vm_replacements SET cleanup_wait_ms = ?, cleanup_after = ?, updated_at = ? WHERE id = ? AND user_id = ? AND cleanup_pending = 1`,
		wait, s.stamp(now.Add(time.Duration(wait)*time.Millisecond)), s.stamp(now), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// DueCleanups lists leftover machines whose retry time has arrived and that are still inside 24 hours.
func (s *Store) DueCleanups(ctx context.Context) ([]Replacement, error) {
	now := s.Now()
	rows, err := s.db.QueryContext(ctx, replSelect+` WHERE cleanup_pending = 1 AND cleanup_alerted = 0 AND cleanup_after <= ? AND cleanup_since > ? ORDER BY cleanup_since`,
		s.stamp(now), s.stamp(now.Add(-24*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Replacement
	for rows.Next() {
		row, err := scanReplacement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ExpiredCleanups lists leftover machines whose 24 hour retry window has ended and that have not been reported.
func (s *Store) ExpiredCleanups(ctx context.Context) ([]Replacement, error) {
	rows, err := s.db.QueryContext(ctx, replSelect+` WHERE cleanup_pending = 1 AND cleanup_alerted = 0 AND cleanup_since != '' AND cleanup_since <= ? ORDER BY cleanup_since`,
		s.stamp(s.Now().Add(-24*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Replacement
	for rows.Next() {
		row, err := scanReplacement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// RecordCleanupStopped marks the 24 hour window closed and writes the event and audit row.
// The flag, the event, and the audit commit together. A failure leaves the row unalerted so the next pass can try again.
func (s *Store) RecordCleanupStopped(ctx context.Context, row Replacement, actorKind, actorID, fromTag, toTag string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE vm_replacements SET cleanup_alerted = 1, updated_at = ? WHERE id = ? AND user_id = ? AND cleanup_pending = 1 AND cleanup_alerted = 0`,
		stamp, row.ID, row.UserID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vm_events(agent_vm_id, workspace_id, user_id, agent_id, kind, at, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, 0)`,
		row.OldVMID, row.WorkspaceID, row.UserID, row.AgentID, EventReplaceCleanup, stamp); err != nil {
		return err
	}
	detail, err := json.Marshal(map[string]string{
		"actor": row.Actor,
		"from":  fromTag,
		"to":    toTag,
	})
	if err != nil {
		return err
	}
	if err := s.insertAudit(ctx, tx, row.WorkspaceID, row.UserID, actorKind, actorID, "machine.replace_cleanup_stopped", "agent:"+row.AgentID, string(detail)); err != nil {
		return err
	}
	if s.cleanupWriteBlocked() {
		return errors.New("cleanup write failed")
	}
	return tx.Commit()
}

// TouchReplacement records that the owner process is still working.
func (s *Store) TouchReplacement(ctx context.Context, userID, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE vm_replacements SET updated_at = ? WHERE id = ? AND user_id = ? AND state = 'running'`,
		s.stamp(s.Now()), id, userID)
	return err
}

// CleanupPending reports a pre-switch replace whose new machine still needs to be deleted.
func (s *Store) CleanupPending(ctx context.Context, userID, agentID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_replacements WHERE user_id = ? AND agent_id = ? AND cleanup_pending = 1`,
		userID, agentID).Scan(&n)
	return n > 0, err
}

// SwitchReplacement inserts the new live row, retires the old one, and revokes the old token.
// The plaintext token is copied onto agent_vms and removed from the staging table.
func (s *Store) SwitchReplacement(ctx context.Context, userID, id, token, healthVersion string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, err := scanReplacement(tx.QueryRowContext(ctx, replSelect+` WHERE id = ? AND user_id = ? AND state = 'running'`, id, userID))
	if err != nil {
		return err
	}
	if row.Step == "switched" || row.Step == "delete" {
		return nil
	}
	var liveID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM agent_vms WHERE id = ? AND user_id = ? AND agent_id = ? AND deleted_at IS NULL`,
		row.OldVMID, userID, row.AgentID).Scan(&liveID)
	if err != nil {
		return scanErr(err)
	}
	stamp := s.stamp(s.Now())
	hash := tokenHash(token)
	vmState := "running"
	agentStatus := "ready"
	if row.OldState == "paused" {
		vmState = "paused"
		agentStatus = "paused"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_vms SET state = 'replaced', deleted_at = ?, state_gen = state_gen + 1 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		stamp, row.OldVMID, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_vms(
  id, workspace_id, user_id, agent_id, microvm_id, name, endpoint, image_ref, state, failure,
  agent_token, agent_token_hash, created_at, last_wake_at, created_running_at, observe_boot, state_gen)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, 1, 1)`,
		row.NewVMID, row.WorkspaceID, userID, row.AgentID, row.NewMicro, row.NewName, row.NewEndpoint, row.NewImage, vmState,
		token, hash, stamp, stamp, stamp); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = ?, activity = '' WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentStatus, row.AgentID, userID); err != nil {
		return err
	}
	healthVersion = strings.TrimSpace(healthVersion)
	if healthVersion != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET pending_bundle = '' WHERE id = ? AND user_id = ? AND pending_bundle = ? AND deleted_at IS NULL`,
			row.AgentID, userID, healthVersion); err != nil {
			return err
		}
	}
	if err := revokeAgentTokens(ctx, tx, userID, row.AgentID, hash, row.NewVMID, stamp); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vm_replacements SET step = 'switched', new_token_hash = ?, updated_at = ? WHERE id = ? AND user_id = ? AND state = 'running'`,
		hash, stamp, id, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vm_replacement_tokens WHERE replacement_id = ? AND user_id = ?`, id, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vm_events(agent_vm_id, workspace_id, user_id, agent_id, kind, at, duration_ms)
VALUES (?, ?, ?, ?, ?, ?, 0)`,
		row.NewVMID, row.WorkspaceID, userID, row.AgentID, EventReplaceSwitch, stamp); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishReplacement marks the replace succeeded. deletePending stays set when the old machine is still up.
// The agent id is read before the commit. A failed read leaves the lock in place, and the hook runs only after the commit.
func (s *Store) FinishReplacement(ctx context.Context, userID, id string, deletePending bool) error {
	pending := 0
	if deletePending {
		pending = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var agentID string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id FROM vm_replacements WHERE id = ? AND user_id = ? AND state = 'running'`, id, userID).Scan(&agentID); err != nil {
		return scanErr(err)
	}
	stamp := s.stamp(s.Now())
	res, err := tx.ExecContext(ctx, `UPDATE vm_replacements SET state = 'succeeded', step = 'done', delete_pending = ?, finished_at = ?, updated_at = ?
WHERE id = ? AND user_id = ? AND state = 'running'`,
		pending, stamp, stamp, id, userID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.noteReplaceDone(userID, agentID)
	return nil
}

// ClearDeletePending clears the retry mark after the old machine is gone.
func (s *Store) ClearDeletePending(ctx context.Context, userID, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE vm_replacements SET delete_pending = 0, updated_at = ? WHERE id = ? AND user_id = ?`,
		s.stamp(s.Now()), id, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

func (s *Store) stageReplacementToken(ctx context.Context, userID, agentID, vmID, plain string) error {
	var replID, workspace string
	err := s.db.QueryRowContext(ctx, `SELECT id, workspace_id FROM vm_replacements
WHERE user_id = ? AND agent_id = ? AND new_vm_id = ? AND state = 'running'`, userID, agentID, vmID).Scan(&replID, &workspace)
	if err != nil {
		return scanErr(err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO vm_replacement_tokens(replacement_id, workspace_id, user_id, agent_id, agent_vm_id, token)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(replacement_id) DO UPDATE SET token = excluded.token`,
		replID, workspace, userID, agentID, vmID, plain)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE vm_replacements SET new_token_hash = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		tokenHash(plain), s.stamp(s.Now()), replID, userID)
	return err
}

// ReplacementToken returns the staged agent token for one running replace.
// Callers must not log it.
func (s *Store) ReplacementToken(ctx context.Context, userID, id string) (string, error) {
	var token string
	err := s.db.QueryRowContext(ctx, `SELECT token FROM vm_replacement_tokens WHERE replacement_id = ? AND user_id = ?`, id, userID).Scan(&token)
	if err != nil {
		return "", scanErr(err)
	}
	return token, nil
}

// revokeBound retires agent and bootstrap tokens bound to one replacement row.
// The live token has an empty agent_vm_id and is left in place.
func revokeBound(ctx context.Context, tx *sql.Tx, userID, agentID, vmID, stamp string) error {
	vmID = strings.TrimSpace(vmID)
	if vmID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ?
WHERE user_id = ? AND agent_id = ? AND agent_vm_id = ? AND revoked_at IS NULL`,
		stamp, userID, agentID, vmID)
	return err
}

// revokeAgentTokens retires agent tokens other than keepHash.
// keepVM, when set, leaves an unused bootstrap that is bound to the new row.
// An empty keepVM revokes every unused bootstrap for the agent.
func revokeAgentTokens(ctx context.Context, tx *sql.Tx, userID, agentID, keepHash, keepVM, stamp string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ?
WHERE user_id = ? AND agent_id = ? AND kind = 'agent' AND revoked_at IS NULL AND token_hash != ?`,
		stamp, userID, agentID, keepHash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ?
WHERE user_id = ? AND agent_id = ? AND kind = 'bootstrap' AND used_at IS NULL AND revoked_at IS NULL
AND (? = '' OR agent_vm_id != ?)`,
		stamp, userID, agentID, keepVM, keepVM); err != nil {
		return err
	}
	return nil
}

// WriteMachineAudit stores one replace audit row. Detail is image tags and the actor, never a secret.
func (s *Store) WriteMachineAudit(ctx context.Context, userID, actorKind, actorID, action, agentID, fromTag, toTag, actor string) error {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return err
	}
	detail, err := json.Marshal(map[string]string{
		"actor": actor,
		"from":  fromTag,
		"to":    toTag,
	})
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.insertAudit(ctx, tx, agent.WorkspaceID, userID, actorKind, actorID, action, "agent:"+agentID, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}

// AgentByIDOrSlug finds one agent by id or Cast CLI slug.
// The admin command on the host uses it. An id match wins.
func (s *Store) AgentByIDOrSlug(ctx context.Context, key string) (Agent, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Agent{}, ErrNotFound
	}
	var userID, id string
	err := s.db.QueryRowContext(ctx, `SELECT user_id, id FROM agents WHERE id = ? AND deleted_at IS NULL`, key).Scan(&userID, &id)
	if err == nil {
		return s.GetAgent(ctx, userID, id)
	}
	if err != sql.ErrNoRows {
		return Agent{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, id FROM agents WHERE cli_slug = ? AND deleted_at IS NULL`, key)
	if err != nil {
		return Agent{}, err
	}
	defer rows.Close()
	var found [][2]string
	for rows.Next() {
		var u, a string
		if err := rows.Scan(&u, &a); err != nil {
			return Agent{}, err
		}
		found = append(found, [2]string{u, a})
	}
	if err := rows.Err(); err != nil {
		return Agent{}, err
	}
	if len(found) != 1 {
		return Agent{}, ErrNotFound
	}
	return s.GetAgent(ctx, found[0][0], found[0][1])
}

// BundleVersions returns the saved bundle version and the pending one.
func (s *Store) BundleVersions(ctx context.Context, userID, agentID string) (current, pending string, err error) {
	if _, err = s.GetAgent(ctx, userID, agentID); err != nil {
		return "", "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(bundle_version, ''), COALESCE(pending_bundle, '') FROM agents WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, agentID, userID).Scan(&current, &pending)
	if err != nil {
		return "", "", scanErr(err)
	}
	return current, pending, nil
}

// LiveMachines lists every live machine in this database. The admin list uses it.
// It returns no tokens.
func (s *Store) LiveMachines(ctx context.Context) ([]VM, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, workspace_id, user_id, agent_id, microvm_id, name, endpoint, image_ref, state, failure, COALESCE(last_wake_at, ''), created_at, COALESCE(created_running_at, '')
FROM agent_vms WHERE deleted_at IS NULL ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VM
	for rows.Next() {
		var vm VM
		if err := rows.Scan(&vm.ID, &vm.Workspace, &vm.UserID, &vm.AgentID, &vm.MicroVMID, &vm.Name, &vm.Endpoint, &vm.Image, &vm.State, &vm.Failure, &vm.LastWake, &vm.CreatedAt, &vm.CreatedRunning); err != nil {
			return nil, err
		}
		out = append(out, vm)
	}
	return out, rows.Err()
}

// CountAgentRows returns row counts the replace acceptance check keeps stable.
func (s *Store) CountAgentRows(ctx context.Context, userID, agentID string) (map[string]int, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	out := map[string]int{}
	queries := map[string]string{
		"threads":             `SELECT COUNT(*) FROM threads WHERE user_id = ? AND agent_id = ?`,
		"messages":            `SELECT COUNT(*) FROM messages WHERE user_id = ? AND thread_id IN (SELECT id FROM threads WHERE user_id = ? AND agent_id = ?)`,
		"runs":                `SELECT COUNT(*) FROM runs WHERE user_id = ? AND agent_id = ?`,
		"run_events":          `SELECT COUNT(*) FROM run_events WHERE run_id IN (SELECT id FROM runs WHERE user_id = ? AND agent_id = ?)`,
		"approvals":           `SELECT COUNT(*) FROM approvals WHERE user_id = ? AND run_id IN (SELECT id FROM runs WHERE user_id = ? AND agent_id = ?)`,
		"audit_log":           `SELECT COUNT(*) FROM audit_log WHERE user_id = ? AND target = 'agent:' || ?`,
		"journal_entries":     `SELECT COUNT(*) FROM journal_entries WHERE user_id = ? AND agent_id = ?`,
		"cli_tokens":          `SELECT COUNT(*) FROM cli_tokens WHERE user_id = ? AND agent_id = ?`,
		"agent_bundles":       `SELECT COUNT(*) FROM agent_bundles WHERE user_id = ? AND agent_id = ?`,
		"agent_secret_grants": `SELECT COUNT(*) FROM agent_secret_grants WHERE user_id = ? AND agent_id = ?`,
	}
	for name, q := range queries {
		args := []any{userID, agentID}
		if strings.Count(q, "?") == 3 {
			args = []any{userID, userID, agentID}
		}
		var n int
		if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, nil
}

// AuditActions returns the actions stored for one agent, oldest first.
func (s *Store) AuditActions(ctx context.Context, userID, agentID string) ([]string, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT action FROM audit_log WHERE user_id = ? AND target = ? ORDER BY id`, userID, "agent:"+agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var action string
		if err := rows.Scan(&action); err != nil {
			return nil, err
		}
		out = append(out, action)
	}
	return out, rows.Err()
}

// ReplacementAge reports how long ago a stamp was, using the store clock.
func (s *Store) ReplacementAge(stamp string) time.Duration {
	t, err := parseTime(stamp)
	if err != nil || t.IsZero() {
		return 0
	}
	return s.Now().Sub(t)
}
