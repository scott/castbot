package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestMarkPausedIfUnchanged(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	owner := makeUser(t, s, "pause-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "castbot-pause", "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, owner.ID, agent.ID, "vm-pause", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	vm, err := s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm.LastWake == "" {
		t.Fatal(err)
	}
	first := vm.StateGen
	now = now.Add(time.Minute)
	s.SetNow(func() time.Time { return now })
	if err := s.ApplyVMState(ctx, owner.ID, agent.ID, "running", vm.Endpoint, ""); err != nil {
		t.Fatal(err)
	}
	vm, err = s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm.StateGen == first {
		t.Fatal("state generation did not move")
	}
	changed, err := s.MarkPausedIfUnchanged(ctx, owner.ID, agent.ID, vm.MicroVMID, first, "")
	if err != nil || changed {
		t.Fatalf("stale update %v %v", changed, err)
	}
	vm, err = s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm.State != "running" {
		t.Fatalf("%+v %v", vm, err)
	}
	changed, err = s.MarkPausedIfUnchanged(ctx, owner.ID, agent.ID, vm.MicroVMID, vm.StateGen, "https://vm.example")
	if err != nil || !changed {
		t.Fatalf("fresh update %v %v", changed, err)
	}
	vm, err = s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm.State != "paused" {
		t.Fatalf("%+v %v", vm, err)
	}
}

func TestOldRowsDoNotStampFirstRunning(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "oldvm-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, owner.ID, agent.ID, "vm-old", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	vm, err := s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm.CreatedRunning == "" {
		t.Fatal("new row was not stamped")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_vms SET observe_boot = 0, created_running_at = NULL WHERE id = ? AND user_id = ?`, vm.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyVMState(ctx, owner.ID, agent.ID, "running", vm.Endpoint, ""); err != nil {
		t.Fatal(err)
	}
	vm, err = s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm.CreatedRunning != "" {
		t.Fatalf("old row stamped %q", vm.CreatedRunning)
	}
}

func TestUpgradeLeavesFirstRunningUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	s, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner := makeUser(t, s, "upgrade-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "castbot-up", "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, owner.ID, agent.ID, "vm-up", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agent_vms SET created_running_at = NULL WHERE user_id = ?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE agent_vms DROP COLUMN observe_boot`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.ApplyVMState(ctx, owner.ID, agent.ID, "running", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	vm, err := s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm.CreatedRunning != "" {
		t.Fatalf("upgraded row stamped %q %v", vm.CreatedRunning, err)
	}
	if err := s.MarkVMDeleted(ctx, owner.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginVM(ctx, owner.ID, agent.ID, "castbot-new", "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishVM(ctx, owner.ID, agent.ID, "vm-new", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	vm, err = s.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || vm.CreatedRunning == "" {
		t.Fatal("new row after upgrade was not stamped")
	}
}

func TestPreS07CLITokensCountAsShown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	s, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	owner := makeUser(t, s, "shown-owner@example.com", "Owner")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.IssueCLIToken(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var created string
	if err := s.db.QueryRowContext(ctx, `SELECT created_at FROM cli_tokens WHERE id = ? AND user_id = ?`, id, owner.ID).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE cli_tokens DROP COLUMN viewed_at`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	toks, err := s.ListCLITokens(ctx, owner.ID, agent.ID)
	if err != nil || len(toks) != 1 || toks[0].ViewedAt != created {
		t.Fatalf("backfill %+v %v", toks, err)
	}
	id2, _, err := s.IssueCLIToken(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	toks, err = s.ListCLITokens(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range toks {
		if tok.ID == id2 && tok.ViewedAt != "" {
			t.Fatal("a later unseen token was marked shown")
		}
	}
}
