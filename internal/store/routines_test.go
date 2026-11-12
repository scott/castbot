package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/cron"
)

func TestMigrateV17Routines(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "v15.db")
	s := openStore(t, path)
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS routine_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS routines`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE IF EXISTS scheduler_meta`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE vm_events DROP COLUMN code`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version = 17`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	up := openStore(t, path)
	assertRoutineSchema(t, up)
	up.Close()
	again := openStore(t, path)
	assertRoutineSchema(t, again)
	var n int
	if err := again.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = 17`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 17 count %d %v", n, err)
	}
}

func assertRoutineSchema(t *testing.T, s *Store) {
	t.Helper()
	for _, table := range []string{"routines", "routine_runs"} {
		var name string
		if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
	}
	for _, col := range []string{"approval_policy", "missed_policy", "enabled"} {
		missing, err := s.columnMissing("routines", col)
		if err != nil || missing {
			t.Fatalf("routines.%s missing %v", col, err)
		}
	}
	missing, err := s.columnMissing("vm_events", "code")
	if err != nil || missing {
		t.Fatal("vm_events.code", err, missing)
	}
	var unique int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('routine_runs') WHERE name = 'idx_routine_runs_slot' AND "unique" = 1`).Scan(&unique); err != nil || unique != 1 {
		t.Fatalf("unique index %d %v", unique, err)
	}
}

func TestClaimOnceAndFakeClock(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "clock@example.com", "Clock")
	if err := s.SetTimezone(ctx, user.ID, "America/Denver"); err != nil {
		t.Fatal(err)
	}
	agent := routineAgent(t, s, user.ID)
	loc, _ := time.LoadLocation("America/Denver")
	// Thursday Nov 12, 2026 07:59 MST. The weekday fire is 08:00.
	now := time.Date(2026, 11, 12, 7, 59, 0, 0, loc)
	s.SetNow(func() time.Time { return now })
	in := RoutineInput{Name: "Morning docs check", Cron: "0 8 * * 1-5", Timezone: "America/Denver", Prompt: "Check the docs.", ApprovalPolicy: ApprovalWait, MissedPolicy: MissedOnce, Enabled: true}
	routine, err := s.CreateRoutine(ctx, user.ID, agent.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("early claim %#v", got)
	}
	now = time.Date(2026, 11, 12, 8, 0, 0, 0, loc)
	s.SetNow(func() time.Time { return now })
	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []RoutineRun
	var errs []error
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := s.ClaimDue(ctx)
			mu.Lock()
			all = append(all, rows...)
			if err != nil {
				errs = append(errs, err)
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	rows, err := s.ListRoutineRuns(ctx, user.ID, routine.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(all) != 1 {
		t.Fatalf("rows %d claimed %d", len(rows), len(all))
	}
	when, err := time.Parse(time.RFC3339, rows[0].ScheduledFor)
	if err != nil {
		t.Fatal(err)
	}
	local := when.In(loc)
	if local.Hour() != 8 || local.Minute() != 0 || local.Day() != 12 {
		t.Fatalf("scheduled %s", local)
	}
	// A row left behind without advancing the schedule must not insert a second one.
	if _, err := s.db.ExecContext(ctx, `UPDATE routines SET next_run_at = ? WHERE id = ?`, rows[0].ScheduledFor, routine.ID); err != nil {
		t.Fatal(err)
	}
	again, err := s.ClaimDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("crash reclaim started another row: %d", len(again))
	}
	rows, err = s.ListRoutineRuns(ctx, user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("still one row, got %d %v", len(rows), err)
	}
}

func TestMissedOnceAndSkipAndPause(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "miss@example.com", "Miss")
	agent := routineAgent(t, s, user.ID)
	loc, _ := time.LoadLocation("America/Denver")
	start := time.Date(2026, 11, 7, 17, 0, 0, 0, loc) // Saturday, before Sunday 6:00 PM and Monday 8:00 AM
	s.SetNow(func() time.Time { return start })
	once, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{
		Name: "Morning docs check", Cron: "0 8 * * 1-5", Timezone: "America/Denver", Prompt: "Check the docs.", MissedPolicy: MissedOnce, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	skip, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{
		Name: "Weekend changelog check", Cron: "0 18 * * 0", Timezone: "America/Denver", Prompt: "Skip me.", MissedPolicy: MissedSkip, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{
		Name: "Paused check", Cron: "0 8 * * 1-5", Timezone: "America/Denver", Prompt: "Stay quiet.", MissedPolicy: MissedOnce, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRoutineEnabled(ctx, user.ID, paused.ID, false); err != nil {
		t.Fatal(err)
	}
	// Downtime covers Mon and Tue 8:00, and Sun Nov 8 6:00 PM.
	back := time.Date(2026, 11, 10, 9, 14, 0, 0, loc)
	s.SetNow(func() time.Time { return back })
	claimed, err := s.ClaimDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var catchup int
	for _, row := range claimed {
		if row.RoutineID == once.ID && row.Status == RoutineLate && row.Launch {
			catchup++
		}
		if row.RoutineID == once.ID && row.Status == RoutineMissed && row.Launch {
			t.Fatal("missed row launched")
		}
	}
	if catchup != 1 {
		t.Fatalf("catch-up launches %d", catchup)
	}
	onceRows, err := s.ListRoutineRuns(ctx, user.ID, once.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(onceRows) != 2 {
		t.Fatalf("once rows %d", len(onceRows))
	}
	var late, missed int
	for _, row := range onceRows {
		switch row.Status {
		case RoutineLate:
			late++
		case RoutineMissed:
			missed++
			if row.RunID != "" {
				t.Fatal("missed row started a run")
			}
		default:
			t.Fatalf("status %s", row.Status)
		}
	}
	if late != 1 || missed != 1 {
		t.Fatalf("late %d missed %d", late, missed)
	}
	// Newest scheduled time is the late one.
	newest := onceRows[0]
	if newest.Status != RoutineLate {
		t.Fatalf("newest %s", newest.Status)
	}
	skipRows, err := s.ListRoutineRuns(ctx, user.ID, skip.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipRows) != 1 || skipRows[0].Status != RoutineSkipped || skipRows[0].Launch {
		t.Fatalf("skip %#v", skipRows)
	}
	pausedRows, err := s.ListRoutineRuns(ctx, user.ID, paused.ID, 0)
	if err != nil || len(pausedRows) != 0 {
		t.Fatalf("paused rows %d %v", len(pausedRows), err)
	}
	if _, err := s.SetRoutineEnabled(ctx, user.ID, paused.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDue(ctx); err != nil {
		t.Fatal(err)
	}
	pausedRows, err = s.ListRoutineRuns(ctx, user.ID, paused.ID, 0)
	if err != nil || len(pausedRows) != 0 {
		t.Fatalf("paused catch-up %d %v", len(pausedRows), err)
	}
	resumed, err := s.GetRoutine(ctx, user.ID, paused.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := time.Parse(time.RFC3339, resumed.NextRunAt)
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(back) {
		t.Fatalf("next %s not after resume", next)
	}
}

func TestMinIntervalAndZones(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "zone@example.com", "Zone")
	if err := s.SetTimezone(ctx, user.ID, "America/Denver"); err != nil {
		t.Fatal(err)
	}
	agent := routineAgent(t, s, user.ID)
	for _, expr := range []string{"0 * * * *", "0,15,30,45 * * * *"} {
		if _, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "Ok " + expr, Cron: expr, Timezone: "America/Denver", Prompt: "Fine.", Enabled: true}); err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
	}
	if _, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "Too close", Cron: "*/10 * * * *", Timezone: "America/Denver", Prompt: "No.", Enabled: true}); err != ErrInvalid {
		t.Fatalf("10 min: %v", err)
	}
	zones := cron.Zones(user.Timezone)
	if len(zones) < 100 {
		t.Fatalf("zones %d", len(zones))
	}
	if zones[0].Name != "America/Denver" {
		t.Fatalf("first %s", zones[0].Name)
	}
	var tokyo bool
	for _, z := range zones {
		if z.Name == "Asia/Tokyo" {
			tokyo = true
		}
	}
	if !tokyo {
		t.Fatal("missing Tokyo")
	}
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	sched, err := cron.Parse("0 8 * * *")
	if err != nil {
		t.Fatal(err)
	}
	// Day before and day after the 2026 spring-forward in Denver.
	before := time.Date(2026, 3, 7, 12, 0, 0, 0, loc)
	after := time.Date(2026, 3, 9, 12, 0, 0, 0, loc)
	nextBefore, ok := sched.Next(before, loc)
	nextAfter, ok2 := sched.Next(after, loc)
	if !ok || !ok2 || nextBefore.In(loc).Hour() != 8 || nextAfter.In(loc).Hour() != 8 {
		t.Fatalf("dst %s %s", nextBefore, nextAfter)
	}
}

func TestRunNowClaimPath(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "now@example.com", "Now")
	agent := routineAgent(t, s, user.ID)
	routine, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "Morning docs check", Cron: "0 8 * * 1-5", Timezone: "America/Denver", Prompt: "Check the docs.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.ClaimManual(ctx, user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Source != RoutineSourceManual || !row.Launch {
		t.Fatalf("%#v", row)
	}
	if _, err := s.ClaimManual(ctx, user.ID, routine.ID); err != ErrRoutineBusy {
		t.Fatalf("second manual %v", err)
	}
	rows, err := s.ListRoutineRuns(ctx, user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %d %v", len(rows), err)
	}
}

func TestQueueAndHold(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "queue@example.com", "Queue")
	agent := routineAgent(t, s, user.ID)
	loc, _ := time.LoadLocation("America/Denver")
	now := time.Date(2026, 11, 12, 7, 0, 0, 0, loc)
	s.SetNow(func() time.Time { return now })
	a, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "A", Cron: "0 8 * * 1-5", Timezone: "America/Denver", Prompt: "A prompt.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "B", Cron: "0 8 * * 1-5", Timezone: "America/Denver", Prompt: "B prompt.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 12, 8, 0, 0, 0, loc)
	s.SetNow(func() time.Time { return now })
	rows, err := s.ClaimDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var started, queued int
	for _, row := range rows {
		if row.Launch {
			started++
		}
		if row.Status == RoutineQueued {
			queued++
			if row.Detail != "Waits for the reply in progress" {
				t.Fatal(row.Detail)
			}
		}
	}
	if started != 1 || queued != 1 {
		t.Fatalf("started %d queued %d", started, queued)
	}
	rr, th, run, err := s.BeginRoutineExecution(ctx, user.ID, launchID(rows))
	if err != nil {
		t.Fatal(err)
	}
	if th.Kind != "routine" || run.RoutineRunID != rr.ID {
		t.Fatalf("thread %s run %s", th.Kind, run.RoutineRunID)
	}
	if _, _, _, err := s.BeginRoutineExecution(ctx, user.ID, rr.ID); err != ErrConflict {
		t.Fatalf("second begin %v", err)
	}
	_ = a
	_ = b
	other := makeUser(t, s, "other@example.com", "Other")
	if _, err := s.GetRoutine(ctx, other.ID, a.ID); err != ErrNotFound {
		t.Fatalf("authz %v", err)
	}
	if _, err := s.ListRoutineRuns(ctx, other.ID, a.ID, 10); err != ErrNotFound {
		t.Fatalf("runs authz %v", err)
	}
}

func TestHoldDuringReplace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "hold@example.com", "Hold")
	agent := routineAgent(t, s, user.ID)
	vm := holdVM(t, s, user.ID, agent.ID)
	loc, _ := time.LoadLocation("America/Denver")
	due := time.Date(2026, 11, 12, 8, 0, 0, 0, loc)
	s.SetNow(func() time.Time { return due.Add(-time.Hour) })
	routine, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "Morning docs check", Cron: "0 8 * * *", Timezone: "America/Denver", Prompt: "Check the docs.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginReplacement(ctx, user.ID, agent.ID, "web:test", "example.invalid/castbot-runtime:next", false); err != nil {
		t.Fatal(err)
	}
	s.SetNow(func() time.Time { return due })
	rows, err := s.ClaimDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != RoutineHeld || rows[0].Launch {
		t.Fatalf("held %#v", rows)
	}
	if rows[0].Detail != "Waits for the machine replacement" {
		t.Fatal(rows[0].Detail)
	}
	// Still inside the hour: on time once the lock clears.
	s.SetNow(func() time.Time { return due.Add(30 * time.Minute) })
	promoted, ok, err := s.PromoteRoutine(ctx, user.ID, agent.ID)
	if err != nil || ok || promoted.ID != "" {
		t.Fatalf("promoted during replace %v %v", ok, err)
	}
	repl, err := s.ActiveReplacement(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishReplacement(ctx, user.ID, repl.ID, false); err != nil {
		t.Fatal(err)
	}
	promoted, ok, err = s.PromoteRoutine(ctx, user.ID, agent.ID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	rr, _, _, err := s.BeginRoutineExecution(ctx, user.ID, promoted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Status != RoutineRunning {
		t.Fatalf("on time status %s", rr.Status)
	}
	_ = vm
	_ = routine
}

func TestHoldLateAfterOneHour(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "late@example.com", "Late")
	agent := routineAgent(t, s, user.ID)
	holdVM(t, s, user.ID, agent.ID)
	loc, _ := time.LoadLocation("America/Denver")
	due := time.Date(2026, 11, 12, 8, 0, 0, 0, loc)
	s.SetNow(func() time.Time { return due.Add(-time.Hour) })
	if _, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "Morning docs check", Cron: "0 8 * * *", Timezone: "America/Denver", Prompt: "Check the docs.", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BeginReplacement(ctx, user.ID, agent.ID, "web:test", "example.invalid/castbot-runtime:next", false); err != nil {
		t.Fatal(err)
	}
	s.SetNow(func() time.Time { return due })
	rows, err := s.ClaimDue(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(err, len(rows))
	}
	repl, err := s.ActiveReplacement(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishReplacement(ctx, user.ID, repl.ID, false); err != nil {
		t.Fatal(err)
	}
	s.SetNow(func() time.Time { return due.Add(time.Hour + time.Minute) })
	promoted, ok, err := s.PromoteRoutine(ctx, user.ID, agent.ID)
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	rr, _, _, err := s.BeginRoutineExecution(ctx, user.ID, promoted.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Status != RoutineLate {
		t.Fatalf("late status %s", rr.Status)
	}
}

func TestDeleteKeepsThreads(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "del@example.com", "Del")
	agent := routineAgent(t, s, user.ID)
	routine, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "Morning docs check", Cron: "0 8 * * 1-5", Timezone: "America/Denver", Prompt: "Check the docs.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.ClaimManual(ctx, user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, th, run, err := s.BeginRoutineExecution(ctx, user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRoutine(ctx, user.ID, routine.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetThread(ctx, user.ID, th.ID)
	if err != nil || got.Kind != "routine" || got.Title != "deleted routine" {
		t.Fatal(err, got.Kind, got.Title)
	}
	still, err := s.GetRun(ctx, user.ID, run.ID)
	if err != nil || still.Status != "queued" {
		t.Fatal(err, still.Status)
	}
	live, err := s.ListRoutines(ctx, user.ID, agent.ID)
	if err != nil || len(live) != 0 {
		t.Fatalf("live %d %v", len(live), err)
	}
}

func TestPanelHistoryCap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "hist@example.com", "Hist")
	agent := routineAgent(t, s, user.ID)
	routine, err := s.CreateRoutine(ctx, user.ID, agent.ID, RoutineInput{Name: "Morning docs check", Cron: "0 8 * * 1-5", Timezone: "America/Denver", Prompt: "Check the docs.", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 31; i++ {
		when := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC).Add(time.Duration(i) * 24 * time.Hour)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO routine_runs(id, workspace_id, user_id, routine_id, agent_id, scheduled_for, status, detail, source, created_at)
VALUES (?, ?, ?, ?, ?, ?, 'done', 'done', 'schedule', ?)`,
			"run-"+when.Format("20060102"), routine.WorkspaceID, user.ID, routine.ID, agent.ID, when.Format(time.RFC3339), when.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListRoutineRuns(ctx, user.ID, routine.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 30 {
		t.Fatalf("panel %d", len(rows))
	}
	n, err := s.CountRoutineRuns(ctx, user.ID, routine.ID)
	if err != nil || n != 31 {
		t.Fatalf("count %d %v", n, err)
	}
}

func routineAgent(t *testing.T, s *Store, userID string) Agent {
	t.Helper()
	sec, err := s.SystemSection(context.Background(), userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(context.Background(), userID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func holdVM(t *testing.T, s *Store, userID, agentID string) VM {
	t.Helper()
	vm, err := s.BeginVM(context.Background(), userID, agentID, "atlas", "example.invalid/castbot-runtime:s07")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(context.Background(), userID, agentID, "vm-hold", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	return vm
}

func launchID(rows []RoutineRun) string {
	for _, row := range rows {
		if row.Launch {
			return row.ID
		}
	}
	return ""
}

func openStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path, Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Keep sql imported if a helper needs it. The migration test uses the driver through the store.
var _ = sql.ErrNoRows
