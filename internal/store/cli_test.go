package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCLITokenIsHashOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "cli-owner@example.com", "Owner")
	other := makeUser(t, s, "cli-other@example.com", "Other")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	id, plain, err := s.IssueCLIToken(ctx, owner.ID, agent.ID)
	if err != nil || plain == "" || id == "" {
		t.Fatal(err)
	}
	var hash, prefix string
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash, prefix FROM cli_tokens WHERE id = ? AND user_id = ?`, id, owner.ID).Scan(&hash, &prefix); err != nil {
		t.Fatal(err)
	}
	if hash == plain || hash == "" || !strings.HasPrefix(plain, prefix) || len(prefix) >= len(plain) {
		t.Fatal("token stored in the clear")
	}
	blob := hash + " " + prefix
	if strings.Contains(blob, plain) {
		t.Fatal("token stored in the clear")
	}
	if _, _, err := s.IssueCLIToken(ctx, other.ID, agent.ID); err != ErrNotFound {
		t.Fatalf("other issue %v", err)
	}
	if _, err := s.ListCLITokens(ctx, other.ID, agent.ID); err != ErrNotFound {
		t.Fatalf("other list %v", err)
	}
	if err := s.RevokeCLIToken(ctx, other.ID, agent.ID, id); err != ErrNotFound {
		t.Fatalf("other revoke %v", err)
	}
	if err := s.RevokeCLIToken(ctx, owner.ID, agent.ID, id); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListCLITokens(ctx, owner.ID, agent.ID)
	if err != nil || len(listed) != 1 || listed[0].RevokedAt == "" || listed[0].Prefix != prefix {
		t.Fatalf("%+v %v", listed, err)
	}
	if strings.Contains(listed[0].Prefix, plain[12:]) {
		t.Fatal("list returned the raw token")
	}
}

func TestJournalIngestIsIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "journal-owner@example.com", "Owner")
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
	entries := []JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:00Z", SessionID: "sess-1", Caller: "cli:" + id, Kind: "request", Text: "one"},
		{Seq: 2, At: "2026-11-10T12:00:01Z", SessionID: "sess-1", Caller: "cli:" + id, Kind: "reply", Text: "echo one"},
		{Seq: 3, At: "2026-11-10T12:00:02Z", SessionID: "sess-1", Caller: "cli:" + id, Kind: "permission", Tool: "bash"},
		{Seq: 4, At: "2026-11-10T12:00:03Z", SessionID: "sess-1", Caller: "cli:" + id, Kind: "decision", Tool: "bash", Decision: "approved"},
	}
	last, err := s.IngestJournal(ctx, owner.ID, agent.ID, entries)
	if err != nil || last != 4 {
		t.Fatalf("last %d %v", last, err)
	}
	again, err := s.IngestJournal(ctx, owner.ID, agent.ID, entries)
	if err != nil || again != 4 {
		t.Fatalf("replay %d %v", again, err)
	}
	threads, err := s.ListThreadsByAgent(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var thread Thread
	for _, th := range threads {
		if th.Kind == "cli" && th.CastSessionID == "sess-1" && th.Title == "Cast CLI" {
			thread = th
		}
	}
	if thread.ID == "" {
		t.Fatal("missing cli thread")
	}
	msgs, err := s.ListMessages(ctx, owner.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("messages %d", len(msgs))
	}
	if msgs[2].AuthorKind != "system" || !strings.Contains(msgs[2].Body, "bash") || !strings.Contains(msgs[2].Body, "Cast CLI") {
		t.Fatalf("permission %s", msgs[2].Body)
	}
	if !strings.Contains(msgs[3].Body, "approved") {
		t.Fatalf("decision %s", msgs[3].Body)
	}
	listed, err := s.ListCLITokens(ctx, owner.ID, agent.ID)
	if err != nil || listed[0].LastUsedAt == "" {
		t.Fatal("last used was not recorded")
	}
}

func TestSweepExpiredDrafts(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "draft-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	id, err := s.PutImportDraft(ctx, owner.ID, agent.ID, "name: Atlas\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportDraft(ctx, owner.ID, agent.ID, id); err != nil {
		t.Fatal(err)
	}
	s.SetNow(func() time.Time { return now.Add(2 * time.Hour) })
	n, err := s.SweepExpiredDrafts(ctx)
	if err != nil || n != 1 {
		t.Fatalf("swept %d %v", n, err)
	}
	if _, err := s.ImportDraft(ctx, owner.ID, agent.ID, id); err != ErrNotFound {
		t.Fatalf("draft remained %v", err)
	}
}
