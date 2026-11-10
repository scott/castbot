package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/scott/castbot/internal/auth"
)

func TestMigratedThreadIndexMatchesFresh(t *testing.T) {
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	fresh := testStore(t)
	want := indexCols(t, fresh)

	oldPath := filepath.Join(t.TempDir(), "old.db")
	writeThreads(t, oldPath, `CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dm', 'group', 'routine')),
  agent_id TEXT,
  title TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`, `CREATE INDEX idx_threads_user ON threads(user_id, created_at)`, false)
	old, err := Open(oldPath, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { old.Close() })
	if got := indexCols(t, old); !sameCols(got, want) {
		t.Fatalf("rebuilt index %v, fresh %v", got, want)
	}
	var session string
	if err := old.db.QueryRow(`SELECT cast_session_id FROM threads WHERE id = 't1'`).Scan(&session); err != nil {
		t.Fatal(err)
	}
	if session != "" {
		t.Fatalf("session %q", session)
	}

	driftPath := filepath.Join(t.TempDir(), "drift.db")
	writeThreads(t, driftPath, `CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dm', 'group', 'routine', 'cli')),
  agent_id TEXT,
  title TEXT NOT NULL DEFAULT '',
  cast_session_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`, `CREATE INDEX idx_threads_user ON threads(user_id, created_at)`, true)
	drift, err := Open(driftPath, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { drift.Close() })
	if got := indexCols(t, drift); !sameCols(got, want) {
		t.Fatalf("repaired index %v, fresh %v", got, want)
	}
}

func writeThreads(t *testing.T, path, ddl, index string, withSession bool) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(index); err != nil {
		t.Fatal(err)
	}
	stmt := `INSERT INTO threads(id, workspace_id, user_id, kind, agent_id, title, created_at) VALUES ('t1', 'w', 'u', 'dm', 'a', 'Chat', '2026-01-01T00:00:00Z')`
	if withSession {
		stmt = `INSERT INTO threads(id, workspace_id, user_id, kind, agent_id, title, cast_session_id, created_at) VALUES ('t1', 'w', 'u', 'dm', 'a', 'Chat', '', '2026-01-01T00:00:00Z')`
	}
	if _, err := db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}

func indexCols(t *testing.T, s *Store) []string {
	t.Helper()
	cols, err := s.indexColumns("idx_threads_user")
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0] != "user_id" || cols[1] != "agent_id" {
		t.Fatalf("index %v", cols)
	}
	return cols
}

func sameCols(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
