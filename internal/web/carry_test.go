package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestSweepCopiedDatabaseKeepsUnknownAgents(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.db")
	st, err := store.Open(orig, store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.Hash("correct-horse-1", st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, store.NewUser{
		Email: "copy@example.com", DisplayName: "Copy", Workspace: "Personal",
		PasswordHash: hash, TOTPSecret: secret, Theme: "dark",
	})
	if err != nil {
		t.Fatal(err)
	}
	sec, err := st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginVM(ctx, user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s04"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseVM(ctx, user.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	inst, err := st.InstanceID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(dir, "copy.db")
	for _, ext := range []string{"", "-wal", "-shm"} {
		raw, err := os.ReadFile(orig + ext)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if err := os.WriteFile(copyPath+ext, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st2, err := store.Open(copyPath, store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	got, err := st2.InstanceID(ctx)
	if err != nil || got != inst {
		t.Fatalf("copy instance %s %v", got, err)
	}
	key := make([]byte, 32)
	srv := New(st2, key, nil)
	t.Cleanup(srv.Close)
	fake := host.NewFake()
	srv.SetHost(fake, "example.invalid/castbot-runtime:s04", "https://castbot.example")
	tag := host.InstanceTag(inst)
	fake.Machines["known"] = host.Machine{ID: "known", Name: "castbot-known", State: "running", Tags: []string{"castbot", tag, "agent:" + agent.ID}}
	fake.Machines["other"] = host.Machine{ID: "other", Name: "castbot-other-db", State: "running", Tags: []string{"castbot", tag, "agent:does-not-exist-here"}}
	srv.SweepMachines(ctx)
	if _, ok := fake.Machines["known"]; ok {
		t.Fatal("a failed row in this database was left running")
	}
	if _, ok := fake.Machines["other"]; !ok {
		t.Fatal("a copied database deleted an agent it does not know")
	}
}
