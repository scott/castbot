package store

import (
	"context"
	"testing"
)

func TestRunEventsAreScopedAndMonotonic(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := makeUser(t, s, "a@example.com", "User A")
	b := makeUser(t, s, "b@example.com", "User B")
	sec, err := s.CreateSection(ctx, a.ID, "Private")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, a.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.CreateMessage(ctx, a.ID, thread.ID, "user", a.ID, "hello there")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctx, b.ID, thread.ID, agent.ID); err != ErrNotFound {
		t.Fatalf("other user create %v", err)
	}
	run, err := s.CreateRun(ctx, a.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageRun(ctx, a.ID, msg.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	first, err := s.AppendRunEvent(ctx, a.ID, run.ID, "text", `{"text":"aa"}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AppendRunEvent(ctx, a.ID, run.ID, "text", `{"text":" bb"}`, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("seq %d %d", first.Seq, second.Seq)
	}
	if _, err := s.AppendRunEvent(ctx, b.ID, run.ID, "text", `{"text":"no"}`, 3); err != ErrNotFound {
		t.Fatalf("other append %v", err)
	}
	saved, ev, err := s.FinishRun(ctx, a.ID, run.ID, "aa bb", 3)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Body != "aa bb" || ev.Kind != "done" || ev.Seq != 3 {
		t.Fatalf("finish %#v %#v", saved, ev)
	}
	if _, _, err := s.FinishRun(ctx, a.ID, run.ID, "aa bb", 4); err == nil {
		t.Fatal("second finish")
	}
	got, err := s.GetRun(ctx, a.ID, run.ID)
	if err != nil || got.Status != "done" || got.StreamOffset != 3 {
		t.Fatalf("run %#v %v", got, err)
	}
	events, err := s.ListRunEvents(ctx, a.ID, run.ID, 0)
	if err != nil || len(events) != 3 {
		t.Fatalf("events %d %v", len(events), err)
	}
	for i, ev := range events {
		if ev.Seq != i+1 {
			t.Fatalf("seq %d", ev.Seq)
		}
	}
	if _, err := s.ListRunEvents(ctx, a.ID, run.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteThread(ctx, a.ID, thread.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(ctx, a.ID, run.ID); err != ErrNotFound {
		t.Fatalf("deleted run %v", err)
	}
}
