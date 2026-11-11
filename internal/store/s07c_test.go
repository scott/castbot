package store

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestSameSecondWakeDoesNotPause(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	owner := makeUser(t, s, "wake-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "castbot-wake", "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, owner.ID, agent.ID, "vm-wake", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	first, err := s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyVMState(ctx, owner.ID, agent.ID, "running", first.Endpoint, ""); err != nil {
		t.Fatal(err)
	}
	second, err := s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.LastWake != first.LastWake {
		t.Fatalf("wake stamp moved in the same second %q %q", first.LastWake, second.LastWake)
	}
	if second.StateGen == first.StateGen {
		t.Fatal("state generation did not move")
	}
	changed, err := s.MarkPausedIfUnchanged(ctx, owner.ID, agent.ID, second.MicroVMID, first.StateGen, "")
	if err != nil || changed {
		t.Fatalf("stale same-second pause %v %v", changed, err)
	}
	got, err := s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || got.State != "running" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOrphanCLIDecisionLandsInCatchAll(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "orphan-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.IssueCLIToken(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	caller := "cli:" + id
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:00Z", Caller: caller, Kind: "decision", RequestID: "req-1", Tool: "bash", Decision: "approved"},
	}); err != nil {
		t.Fatal(err)
	}
	if cliMessages(t, s, owner.ID, agent.ID) != 0 {
		t.Fatal("decision was filed before a reply or the wait")
	}
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 2, At: "2026-11-10T12:00:05Z", SessionID: "sess-9", Caller: caller, Kind: "reply", Text: "done"},
	}); err != nil {
		t.Fatal(err)
	}
	threads, err := s.ListThreadsByAgent(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var catchAll, reply Thread
	for _, th := range threads {
		if th.Kind != "cli" {
			continue
		}
		switch th.CastSessionID {
		case cliCatchAllSession:
			catchAll = th
		case "sess-9":
			reply = th
		}
	}
	if catchAll.ID == "" || catchAll.Title != "Cast CLI" {
		t.Fatal("missing catch-all thread")
	}
	if reply.ID == "" {
		t.Fatal("reply left its own thread")
	}
	caught, err := s.ListMessages(ctx, owner.ID, catchAll.ID)
	if err != nil || len(caught) != 1 || !strings.Contains(caught[0].Body, "approved") || !strings.Contains(caught[0].Body, "bash") {
		t.Fatalf("%v %+v", err, caught)
	}
	replies, err := s.ListMessages(ctx, owner.ID, reply.ID)
	if err != nil || len(replies) != 1 || replies[0].Body != "done" {
		t.Fatalf("%v %+v", err, replies)
	}
}

func TestOrphanCLIDecisionWaitsFiveMinutes(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	owner := makeUser(t, s, "wait-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.IssueCLIToken(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:00Z", Caller: "cli:" + id, Kind: "decision", RequestID: "req-1", Decision: "approved"},
		{Seq: 2, At: "2026-11-10T12:00:00Z", Caller: "control_plane", Kind: "decision", RequestID: "req-web", SessionID: "web", Decision: "approved"},
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Minute)
	s.SetNow(func() time.Time { return now })
	n, err := s.SweepOrphanCLIDecisions(ctx)
	if err != nil || n != 0 || cliMessages(t, s, owner.ID, agent.ID) != 0 {
		t.Fatalf("early sweep %d %v", n, err)
	}
	now = now.Add(time.Minute)
	s.SetNow(func() time.Time { return now })
	n, err = s.SweepOrphanCLIDecisions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	threads, err := s.ListThreadsByAgent(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	cliThreads := 0
	for _, th := range threads {
		if th.Kind != "cli" {
			continue
		}
		cliThreads++
		if th.CastSessionID != cliCatchAllSession {
			t.Fatalf("session %s", th.CastSessionID)
		}
		msgs, err := s.ListMessages(ctx, owner.ID, th.ID)
		if err != nil || len(msgs) != 1 {
			t.Fatalf("%v %+v", err, msgs)
		}
		body = msgs[0].Body
	}
	if cliThreads != 1 || !strings.Contains(body, "An approval was approved.") {
		t.Fatalf("threads %d body %q", cliThreads, body)
	}
	again, err := s.SweepOrphanCLIDecisions(ctx)
	if err != nil || again != 0 {
		t.Fatalf("second sweep %d %v", again, err)
	}
}

func TestOrphanCLIDecisionSurvivesOtherAgent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	owner := makeUser(t, s, "n6-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	atlas, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	bolt, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Bolt", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	ida, _, err := s.IssueCLIToken(ctx, owner.ID, atlas.ID)
	if err != nil {
		t.Fatal(err)
	}
	idb, _, err := s.IssueCLIToken(ctx, owner.ID, bolt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestJournal(ctx, owner.ID, bolt.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:00Z", SessionID: "sb", Caller: "cli:" + idb, Kind: "reply", Text: "hello from Bolt"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestJournal(ctx, owner.ID, atlas.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:00Z", Caller: "cli:" + ida, Kind: "decision", RequestID: "req-a", Tool: "bash", Decision: "approved"},
		{Seq: 2, At: "2026-11-10T12:00:05Z", SessionID: "sa", Caller: "cli:" + ida, Kind: "reply", Text: "done"},
	}); err != nil {
		t.Fatal(err)
	}
	if cliMessages(t, s, owner.ID, atlas.ID) != 2 {
		t.Fatalf("atlas cli messages %d", cliMessages(t, s, owner.ID, atlas.ID))
	}
	now = now.Add(10 * time.Minute)
	s.SetNow(func() time.Time { return now })
	n, err := s.SweepOrphanCLIDecisions(ctx)
	if err != nil || n != 0 || cliMessages(t, s, owner.ID, atlas.ID) != 2 {
		t.Fatalf("sweep %d messages %d %v", n, cliMessages(t, s, owner.ID, atlas.ID), err)
	}
}

func TestOrphanCLIDecisionSurvivesNewMachine(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	owner := makeUser(t, s, "n6-vm-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.IssueCLIToken(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	caller := "cli:" + id
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:00Z", Caller: caller, Kind: "decision", RequestID: "req-old", Tool: "bash", Decision: "approved"},
		{Seq: 2, At: "2026-11-10T12:00:05Z", SessionID: "old", Caller: caller, Kind: "reply", Text: "old"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "castbot-n6", "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, owner.ID, agent.ID, "vm-n6", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:01:00Z", Caller: caller, Kind: "decision", RequestID: "req-new", Tool: "bash", Decision: "approved"},
		{Seq: 2, At: "2026-11-10T12:01:05Z", SessionID: "new", Caller: caller, Kind: "reply", Text: "new"},
	}); err != nil {
		t.Fatal(err)
	}
	if cliMessages(t, s, owner.ID, agent.ID) != 4 {
		t.Fatalf("cli messages %d", cliMessages(t, s, owner.ID, agent.ID))
	}
}

func TestWebJournalSeqReusedOnNewMachine(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "reuse-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateMessage(ctx, owner.ID, thread.ID, "user", owner.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateMessage(ctx, owner.ID, thread.ID, "user", owner.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "castbot-reuse-1", "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, owner.ID, agent.ID, "vm-reuse-1", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	vm1, err := s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:00Z", SessionID: thread.ID, Caller: "control_plane", Kind: "request", Text: "first", MessageID: first.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkVMDeleted(ctx, owner.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "castbot-reuse-2", "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, owner.ID, agent.ID, "vm-reuse-2", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	vm2, err := s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm2.ID == vm1.ID {
		t.Fatalf("vm %+v %v", vm2, err)
	}
	run, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestJournal(ctx, owner.ID, agent.ID, []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:01:00Z", SessionID: thread.ID, Caller: "control_plane", Kind: "request", Text: "second", MessageID: second.ID},
		{Seq: 2, At: "2026-11-10T12:01:01Z", SessionID: thread.ID, Caller: "control_plane", Kind: "reply", Text: "done", StreamID: run.ID},
	}); err != nil {
		t.Fatal(err)
	}
	msg, _, err := s.FinishRun(ctx, owner.ID, run.ID, "done", 1)
	if err != nil {
		t.Fatal(err)
	}
	if msg.JournalSeq != 2 {
		t.Fatalf("reply seq %d", msg.JournalSeq)
	}
	var vmA, vmB string
	var seqA, seqB int
	if err := s.db.QueryRowContext(ctx, `SELECT journal_seq, agent_vm_id FROM messages WHERE id = ?`, first.ID).Scan(&seqA, &vmA); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT journal_seq, agent_vm_id FROM messages WHERE id = ?`, second.ID).Scan(&seqB, &vmB); err != nil {
		t.Fatal(err)
	}
	if seqA != 1 || seqB != 1 || vmA != vm1.ID || vmB != vm2.ID {
		t.Fatalf("first %d %s second %d %s", seqA, vmA, seqB, vmB)
	}
	var replyVM string
	if err := s.db.QueryRowContext(ctx, `SELECT agent_vm_id FROM messages WHERE id = ?`, msg.ID).Scan(&replyVM); err != nil {
		t.Fatal(err)
	}
	if replyVM != vm2.ID {
		t.Fatalf("reply machine %s", replyVM)
	}
}

func cliMessages(t *testing.T, s *Store, userID, agentID string) int {
	t.Helper()
	threads, err := s.ListThreadsByAgent(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, th := range threads {
		if th.Kind != "cli" {
			continue
		}
		msgs, err := s.ListMessages(context.Background(), userID, th.ID)
		if err != nil {
			t.Fatal(err)
		}
		n += len(msgs)
	}
	return n
}

func TestV14AddsStateGenAndRecoverSeq(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	s, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_messages_journal`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE agent_vms DROP COLUMN state_gen`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE approvals DROP COLUMN recover_seq`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE journal_entries DROP COLUMN placed`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE messages DROP COLUMN agent_vm_id`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version = 14`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	if !columnExists(t, s, "agent_vms", "state_gen") || !columnExists(t, s, "approvals", "recover_seq") || !columnExists(t, s, "journal_entries", "placed") || !columnExists(t, s, "messages", "agent_vm_id") {
		t.Fatal("v14 columns missing after reopen")
	}
	cols, err := s.indexColumns("idx_messages_journal")
	if err != nil || len(cols) != 3 || cols[2] != "agent_vm_id" {
		t.Fatalf("journal index %v %v", cols, err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT version FROM schema_migrations WHERE version = 14`).Scan(&version); err != nil || version != 14 {
		t.Fatalf("version %d %v", version, err)
	}
	var check string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity %q %v", check, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	if !columnExists(t, s, "agent_vms", "state_gen") || !columnExists(t, s, "approvals", "recover_seq") || !columnExists(t, s, "journal_entries", "placed") || !columnExists(t, s, "messages", "agent_vm_id") {
		t.Fatal("second open dropped a column")
	}
	s.Close()
}

func columnExists(t *testing.T, s *Store, table, name string) bool {
	t.Helper()
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var col, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &col, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if col == name {
			return true
		}
	}
	return rows.Err() == nil && false
}

func TestV14BackupIfPresent(t *testing.T) {
	path := os.Getenv("CASTBOT_BACKUP")
	if path == "" {
		path = filepath.Join("..", "..", "backups", "2026-11-11_0603", "castbot.db")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Skip("backup not present")
	}
	sum := sha256.Sum256(before)
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(copyPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	s, err := Open(copyPath, opt)
	if err != nil {
		t.Fatal(err)
	}
	var check string
	if err := s.db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity %q %v", check, err)
	}
	if !columnExists(t, s, "agent_vms", "state_gen") || !columnExists(t, s, "approvals", "recover_seq") || !columnExists(t, s, "journal_entries", "placed") || !columnExists(t, s, "messages", "agent_vm_id") {
		t.Fatal("backup did not gain v14 columns")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(copyPath, opt)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(after) != sum {
		t.Fatal("backup bytes changed")
	}
}
