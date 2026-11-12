package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/scott/castbot/internal/cron"
)

const (
	ApprovalWait = "wait"
	ApprovalDeny = "deny"
	MissedOnce   = "once"
	MissedSkip   = "skip"

	RoutineScheduled = "scheduled"
	RoutineQueued    = "queued"
	RoutineHeld      = "held"
	RoutineRunning   = "running"
	RoutineDone      = "done"
	RoutineFailed    = "failed"
	RoutineLate      = "late"
	RoutineMissed    = "missed"
	RoutineSkipped   = "skipped"
	RoutineExpired   = "expired"

	RoutineSourceSchedule = "schedule"
	RoutineSourceManual   = "manual"

	// OnTimeWindow is how long after the scheduled minute a start still counts as on time.
	OnTimeWindow = time.Hour
)

// ErrRoutineBusy means this routine already has a run that has not finished.
var ErrRoutineBusy = errRoutineBusy{}

type errRoutineBusy struct{}

func (errRoutineBusy) Error() string { return "routine busy" }

// ErrAgentBusy means another reply is streaming, so this slot was left queued.
var ErrAgentBusy = errAgentBusy{}

type errAgentBusy struct{}

func (errAgentBusy) Error() string { return "agent busy" }

// Routine is one cron prompt for one agent.
type Routine struct {
	ID             string
	WorkspaceID    string
	UserID         string
	AgentID        string
	Name           string
	Cron           string
	Timezone       string
	Prompt         string
	Enabled        bool
	ApprovalPolicy string
	MissedPolicy   string
	NextRunAt      string
	LastRunAt      string
	CreatedAt      string
	DeletedAt      string
}

// RoutineRun is one scheduled time for a routine.
// RunID and ThreadID stay empty until a run actually starts.
type RoutineRun struct {
	ID           string
	WorkspaceID  string
	UserID       string
	RoutineID    string
	AgentID      string
	RunID        string
	ThreadID     string
	ScheduledFor string
	StartedAt    string
	FinishedAt   string
	Status       string
	Detail       string
	Source       string
	Catchup      bool
	PausedMS     int
	WakeMS       int
	OutageFrom   string
	OutageTo     string
	CreatedAt    string
	Launch       bool
}

// RoutineInput is a create or update.
type RoutineInput struct {
	Name           string
	Cron           string
	Timezone       string
	Prompt         string
	ApprovalPolicy string
	MissedPolicy   string
	Enabled        bool
}

func (s *Store) migrateV17() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS routines (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  name TEXT NOT NULL,
  cron TEXT NOT NULL,
  timezone TEXT NOT NULL,
  prompt TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
  approval_policy TEXT NOT NULL DEFAULT 'wait' CHECK (approval_policy IN ('wait', 'deny')),
  missed_policy TEXT NOT NULL DEFAULT 'once' CHECK (missed_policy IN ('once', 'skip')),
  next_run_at TEXT,
  last_run_at TEXT,
  created_at TEXT NOT NULL,
  deleted_at TEXT
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS routine_runs (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  routine_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  run_id TEXT,
  thread_id TEXT,
  scheduled_for TEXT NOT NULL,
  started_at TEXT,
  finished_at TEXT,
  status TEXT NOT NULL CHECK (status IN ('scheduled', 'queued', 'held', 'running', 'done', 'failed', 'late', 'missed', 'skipped', 'expired')),
  detail TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT 'schedule' CHECK (source IN ('schedule', 'manual')),
  catchup INTEGER NOT NULL DEFAULT 0,
  paused_ms INTEGER NOT NULL DEFAULT -1,
  wake_ms INTEGER NOT NULL DEFAULT -1,
  outage_from TEXT NOT NULL DEFAULT '',
  outage_to TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_routine_runs_slot ON routine_runs(routine_id, scheduled_for)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_routines_user ON routines(user_id, agent_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_routine_runs_user ON routine_runs(user_id, routine_id, scheduled_for)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS scheduler_meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  last_tick_at TEXT NOT NULL DEFAULT ''
)`); err != nil {
		return err
	}
	if err := s.ensureColumn("vm_events", "code", `ALTER TABLE vm_events ADD COLUMN code TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO schema_migrations(version) VALUES (17) ON CONFLICT(version) DO NOTHING`)
	return err
}

// SetReplaceDone chains a callback for when a replacement lock clears.
// Each call keeps the previous callback.
func (s *Store) SetReplaceDone(fn func(userID, agentID string)) {
	if fn == nil {
		return
	}
	s.replaceMu.Lock()
	prev := s.replaceDone
	s.replaceDone = func(userID, agentID string) {
		if prev != nil {
			prev(userID, agentID)
		}
		fn(userID, agentID)
	}
	s.replaceMu.Unlock()
}

func (s *Store) noteReplaceDone(userID, agentID string) {
	s.replaceMu.Lock()
	fn := s.replaceDone
	s.replaceMu.Unlock()
	if fn != nil && userID != "" && agentID != "" {
		fn(userID, agentID)
	}
}

// CreateRoutine stores a schedule. next_run_at is the first fire after now.
func (s *Store) CreateRoutine(ctx context.Context, userID, agentID string, in RoutineInput) (Routine, error) {
	agent, sched, loc, err := s.prepRoutine(ctx, userID, agentID, in)
	if err != nil {
		return Routine{}, err
	}
	next, ok := sched.Next(s.Now(), loc)
	if !ok {
		return Routine{}, ErrInvalid
	}
	id, err := newID()
	if err != nil {
		return Routine{}, err
	}
	enabled := 0
	if in.Enabled {
		enabled = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO routines(id, workspace_id, user_id, agent_id, name, cron, timezone, prompt, enabled, approval_policy, missed_policy, next_run_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, agent.WorkspaceID, userID, agentID, strings.TrimSpace(in.Name), sched.Raw, loc.String(), strings.TrimSpace(in.Prompt), enabled, policyOr(in.ApprovalPolicy, ApprovalWait), policyMissed(in.MissedPolicy), s.stamp(next), s.stamp(s.Now()))
	if err != nil {
		return Routine{}, err
	}
	return s.GetRoutine(ctx, userID, id)
}

// UpdateRoutine replaces the editable fields and recomputes the next fire from now.
// Times that passed while the row was saved are not caught up here.
func (s *Store) UpdateRoutine(ctx context.Context, userID, id string, in RoutineInput) (Routine, error) {
	cur, err := s.GetRoutine(ctx, userID, id)
	if err != nil {
		return Routine{}, err
	}
	_, sched, loc, err := s.prepRoutine(ctx, userID, cur.AgentID, in)
	if err != nil {
		return Routine{}, err
	}
	next, ok := sched.Next(s.Now(), loc)
	if !ok {
		return Routine{}, ErrInvalid
	}
	enabled := 0
	var nextAny any
	if in.Enabled {
		enabled = 1
		nextAny = s.stamp(next)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE routines SET name = ?, cron = ?, timezone = ?, prompt = ?, enabled = ?, approval_policy = ?, missed_policy = ?, next_run_at = ?
WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		strings.TrimSpace(in.Name), sched.Raw, loc.String(), strings.TrimSpace(in.Prompt), enabled, policyOr(in.ApprovalPolicy, ApprovalWait), policyMissed(in.MissedPolicy), nextAny, id, userID)
	if err != nil {
		return Routine{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Routine{}, err
	}
	return s.GetRoutine(ctx, userID, id)
}

// SetRoutineEnabled pauses or resumes a routine.
// Turning it on schedules the next fire after now. Runs that passed while paused are not caught up.
func (s *Store) SetRoutineEnabled(ctx context.Context, userID, id string, on bool) (Routine, error) {
	cur, err := s.GetRoutine(ctx, userID, id)
	if err != nil {
		return Routine{}, err
	}
	if !on {
		res, err := s.db.ExecContext(ctx, `UPDATE routines SET enabled = 0 WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID)
		if err != nil {
			return Routine{}, err
		}
		if err := rowsAffected(res); err != nil {
			return Routine{}, err
		}
		return s.GetRoutine(ctx, userID, id)
	}
	sched, err := cron.Parse(cur.Cron)
	if err != nil {
		return Routine{}, ErrInvalid
	}
	loc, err := time.LoadLocation(cur.Timezone)
	if err != nil {
		return Routine{}, ErrInvalid
	}
	next, ok := sched.Next(s.Now(), loc)
	if !ok {
		return Routine{}, ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE routines SET enabled = 1, next_run_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, s.stamp(next), id, userID)
	if err != nil {
		return Routine{}, err
	}
	if err := rowsAffected(res); err != nil {
		return Routine{}, err
	}
	return s.GetRoutine(ctx, userID, id)
}

// DeleteRoutine soft-deletes the schedule. Threads and an in-progress run stay.
func (s *Store) DeleteRoutine(ctx context.Context, userID, id string) error {
	if _, err := s.GetRoutine(ctx, userID, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE routines SET enabled = 0, deleted_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, s.stamp(s.Now()), id, userID)
	if err != nil {
		return err
	}
	if err := rowsAffected(res); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE threads SET title = 'deleted routine' WHERE user_id = ? AND id IN (SELECT thread_id FROM routine_runs WHERE user_id = ? AND routine_id = ? AND thread_id IS NOT NULL AND thread_id != '')`, userID, userID, id)
	return err
}

// GetRoutine returns one of the caller's routines, including a deleted one.
func (s *Store) GetRoutine(ctx context.Context, userID, id string) (Routine, error) {
	row := s.db.QueryRowContext(ctx, routineSelect+` WHERE id = ? AND user_id = ?`, id, userID)
	return scanRoutine(row)
}

// GetLiveRoutine returns one routine that has not been deleted.
func (s *Store) GetLiveRoutine(ctx context.Context, userID, id string) (Routine, error) {
	row := s.db.QueryRowContext(ctx, routineSelect+` WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, userID)
	return scanRoutine(row)
}

// ListRoutines returns the caller's live routines for one agent, oldest first.
func (s *Store) ListRoutines(ctx context.Context, userID, agentID string) ([]Routine, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, routineSelect+` WHERE user_id = ? AND agent_id = ? AND deleted_at IS NULL ORDER BY created_at, id`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRoutines(rows)
}

// ListRoutineRuns returns runs for one routine, newest scheduled time first.
// limit 0 returns every row.
func (s *Store) ListRoutineRuns(ctx context.Context, userID, routineID string, limit int) ([]RoutineRun, error) {
	if _, err := s.GetRoutine(ctx, userID, routineID); err != nil {
		return nil, err
	}
	q := routineRunSelect + ` WHERE user_id = ? AND routine_id = ? ORDER BY scheduled_for DESC, id DESC`
	args := []any{userID, routineID}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRoutineRuns(rows)
}

// CountRoutineRuns counts stored runs for one routine.
func (s *Store) CountRoutineRuns(ctx context.Context, userID, routineID string) (int, error) {
	if _, err := s.GetRoutine(ctx, userID, routineID); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routine_runs WHERE user_id = ? AND routine_id = ?`, userID, routineID).Scan(&n)
	return n, err
}

// ListStartedRoutineRuns returns runs that have a thread, oldest start first.
// The agent chat uses them as timeline markers.
func (s *Store) ListStartedRoutineRuns(ctx context.Context, userID, agentID string) ([]RoutineRun, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, routineRunSelect+` WHERE user_id = ? AND agent_id = ? AND thread_id IS NOT NULL AND thread_id != '' ORDER BY COALESCE(started_at, scheduled_for), id`, userID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRoutineRuns(rows)
}

// GetRoutineRun returns one of the caller's routine runs.
func (s *Store) GetRoutineRun(ctx context.Context, userID, id string) (RoutineRun, error) {
	row := s.db.QueryRowContext(ctx, routineRunSelect+` WHERE id = ? AND user_id = ?`, id, userID)
	return scanRoutineRun(row)
}

// RoutineRunByThread returns the routine run whose thread is this one.
func (s *Store) RoutineRunByThread(ctx context.Context, userID, threadID string) (RoutineRun, error) {
	if _, err := s.GetThread(ctx, userID, threadID); err != nil {
		return RoutineRun{}, err
	}
	row := s.db.QueryRowContext(ctx, routineRunSelect+` WHERE user_id = ? AND thread_id = ?`, userID, threadID)
	return scanRoutineRun(row)
}

// NextQueuedRun is the oldest queued run for an agent. The caller starts it
// when the agent is free. A second start of the same id is a no-op.
func (s *Store) NextQueuedRun(ctx context.Context, userID, agentID string) (Run, bool, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return Run{}, false, err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM runs WHERE user_id = ? AND agent_id = ? AND status = 'queued' ORDER BY started_at, id LIMIT 1`, userID, agentID).Scan(&id)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, err
	}
	run, err := s.GetRun(ctx, userID, id)
	if err != nil {
		return Run{}, false, err
	}
	return run, true, nil
}

// RoutineRunByRun returns the routine run attached to one of the caller's runs.
func (s *Store) RoutineRunByRun(ctx context.Context, userID, runID string) (RoutineRun, error) {
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return RoutineRun{}, err
	}
	row := s.db.QueryRowContext(ctx, routineRunSelect+` WHERE user_id = ? AND run_id = ?`, userID, runID)
	return scanRoutineRun(row)
}

// RoutineInProgress reports whether this routine has a run that has not finished.
func (s *Store) RoutineInProgress(ctx context.Context, userID, routineID string) (bool, error) {
	if _, err := s.GetRoutine(ctx, userID, routineID); err != nil {
		return false, err
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routine_runs
WHERE user_id = ? AND routine_id = ? AND finished_at IS NULL AND status IN ('queued', 'held', 'running', 'late')`, userID, routineID).Scan(&n)
	return n > 0, err
}

// AgentBusy reports a queued, streaming, or waiting run on the agent,
// or a claimed routine slot that has not started a run yet.
func (s *Store) AgentBusy(ctx context.Context, userID, agentID string) (bool, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return false, err
	}
	n, err := agentBusyCount(ctx, s.db, userID, agentID)
	return n > 0, err
}

// ClaimDue claims every enabled routine whose next fire is due.
// The insert and the next_run_at advance commit together. A second claim is a no-op.
func (s *Store) ClaimDue(ctx context.Context) ([]RoutineRun, error) {
	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return nil, err
	}
	commit := false
	defer func() { _ = s.endImmediate(ctx, conn, commit) }()
	now := s.Now()
	var last string
	if err := conn.QueryRowContext(ctx, `SELECT last_tick_at FROM scheduler_meta WHERE id = 1`).Scan(&last); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, routineSelect+` WHERE enabled = 1 AND deleted_at IS NULL AND next_run_at IS NOT NULL AND next_run_at != '' AND next_run_at <= ? ORDER BY next_run_at, id`, s.stamp(now))
	if err != nil {
		return nil, err
	}
	due, err := scanRoutines(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	busy := map[string]bool{}
	var claimed []RoutineRun
	for _, routine := range due {
		got, err := s.claimOne(ctx, conn, routine, now, last, busy)
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, got...)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO scheduler_meta(id, last_tick_at) VALUES (1, ?)
ON CONFLICT(id) DO UPDATE SET last_tick_at = excluded.last_tick_at`, s.stamp(now)); err != nil {
		return nil, err
	}
	commit = true
	return claimed, nil
}

func (s *Store) claimOne(ctx context.Context, conn *sql.Conn, routine Routine, now time.Time, lastTick string, busy map[string]bool) ([]RoutineRun, error) {
	sched, err := cron.Parse(routine.Cron)
	if err != nil {
		return nil, nil
	}
	loc, err := time.LoadLocation(routine.Timezone)
	if err != nil {
		return nil, nil
	}
	first, err := parseTime(routine.NextRunAt)
	if err != nil || first.IsZero() {
		return nil, nil
	}
	fires := sched.Between(first.Add(-time.Second), now, loc)
	if len(fires) == 0 {
		next, ok := sched.Next(now, loc)
		if !ok {
			return nil, nil
		}
		_, err := conn.ExecContext(ctx, `UPDATE routines SET next_run_at = ? WHERE id = ? AND user_id = ?`, s.stamp(next), routine.ID, routine.UserID)
		return nil, err
	}
	next, ok := sched.Next(now, loc)
	nextStamp := ""
	if ok {
		nextStamp = s.stamp(next)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE routines SET next_run_at = ?, last_run_at = ? WHERE id = ? AND user_id = ?`, nextStamp, s.stamp(fires[len(fires)-1]), routine.ID, routine.UserID); err != nil {
		return nil, err
	}
	outageFrom := lastTick
	if outageFrom == "" {
		outageFrom = s.stamp(fires[0])
	}
	outageTo := s.stamp(now)
	if routine.MissedPolicy == MissedSkip {
		var out []RoutineRun
		for _, fire := range fires {
			row, inserted, err := s.insertSlot(ctx, conn, routine, fire, RoutineSkipped, "castbot was offline · skipped, as set", RoutineSourceSchedule, false, outageFrom, outageTo, false)
			if err != nil {
				return nil, err
			}
			if inserted {
				out = append(out, row)
			}
		}
		return out, nil
	}
	var out []RoutineRun
	for i, fire := range fires {
		last := i == len(fires)-1
		if !last {
			row, inserted, err := s.insertSlot(ctx, conn, routine, fire, RoutineMissed, "castbot was offline · covered by the one catch-up run", RoutineSourceSchedule, false, outageFrom, outageTo, false)
			if err != nil {
				return nil, err
			}
			if inserted {
				out = append(out, row)
			}
			continue
		}
		status, detail, launch := s.slotDisposition(ctx, conn, routine, fire, len(fires) > 1, now, busy)
		row, inserted, err := s.insertSlot(ctx, conn, routine, fire, status, detail, RoutineSourceSchedule, len(fires) > 1, outageFrom, outageTo, launch)
		if err != nil {
			return nil, err
		}
		if inserted {
			if launch {
				busy[routine.UserID+"/"+routine.AgentID] = true
			}
			out = append(out, row)
		}
	}
	return out, nil
}

func (s *Store) slotDisposition(ctx context.Context, conn *sql.Conn, routine Routine, fire time.Time, catchup bool, now time.Time, busy map[string]bool) (status, detail string, launch bool) {
	key := routine.UserID + "/" + routine.AgentID
	if s.replacingConn(ctx, conn, routine.UserID, routine.AgentID) {
		return RoutineHeld, "Waits for the machine replacement", false
	}
	occupied, _ := s.busyConn(ctx, conn, routine.UserID, routine.AgentID)
	if occupied || busy[key] {
		busy[key] = true
		return RoutineQueued, "Waits for the reply in progress", false
	}
	late := catchup || now.Sub(fire) > OnTimeWindow
	if late {
		return RoutineLate, "castbot was offline", true
	}
	return RoutineRunning, "", true
}

// ClaimManual claims one run-now slot through the same unique insert as a schedule.
// A run of this routine that has not finished, or a live replacement, is refused.
func (s *Store) ClaimManual(ctx context.Context, userID, routineID string) (RoutineRun, error) {
	routine, err := s.GetLiveRoutine(ctx, userID, routineID)
	if err != nil {
		return RoutineRun{}, err
	}
	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return RoutineRun{}, err
	}
	commit := false
	defer func() { _ = s.endImmediate(ctx, conn, commit) }()
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM routine_runs WHERE user_id = ? AND routine_id = ? AND finished_at IS NULL AND status IN ('queued', 'held', 'running', 'late')`, userID, routineID).Scan(&n); err != nil {
		return RoutineRun{}, err
	}
	if n > 0 {
		return RoutineRun{}, ErrRoutineBusy
	}
	if s.replacingConn(ctx, conn, userID, routine.AgentID) {
		return RoutineRun{}, ErrProtected
	}
	when := s.Now()
	status := RoutineRunning
	detail := "Run now by you"
	launch := true
	occupied, err := s.busyConn(ctx, conn, userID, routine.AgentID)
	if err != nil {
		return RoutineRun{}, err
	}
	if occupied {
		status = RoutineQueued
		detail = "Waits for the reply in progress"
		launch = false
	}
	var row RoutineRun
	var inserted bool
	for i := 0; i < 3 && !inserted; i++ {
		row, inserted, err = s.insertSlot(ctx, conn, routine, when, status, detail, RoutineSourceManual, false, "", "", launch)
		if err != nil {
			return RoutineRun{}, err
		}
		when = when.Add(time.Millisecond)
	}
	if !inserted {
		return RoutineRun{}, ErrConflict
	}
	commit = true
	return row, nil
}

func (s *Store) insertSlot(ctx context.Context, conn *sql.Conn, routine Routine, fire time.Time, status, detail, source string, catchup bool, outageFrom, outageTo string, launch bool) (RoutineRun, bool, error) {
	id, err := newID()
	if err != nil {
		return RoutineRun{}, false, err
	}
	catch := 0
	if catchup {
		catch = 1
	}
	stamp := s.stamp(fire.UTC())
	if source == RoutineSourceManual {
		stamp = fire.UTC().Format(time.RFC3339Nano)
	}
	res, err := conn.ExecContext(ctx, `INSERT INTO routine_runs(id, workspace_id, user_id, routine_id, agent_id, scheduled_for, status, detail, source, catchup, paused_ms, wake_ms, outage_from, outage_to, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, -1, -1, ?, ?, ?)
ON CONFLICT(routine_id, scheduled_for) DO NOTHING`,
		id, routine.WorkspaceID, routine.UserID, routine.ID, routine.AgentID, stamp, status, detail, source, catch, outageFrom, outageTo, s.stamp(s.Now()))
	if err != nil {
		return RoutineRun{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return RoutineRun{}, false, err
	}
	if n == 0 {
		return RoutineRun{}, false, nil
	}
	row, err := scanRoutineRun(conn.QueryRowContext(ctx, routineRunSelect+` WHERE id = ? AND user_id = ?`, id, routine.UserID))
	if err != nil {
		return RoutineRun{}, false, err
	}
	row.Launch = launch
	return row, true, nil
}

// BeginRoutineExecution creates the thread and the run for a claimed slot.
// A second call finds the run already attached and returns ErrConflict.
func (s *Store) BeginRoutineExecution(ctx context.Context, userID, routineRunID string) (RoutineRun, Thread, Run, error) {
	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	defer func() {
		if conn != nil {
			_ = s.endImmediate(ctx, conn, false)
		}
	}()
	rr, err := scanRoutineRun(conn.QueryRowContext(ctx, routineRunSelect+` WHERE id = ? AND user_id = ?`, routineRunID, userID))
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	if rr.RunID != "" {
		return RoutineRun{}, Thread{}, Run{}, ErrConflict
	}
	switch rr.Status {
	case RoutineRunning, RoutineLate, RoutineQueued, RoutineHeld:
	default:
		return RoutineRun{}, Thread{}, Run{}, ErrInvalid
	}
	// A queued chat run is waiting its turn and must not block this claimed slot.
	// A streaming or waiting reply is already in progress, so the slot goes back to queued
	// in this same transaction and no run row is inserted.
	var executing int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE user_id = ? AND agent_id = ? AND status IN ('streaming', 'waiting_approval')`, rr.UserID, rr.AgentID).Scan(&executing); err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	if executing > 0 {
		if _, err := conn.ExecContext(ctx, `UPDATE routine_runs SET status = ?, detail = ? WHERE id = ? AND user_id = ? AND finished_at IS NULL AND (run_id IS NULL OR run_id = '')`,
			RoutineQueued, "Waits for the reply in progress", rr.ID, userID); err != nil {
			return RoutineRun{}, Thread{}, Run{}, err
		}
		err = s.endImmediate(ctx, conn, true)
		conn = nil
		if err != nil {
			return RoutineRun{}, Thread{}, Run{}, err
		}
		return RoutineRun{}, Thread{}, Run{}, ErrAgentBusy
	}
	routine, err := scanRoutine(conn.QueryRowContext(ctx, routineSelect+` WHERE id = ? AND user_id = ?`, rr.RoutineID, userID))
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	threadID, err := newID()
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	runID, err := newID()
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	title := routine.Name
	if routine.DeletedAt != "" {
		title = "deleted routine"
	}
	now := s.stamp(s.Now())
	if _, err := conn.ExecContext(ctx, `INSERT INTO threads(id, workspace_id, user_id, kind, agent_id, title, created_at) VALUES (?, ?, ?, 'routine', ?, ?, ?)`,
		threadID, rr.WorkspaceID, userID, rr.AgentID, title, now); err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO runs(id, workspace_id, thread_id, agent_id, user_id, routine_run_id, status, stream_offset, started_at, error) VALUES (?, ?, ?, ?, ?, ?, 'queued', 0, ?, '')`,
		runID, rr.WorkspaceID, threadID, rr.AgentID, userID, rr.ID, now); err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	scheduled, _ := parseTime(rr.ScheduledFor)
	status := rr.Status
	if rr.Catchup || (!scheduled.IsZero() && s.Now().Sub(scheduled) > OnTimeWindow) {
		status = RoutineLate
	} else if status == RoutineQueued || status == RoutineHeld {
		status = RoutineRunning
	}
	res, err := conn.ExecContext(ctx, `UPDATE routine_runs SET run_id = ?, thread_id = ?, started_at = ?, status = ? WHERE id = ? AND user_id = ? AND (run_id IS NULL OR run_id = '')`,
		runID, threadID, now, status, rr.ID, userID)
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	if err := rowsAffected(res); err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	err = s.endImmediate(ctx, conn, true)
	conn = nil
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	rr, err = s.GetRoutineRun(ctx, userID, rr.ID)
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	th, err := s.GetThread(ctx, userID, threadID)
	if err != nil {
		return RoutineRun{}, Thread{}, Run{}, err
	}
	run, err := s.GetRun(ctx, userID, runID)
	return rr, th, run, err
}

// PromoteRoutine takes the oldest queued or held slot for an agent that is free.
func (s *Store) PromoteRoutine(ctx context.Context, userID, agentID string) (RoutineRun, bool, error) {
	if _, err := s.GetAgent(ctx, userID, agentID); err != nil {
		return RoutineRun{}, false, err
	}
	conn, err := s.beginImmediate(ctx)
	if err != nil {
		return RoutineRun{}, false, err
	}
	commit := false
	defer func() { _ = s.endImmediate(ctx, conn, commit) }()
	if s.replacingConn(ctx, conn, userID, agentID) {
		return RoutineRun{}, false, nil
	}
	occupied, err := s.busyConn(ctx, conn, userID, agentID)
	if err != nil {
		return RoutineRun{}, false, err
	}
	if occupied {
		return RoutineRun{}, false, nil
	}
	rr, err := scanRoutineRun(conn.QueryRowContext(ctx, routineRunSelect+` WHERE user_id = ? AND agent_id = ? AND status IN ('queued', 'held') AND finished_at IS NULL ORDER BY scheduled_for, id LIMIT 1`, userID, agentID))
	if err != nil {
		if err == ErrNotFound {
			return RoutineRun{}, false, nil
		}
		return RoutineRun{}, false, err
	}
	commit = true
	rr.Launch = true
	return rr, true, nil
}

// DeferRoutineRun parks an unstarted slot. A slot that already has a run is left alone.
func (s *Store) DeferRoutineRun(ctx context.Context, userID, id, status, detail string) error {
	switch status {
	case RoutineQueued, RoutineHeld:
	default:
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, `UPDATE routine_runs SET status = ?, detail = ? WHERE id = ? AND user_id = ? AND finished_at IS NULL AND (run_id IS NULL OR run_id = '')`,
		status, detail, id, userID)
	if err != nil {
		return err
	}
	_, err = res.RowsAffected()
	return err
}

// UnstartedRoutineRuns returns claimed slots that still have no run.
// A tick uses this to finish a launch that stopped after the claim.
func (s *Store) UnstartedRoutineRuns(ctx context.Context) ([]RoutineRun, error) {
	rows, err := s.db.QueryContext(ctx, routineRunSelect+` WHERE status IN ('running', 'late') AND (run_id IS NULL OR run_id = '') AND finished_at IS NULL ORDER BY scheduled_for, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanRoutineRuns(rows)
	for i := range out {
		out[i].Launch = true
	}
	return out, err
}

// NoteRoutineWake stores how long the machine was paused and how long waking took.
// pausedMS is -1 when the machine was already running.
func (s *Store) NoteRoutineWake(ctx context.Context, userID, routineRunID string, pausedMS, wakeMS int) error {
	if _, err := s.GetRoutineRun(ctx, userID, routineRunID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE routine_runs SET paused_ms = ?, wake_ms = ? WHERE id = ? AND user_id = ?`, pausedMS, wakeMS, routineRunID, userID)
	if err != nil {
		return err
	}
	return rowsAffected(res)
}

// FinishRoutineRun records the terminal status for the slot attached to a run.
func (s *Store) FinishRoutineRun(ctx context.Context, userID, runID, status, detail string) error {
	switch status {
	case RoutineDone, RoutineFailed, RoutineLate, RoutineExpired, RoutineRunning:
	default:
		return ErrInvalid
	}
	if _, err := s.GetRun(ctx, userID, runID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE routine_runs SET status = ?, detail = ?, finished_at = ? WHERE user_id = ? AND run_id = ? AND finished_at IS NULL`,
		status, detail, s.stamp(s.Now()), userID, runID)
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
	return nil
}

// CreateOriginMessage appends a message with an explicit origin.
// origin routine is the routine prompt. Other origins stay web.
func (s *Store) CreateOriginMessage(ctx context.Context, userID, threadID, authorKind, authorID, body, origin string) (Message, error) {
	msg, err := s.CreateMessage(ctx, userID, threadID, authorKind, authorID, body)
	if err != nil {
		return Message{}, err
	}
	origin = strings.TrimSpace(origin)
	if origin == "" {
		origin = "web"
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE messages SET origin = ? WHERE id = ? AND user_id = ?`, origin, msg.ID, userID); err != nil {
		return Message{}, err
	}
	return s.GetMessage(ctx, userID, msg.ID)
}

// LastTick is the scheduler clock stored by the previous claim. Tests read it.
func (s *Store) LastTick(ctx context.Context) (string, error) {
	var last string
	err := s.db.QueryRowContext(ctx, `SELECT last_tick_at FROM scheduler_meta WHERE id = 1`).Scan(&last)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return last, err
}

func (s *Store) prepRoutine(ctx context.Context, userID, agentID string, in RoutineInput) (Agent, cron.Schedule, *time.Location, error) {
	agent, err := s.GetAgent(ctx, userID, agentID)
	if err != nil {
		return Agent{}, cron.Schedule{}, nil, err
	}
	name := strings.TrimSpace(in.Name)
	prompt := strings.TrimSpace(in.Prompt)
	if name == "" || len(name) > 80 || prompt == "" || len(prompt) > 8000 {
		return Agent{}, cron.Schedule{}, nil, ErrInvalid
	}
	sched, err := cron.Parse(in.Cron)
	if err != nil {
		return Agent{}, cron.Schedule{}, nil, ErrInvalid
	}
	loc, err := time.LoadLocation(strings.TrimSpace(in.Timezone))
	if err != nil {
		return Agent{}, cron.Schedule{}, nil, ErrInvalid
	}
	if cron.TooFrequent(sched, loc) {
		return Agent{}, cron.Schedule{}, nil, ErrInvalid
	}
	switch policyOr(in.ApprovalPolicy, ApprovalWait) {
	case ApprovalWait, ApprovalDeny:
	default:
		return Agent{}, cron.Schedule{}, nil, ErrInvalid
	}
	switch policyMissed(in.MissedPolicy) {
	case MissedOnce, MissedSkip:
	default:
		return Agent{}, cron.Schedule{}, nil, ErrInvalid
	}
	return agent, sched, loc, nil
}

func (s *Store) beginImmediate(ctx context.Context) (*sql.Conn, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func (s *Store) endImmediate(ctx context.Context, conn *sql.Conn, commit bool) error {
	stmt := `ROLLBACK`
	if commit {
		stmt = `COMMIT`
	}
	_, err := conn.ExecContext(ctx, stmt)
	cerr := conn.Close()
	if err != nil {
		return err
	}
	return cerr
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func agentBusyCount(ctx context.Context, q queryRower, userID, agentID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT (
  SELECT COUNT(*) FROM runs WHERE user_id = ? AND agent_id = ? AND status IN ('queued', 'streaming', 'waiting_approval')
) + (
  SELECT COUNT(*) FROM routine_runs WHERE user_id = ? AND agent_id = ? AND finished_at IS NULL
    AND status IN ('running', 'late') AND (run_id IS NULL OR run_id = '')
)`, userID, agentID, userID, agentID).Scan(&n)
	return n, err
}

func (s *Store) busyConn(ctx context.Context, conn *sql.Conn, userID, agentID string) (bool, error) {
	n, err := agentBusyCount(ctx, conn, userID, agentID)
	return n > 0, err
}

func (s *Store) replacingConn(ctx context.Context, conn *sql.Conn, userID, agentID string) bool {
	var n int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM vm_replacements WHERE user_id = ? AND agent_id = ? AND state = 'running'`, userID, agentID).Scan(&n)
	return err == nil && n > 0
}

func policyOr(v, def string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	return v
}

func policyMissed(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return MissedOnce
	}
	return v
}

func loadLoc(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

const routineSelect = `SELECT id, workspace_id, user_id, agent_id, name, cron, timezone, prompt, enabled, approval_policy, missed_policy, COALESCE(next_run_at, ''), COALESCE(last_run_at, ''), created_at, COALESCE(deleted_at, '') FROM routines`

const routineRunSelect = `SELECT id, workspace_id, user_id, routine_id, agent_id, COALESCE(run_id, ''), COALESCE(thread_id, ''), scheduled_for, COALESCE(started_at, ''), COALESCE(finished_at, ''), status, detail, source, catchup, paused_ms, wake_ms, outage_from, outage_to, created_at FROM routine_runs`

func scanRoutine(row interface{ Scan(...any) error }) (Routine, error) {
	var r Routine
	var enabled int
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.UserID, &r.AgentID, &r.Name, &r.Cron, &r.Timezone, &r.Prompt, &enabled, &r.ApprovalPolicy, &r.MissedPolicy, &r.NextRunAt, &r.LastRunAt, &r.CreatedAt, &r.DeletedAt)
	if err != nil {
		return Routine{}, scanErr(err)
	}
	r.Enabled = enabled == 1
	return r, nil
}

func scanRoutines(rows *sql.Rows) ([]Routine, error) {
	var out []Routine
	for rows.Next() {
		r, err := scanRoutine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRoutineRun(row interface{ Scan(...any) error }) (RoutineRun, error) {
	var r RoutineRun
	var catch int
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.UserID, &r.RoutineID, &r.AgentID, &r.RunID, &r.ThreadID, &r.ScheduledFor, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Detail, &r.Source, &catch, &r.PausedMS, &r.WakeMS, &r.OutageFrom, &r.OutageTo, &r.CreatedAt)
	if err != nil {
		return RoutineRun{}, scanErr(err)
	}
	r.Catchup = catch == 1
	return r, nil
}

func scanRoutineRuns(rows *sql.Rows) ([]RoutineRun, error) {
	var out []RoutineRun
	for rows.Next() {
		r, err := scanRoutineRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
