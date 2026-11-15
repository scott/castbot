package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRuntimeRollbackOffer(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, st, "rollback-a@example.com", "Ada")
	other := makeUser(t, st, "rollback-b@example.com", "Bea")
	sec, err := st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready", Model: "model-large"})
	if err != nil {
		t.Fatal(err)
	}
	const oldImage = "example.invalid/castbot-runtime:7b5e414"
	const nextImage = "example.invalid/castbot-runtime:ff6d7ea"
	if _, err := st.BeginVM(ctx, owner.ID, agent.ID, "castbot-old", oldImage); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishVM(ctx, owner.ID, agent.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 28`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 28 count %d %v", n, err)
	}

	same, _, err := st.BeginReplacement(ctx, owner.ID, agent.ID, "web:ada@example.com", oldImage, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishReplacement(ctx, owner.ID, same.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActiveRollback(ctx, owner.ID, agent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("same image offer %v", err)
	}

	row, _, err := st.BeginReplacement(ctx, owner.ID, agent.ID, "web:ada@example.com", nextImage, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishReplacement(ctx, owner.ID, row.ID, false); err != nil {
		t.Fatal(err)
	}
	got, err := st.ActiveRollback(ctx, owner.ID, agent.ID)
	if err != nil || got.Image != oldImage {
		t.Fatalf("offer %+v %v", got, err)
	}
	if _, err := st.ActiveRollback(ctx, other.ID, agent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user %v", err)
	}
	if err := st.NoteRuntimeRollback(ctx, other.ID, agent.ID, "nope", oldImage); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other note %v", err)
	}

	st.SetNow(func() time.Time { return time.Now().UTC().Add(RollbackWindow + time.Hour) })
	if _, err := st.ActiveRollback(ctx, owner.ID, agent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired offer %v", err)
	}
}

func TestNoCopyMoveRetiresRollback(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, st, "rollback-nocopy@example.com", "Ada")
	sec, err := st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready", Model: "model-large"})
	if err != nil {
		t.Fatal(err)
	}
	const oldImage = "example.invalid/castbot-runtime:7b5e414"
	const nextImage = "example.invalid/castbot-runtime:ff6d7ea"
	if _, err := st.BeginVM(ctx, owner.ID, agent.ID, "castbot-old", nextImage); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishVM(ctx, owner.ID, agent.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.NoteRuntimeRollback(ctx, owner.ID, agent.ID, "offer", oldImage); err != nil {
		t.Fatal(err)
	}
	row, _, err := st.BeginReplacement(ctx, owner.ID, agent.ID, "cli", "example.invalid/castbot-runtime:aaaaaaa", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishReplacement(ctx, owner.ID, row.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActiveRollback(ctx, owner.ID, agent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no-copy kept an offer %v", err)
	}
}
