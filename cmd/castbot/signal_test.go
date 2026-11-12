package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
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
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	envPath := filepath.Join(dir, "castbot.env")
	key := strings.Repeat("0123456789abcdef", 4)
	if err := os.WriteFile(envPath, []byte("CASTBOT_SESSION_KEY="+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(up.Close)
	t.Setenv("CASTBOT_ENV_FILE", envPath)
	t.Setenv("CASTBOT_DB", dbPath)
	t.Setenv("CASTBOT_IMAGE", "example.invalid/castbot-runtime:ff6d7ea")
	t.Setenv("CASTBOT_DO_TOKEN", "machine-token-for-tests")
	t.Setenv("CASTBOT_PUBLIC_URL", up.URL)
	st, err := store.Open(dbPath, store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	hash, err := auth.Hash("right-password-1", st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, store.NewUser{Email: "ada@example.com", DisplayName: "Ada", Workspace: "Personal", PasswordHash: hash, TOTPSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	sec, err := st.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginVM(ctx, user.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishVM(ctx, user.ID, agent.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	boot, err := st.IssueBootstrap(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := st.ExchangeBootstrap(ctx, agent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVMToken(ctx, agent.ID, token); err != nil {
		t.Fatal(err)
	}
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
	prev := newMachineHost
	newMachineHost = func(host.Options) (host.AgentHost, error) { return hold, nil }
	t.Cleanup(func() { newMachineHost = prev })
	done := make(chan int, 1)
	go func() {
		done <- adminReplace([]string{"--yes", agent.ID})
	}()
	select {
	case <-hold.started:
	case code := <-done:
		t.Fatalf("import did not start: exit %d calls %v", code, fake.Calls)
	case <-time.After(5 * time.Second):
		t.Fatalf("import did not start: calls %v", fake.Calls)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("replace succeeded after interrupt")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replace did not roll back")
	}
	if _, ok := fake.Machines["vm-test"]; ok {
		t.Fatal("new machine remains after interrupt")
	}
}
