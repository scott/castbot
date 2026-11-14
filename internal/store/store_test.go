package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"), Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWALAndOwnerColumns(t *testing.T) {
	s := testStore(t)
	mode, err := s.JournalMode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal %s", mode)
	}
	tables := []string{"credentials", "sessions", "sections", "agents", "threads", "messages", "login_challenges", "trusted_devices", "runs", "agent_bundles", "agent_tokens", "secret_fixtures", "secrets", "agent_secret_grants", "agent_vms", "user_quotas", "agent_avatars", "import_drafts", "cli_tokens", "journal_entries", "turn_journal_cursor", "approvals", "audit_log", "vm_replacements", "vm_replacement_tokens", "create_drafts", "connection_grants", "secret_requests", "connect_approvals", "approval_uses", "connection_settings", "connection_policies"}
	for _, table := range tables {
		rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		cols := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull int
			var dflt any
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			cols[name] = true
		}
		rows.Close()
		if !cols["user_id"] || !cols["workspace_id"] {
			t.Fatalf("%s columns %#v", table, cols)
		}
	}
}

func TestScopedReads(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := makeUser(t, s, "a@example.com", "User A")
	b := makeUser(t, s, "b@example.com", "User B")
	sec, err := s.CreateSection(ctx, a.ID, "Private")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, a.ID, AgentInput{SectionID: sec.ID, Name: "PrivateAtlas", Title: "Secret", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.CreateMessage(ctx, a.ID, thread.ID, "user", a.ID, "secret body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSection(ctx, b.ID, sec.ID); err != ErrNotFound {
		t.Fatalf("section %v", err)
	}
	if _, err := s.ListAgentsInSection(ctx, b.ID, sec.ID); err != ErrNotFound {
		t.Fatalf("list agents %v", err)
	}
	if _, err := s.GetAgent(ctx, b.ID, agent.ID); err != ErrNotFound {
		t.Fatalf("agent %v", err)
	}
	if _, err := s.ListThreadsByAgent(ctx, b.ID, agent.ID); err != ErrNotFound {
		t.Fatalf("threads %v", err)
	}
	if _, err := s.GetThread(ctx, b.ID, thread.ID); err != ErrNotFound {
		t.Fatalf("thread %v", err)
	}
	if _, err := s.ListMessages(ctx, b.ID, thread.ID); err != ErrNotFound {
		t.Fatalf("messages %v", err)
	}
	if _, err := s.GetMessage(ctx, b.ID, msg.ID); err != ErrNotFound {
		t.Fatalf("message %v", err)
	}
	run, err := s.CreateRun(ctx, a.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunEvent(ctx, a.ID, run.ID, "text", `{"text":"secret"}`, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(ctx, b.ID, run.ID); err != ErrNotFound {
		t.Fatalf("run %v", err)
	}
	if _, err := s.ListRunEvents(ctx, b.ID, run.ID, 0); err != ErrNotFound {
		t.Fatalf("events %v", err)
	}
	if _, err := s.ListThreadRuns(ctx, b.ID, thread.ID); err != ErrNotFound {
		t.Fatalf("thread runs %v", err)
	}
	own, err := s.ListAgents(ctx, a.ID)
	if err != nil || len(own) != 1 {
		t.Fatalf("owner list %v %d", err, len(own))
	}
	other, err := s.ListAgents(ctx, b.ID)
	if err != nil || len(other) != 0 {
		t.Fatalf("other list %v %d", err, len(other))
	}
}

func TestLockout(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cur := now
	s.SetNow(func() time.Time { return cur })
	hash, err := auth.Hash("right-password-1", s.argon)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, NewUser{Email: "lock@example.com", DisplayName: "Lock", Workspace: "Personal", PasswordHash: hash, TOTPSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	ip := "203.0.113.10"
	var last Attempt
	for i := 1; i <= 5; i++ {
		last, err = s.CheckPassword(ctx, "lock@example.com", "wrong-password-1", ip)
		if err != nil {
			t.Fatal(err)
		}
	}
	if last.Status != "locked" {
		t.Fatalf("status %s tries %d", last.Status, last.TriesLeft)
	}
	if !last.LockedUntil.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("until %s", last.LockedUntil)
	}
	ok, err := s.CheckPassword(ctx, user.Email, "right-password-1", ip)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Status != "locked" {
		t.Fatalf("correct password during lock: %s", ok.Status)
	}
	unknown, err := s.CheckPassword(ctx, "nobody@example.com", "wrong-password-1", "203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Status != "fail" || unknown.User.ID != "" {
		t.Fatalf("unknown %#v", unknown)
	}
	cur = now.Add(15*time.Minute + time.Second)
	again, err := s.CheckPassword(ctx, user.Email, "right-password-1", ip)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != "ok" {
		t.Fatalf("after window %s", again.Status)
	}
}

func TestTOTPReplay(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 1, 1, 0, 0, time.UTC)
	cur := now
	s.SetNow(func() time.Time { return cur })
	hash, err := auth.Hash("right-password-1", s.argon)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(ctx, NewUser{Email: "replay@example.com", DisplayName: "Replay", Workspace: "Personal", PasswordHash: hash, TOTPSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	code, err := auth.TOTPCode(secret, cur)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := s.BeginChallenge(ctx, user, false)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := s.CheckCode(ctx, ch.ID, code, "", "203.0.113.10")
	if err != nil || ok.Status != "ok" {
		t.Fatalf("first %s %v", ok.Status, err)
	}
	ch, err = s.BeginChallenge(ctx, user, false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CheckCode(ctx, ch.ID, code, "", "203.0.113.10")
	if err != nil || again.Status != "fail" {
		t.Fatalf("replay %s %v", again.Status, err)
	}
	older, err := auth.TOTPCode(secret, cur.Add(-30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ch, err = s.BeginChallenge(ctx, user, false)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.CheckCode(ctx, ch.ID, older, "", "203.0.113.10")
	if err != nil || old.Status != "fail" {
		t.Fatalf("older step %s %v", old.Status, err)
	}
	cur = now.Add(30 * time.Second)
	next, err := auth.TOTPCode(secret, cur)
	if err != nil {
		t.Fatal(err)
	}
	ch, err = s.BeginChallenge(ctx, user, false)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := s.CheckCode(ctx, ch.ID, next, "", "203.0.113.10")
	if err != nil || fresh.Status != "ok" {
		t.Fatalf("next step %s %v", fresh.Status, err)
	}
}

func TestFailureHints(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	makeUser(t, s, "hint@example.com", "Hint")
	ip := "203.0.113.20"
	want := []int{4, 3, 2, 1}
	for i, left := range want {
		att, err := s.CheckPassword(ctx, "hint@example.com", "wrong-password-1", ip)
		if err != nil {
			t.Fatal(err)
		}
		if att.Status != "fail" || att.TriesLeft != left {
			t.Fatalf("try %d status %s left %d", i+1, att.Status, att.TriesLeft)
		}
	}
}

func TestClearPendingKeepsANewerVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "pending@example.com", "Pending")
	sec, err := s.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, user.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPendingBundle(ctx, user.ID, agent.ID, "version-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPendingBundle(ctx, user.ID, agent.ID, "version-b"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearPendingBundle(ctx, user.ID, agent.ID, "version-a"); err != nil {
		t.Fatal(err)
	}
	got, err := s.PendingBundle(ctx, user.ID, agent.ID)
	if err != nil || got != "version-b" {
		t.Fatalf("pending %q %v", got, err)
	}
	if err := s.ClearPendingBundle(ctx, user.ID, agent.ID, "version-b"); err != nil {
		t.Fatal(err)
	}
	got, err = s.PendingBundle(ctx, user.ID, agent.ID)
	if err != nil || got != "" {
		t.Fatalf("cleared %q %v", got, err)
	}
}

func TestImportDraftIsScoped(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := makeUser(t, s, "draft-a@example.com", "A")
	b := makeUser(t, s, "draft-b@example.com", "B")
	sec, err := s.SystemSection(ctx, a.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, a.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.PutImportDraft(ctx, a.ID, agent.ID, "skills:\n  - review\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportDraft(ctx, b.ID, agent.ID, id); err != ErrNotFound {
		t.Fatalf("other user %v", err)
	}
	if _, err := s.ImportDraft(ctx, a.ID, "", id); err != ErrNotFound {
		t.Fatalf("other agent %v", err)
	}
	got, err := s.ImportDraft(ctx, a.ID, agent.ID, id)
	if err != nil || !strings.Contains(got, "review") {
		t.Fatalf("%q %v", got, err)
	}
}

func makeUser(t *testing.T, s *Store, email, name string) User {
	t.Helper()
	hash, err := auth.Hash("right-password-1", s.argon)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(context.Background(), NewUser{Email: email, DisplayName: name, Workspace: "Personal", PasswordHash: hash, TOTPSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return u
}
