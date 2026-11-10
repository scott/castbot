package store

import (
	"context"
	"errors"
	"testing"
)

func TestInstanceIDIsStablePerDatabase(t *testing.T) {
	ctx := context.Background()
	a := testStore(t)
	b := testStore(t)
	first, err := a.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.InstanceID(ctx)
	if err != nil || again != first {
		t.Fatalf("stable %q %q %v", first, again, err)
	}
	other, err := b.InstanceID(ctx)
	if err != nil || other == first || other == "" {
		t.Fatalf("other %q %v", other, err)
	}
}

func TestQuotaAndVMScope(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := makeUser(t, s, "a@example.com", "User A")
	b := makeUser(t, s, "b@example.com", "User B")
	used, max, err := s.Quota(ctx, a.ID)
	if err != nil || used != 0 || max != DefaultMaxAgents {
		t.Fatalf("quota %d %d %v", used, max, err)
	}
	sec, err := s.SystemSection(ctx, a.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetQuota(ctx, a.ID, 1); err != nil {
		t.Fatal(err)
	}
	first, _, err := s.CreateAgent(ctx, a.ID, AgentInput{SectionID: sec.ID, Name: "One", Title: "A"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateAgent(ctx, a.ID, AgentInput{SectionID: sec.ID, Name: "Two", Title: "B"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, a.ID, first.ID, "castbot-"+first.ID, "example.invalid/castbot-runtime:s04"); err != nil {
		t.Fatal(err)
	}
	_, err = s.BeginVM(ctx, a.ID, second.ID, "castbot-"+second.ID, "example.invalid/castbot-runtime:s04")
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("quota %v", err)
	}
	if _, err := s.LiveVM(ctx, b.ID, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b saw a: %v", err)
	}
	if _, err := s.BeginVM(ctx, b.ID, first.ID, "nope", "example.invalid/castbot-runtime:s04"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b create %v", err)
	}
	if err := s.ReleaseVM(ctx, a.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, a.ID, second.ID, "castbot-"+second.ID, "example.invalid/castbot-runtime:s04"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, a.ID, second.ID, "vm-real", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	vm, err := s.LiveVM(ctx, a.ID, second.ID)
	if err != nil || vm.State != "running" || vm.Token != "" || vm.Endpoint != "https://vm.example" {
		t.Fatalf("%+v %v", vm, err)
	}
	agent, err := s.GetAgent(ctx, a.ID, second.ID)
	if err != nil || agent.Status != "ready" {
		t.Fatalf("status %s %v", agent.Status, err)
	}
	if err := s.ApplyVMState(ctx, a.ID, second.ID, "paused", "", ""); err != nil {
		t.Fatal(err)
	}
	agent, err = s.GetAgent(ctx, a.ID, second.ID)
	if err != nil || agent.Status != "paused" {
		t.Fatal(agent.Status, err)
	}
	if _, err := s.LiveVM(ctx, b.ID, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
