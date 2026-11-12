package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
)

func TestReplaceMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	opt := Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	st, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 15`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("version 15 count %d", n)
	}
	for _, col := range []string{"agent_vm_id", "revoked_at"} {
		var name string
		err := st.db.QueryRow(`SELECT name FROM pragma_table_info('agent_tokens') WHERE name = ?`, col).Scan(&name)
		if err != nil || name != col {
			t.Fatalf("column %s: %v", col, err)
		}
	}
	var col string
	if err := st.db.QueryRow(`SELECT name FROM pragma_table_info('vm_replacements') WHERE name = 'cleanup_pending'`).Scan(&col); err != nil || col != "cleanup_pending" {
		t.Fatalf("cleanup_pending %s %v", col, err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 14`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version 14 count %d %v", n, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if err := again.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 15`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("reopen %d %v", n, err)
	}
}

func TestReplaceLockAndTokenBinding(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, st, "replace-a@example.com", "Ada")
	other := makeUser(t, st, "replace-b@example.com", "Bea")
	sec, err := st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready", Model: "model-large"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginVM(ctx, owner.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishVM(ctx, owner.ID, agent.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	boot, err := st.IssueBootstrap(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	liveToken, err := st.ExchangeBootstrap(ctx, agent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveVMToken(ctx, agent.ID, liveToken); err != nil {
		t.Fatal(err)
	}
	row, _, err := st.BeginReplacement(ctx, owner.ID, agent.ID, "web:ada@example.com", "example.invalid/castbot-runtime:ff6d7ea", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.BeginReplacement(ctx, owner.ID, agent.ID, "web:ada@example.com", "example.invalid/castbot-runtime:ff6d7ea", false); err == nil {
		t.Fatal("second lock")
	} else if !strings.Contains(err.Error(), "A replacement is already running.") {
		t.Fatal(err)
	}
	nextBoot, err := st.IssueBootstrapFor(ctx, owner.ID, agent.ID, row.NewVMID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := st.ExchangeBootstrap(ctx, agent.ID, nextBoot)
	if err != nil {
		t.Fatal(err)
	}
	wrote, err := st.PlaceVMToken(ctx, agent.ID, next)
	if err != nil || wrote {
		t.Fatalf("live write %v %v", wrote, err)
	}
	_, still, err := st.VMCredentials(ctx, owner.ID, agent.ID)
	if err != nil || still != liveToken {
		t.Fatalf("live token changed")
	}
	staged, err := st.ReplacementToken(ctx, owner.ID, row.ID)
	if err != nil || staged != next {
		t.Fatal(err)
	}
	var raw string
	if err := st.db.QueryRow(`SELECT new_microvm_id || new_endpoint || new_token_hash || actor FROM vm_replacements WHERE id = ?`, row.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, next) || strings.Contains(raw, nextBoot) || strings.Contains(raw, liveToken) {
		t.Fatal("secret stored on the replacement row")
	}
	if _, err := st.GetReplacement(ctx, other.ID, row.ID); err != ErrNotFound {
		t.Fatalf("other user %v", err)
	}
	if err := st.AddVMEventFor(ctx, owner.ID, agent.ID, row.NewVMID, EventReplaceRequest, 0); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListVMEvents(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == EventReplaceRequest && ev.AgentVMID == row.NewVMID {
			found = true
		}
	}
	if !found {
		t.Fatal("event filed on the live row")
	}
	beforeLive, err := st.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeGen := beforeLive.StateGen
	if err := st.SwitchReplacement(ctx, owner.ID, row.ID, next, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UserForAgentToken(ctx, agent.ID, liveToken); err != ErrNotFound {
		t.Fatalf("old token %v", err)
	}
	if _, err := st.ReplacementToken(ctx, owner.ID, row.ID); err != ErrNotFound {
		t.Fatal("staged token kept")
	}
	var retiredGen int64
	if err := st.db.QueryRow(`SELECT state_gen FROM agent_vms WHERE id = ?`, row.OldVMID).Scan(&retiredGen); err != nil {
		t.Fatal(err)
	}
	if retiredGen != beforeGen+1 {
		t.Fatalf("retired state_gen %d, was %d", retiredGen, beforeGen)
	}
	live, err := st.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil || live.ID != row.NewVMID {
		t.Fatalf("switched row %+v %v", live, err)
	}
	if live.StateGen != 1 {
		t.Fatalf("new state_gen %d", live.StateGen)
	}
	_, got, err := st.VMCredentials(ctx, owner.ID, agent.ID)
	if err != nil || got != next {
		t.Fatal("new token was not copied onto the live row")
	}
}

func TestReplacePrecheckLines(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, st, "precheck@example.com", "Ada")
	sec, err := st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := st.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, line, err := st.ReplacePrecheck(ctx, owner.ID, agent.ID); err != nil || line != "This agent has no machine yet." {
		t.Fatalf("no machine %q %v", line, err)
	}
	if _, err := st.BeginVM(ctx, owner.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if _, line, err := st.ReplacePrecheck(ctx, owner.ID, agent.ID); err != nil || line != "The machine is still starting." {
		t.Fatalf("creating %q %v", line, err)
	}
	if err := st.FinishVM(ctx, owner.ID, agent.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, line, err := st.ReplacePrecheck(ctx, owner.ID, agent.ID); err != nil || line != "Finish or stop the reply in progress first." {
		t.Fatalf("run %q %v", line, err)
	}
	if _, _, err := st.FinishRun(ctx, owner.ID, run.ID, "done", 1); err != nil {
		t.Fatal(err)
	}
	open, err := st.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RecordPermission(ctx, owner.ID, open.ID, json.RawMessage(`{"id":"req-1","tool":"bash","arguments":{}}`), st.Now().Add(time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE runs SET status = 'done' WHERE id = ?`, open.ID); err != nil {
		t.Fatal(err)
	}
	if _, line, err := st.ReplacePrecheck(ctx, owner.ID, agent.ID); err != nil || line != "Answer the waiting approval first." {
		t.Fatalf("approval %q %v", line, err)
	}
}

func TestDeleteRevokesAgentToken(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, st, "revoke@example.com", "Ada")
	sec, err := st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginVM(ctx, owner.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishVM(ctx, owner.ID, agent.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	boot, err := st.IssueBootstrap(ctx, owner.ID, agent.ID)
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
	before, err := st.LiveVM(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkVMDeleted(ctx, owner.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	var state string
	var gen int64
	if err := st.db.QueryRow(`SELECT state, state_gen FROM agent_vms WHERE id = ?`, before.ID).Scan(&state, &gen); err != nil {
		t.Fatal(err)
	}
	if state != "deleted" || gen != before.StateGen+1 {
		t.Fatalf("deleted row %s gen %d, was %d", state, gen, before.StateGen)
	}
	if _, err := st.UserForAgentToken(ctx, agent.ID, token); err != ErrNotFound {
		t.Fatalf("revoked token %v", err)
	}
}
