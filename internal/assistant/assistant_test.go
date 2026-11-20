package assistant

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"), store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func makeUser(t *testing.T, s *store.Store, email, name string) store.User {
	t.Helper()
	hash, err := auth.Hash("right-password-1", s.Argon())
	if err != nil {
		t.Fatal(err)
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreateUser(context.Background(), store.NewUser{
		Email: email, DisplayName: name, Workspace: "Personal",
		PasswordHash: hash, TOTPSecret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestEnsureCreatesCastBot(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "owner@example.com", "Owner")
	other := makeUser(t, s, "other@example.com", "Other")

	res, err := Ensure(ctx, s, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Agent.Name != Name || res.Agent.Title != Title || res.Agent.Description != Job {
		t.Fatalf("agent %+v", res.Agent)
	}
	if !res.Agent.CanDelegate || res.Agent.Status != "draft" || res.Agent.IdleTimeout != idleTimeout || res.Agent.Size != machineSize {
		t.Fatalf("switches %+v", res.Agent)
	}
	if len(Job) > 160 || len(Title) > 80 || len(Name) > 40 {
		t.Fatalf("lengths name %d title %d job %d", len(Name), len(Title), len(Job))
	}
	model, err := defaultModel()
	if err != nil || res.Agent.Model != model || model == "" {
		t.Fatalf("model %q %v", res.Agent.Model, err)
	}
	sec, err := s.GetSection(ctx, owner.ID, res.Agent.SectionID)
	if err != nil || sec.Kind != "pinned" {
		t.Fatalf("section %+v %v", sec, err)
	}
	greeted, err := s.AgentGreeted(ctx, owner.ID, res.Agent.ID)
	if err != nil || greeted != "" {
		t.Fatalf("greeted %q %v", greeted, err)
	}
	bundle, err := s.LatestBundle(ctx, owner.ID, res.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Instructions != Instructions() || !strings.Contains(bundle.Instructions, "Be faithful") || !strings.Contains(bundle.Instructions, "delegate") {
		t.Fatalf("instructions %q", bundle.Instructions)
	}
	if strings.Contains(bundle.Instructions, "—") || strings.Contains(bundle.YAML, "—") {
		t.Fatal("em dash in the bundle")
	}
	for _, word := range []string{"OpenAI", "Anthropic", "Gemini", "GitHub", "DigitalOcean", "OpenRouter", "Claude"} {
		if strings.Contains(bundle.Instructions, word) {
			t.Fatalf("instructions name %s", word)
		}
	}
	spec, err := castcfg.ParseYAML(bundle.YAML)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != Name || spec.Title != Title || spec.Model != model {
		t.Fatalf("yaml %+v", spec)
	}
	if spec.Permissions["bash"] != "ask" || spec.Permissions["git_push"] != "ask" || spec.Permissions["read"] != "always-allow" {
		t.Fatalf("permissions %#v", spec.Permissions)
	}
	for _, tool := range castcfg.Tools() {
		if spec.Permissions[tool.Name] != castcfg.DefaultLevel(tool.Name) {
			t.Fatalf("tool %s %#v", tool.Name, spec.Permissions)
		}
	}
	th, err := s.DMThread(ctx, owner.ID, res.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListMessages(ctx, owner.ID, th.ID)
	if err != nil || len(msgs) != 1 || msgs[0].Body != Note || msgs[0].Origin != "created" {
		t.Fatalf("messages %+v %v", msgs, err)
	}
	if _, err := s.GetAgent(ctx, other.ID, res.Agent.ID); err != store.ErrNotFound {
		t.Fatalf("other user read %v", err)
	}
	if rows, err := s.ListAgents(ctx, other.ID); err != nil || len(rows) != 0 {
		t.Fatalf("other list %+v %v", rows, err)
	}

	again, err := Ensure(ctx, s, owner.ID)
	if err != nil || again.Created || again.Agent.ID != res.Agent.ID {
		t.Fatalf("second %+v %v", again, err)
	}
	if err := s.SaveAgentFields(ctx, owner.ID, res.Agent.ID, store.AgentInput{
		SectionID: res.Agent.SectionID, Name: Name, Title: "Renamed", Description: Job,
		CanDelegate: true, Model: model,
	}); err != nil {
		t.Fatal(err)
	}
	kept, err := Ensure(ctx, s, owner.ID)
	if err != nil || kept.Agent.Title != "Renamed" {
		t.Fatalf("rewrite %+v %v", kept.Agent, err)
	}

	theirs, err := Ensure(ctx, s, other.ID)
	if err != nil || !theirs.Created || theirs.Agent.ID == res.Agent.ID || !theirs.Agent.CanDelegate {
		t.Fatalf("other agent %+v %v", theirs, err)
	}
	if _, err := s.GetAgent(ctx, owner.ID, theirs.Agent.ID); err != store.ErrNotFound {
		t.Fatalf("owner read other %v", err)
	}
}

func TestEnsureRefusesWhenAgentsExist(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "owner@example.com", "Owner")
	sec, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(ctx, s, owner.ID); err != ErrInUse {
		t.Fatalf("err %v", err)
	}
	rows, err := s.ListAgents(ctx, owner.ID)
	if err != nil || len(rows) != 1 || rows[0].Name != "Atlas" {
		t.Fatalf("rows %+v %v", rows, err)
	}
}

func TestEnsureUnknownUser(t *testing.T) {
	s := testStore(t)
	if _, err := Ensure(context.Background(), s, "missing"); err != store.ErrNotFound {
		t.Fatalf("err %v", err)
	}
}
