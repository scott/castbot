package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestSaveAgentFieldsJobAndDelegate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "job@example.com", "Owner")
	other := makeUser(t, s, "other-job@example.com", "Other")
	sec, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper",
		Description: "  Writes the brief  ", CanDelegate: true, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Description != "Writes the brief" || !agent.CanDelegate {
		t.Fatalf("create %+v", agent)
	}
	plain, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Plain", Title: "Quiet", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.CanDelegate || plain.Description != "" {
		t.Fatalf("default %+v", plain)
	}

	if err := s.SaveAgentFields(ctx, owner.ID, agent.ID, AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Description: "Checks the docs",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgent(ctx, owner.ID, agent.ID)
	if err != nil || got.Description != "Checks the docs" || got.CanDelegate {
		t.Fatalf("save %+v %v", got, err)
	}

	if err := s.SaveAgentFields(ctx, other.ID, agent.ID, AgentInput{
		SectionID: sec.ID, Name: "Stolen", Title: "Nope", Description: "nope", CanDelegate: true,
	}); err != ErrNotFound {
		t.Fatalf("other user %v", err)
	}
	got, err = s.GetAgent(ctx, owner.ID, agent.ID)
	if err != nil || got.Name != "Atlas" || got.Description != "Checks the docs" || got.CanDelegate {
		t.Fatalf("row changed %+v %v", got, err)
	}

	long := strings.Repeat("a", 161)
	if err := s.SaveAgentFields(ctx, owner.ID, agent.ID, AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Description: long, CanDelegate: true,
	}); err != ErrInvalid {
		t.Fatalf("long job %v", err)
	}
	got, err = s.GetAgent(ctx, owner.ID, agent.ID)
	if err != nil || got.Description != "Checks the docs" || got.CanDelegate {
		t.Fatalf("long job wrote %+v %v", got, err)
	}

	okJob := strings.Repeat("b", 160)
	if err := s.SaveAgentFields(ctx, owner.ID, agent.ID, AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Description: okJob, CanDelegate: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAgent(ctx, owner.ID, agent.ID)
	if err != nil || got.Description != okJob || !got.CanDelegate {
		t.Fatalf("160 %+v %v", got, err)
	}
}

func TestMigrateV26Backfill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opts := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	s, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner := makeUser(t, s, "backfill@example.com", "Owner")
	other := makeUser(t, s, "backfill-b@example.com", "Other")
	sec, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	otherSec, err := s.SystemSection(ctx, other.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(userID, sectionID, name string, delegate bool) Agent {
		t.Helper()
		ag, _, err := s.CreateAgent(ctx, userID, AgentInput{
			SectionID: sectionID, Name: name, Title: "Role", Status: "ready", CanDelegate: delegate,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ag
	}
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return at })
	put := func(userID, agentID, version, instructions string) {
		t.Helper()
		if _, err := s.PutBundle(ctx, userID, agentID, version, "name: Agent\n", instructions); err != nil {
			t.Fatal(err)
		}
	}
	bump := func() { at = at.Add(time.Minute) }

	coord := mk(owner.ID, sec.ID, "Coord", false)
	put(owner.ID, coord.ID, "1", "You coordinate the crew.\n")
	bump()
	upper := mk(owner.ID, sec.ID, "Upper", false)
	put(owner.ID, upper.ID, "1", "Please COORDINATE this.\n")
	bump()
	word := mk(owner.ID, sec.ID, "Sub", false)
	put(owner.ID, word.ID, "1", "Use coordinates.\n")
	bump()
	stale := mk(owner.ID, sec.ID, "Stale", false)
	put(owner.ID, stale.ID, "1", "You coordinate.\n")
	bump()
	put(owner.ID, stale.ID, "2", "Be concise.\n")
	bump()
	none := mk(owner.ID, sec.ID, "None", false)
	manual := mk(owner.ID, sec.ID, "Manual", true)
	put(owner.ID, manual.ID, "1", "Be concise.\n")
	bump()
	tieNew := mk(owner.ID, sec.ID, "TieNew", false)
	put(owner.ID, tieNew.ID, "1", "Be concise.\n")
	put(owner.ID, tieNew.ID, "2", "You coordinate.\n")
	bump()
	tieOld := mk(owner.ID, sec.ID, "TieOld", false)
	put(owner.ID, tieOld.ID, "1", "You coordinate.\n")
	put(owner.ID, tieOld.ID, "2", "Be concise.\n")
	bump()
	bAgent := mk(other.ID, otherSec.ID, "Bee", false)
	put(other.ID, bAgent.ID, "1", "You coordinate.\n")

	// A fresh database already has the column, so this open does not backfill.
	got, err := s.GetAgent(ctx, owner.ID, coord.ID)
	if err != nil || got.CanDelegate {
		t.Fatalf("fresh backfill %+v %v", got, err)
	}
	got, err = s.GetAgent(ctx, owner.ID, manual.ID)
	if err != nil || !got.CanDelegate {
		t.Fatalf("manual %+v %v", got, err)
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
	if _, err := db.Exec(`ALTER TABLE agents DROP COLUMN can_delegate`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 26`); err != nil {
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
	want := []struct {
		userID, id string
		on         bool
	}{
		{owner.ID, coord.ID, true},
		{owner.ID, upper.ID, true},
		{owner.ID, word.ID, true},
		{owner.ID, stale.ID, false},
		{owner.ID, none.ID, false},
		{owner.ID, manual.ID, false},
		{owner.ID, tieNew.ID, true},
		{owner.ID, tieOld.ID, false},
		{other.ID, bAgent.ID, true},
	}
	for _, tc := range want {
		got, err := s.GetAgent(ctx, tc.userID, tc.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.CanDelegate != tc.on {
			t.Fatalf("%s can_delegate %v", got.Name, got.CanDelegate)
		}
	}

	if err := s.SaveAgentFields(ctx, owner.ID, coord.ID, AgentInput{
		SectionID: sec.ID, Name: "Coord", Title: "Role", CanDelegate: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err = s.GetAgent(ctx, owner.ID, coord.ID)
	if err != nil || got.CanDelegate {
		t.Fatalf("reopen turned the switch back on %+v %v", got, err)
	}
	got, err = s.GetAgent(ctx, owner.ID, upper.ID)
	if err != nil || !got.CanDelegate {
		t.Fatalf("reopen cleared a backfill %+v %v", got, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 26`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 26 rows %d %v", n, err)
	}
}
