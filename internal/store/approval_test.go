package store

import (
	"context"
	"encoding/json"
	"strings"
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
	wait, reason, err := s.DeferGuestRestart(ctx, owner.ID, agent.ID, run.ID)
	if err != nil || wait || reason != "" {
		t.Fatalf("fresh run deferred %v %s %v", wait, reason, err)
	}
	if _, err := s.AppendRunEvent(ctx, owner.ID, run.ID, "text", `{"text":"hi"}`, 2); err != nil {
		t.Fatal(err)
	}
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, run.ID)
	if err != nil || !wait || reason != "this run has already started" {
		t.Fatalf("started run %v %s %v", wait, reason, err)
	}
	fresh, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStreaming(ctx, owner.ID, fresh.ID); err != nil {
		t.Fatal(err)
	}
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, fresh.ID)
	if err != nil || !wait || reason == "" {
		t.Fatalf("other open run %v %s %v", wait, reason, err)
	}
	if _, _, err := s.FinishRun(ctx, owner.ID, run.ID, "hi", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordPermission(ctx, owner.ID, fresh.ID, json.RawMessage(`{"id":"req-defer","tool":"bash","arguments":{"command":"ls"}}`), time.Now().Add(time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, "")
	if err != nil || !wait || reason == "" {
		t.Fatalf("pending approval %v %s %v", wait, reason, err)
	}
}

func TestDeferGuestRestartBound(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	now := start
	s.SetNow(func() time.Time { return now })
	owner := makeUser(t, s, "bound-s07b@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	older, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStreaming(ctx, owner.ID, older.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunEvent(ctx, owner.ID, older.ID, "text", `{"text":"hi"}`, 2); err != nil {
		t.Fatal(err)
	}
	now = start.Add(time.Minute)
	newer, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStreaming(ctx, owner.ID, newer.ID); err != nil {
		t.Fatal(err)
	}
	// The older run has been open for a minute. That age does not start the bound.
	wait, reason, err := s.DeferGuestRestart(ctx, owner.ID, agent.ID, newer.ID)
	if err != nil || !wait || strings.HasPrefix(reason, "bound:") {
		t.Fatalf("before wanted %v %s %v", wait, reason, err)
	}
	now = start.Add(16 * time.Minute)
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, newer.ID)
	if err != nil || !wait || strings.HasPrefix(reason, "bound:") {
		t.Fatalf("old run without a wanted stamp %v %s %v", wait, reason, err)
	}
	wantedAt := now
	s.NoteRestartWanted(owner.ID, agent.ID)
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, newer.ID)
	if err != nil || !wait || strings.HasPrefix(reason, "bound:") {
		t.Fatalf("first deferral %v %s %v", wait, reason, err)
	}
	now = wantedAt.Add(DeferRestartLimit - time.Second)
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, newer.ID)
	if err != nil || !wait || strings.HasPrefix(reason, "bound:") {
		t.Fatalf("inside the bound %v %s %v", wait, reason, err)
	}
	now = wantedAt.Add(DeferRestartLimit)
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, newer.ID)
	if err != nil || wait || !strings.HasPrefix(reason, "bound:") {
		t.Fatalf("bound %v %s %v", wait, reason, err)
	}
}

func TestDeferGuestRestartDoesNotForcePendingApproval(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	now := start
	s.SetNow(func() time.Time { return now })
	owner := makeUser(t, s, "bound-ask-s07b@example.com", "Owner")
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
	if _, err := s.AppendRunEvent(ctx, owner.ID, run.ID, "text", `{"text":"hi"}`, 2); err != nil {
		t.Fatal(err)
	}
	now = start.Add(16 * time.Minute)
	ap, err := s.RecordPermission(ctx, owner.ID, run.ID, json.RawMessage(`{"id":"req-bound","tool":"bash","arguments":{"command":"ls"}}`), now.Add(time.Hour), 3)
	if err != nil {
		t.Fatal(err)
	}
	s.NoteRestartWanted(owner.ID, agent.ID)
	wait, reason, err := s.DeferGuestRestart(ctx, owner.ID, agent.ID, "")
	if err != nil || !wait || reason != "pending approval" {
		t.Fatalf("fresh ask %v %s %v", wait, reason, err)
	}
	now = now.Add(DeferRestartLimit)
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, "")
	if err != nil || !wait || strings.HasPrefix(reason, "bound:") {
		t.Fatalf("forced a pending approval %v %s %v", wait, reason, err)
	}
	row, err := s.GetApproval(ctx, owner.ID, ap.ID)
	if err != nil || row.Status != "pending" || row.DecidedBy != "" {
		t.Fatalf("row %#v %v", row, err)
	}
	if _, err := s.MarkInactive(ctx, owner.ID, ap.ID, "user", owner.ID); err != nil {
		t.Fatal(err)
	}
	wait, reason, err = s.DeferGuestRestart(ctx, owner.ID, agent.ID, "")
	if err != nil || wait || !strings.HasPrefix(reason, "bound:") {
		t.Fatalf("after the approval %v %s %v", wait, reason, err)
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
		{Seq: 2, At: "2026-11-10T22:12:17Z", SessionID: "", Caller: caller, Kind: "decision", RequestID: "req-real", Decision: "approved"},
		{Seq: 3, At: "2026-11-10T22:12:18Z", SessionID: "sess-real", Caller: caller, Kind: "permission", Tool: "bash", RequestID: "req-real"},
	}); err != nil {
		t.Fatal(err)
	}
	threads, err := s.ListThreadsByAgent(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cli Thread
	ncli := 0
	for _, th := range threads {
		if th.Kind == "cli" {
			ncli++
			cli = th
		}
	}
	if ncli != 1 || cli.CastSessionID != "sess-real" {
		t.Fatalf("threads %#v", threads)
	}
	msgs, err := s.ListMessages(ctx, owner.ID, cli.ID)
	if err != nil {
		t.Fatal(err)
	}
	asked, decided := -1, -1
	for i, m := range msgs {
		if m.Body == "An approval was asked for bash. Answer it in the Cast CLI." {
			asked = i
		}
		if m.Body == "An approval for bash was approved." {
			decided = i
		}
	}
	if asked < 0 || decided < asked {
		t.Fatalf("order asked %d decided %d %#v", asked, decided, msgs)
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
