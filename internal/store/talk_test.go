package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/auth"
)

func TestSpecialistTurnCapAndScope(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, st, "owner@example.com", "Owner")
	other := makeUser(t, st, "other@example.com", "Other")
	sec, err := st.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	chief, thread, err := st.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Chief", Title: "Lead", Description: "Coordinates"})
	if err != nil {
		t.Fatal(err)
	}
	ada, _, err := st.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Ada", Title: "Writer", Description: "Drafts"})
	if err != nil {
		t.Fatal(err)
	}
	bea, _, err := st.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Bea", Title: "Editor", Description: "Checks"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := st.CreateMessage(ctx, owner.ID, thread.ID, "user", owner.ID, "Split this job.")
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.EnsureAgentJob(ctx, owner.ID, thread.ID, msg.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.JobByMessage(ctx, other.ID, msg.ID); err != ErrNotFound {
		t.Fatalf("other job %v", err)
	}
	for i := 0; i < 7; i++ {
		if err := st.ClaimSpecialistTurns(ctx, owner.ID, job.ID, 1); err != nil {
			t.Fatalf("claim %d %v", i, err)
		}
	}
	if err := st.ClaimSpecialistTurns(ctx, owner.ID, job.ID, 2); err != ErrRejected {
		t.Fatalf("overrun %v", err)
	}
	got, err := st.JobByID(ctx, owner.ID, job.ID)
	if err != nil || got.SpecialistTurns != 7 {
		t.Fatalf("turns %#v %v", got, err)
	}
	if err := st.ClaimSpecialistTurns(ctx, owner.ID, job.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.ClaimSpecialistTurns(ctx, owner.ID, job.ID, 1); err != ErrRejected {
		t.Fatalf("ninth %v", err)
	}
	if _, err := st.CreateThread(ctx, owner.ID, "agent_dm", chief.ID, "nope"); err != ErrInvalid {
		t.Fatalf("public create %v", err)
	}
	dm, err := st.CreateAgentThread(ctx, owner.ID, "agent_dm", chief.ID, "DM with Ada", job.ID, []string{chief.ID, ada.ID})
	if err != nil {
		t.Fatal(err)
	}
	if dm.AgentID != chief.ID || dm.Kind != "agent_dm" {
		t.Fatalf("thread %#v", dm)
	}
	if _, err := st.GetThread(ctx, other.ID, dm.ID); err != ErrNotFound {
		t.Fatalf("other thread %v", err)
	}
	if _, err := st.Participants(ctx, other.ID, dm.ID); err != ErrNotFound {
		t.Fatalf("other participants %v", err)
	}
	parts, err := st.Participants(ctx, owner.ID, dm.ID)
	if err != nil || len(parts) != 2 {
		t.Fatalf("participants %#v %v", parts, err)
	}
	if _, err := st.CreateRun(ctx, owner.ID, dm.ID, ada.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateRun(ctx, owner.ID, dm.ID, bea.ID); err != ErrNotFound {
		t.Fatalf("stranger run %v", err)
	}
	if _, err := st.CreateRun(ctx, other.ID, dm.ID, ada.ID); err != ErrNotFound {
		t.Fatalf("other run %v", err)
	}
	again, err := st.FindAgentDM(ctx, owner.ID, job.ID, ada.ID, chief.ID)
	if err != nil || again.ID != dm.ID {
		t.Fatalf("find %#v %v", again, err)
	}
	if _, err := st.CreateAgentThread(ctx, owner.ID, "agent_dm", chief.ID, "no", job.ID, []string{chief.ID, "missing"}); err != ErrNotFound {
		t.Fatalf("missing agent %v", err)
	}
}

func TestAgentThreadMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	st, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := makeUser(t, st, "mig@example.com", "Mig")
	sec, err := st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := st.CreateAgent(ctx, user.ID, AgentInput{SectionID: sec.ID, Name: "Kept", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TABLE threads_old (
  id TEXT PRIMARY KEY,
  workspace_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('dm', 'group', 'routine', 'cli')),
  agent_id TEXT,
  title TEXT NOT NULL DEFAULT '',
  cast_session_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO threads_old(id, workspace_id, user_id, kind, agent_id, title, cast_session_id, created_at)
SELECT id, workspace_id, user_id, kind, agent_id, title, cast_session_id, created_at FROM threads`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DROP TABLE threads`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`ALTER TABLE threads_old RENAME TO threads`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE INDEX idx_threads_user ON threads(user_id, agent_id)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM schema_migrations WHERE version = 25`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 25`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 25 %d %v", n, err)
	}
	var ddl string
	if err := st.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'threads'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "'agent_dm'") || !strings.Contains(ddl, "'group'") {
		t.Fatalf("ddl %s", ddl)
	}
	var jobCol int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('threads') WHERE name = 'job_id'`).Scan(&jobCol); err != nil || jobCol != 1 {
		t.Fatalf("job_id %d %v", jobCol, err)
	}
	got, err := st.GetThread(ctx, user.ID, thread.ID)
	if err != nil || got.AgentID != agent.ID || got.Kind != "dm" {
		t.Fatalf("kept %#v %v", got, err)
	}
	other, _, err := st.CreateAgent(ctx, user.ID, AgentInput{SectionID: sec.ID, Name: "Ada", Title: "Writer"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "A job")
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.EnsureAgentJob(ctx, user.ID, thread.ID, msg.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAgentThread(ctx, user.ID, "agent_chat", agent.ID, "Chat", job.ID, []string{agent.ID, other.ID}); err != nil {
		t.Fatal(err)
	}
}
