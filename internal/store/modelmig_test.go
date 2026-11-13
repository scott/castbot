package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/castcfg"
)

func TestMigrateV21RewritesPlaceholdersOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opts := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	s, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	a := makeUser(t, s, "a@example.com", "User A")
	b := makeUser(t, s, "b@example.com", "User B")
	secA, err := s.SystemSection(ctx, a.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	secB, err := s.SystemSection(ctx, b.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	real := "digitalocean/deepseek-3.2"
	if !castcfg.KnownModel(real) {
		t.Fatal(real)
	}
	placeholder, _, err := s.CreateAgent(ctx, a.ID, AgentInput{
		SectionID: secA.ID, Name: "Atlas", Title: "Atlas", Model: "openrouter/minimax/minimax-m2.5", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	kept, _, err := s.CreateAgent(ctx, a.ID, AgentInput{
		SectionID: secA.ID, Name: "Ledger", Title: "Ledger", Model: real, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.CreateAgent(ctx, b.ID, AgentInput{
		SectionID: secB.ID, Name: "Atlas", Title: "Atlas", Model: real, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	instructions := "Help with the books.\n"
	realYAML := "name: Ledger\nbrain:\n  model: " + real + "\n"
	if _, err := s.PutBundle(ctx, a.ID, placeholder.ID, "v1", "name: Atlas\nbrain:\n  model: openrouter/minimax/minimax-m2.5\n", instructions); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutBundle(ctx, a.ID, kept.ID, "v1", realYAML, instructions); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCreateDraft(ctx, a.ID, `{"card":{"model":"openrouter/minimax/minimax-m2.5"}}`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agents SET model = 'Provider A / model-large', kept_yaml = 'model: Provider A / model-large' WHERE id = ?`, placeholder.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agent_bundles SET agent_yaml = 'name: Atlas\nbrain:\n  model: Provider A / model-small\n' WHERE agent_id = ?`, placeholder.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE create_drafts SET payload = '{"card":{"model":"Provider B / model-large"}}' WHERE user_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT sha256 FROM agent_bundles WHERE agent_id = ?`, kept.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 21`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgent(ctx, a.ID, placeholder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "openrouter/minimax/minimax-m2.5" {
		t.Fatalf("placeholder model %q", got.Model)
	}
	keptYAML, err := s.KeptYAML(ctx, a.ID, placeholder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if keptYAML != "model: openrouter/minimax/minimax-m2.5" {
		t.Fatalf("kept %q", keptYAML)
	}
	same, err := s.GetAgent(ctx, a.ID, kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	if same.Model != real {
		t.Fatalf("real model changed %q", same.Model)
	}
	otherAgent, err := s.GetAgent(ctx, b.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherAgent.Model != real {
		t.Fatalf("other user %q", otherAgent.Model)
	}
	bundled, err := s.LatestBundle(ctx, a.ID, placeholder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bundled.YAML, "openrouter/anthropic/claude-opus-4.5") || strings.Contains(bundled.YAML, "Provider ") {
		t.Fatalf("yaml %s", bundled.YAML)
	}
	if bundled.Instructions != instructions {
		t.Fatalf("instructions %q", bundled.Instructions)
	}
	_, sum, err := bundle.Archive(bundled.YAML, bundled.Instructions)
	if err != nil {
		t.Fatal(err)
	}
	if bundled.SHA256 != sum {
		t.Fatalf("sha %s want %s", bundled.SHA256, sum)
	}
	keptBundle, err := s.LatestBundle(ctx, a.ID, kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	if keptBundle.YAML != realYAML || keptBundle.SHA256 != before {
		t.Fatalf("untouched bundle changed")
	}
	draft, err := s.LoadCreateDraft(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(draft.Payload, "digitalocean/deepseek-v4.1-flash") || strings.Contains(draft.Payload, "Provider ") {
		t.Fatalf("draft %s", draft.Payload)
	}

	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	again, err := s.GetAgent(ctx, a.ID, placeholder.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Model != got.Model {
		t.Fatalf("second open %q", again.Model)
	}
}
