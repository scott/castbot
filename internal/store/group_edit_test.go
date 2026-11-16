package store

import (
	"context"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestGroupPinPlaceFoldEditDelete(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 31`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema 31 %d %v", n, err)
	}
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	owner := makeUser(t, s, "owner-105@example.com", "Alex Rivera")
	other := makeUser(t, s, "other-105@example.com", "Blair Quinn")
	hash, err := auth.Hash("correct-horse-1", s.argon)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateInvite(ctx, owner.ID, "pip-105@example.com", "Pip Lane")
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
	atlas, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	pip, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Pip", Title: "Notes", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	moss, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Moss", Title: "Research", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.StartGroup(ctx, owner.ID)
	if err != nil || started.Title != "New group" {
		t.Fatalf("start %q %v", started.Title, err)
	}
	if agents, err := s.GroupAgents(ctx, owner.ID, started.ID); err != nil || len(agents) != 0 {
		t.Fatalf("start agents %#v %v", agents, err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, started.ID, "Draft", []string{atlas.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, started.ID, "Draft", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, started.ID, "Notes", []string{atlas.ID, pip.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, started.ID, "Notes", []string{atlas.ID}, nil); err != ErrInvalid {
		t.Fatalf("drop below two %v", err)
	}
	if err := s.SetGroupTitle(ctx, member.ID, started.ID, "Nope"); err != ErrNotFound {
		t.Fatalf("member title %v", err)
	}
	if err := s.SetGroupTitle(ctx, owner.ID, started.ID, "  "); err != ErrInvalid {
		t.Fatalf("blank title %v", err)
	}
	if err := s.SetGroupTitle(ctx, owner.ID, started.ID, "Planning"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetThread(ctx, owner.ID, started.ID); err != nil || got.Title != "Planning" {
		t.Fatalf("renamed %q %v", got.Title, err)
	}
	if err := s.DeleteGroup(ctx, owner.ID, started.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	older, err := s.CreateGroup(ctx, owner.ID, "Older", []string{atlas.ID, pip.ID}, []string{member.ID})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	newer, err := s.CreateGroup(ctx, owner.ID, "Newer", []string{atlas.ID, pip.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cards, err := s.ListGroupCards(ctx, owner.ID)
	if err != nil || len(cards) != 2 || cards[0].ID != newer.ID || cards[1].ID != older.ID || cards[0].Pinned || cards[0].SectionID != "" {
		t.Fatalf("newest first %#v %v", cards, err)
	}
	now = now.Add(time.Second)
	if err := s.SetGroupPin(ctx, owner.ID, older.ID, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := s.SetGroupPin(ctx, owner.ID, newer.ID, true); err != nil {
		t.Fatal(err)
	}
	cards, err = s.ListGroupCards(ctx, owner.ID)
	if err != nil || cards[0].ID != newer.ID || cards[1].ID != older.ID || !cards[0].Pinned || !cards[1].Pinned {
		t.Fatalf("recent pin first %#v %v", cards, err)
	}
	memberCards, err := s.ListGroupCards(ctx, member.ID)
	if err != nil || len(memberCards) != 1 || memberCards[0].ID != older.ID || memberCards[0].Pinned {
		t.Fatalf("member pin %#v %v", memberCards, err)
	}
	if err := s.SetGroupPin(ctx, member.ID, older.ID, true); err != nil {
		t.Fatal(err)
	}
	cards, err = s.ListGroupCards(ctx, owner.ID)
	if err != nil || !cards[0].Pinned || cards[0].ID != newer.ID {
		t.Fatal("owner pin changed")
	}
	if err := s.SetGroupPin(ctx, other.ID, older.ID, true); err != ErrNotFound {
		t.Fatalf("other pin %v", err)
	}

	research, err := s.CreateSection(ctx, owner.ID, "Research")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PlaceGroup(ctx, owner.ID, older.ID, research.ID); err != nil {
		t.Fatal(err)
	}
	cards, err = s.ListGroupCards(ctx, owner.ID)
	if err != nil || cards[0].ID != newer.ID || cards[0].SectionID != "" || cards[1].ID != older.ID || cards[1].SectionID != research.ID || !cards[1].Pinned {
		t.Fatalf("placed %#v %v", cards, err)
	}
	memberCards, err = s.ListGroupCards(ctx, member.ID)
	if err != nil || memberCards[0].SectionID != "" {
		t.Fatal("member placement changed")
	}
	if err := s.PlaceGroup(ctx, owner.ID, older.ID, "groups"); err != nil {
		t.Fatal(err)
	}
	cards, err = s.ListGroupCards(ctx, owner.ID)
	if err != nil || cards[1].SectionID != "" || !cards[1].Pinned {
		t.Fatal("pin was not kept")
	}
	if err := s.PlaceGroup(ctx, owner.ID, older.ID, research.ID); err != nil {
		t.Fatal(err)
	}
	memberResearch, err := s.CreateSection(ctx, member.ID, "Research")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PlaceGroup(ctx, owner.ID, older.ID, memberResearch.ID); err != ErrNotFound {
		t.Fatalf("other section %v", err)
	}
	cards, err = s.ListGroupCards(ctx, owner.ID)
	if err != nil || cards[1].SectionID != research.ID {
		t.Fatal("bad section moved the group")
	}
	if err := s.DeleteSection(ctx, owner.ID, research.ID); err != nil {
		t.Fatal(err)
	}
	cards, err = s.ListGroupCards(ctx, owner.ID)
	if err != nil || cards[1].ID != older.ID || cards[1].SectionID != "" {
		t.Fatalf("section delete %#v %v", cards, err)
	}
	if _, err := s.GetThread(ctx, owner.ID, older.ID); err != nil {
		t.Fatal(err)
	}
	pinned, err := s.SystemSection(ctx, owner.ID, "pinned")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSection(ctx, owner.ID, pinned.ID); err != ErrProtected {
		t.Fatalf("pinned %v", err)
	}
	if err := s.DeleteSection(ctx, owner.ID, sec.ID); err != ErrProtected {
		t.Fatalf("agents section %v", err)
	}
	busy, err := s.CreateSection(ctx, owner.ID, "Busy")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: busy.ID, Name: "Nia", Title: "Notes", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSection(ctx, owner.ID, busy.ID); err != ErrConflict {
		t.Fatalf("section with an agent %v", err)
	}

	folded, err := s.ToggleFold(ctx, owner.ID, sec.ID)
	if err != nil || !folded {
		t.Fatalf("fold %v %v", folded, err)
	}
	again, err := s.ToggleFold(ctx, owner.ID, sec.ID)
	if err != nil || again {
		t.Fatalf("expand %v %v", again, err)
	}
	if _, err := s.ToggleFold(ctx, owner.ID, "groups"); err != nil {
		t.Fatal(err)
	}
	ownerFolds, err := s.FoldedSections(ctx, owner.ID)
	if err != nil || !ownerFolds["groups"] || ownerFolds[sec.ID] {
		t.Fatalf("folds %#v %v", ownerFolds, err)
	}
	if _, err := s.ToggleFold(ctx, member.ID, sec.ID); err != ErrNotFound {
		t.Fatalf("member fold %v", err)
	}
	memberFolds, err := s.FoldedSections(ctx, member.ID)
	if err != nil || len(memberFolds) != 0 {
		t.Fatalf("member folds %#v", memberFolds)
	}
	if ownerFolds, err = s.FoldedSections(ctx, owner.ID); err != nil || !ownerFolds["groups"] {
		t.Fatal("owner fold changed")
	}

	if err := s.SetGroupPin(ctx, member.ID, older.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroup(ctx, member.ID, older.ID, "Nope", []string{atlas.ID, pip.ID}, nil); err != ErrNotFound {
		t.Fatalf("member edit %v", err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, older.ID, "   ", []string{atlas.ID, pip.ID}, nil); err != ErrInvalid {
		t.Fatalf("empty title %v", err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, older.ID, "Planning", []string{atlas.ID}, []string{member.ID}); err != ErrInvalid {
		t.Fatalf("one agent %v", err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, older.ID, "Planning", []string{atlas.ID, pip.ID}, []string{other.ID}); err != ErrNotFound {
		t.Fatalf("other person %v", err)
	}
	outsider, _, err := s.CreateAgent(ctx, other.ID, AgentInput{SectionID: mustSection(t, s, other.ID), Name: "Out", Title: "X", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, older.ID, "Planning", []string{atlas.ID, outsider.ID}, nil); err != ErrNotFound {
		t.Fatalf("other agent %v", err)
	}
	got, err := s.GetThread(ctx, owner.ID, older.ID)
	if err != nil || got.Title != "Older" {
		t.Fatalf("title changed %q", got.Title)
	}
	open, err := s.CreateRun(ctx, owner.ID, older.ID, atlas.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, older.ID, "Planning", []string{atlas.ID, pip.ID}, []string{member.ID}); err != ErrConflict {
		t.Fatalf("busy edit %v", err)
	}
	if err := s.DeleteGroup(ctx, owner.ID, older.ID); err != ErrConflict {
		t.Fatalf("busy delete %v", err)
	}
	if _, err := s.FailRun(ctx, owner.ID, open.ID, "stopped", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGroup(ctx, owner.ID, older.ID, "Planning", []string{atlas.ID, moss.ID}, nil); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetThread(ctx, owner.ID, older.ID)
	if err != nil || got.Title != "Planning" {
		t.Fatal(got.Title)
	}
	if _, err := s.GroupAccess(ctx, member.ID, older.ID); err != ErrNotFound {
		t.Fatal("removed member still opens the group")
	}
	memberCards, err = s.ListGroupCards(ctx, member.ID)
	if err != nil || len(memberCards) != 0 {
		t.Fatalf("removed pin %#v", memberCards)
	}
	agents, err := s.GroupAgents(ctx, owner.ID, older.ID)
	if err != nil || len(agents) != 2 || agents[0].ID == pip.ID || agents[1].ID == pip.ID {
		t.Fatalf("agents %#v", agents)
	}
	if _, err := s.GetAgent(ctx, owner.ID, pip.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGroup(ctx, member.ID, older.ID); err != ErrNotFound {
		t.Fatalf("member delete %v", err)
	}
	if err := s.DeleteGroup(ctx, other.ID, older.ID); err != ErrNotFound {
		t.Fatalf("other delete %v", err)
	}
	if err := s.DeleteGroup(ctx, owner.ID, older.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GroupAccess(ctx, owner.ID, older.ID); err != ErrNotFound {
		t.Fatal("deleted group still opens")
	}
	if _, err := s.GetAgent(ctx, owner.ID, atlas.ID); err != nil {
		t.Fatal(err)
	}
	cards, err = s.ListGroupCards(ctx, owner.ID)
	if err != nil || len(cards) != 1 || cards[0].ID != newer.ID {
		t.Fatalf("after delete %#v", cards)
	}

	ask, ok := ParseGroupAsk("Rename this group to Planning")
	if !ok || ask.Kind != "rename" || ask.Title != "Planning" {
		t.Fatalf("rename %#v", ask)
	}
	ask, ok = ParseGroupAsk("  add moss to this group ")
	if !ok || ask.Kind != "add" || ask.Name != "moss" {
		t.Fatalf("add %#v", ask)
	}
	ask, ok = ParseGroupAsk("Remove Pip Lane from this group")
	if !ok || ask.Kind != "remove" || ask.Name != "Pip Lane" {
		t.Fatalf("remove %#v", ask)
	}
	ask, ok = ParseGroupAsk("Put this group in Research")
	if !ok || ask.Kind != "place" || ask.Section != "Research" {
		t.Fatalf("place %#v", ask)
	}
	ask, ok = ParseGroupAsk("Delete this group")
	if !ok || ask.Kind != "delete" {
		t.Fatalf("delete %#v", ask)
	}
	ask, ok = ParseGroupAsk("Rename this group to Planning and add Moss to this group")
	if !ok || ask.Kind != "many" {
		t.Fatalf("two %#v %v", ask, ok)
	}
	if _, ok = ParseGroupAsk("hello team"); ok {
		t.Fatal("ordinary post parsed as an ask")
	}
	if _, ok = ParseGroupAsk("Rename this group to"); ok {
		t.Fatal("empty rename parsed")
	}
}

func mustSection(t *testing.T, s *Store, userID string) string {
	t.Helper()
	sec, err := s.SystemSection(context.Background(), userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	return sec.ID
}
