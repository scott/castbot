package store

import (
	"context"
	"testing"
)

func TestMoveAgentSection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "move-owner@example.com", "Ada")
	other := makeUser(t, s, "move-other@example.com", "Bea")
	agents, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	writing, err := s.CreateSection(ctx, owner.ID, "Writing")
	if err != nil {
		t.Fatal(err)
	}
	ada, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: agents.ID, Name: "Ada", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	pip, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: agents.ID, Name: "Pip", Title: "Notes", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	otherAgents, err := s.SystemSection(ctx, other.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	bea, _, err := s.CreateAgent(ctx, other.ID, AgentInput{SectionID: otherAgents.ID, Name: "Bea", Title: "Other", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MoveAgentSection(ctx, other.ID, ada.ID, writing.ID); err != ErrNotFound {
		t.Fatalf("other user %v", err)
	}
	if err := s.MoveAgentSection(ctx, owner.ID, ada.ID, otherAgents.ID); err != ErrNotFound {
		t.Fatalf("other section %v", err)
	}
	if err := s.MoveAgentSection(ctx, owner.ID, bea.ID, writing.ID); err != ErrNotFound {
		t.Fatalf("other agent %v", err)
	}
	if err := s.MoveAgentSection(ctx, owner.ID, ada.ID, writing.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgent(ctx, owner.ID, ada.ID)
	if err != nil || got.SectionID != writing.ID || got.Position != 1 {
		t.Fatalf("moved %+v %v", got, err)
	}
	if err := s.MoveAgentSection(ctx, owner.ID, ada.ID, writing.ID); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetAgent(ctx, owner.ID, ada.ID)
	if err != nil || again.Position != got.Position || again.SectionID != writing.ID {
		t.Fatalf("already there %+v %v", again, err)
	}
	if err := s.MoveAgentSection(ctx, owner.ID, pip.ID, writing.ID); err != nil {
		t.Fatal(err)
	}
	movedPip, err := s.GetAgent(ctx, owner.ID, pip.ID)
	if err != nil || movedPip.SectionID != writing.ID || movedPip.Position <= again.Position {
		t.Fatalf("second %+v %v", movedPip, err)
	}
	stayed, err := s.GetAgent(ctx, other.ID, bea.ID)
	if err != nil || stayed.SectionID != otherAgents.ID {
		t.Fatalf("other agent changed %+v %v", stayed, err)
	}
	if err := s.MoveAgentSection(ctx, owner.ID, ada.ID, agents.ID); err != nil {
		t.Fatal(err)
	}
	back, err := s.GetAgent(ctx, owner.ID, ada.ID)
	if err != nil || back.SectionID != agents.ID {
		t.Fatalf("back %+v %v", back, err)
	}
	if _, err := s.GetAgent(ctx, other.ID, ada.ID); err != ErrNotFound {
		t.Fatalf("other read %v", err)
	}
}
