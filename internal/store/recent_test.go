package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPreferredAgentUse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 30`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema 30 %d %v", n, err)
	}
	owner := makeUser(t, s, "owner-recent@example.com", "Alex Rivera")
	other := makeUser(t, s, "other-recent@example.com", "Blair Quinn")
	if _, err := s.PreferredAgent(ctx, owner.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty %v", err)
	}
	sec, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	same := time.Date(2026, 11, 2, 15, 4, 5, 0, time.UTC)
	ada, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Ada", Title: "Helper", Status: "ready", CreatedAt: same})
	if err != nil {
		t.Fatal(err)
	}
	bea, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Bea", Title: "Helper", Status: "ready", CreatedAt: same})
	if err != nil {
		t.Fatal(err)
	}
	want := ada
	if bea.ID > ada.ID {
		want = bea
	}
	got, err := s.PreferredAgent(ctx, owner.ID)
	if err != nil || got.ID != want.ID {
		t.Fatalf("unused tie %s want %s (%v)", got.ID, want.ID, err)
	}
	older, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{
		SectionID: sec.ID, Name: "Cara", Title: "Helper", Status: "ready",
		CreatedAt: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	newer, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{
		SectionID: sec.ID, Name: "Dora", Title: "Helper", Status: "ready",
		CreatedAt: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.PreferredAgent(ctx, owner.ID)
	if err != nil || got.ID != newer.ID {
		t.Fatalf("unused newest %s want %s", got.ID, newer.ID)
	}
	s.SetNow(func() time.Time { return time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC) })
	if err := s.TouchAgentUse(ctx, owner.ID, older.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.PreferredAgent(ctx, owner.ID)
	if err != nil || got.ID != older.ID {
		t.Fatalf("used older %s", got.ID)
	}
	if err := s.TouchAgentUse(ctx, other.ID, older.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other touch %v", err)
	}
	got, err = s.PreferredAgent(ctx, owner.ID)
	if err != nil || got.ID != older.ID {
		t.Fatalf("other user changed the owner %s", got.ID)
	}
	otherSec, err := s.SystemSection(ctx, other.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	theirs, _, err := s.CreateAgent(ctx, other.ID, AgentInput{SectionID: otherSec.ID, Name: "Eve", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.PreferredAgent(ctx, other.ID)
	if err != nil || got.ID != theirs.ID {
		t.Fatalf("other preferred %s", got.ID)
	}
	s.SetNow(func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) })
	if err := s.TouchAgentUse(ctx, owner.ID, newer.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAgent(ctx, owner.ID, newer.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.PreferredAgent(ctx, owner.ID)
	if err != nil || got.ID != older.ID {
		t.Fatalf("after delete %s want %s", got.ID, older.ID)
	}
	if err := s.TouchAgentUse(ctx, owner.ID, newer.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted touch %v", err)
	}
	s.SetNow(func() time.Time { return time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC) })
	if err := s.TouchAgentUse(ctx, owner.ID, older.ID); err != nil {
		t.Fatal(err)
	}
}
