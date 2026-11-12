package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/scott/castbot/internal/auth"
)

func TestMigrateV19GreetedAtAndDrafts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opts := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	s, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := makeUser(t, s, "timmy@example.com", "Test Timmy")
	sec, err := s.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, user.ID, AgentInput{SectionID: sec.ID, Name: "Timmy", Title: "Welcome", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	greeted, err := s.AgentGreeted(ctx, user.ID, agent.ID)
	if err != nil || greeted == "" || greeted != agent.CreatedAt {
		t.Fatalf("new agent greeted %q created %q err %v", greeted, agent.CreatedAt, err)
	}
	if _, err := s.SaveCreateDraft(ctx, user.ID, `{"turns":[]}`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE agents DROP COLUMN greeted_at`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE create_drafts`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 19`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	greeted, err = s.AgentGreeted(ctx, user.ID, agent.ID)
	if err != nil || greeted != agent.CreatedAt {
		t.Fatalf("backfill %q %v", greeted, err)
	}
	run, started, err := s.BeginGreet(ctx, user.ID, agent.ID, GreetWrite{ThreadID: thread.ID, Body: "Hi"})
	if err != nil || started || run.ID != "" {
		t.Fatalf("existing agent was greeted again: started %v run %s err %v", started, run.ID, err)
	}
	if _, err := s.LoadCreateDraft(ctx, user.ID); err != ErrNotFound {
		t.Fatalf("draft table: %v", err)
	}
	fresh, _, err := s.CreateAgent(ctx, user.ID, AgentInput{SectionID: sec.ID, Name: "Scout", Title: "New", Status: "draft", PendingGreet: true})
	if err != nil {
		t.Fatal(err)
	}
	if g, err := s.AgentGreeted(ctx, user.ID, fresh.ID); err != nil || g != "" {
		t.Fatalf("pending greet %q %v", g, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if g, err := s.AgentGreeted(ctx, user.ID, fresh.ID); err != nil || g != "" {
		t.Fatalf("reopen filled greeted_at %q %v", g, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 19`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 19 rows %d %v", n, err)
	}
}
