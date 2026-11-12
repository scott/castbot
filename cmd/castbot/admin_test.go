package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/store"
)

func TestAdminMachinesAndDryRunMakeNoAPICall(t *testing.T) {
	st, agent := adminAgent(t, "running")
	var out bytes.Buffer
	if listMachines(context.Background(), st, "example.invalid/castbot-runtime:ff6d7ea", &out) != 0 {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), "Atlas") || !strings.Contains(out.String(), "update available") {
		t.Fatal(out.String())
	}
	out.Reset()
	if printPlan(context.Background(), st, agent, "example.invalid/castbot-runtime:ff6d7ea", &out) != 0 {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), "agent: Atlas") || !strings.Contains(out.String(), "target: ff6d7ea") {
		t.Fatal(out.String())
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	if !castbotUp(up.URL) || castbotUp("") {
		t.Fatal("server check")
	}
	var prompted bytes.Buffer
	if confirmAgentName(strings.NewReader("Atlas\n"), &prompted, "Atlas") != true {
		t.Fatal("name")
	}
	if confirmAgentName(strings.NewReader("nope\n"), &prompted, "Atlas") {
		t.Fatal("mismatch accepted")
	}
}

func TestAdminMachinesShowsCleanupPending(t *testing.T) {
	st, agent := adminAgent(t, "running")
	ctx := context.Background()
	row, _, err := st.BeginReplacement(ctx, agent.UserID, agent.ID, "cli", "example.invalid/castbot-runtime:ff6d7ea", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkCleanupPending(ctx, agent.UserID, row.ID, "copy", "copy"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if listMachines(ctx, st, "example.invalid/castbot-runtime:ff6d7ea", &out) != 0 {
		t.Fatal(out.String())
	}
	if !strings.Contains(out.String(), "cleanup pending") {
		t.Fatal(out.String())
	}
}

func TestAdminReplaceExitCodes(t *testing.T) {
	st, agent := adminAgent(t, "paused")
	fake := host.NewFake()
	fake.Machines["vm-old"] = host.Machine{ID: "vm-old", State: "paused", Endpoint: "https://old.example", Tags: []string{"agent:" + agent.ID}}
	fake.SeedDisk("https://old.example", []byte("mem"), map[string][]byte{"a.txt": []byte("aaa")})
	fake.OnCreate = func(spec host.CreateSpec) {
		next, err := st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		_, _ = st.PlaceVMToken(context.Background(), spec.AgentID, next)
	}
	var out bytes.Buffer
	code := runReplace(context.Background(), st, fake, replace.Options{
		UserID: agent.UserID, AgentID: agent.ID, ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: "example.invalid/castbot-runtime:ff6d7ea", PublicURL: "https://castbot.example",
		DataDir: t.TempDir(), BootWait: time.Second,
		Progress: func(title string) { fmtLine(&out, title) },
	}, &out)
	if code != 0 || !strings.Contains(out.String(), "Starting a new machine") || !strings.Contains(out.String(), "replaced Atlas") {
		t.Fatalf("code %d %s", code, out.String())
	}
	if strings.Contains(out.String(), "cbt_") {
		t.Fatal("token in output")
	}
}

func fmtLine(out *bytes.Buffer, title string) {
	out.WriteString(title + "\n")
}

func adminAgent(t *testing.T, state string) (*store.Store, store.Agent) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
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
	if err := st.FinishVM(ctx, user.ID, agent.ID, "vm-old", "https://old.example", state, ""); err != nil {
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
	return st, agent
}
