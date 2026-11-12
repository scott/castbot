package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestFilesMigrationV18(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	s, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"id", "workspace_id", "user_id", "agent_id", "thread_id", "name", "mime", "size", "sha256", "source", "path", "seen_at", "status", "created_at"} {
		if !columnExists(t, s, "files", col) {
			t.Fatalf("files.%s missing", col)
		}
	}
	for _, col := range []string{"id", "workspace_id", "user_id", "agent_id", "name", "size", "seen_at"} {
		if !columnExists(t, s, "file_cache", col) {
			t.Fatalf("file_cache.%s missing", col)
		}
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 18`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 18 count %d %v", n, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 17`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 17 count %d %v", n, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 16`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 16 count %d %v", n, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 18`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("reopen version %d %v", n, err)
	}
	if _, err := s.db.Exec(`DROP TABLE files`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE file_cache`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version = 18`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateFiles(); err != nil {
		t.Fatal(err)
	}
	if !columnExists(t, s, "files", "user_id") || !columnExists(t, s, "file_cache", "user_id") {
		t.Fatal("migration did not restore the tables")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	var check string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity %q %v", check, err)
	}
}

func TestObserveWorkspaceKeepsSeenAt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "files-owner@example.com", "Owner")
	other := makeUser(t, s, "files-other@example.com", "Other")
	sec, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return start })
	if err := s.ObserveWorkspace(ctx, owner.ID, agent.ID, []ListedFile{{Name: "docs/pricing.md", Size: 30}}); err != nil {
		t.Fatal(err)
	}
	first, err := s.ListCached(ctx, owner.ID, agent.ID)
	if err != nil || len(first) != 1 {
		t.Fatalf("cache %#v %v", first, err)
	}
	seen := first[0].SeenAt
	s.SetNow(func() time.Time { return start.Add(2 * time.Hour) })
	if err := s.ObserveWorkspace(ctx, owner.ID, agent.ID, []ListedFile{{Name: "docs/pricing.md", Size: 30}}); err != nil {
		t.Fatal(err)
	}
	again, err := s.ListCached(ctx, owner.ID, agent.ID)
	if err != nil || len(again) != 1 || !again[0].SeenAt.Equal(seen) {
		t.Fatalf("seen changed %#v", again)
	}
	if err := s.ObserveWorkspace(ctx, owner.ID, agent.ID, []ListedFile{{Name: "docs/pricing.md", Size: 40}}); err != nil {
		t.Fatal(err)
	}
	changed, err := s.ListCached(ctx, owner.ID, agent.ID)
	if err != nil || len(changed) != 1 || changed[0].SeenAt.Equal(seen) || changed[0].Size != 40 {
		t.Fatalf("size change %#v", changed)
	}
	row, err := s.InsertUpload(ctx, owner.ID, agent.ID, "", "notes.txt", "text/plain", "ab", "workspace/uploads/notes.txt", "stored", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetFile(ctx, other.ID, row.ID); err != ErrNotFound {
		t.Fatalf("other user %v", err)
	}
	usage, err := s.UploadUsage(ctx, owner.ID, agent.WorkspaceID)
	if err != nil || usage != 4 {
		t.Fatalf("usage %d %v", usage, err)
	}
	otherUsage, err := s.UploadUsage(ctx, other.ID, other.WorkspaceID)
	if err != nil || otherUsage != 0 {
		t.Fatalf("other usage %d %v", otherUsage, err)
	}
}
