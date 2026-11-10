package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestApprovalsMigration(t *testing.T) {
	st := testStore(t)
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 13`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("version 13 count %d", n)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('approvals', 'audit_log')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("tables %d", n)
	}
}

func TestDeferGuestRestartWhileApprovalPending(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "defer-s07b@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStreaming(ctx, owner.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	wait, err := s.DeferGuestRestart(ctx, owner.ID, agent.ID, run.ID)
	if err != nil || wait {
		t.Fatalf("fresh run deferred %v %v", wait, err)
	}
	if _, err := s.AppendRunEvent(ctx, owner.ID, run.ID, "text", `{"text":"hi"}`, 2); err != nil {
		t.Fatal(err)
	}
	wait, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, run.ID)
	if err != nil || !wait {
		t.Fatalf("started run %v %v", wait, err)
	}
	fresh, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStreaming(ctx, owner.ID, fresh.ID); err != nil {
		t.Fatal(err)
	}
	wait, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, fresh.ID)
	if err != nil || !wait {
		t.Fatalf("other open run %v %v", wait, err)
	}
	if _, _, err := s.FinishRun(ctx, owner.ID, run.ID, "hi", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordPermission(ctx, owner.ID, fresh.ID, json.RawMessage(`{"id":"req-defer","tool":"bash","arguments":{"command":"ls"}}`), time.Now().Add(time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	wait, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, "")
	if err != nil || !wait {
		t.Fatalf("pending approval %v %v", wait, err)
	}
}

func TestCLIDecisionPicksUpToolFromLaterPermission(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "shape-s07b@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.IssueCLIToken(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	caller := "cli:" + id
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T22:12:17Z", SessionID: "sess-real", Caller: caller, Kind: "request", Text: "List the demo folder"},
		{Seq: 2, At: "2026-11-10T22:12:17Z", SessionID: "sess-real", Caller: caller, Kind: "decision", RequestID: "req-real", Decision: "approved"},
		{Seq: 3, At: "2026-11-10T22:12:18Z", SessionID: "sess-real", Caller: caller, Kind: "permission", Tool: "bash", RequestID: "req-real"},
	}); err != nil {
		t.Fatal(err)
	}
	threads, err := s.ListThreadsByAgent(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cli Thread
	for _, th := range threads {
		if th.Kind == "cli" {
			cli = th
		}
	}
	msgs, err := s.ListMessages(ctx, owner.ID, cli.ID)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, m := range msgs {
		if m.Body == "An approval for bash was approved." {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("bodies %#v", msgs)
	}
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 4, At: "2026-11-10T22:12:19Z", SessionID: thread.ID, Caller: "control_plane", Kind: "permission", Tool: "bash", RequestID: "req-web"},
		{Seq: 5, At: "2026-11-10T22:12:19Z", SessionID: "", Caller: "control_plane", Kind: "decision", RequestID: "req-web", Decision: "approved"},
	}); err != nil {
		t.Fatal(err)
	}
	web, err := s.ListMessages(ctx, owner.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(web) != 0 {
		t.Fatalf("web thread copied %d messages", len(web))
	}
}
