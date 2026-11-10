package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/scott/castbot/internal/auth"
)

func TestRetryRunIsAtomic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "retry@example.com", "Retry")
	sec, err := s.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, user.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "hello there")
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageRun(ctx, user.ID, msg.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FailRun(ctx, user.ID, run.ID, "The reply was interrupted.", 0); err != nil {
		t.Fatal(err)
	}
	next, moved, err := s.RetryRun(ctx, user.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != "queued" || next.ID == run.ID || moved.Body != "hello there" {
		t.Fatalf("next %#v msg %#v", next, moved)
	}
	if _, _, err := s.RetryRun(ctx, user.ID, run.ID); err != ErrConflict {
		t.Fatalf("second retry %v", err)
	}
	got, err := s.GetRun(ctx, user.ID, run.ID)
	if err != nil || got.Status != "retried" {
		t.Fatalf("claimed %#v %v", got, err)
	}
	runs, err := s.ListThreadRuns(ctx, user.ID, thread.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs %d %v", len(runs), err)
	}
	linked, err := s.UserMessageForRun(ctx, user.ID, next.ID)
	if err != nil || linked.ID != msg.ID {
		t.Fatalf("linked %#v %v", linked, err)
	}
}

func TestOldRunsCheckAllowsRetried(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  thread_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  routine_run_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('queued', 'streaming', 'waiting_approval', 'done', 'failed', 'stopped')),
  stream_offset INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  error TEXT NOT NULL DEFAULT ''
)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO runs(id, workspace_id, thread_id, agent_id, user_id, status, stream_offset, started_at, error) VALUES ('r1', 'w', 't', 'a', 'u', 'streaming', 2, '2020-01-01T00:00:00Z', ''), ('r2', 'w', 't', 'a', 'u', 'done', 4, '2020-01-01T00:00:01Z', '')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var status string
	var offset int
	if err := s.db.QueryRow(`SELECT status, stream_offset FROM runs WHERE id = 'r1'`).Scan(&status, &offset); err != nil {
		t.Fatal(err)
	}
	if status != "streaming" || offset != 2 {
		t.Fatalf("migrated row %s %d", status, offset)
	}
	if err := s.FailAbandoned(context.Background(), "The reply was interrupted."); err != nil {
		t.Fatal(err)
	}
	var message string
	if err := s.db.QueryRow(`SELECT status, error FROM runs WHERE id = 'r1'`).Scan(&status, &message); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || message != "The reply was interrupted." {
		t.Fatalf("abandoned %s %s", status, message)
	}
	if err := s.db.QueryRow(`SELECT status FROM runs WHERE id = 'r2'`).Scan(&status); err != nil || status != "done" {
		t.Fatalf("finished row %s %v", status, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM run_events WHERE run_id = 'r1' AND kind = 'error'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("error events %d %v", n, err)
	}
	if _, err := s.db.Exec(`UPDATE runs SET status = 'retried' WHERE id = 'r1'`); err != nil {
		t.Fatal(err)
	}
}
