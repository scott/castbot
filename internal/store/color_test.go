package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestMigrateV20Backfill(t *testing.T) {
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
	base := time.Date(2026, 1, 2, 15, 0, 0, 0, time.UTC)
	mk := func(user User, sec string, name, color string, at time.Time) Agent {
		t.Helper()
		agent, _, err := s.CreateAgent(ctx, user.ID, AgentInput{
			SectionID: sec, Name: name, Title: name, Color: color, Status: "ready", CreatedAt: at,
		})
		if err != nil {
			t.Fatal(err)
		}
		return agent
	}
	keepA := mk(a, secA.ID, "Atlas", "c1", base)
	heldA := mk(a, secA.ID, "Ledger", "c2", base.Add(time.Minute))
	oldA1 := mk(a, secA.ID, "Timmy", "c3", base.Add(2*time.Minute))
	oldA2 := mk(a, secA.ID, "Welcome", "c4", base.Add(3*time.Minute))
	keepB := mk(b, secB.ID, "Atlas", "c1", base)
	oldB1 := mk(b, secB.ID, "Timmy", "c5", base.Add(time.Minute))
	oldB2 := mk(b, secB.ID, "Welcome", "c6", base.Add(2*time.Minute))
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
	for _, id := range []string{oldA1.ID, oldA2.ID, oldB1.ID, oldB2.ID} {
		if _, err := db.Exec(`UPDATE agents SET avatar_color = 'c0' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 20`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := func(user User, id string) string {
		t.Helper()
		agent, err := s.GetAgent(ctx, user.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		return agent.Color
	}
	if got(a, keepA.ID) != "c1" || got(a, heldA.ID) != "c2" {
		t.Fatalf("kept colors changed: %s %s", got(a, keepA.ID), got(a, heldA.ID))
	}
	a1, a2 := got(a, oldA1.ID), got(a, oldA2.ID)
	if !ValidAgentColor(a1) || !ValidAgentColor(a2) || a1 == a2 || a1 == "c1" || a1 == "c2" || a2 == "c1" || a2 == "c2" || a1 == "c0" || a2 == "c0" {
		t.Fatalf("user A backfill %s %s", a1, a2)
	}
	if a1 != "c3" || a2 != "c4" {
		t.Fatalf("user A order %s %s", a1, a2)
	}
	if got(b, keepB.ID) != "c1" {
		t.Fatalf("user B kept %s", got(b, keepB.ID))
	}
	b1, b2 := got(b, oldB1.ID), got(b, oldB2.ID)
	if b1 != "c2" || b2 != "c3" {
		t.Fatalf("user B independent %s %s", b1, b2)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 20`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 20 rows %d %v", n, err)
	}
	snap := map[string]string{
		keepA.ID: "c1", heldA.ID: "c2", oldA1.ID: a1, oldA2.ID: a2,
		keepB.ID: "c1", oldB1.ID: b1, oldB2.ID: b2,
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, want := range snap {
		user := a
		if id == keepB.ID || id == oldB1.ID || id == oldB2.ID {
			user = b
		}
		if got(user, id) != want {
			t.Fatalf("reopen %s got %s want %s", id, got(user, id), want)
		}
	}
}

func TestNextAgentColorAvoidsInUse(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "picker@example.com", "Picker")
	sec, err := s.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	var first Agent
	for i, key := range PaletteKeys() {
		agent, _, err := s.CreateAgent(ctx, user.ID, AgentInput{
			SectionID: sec.ID, Name: "Agent" + key, Title: "Helper", Status: "ready",
		})
		if err != nil {
			t.Fatal(err)
		}
		if !ValidAgentColor(agent.Color) || agent.Color == "c0" {
			t.Fatalf("color %s", agent.Color)
		}
		if prev, ok := seen[agent.Color]; ok {
			t.Fatalf("shared %s between %s and %s before the palette was full", agent.Color, prev, agent.Name)
		}
		seen[agent.Color] = agent.Name
		if agent.Color != key {
			t.Fatalf("agent %d color %s want %s", i, agent.Color, key)
		}
		if i == 0 {
			first = agent
		}
	}
	tenth, _, err := s.CreateAgent(ctx, user.ID, AgentInput{
		SectionID: sec.ID, Name: "Tenth", Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tenth.Color != "c1" {
		t.Fatalf("reuse %s", tenth.Color)
	}
	if err := s.DeleteAgent(ctx, user.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAgent(ctx, user.ID, tenth.ID); err != nil {
		t.Fatal(err)
	}
	next, err := s.NextAgentColor(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next != "c1" {
		t.Fatalf("deleted color still blocked, next %s", next)
	}
	other := makeUser(t, s, "other-picker@example.com", "Other")
	otherNext, err := s.NextAgentColor(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherNext != "c1" {
		t.Fatalf("other user %s", otherNext)
	}
}

func TestRejectUnknownColorOnWrite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	user := makeUser(t, s, "color-write@example.com", "Writer")
	sec, err := s.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, user.ID, AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Color: "c1", Status: "ready",
	})
	if err != nil || agent.Color != "c1" {
		t.Fatalf("create %s %v", agent.Color, err)
	}
	before, err := s.GetAgent(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentColor(ctx, user.ID, agent.ID, ""); err != ErrInvalid {
		t.Fatalf("empty set %v", err)
	}
	for _, bad := range []string{"c0", "#ff00aa", "nope", "c10", "C1"} {
		if err := s.SetAgentColor(ctx, user.ID, agent.ID, bad); err != ErrInvalid {
			t.Fatalf("set %q err %v", bad, err)
		}
		if _, _, err := s.CreateAgent(ctx, user.ID, AgentInput{
			SectionID: sec.ID, Name: "Bad " + bad, Title: "Nope", Color: bad, Status: "ready",
		}); err != ErrInvalid {
			t.Fatalf("create %q err %v", bad, err)
		}
		if err := s.SaveAgentFields(ctx, user.ID, agent.ID, AgentInput{
			SectionID: sec.ID, Name: "Renamed", Title: "Changed", Color: bad,
		}); err != ErrInvalid {
			t.Fatalf("save %q err %v", bad, err)
		}
	}
	got, err := s.GetAgent(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Color != before.Color || got.Name != before.Name || got.Title != before.Title {
		t.Fatalf("row changed %+v", got)
	}
	rows, err := s.ListAgents(ctx, user.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("agents %d %v", len(rows), err)
	}
	if err := s.SetAgentColor(ctx, user.ID, agent.ID, "c4"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAgent(ctx, user.ID, agent.ID)
	if err != nil || got.Color != "c4" {
		t.Fatalf("set c4 %s %v", got.Color, err)
	}
}
