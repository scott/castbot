package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestInviteMemberAndSeparateQuota(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 29`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema 29 %d %v", n, err)
	}
	owner := makeUser(t, s, "owner@example.com", "Alex Rivera")
	other := makeUser(t, s, "other@example.com", "Blair Quinn")
	if _, _, err := s.CreateInvite(ctx, other.ID, "pip@example.com", "Pip"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateInvite(ctx, owner.ID, "not-an-email", "Pip"); err != ErrInvalid {
		t.Fatalf("email %v", err)
	}
	if _, _, err := s.CreateInvite(ctx, owner.ID, "other@example.com", "Blair"); err != ErrConflict {
		t.Fatalf("existing %v", err)
	}
	inv, token, err := s.CreateInvite(ctx, owner.ID, "Pip@Example.com", "Pip Lane")
	if err != nil || !strings.HasPrefix(token, "cbi_") || inv.Email != "pip@example.com" {
		t.Fatalf("invite %q %q %v", inv.Email, token, err)
	}
	if strings.Contains(mustInviteHash(t, s, inv.ID), token) {
		t.Fatal("plain token was stored")
	}
	again, token2, err := s.CreateInvite(ctx, owner.ID, "pip@example.com", "Pip Lane")
	if err != nil || again.ID != inv.ID || token2 == token {
		t.Fatalf("refresh %v %v", token2 == token, err)
	}
	if _, err := s.PeekInvite(ctx, token); err != ErrNotFound {
		t.Fatalf("old token %v", err)
	}
	peek, err := s.PeekInvite(ctx, token2)
	if err != nil || peek.Email != "pip@example.com" || peek.DisplayName != "Pip Lane" {
		t.Fatalf("peek %v %v", peek, err)
	}
	if _, err := s.ListInvites(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListInvites(ctx, other.ID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("other invites %#v %v", listed, err)
	}
	ownerRows, err := s.ListInvites(ctx, owner.ID)
	if err != nil || len(ownerRows) != 1 || ownerRows[0].ID != inv.ID {
		t.Fatalf("owner invites %#v %v", ownerRows, err)
	}
	hash, err := auth.Hash("correct-horse-1", s.argon)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.AcceptInvite(ctx, token2, hash, secret)
	if err != nil || member.Role != "member" || member.WorkspaceID != owner.WorkspaceID || member.Email != "pip@example.com" {
		t.Fatalf("accept %#v %v", member, err)
	}
	if _, err := s.AcceptInvite(ctx, token2, hash, secret); err != ErrNotFound {
		t.Fatalf("second accept %v", err)
	}
	if _, err := s.PeekInvite(ctx, token2); err != ErrNotFound {
		t.Fatalf("spent peek %v", err)
	}
	people, err := s.ListMembers(ctx, member.ID)
	if err != nil || len(people) != 2 {
		t.Fatalf("members %#v %v", people, err)
	}
	outsiders, err := s.ListMembers(ctx, other.ID)
	if err != nil || len(outsiders) != 1 || outsiders[0].ID != other.ID {
		t.Fatalf("outsider members %#v %v", outsiders, err)
	}
	if _, _, err := s.CreateInvite(ctx, member.ID, "new@example.com", "New"); err != ErrNotFound {
		t.Fatalf("member invite %v", err)
	}
	sec, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuota(ctx, owner.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "vm", "example.invalid/castbot-runtime:s13"); err != nil {
		t.Fatal(err)
	}
	used, max, err := s.Quota(ctx, member.ID)
	if err != nil || used != 0 || max != DefaultMaxAgents {
		t.Fatalf("member quota %d %d %v", used, max, err)
	}
	used, _, err = s.Quota(ctx, owner.ID)
	if err != nil || used != 1 {
		t.Fatalf("owner quota %d %v", used, err)
	}
}

func TestInviteExpiryAndRevoke(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "owner2@example.com", "Alex Rivera")
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	inv, token, err := s.CreateInvite(ctx, owner.ID, "late@example.com", "Late")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(ctx, owner.ID, inv.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(ctx, owner.ID, inv.ID); err != ErrNotFound {
		t.Fatalf("revoke again %v", err)
	}
	other := makeUser(t, s, "other2@example.com", "Blair")
	_, token, err = s.CreateInvite(ctx, owner.ID, "late@example.com", "Late")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(ctx, other.ID, inv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other revoke %v", err)
	}
	s.SetNow(func() time.Time { return now.Add(InviteWindow + time.Minute) })
	if _, err := s.PeekInvite(ctx, token); err != ErrNotFound {
		t.Fatalf("expired %v", err)
	}
}

func TestGroupAccessMentionAndRun(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "owner3@example.com", "Alex Rivera")
	other := makeUser(t, s, "other3@example.com", "Blair Quinn")
	hash, err := auth.Hash("correct-horse-1", s.argon)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateInvite(ctx, owner.ID, "pip3@example.com", "Pip Lane")
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.AcceptInvite(ctx, token, hash, secret)
	if err != nil {
		t.Fatal(err)
	}
	sec, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	atlas, dm, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	pip, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Pip", Title: "Notes", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	docs, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Docs helper", Title: "Docs", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGroup(ctx, owner.ID, "", []string{atlas.ID, pip.ID}, nil); err != ErrInvalid {
		t.Fatal("empty title")
	}
	if _, err := s.CreateGroup(ctx, owner.ID, "Standup", []string{atlas.ID}, nil); err != ErrInvalid {
		t.Fatal("one agent")
	}
	if _, err := s.CreateGroup(ctx, owner.ID, "Standup", []string{atlas.ID, other.ID}, nil); err != ErrNotFound {
		t.Fatal("foreign agent")
	}
	if _, err := s.CreateGroup(ctx, owner.ID, "Standup", []string{atlas.ID, pip.ID}, []string{other.ID}); err != ErrNotFound {
		t.Fatal("foreign member")
	}
	group, err := s.CreateGroup(ctx, owner.ID, "Standup", []string{atlas.ID, pip.ID, docs.ID}, []string{member.ID})
	if err != nil || group.Kind != "group" || group.UserID != owner.ID {
		t.Fatalf("group %#v %v", group, err)
	}
	if _, err := s.GetThread(ctx, member.ID, dm.ID); err != ErrNotFound {
		t.Fatal("member read the private dm")
	}
	if _, err := s.GetThread(ctx, other.ID, group.ID); err != ErrNotFound {
		t.Fatal("outsider read the group")
	}
	if _, err := s.GroupAccess(ctx, other.ID, group.ID); err != ErrNotFound {
		t.Fatal("outsider access")
	}
	if _, err := s.CreateGroupMessage(ctx, other.ID, group.ID, "hello"); err != ErrNotFound {
		t.Fatal("outsider post")
	}
	got, err := s.GroupAccess(ctx, member.ID, group.ID)
	if err != nil || got.ID != group.ID || got.UserID != owner.ID {
		t.Fatalf("member access %#v %v", got, err)
	}
	msg, err := s.CreateGroupMessage(ctx, member.ID, group.ID, "@Atlas and @Docs helper please look")
	if err != nil || msg.UserID != owner.ID || msg.AuthorID != member.ID {
		t.Fatalf("post %#v %v", msg, err)
	}
	agents, err := s.GroupAgents(ctx, member.ID, group.ID)
	if err != nil || len(agents) != 3 {
		t.Fatalf("agents %d %v", len(agents), err)
	}
	mentioned := MentionedAgents(agents, msg.Body)
	if len(mentioned) != 2 || mentioned[0].Name != "Atlas" || mentioned[1].Name != "Docs helper" {
		t.Fatalf("mentions %#v", namesOf(mentioned))
	}
	if got := MentionedAgents(agents, "email atlas@pip.com and @AtlasExtra"); len(got) != 0 {
		t.Fatalf("false mentions %#v", namesOf(got))
	}
	if got := MentionedAgents(agents, "@pip"); len(got) != 1 || got[0].ID != pip.ID {
		t.Fatalf("case %#v", namesOf(got))
	}
	humans, err := s.GroupHumans(ctx, member.ID, group.ID)
	if err != nil || len(humans) != 2 {
		t.Fatalf("humans %d %v", len(humans), err)
	}
	if _, err := s.GroupHumans(ctx, other.ID, group.ID); err != ErrNotFound {
		t.Fatal("outsider humans")
	}
	run, err := s.CreateRun(ctx, owner.ID, group.ID, atlas.ID)
	if err != nil || run.AgentID != atlas.ID || run.ThreadID != group.ID {
		t.Fatalf("run %#v %v", run, err)
	}
	if _, err := s.CreateRun(ctx, owner.ID, group.ID, other.ID); err != ErrNotFound {
		t.Fatal("run for a non-participant")
	}
	if _, err := s.CreateRun(ctx, other.ID, group.ID, atlas.ID); err != ErrNotFound {
		t.Fatal("outsider run")
	}
	open, err := s.GroupOpen(ctx, member.ID, group.ID)
	if err != nil || !open {
		t.Fatalf("open %v %v", open, err)
	}
	groups, err := s.ListGroups(ctx, member.ID)
	if err != nil || len(groups) != 1 || groups[0].ID != group.ID {
		t.Fatalf("list %#v %v", groups, err)
	}
	if rows, err := s.ListGroups(ctx, other.ID); err != nil || len(rows) != 0 {
		t.Fatalf("outsider groups %#v %v", rows, err)
	}
}

func namesOf(agents []Agent) []string {
	out := make([]string, len(agents))
	for i, agent := range agents {
		out[i] = agent.Name
	}
	return out
}

func mustInviteHash(t *testing.T, s *Store, id string) string {
	t.Helper()
	var hash string
	if err := s.db.QueryRow(`SELECT token_hash FROM invites WHERE id = ?`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}
