package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestVMEventsSurviveRestartAndDoNotOverwriteRunning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	st, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := testUser(t, st)
	agent := testAgent(t, st, user.ID)
	if _, err := st.BeginVM(ctx, user.ID, agent.ID, "castbot-test", "example.invalid/runtime"); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return when })
	if err := st.FinishVM(ctx, user.ID, agent.ID, "vm-1", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.AddVMEvent(ctx, user.ID, agent.ID, EventRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	vm, err := st.LiveVM(ctx, user.ID, agent.ID)
	if err != nil || vm.CreatedRunning == "" {
		t.Fatalf("stamp %+v %v", vm, err)
	}
	first := vm.CreatedRunning
	st.SetNow(func() time.Time { return when.Add(time.Hour) })
	if err := st.ApplyVMState(ctx, user.ID, agent.ID, "running", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	vm, err = st.LiveVM(ctx, user.ID, agent.ID)
	if err != nil || vm.CreatedRunning != first {
		t.Fatalf("overwritten %q %q", first, vm.CreatedRunning)
	}
	st.Close()

	st, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	events, err := st.ListVMEvents(ctx, user.ID, agent.ID)
	if err != nil || len(events) != 1 || events[0].Kind != EventRunning || events[0].DurationMS != 1000 {
		t.Fatalf("%v %+v", err, events)
	}
	vm, err = st.LiveVM(ctx, user.ID, agent.ID)
	if err != nil || vm.CreatedRunning != first {
		t.Fatalf("reopen %q %v", vm.CreatedRunning, err)
	}
	other := testUser(t, st)
	if _, err := st.ListVMEvents(ctx, other.ID, agent.ID); err != ErrNotFound {
		t.Fatalf("other user %v", err)
	}
}

func testUser(t *testing.T, st *Store) User {
	t.Helper()
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.Hash("correct-horse-1", st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(context.Background(), NewUser{
		Email: "vm-" + secret[:6] + "@example.com", DisplayName: "Owner", Workspace: "Personal",
		PasswordHash: hash, TOTPSecret: secret, Theme: "dark",
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func testAgent(t *testing.T, st *Store, userID string) Agent {
	t.Helper()
	sec, err := st.SystemSection(context.Background(), userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(context.Background(), userID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}
