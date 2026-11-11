package main

import (
	"context"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/replace"
)

type holdImport struct {
	*host.Fake
	started chan struct{}
}

func (h *holdImport) ImportMemory(ctx context.Context, _, _ string, body io.Reader) error {
	_, _ = io.Copy(io.Discard, body)
	select {
	case h.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestAdminReplaceSignalRollsBack(t *testing.T) {
	st, agent := adminAgent(t, "running")
	fake := host.NewFake()
	fake.OnCreate = func(spec host.CreateSpec) {
		next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	hold := &holdImport{Fake: fake, started: make(chan struct{}, 1)}
	ctx, stop := interruptContext()
	defer stop()
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() {
		_, err := replace.Run(ctx, st, hold, replace.Options{
			UserID: agent.UserID, AgentID: agent.ID,
			ActorKind: "cli", ActorID: "cli", Actor: "cli",
			Image:     "example.invalid/castbot-runtime:ff6d7ea",
			PublicURL: "https://castbot.example",
			DataDir:   dir, BootWait: 2 * time.Second,
		})
		done <- err
	}()
	select {
	case <-hold.started:
	case err := <-done:
		t.Fatalf("import did not start: %v calls %v", err, fake.Calls)
	case <-time.After(3 * time.Second):
		t.Fatalf("import did not start: calls %v", fake.Calls)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("replace succeeded after interrupt")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replace did not roll back")
	}
	if _, ok := fake.Machines["vm-test"]; ok {
		t.Fatal("new machine remains after interrupt")
	}
}
