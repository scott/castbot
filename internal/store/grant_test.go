package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
)

func TestPromptGrantCoversOneMessage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "grant@example.com", "Grant User")
	other := makeUser(t, s, "grant-other@example.com", "Other")
	sec, err := s.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	chief, thread, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	spec, specThread, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Nova", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.CreateMessage(ctx, owner.ID, thread.ID, "user", owner.ID, "Draft the notes")
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := s.CreateRun(ctx, owner.ID, thread.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageRun(ctx, owner.ID, msg.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	job, err := s.EnsureAgentJob(ctx, owner.ID, thread.ID, msg.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := s.CreateBatch(ctx, owner.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	specRun, err := s.CreateRun(ctx, owner.ID, specThread.ID, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddDispatch(ctx, owner.ID, batch.ID, specThread.ID, spec.ID, specRun.ID, "running", "Look up the notes", ""); err != nil {
		t.Fatal(err)
	}
	otherThread, err := s.CreateThread(ctx, owner.ID, "dm", chief.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	otherMsg, err := s.CreateMessage(ctx, owner.ID, otherThread.ID, "user", owner.ID, "A different prompt")
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := s.CreateRun(ctx, owner.ID, otherThread.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMessageRun(ctx, owner.ID, otherMsg.ID, otherRun.ID); err != nil {
		t.Fatal(err)
	}

	got, err := s.PromptMessageID(ctx, owner.ID, specRun.ID)
	if err != nil || got != msg.ID {
		t.Fatalf("specialist prompt %q %v", got, err)
	}
	ids, err := s.PromptRunIDs(ctx, owner.ID, msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasID(ids, runRow.ID) || !hasID(ids, specRun.ID) || hasID(ids, otherRun.ID) {
		t.Fatalf("runs %#v", ids)
	}
	open, err := s.PromptRunsOpen(ctx, owner.ID, msg.ID)
	if err != nil || !open {
		t.Fatalf("open %v %v", open, err)
	}

	first, created, err := s.OpenPromptGrant(ctx, owner.ID, msg.ID)
	if err != nil || !created || first.EndedAt != "" {
		t.Fatalf("open %#v %v %v", first, created, err)
	}
	second, created, err := s.OpenPromptGrant(ctx, owner.ID, msg.ID)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second %#v %v %v", second, created, err)
	}
	active, err := s.PromptGrantActive(ctx, owner.ID, msg.ID)
	if err != nil || !active {
		t.Fatalf("active %v %v", active, err)
	}
	if _, err := s.GetPromptGrant(ctx, other.ID, msg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other %v", err)
	}
	if _, _, err := s.OpenPromptGrant(ctx, other.ID, msg.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other open %v", err)
	}

	if _, _, err := s.FinishRun(ctx, owner.ID, runRow.ID, "done", 1); err != nil {
		t.Fatal(err)
	}
	open, err = s.PromptRunsOpen(ctx, owner.ID, msg.ID)
	if err != nil || !open {
		t.Fatalf("specialist still open %v %v", open, err)
	}
	if err := s.EndPromptGrant(ctx, owner.ID, msg.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.EndPromptGrant(ctx, owner.ID, msg.ID); err != nil {
		t.Fatal(err)
	}
	active, err = s.PromptGrantActive(ctx, owner.ID, msg.ID)
	if err != nil || active {
		t.Fatalf("ended %v %v", active, err)
	}
	reopened, created, err := s.OpenPromptGrant(ctx, owner.ID, msg.ID)
	if err != nil || !created || reopened.ID != first.ID || reopened.EndedAt != "" {
		t.Fatalf("reopen %#v %v %v", reopened, created, err)
	}

	raw := []byte(`{"id":"req-grant","tool":"bash","arguments":{"command":"ls","note":"SECRETTKN-96"}}`)
	appr, err := s.RecordPermission(ctx, owner.ID, specRun.ID, raw, s.Now().Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimDecision(ctx, owner.ID, appr.ID, specRun.ID, "approved", owner.ID); err != nil || !ok {
		t.Fatal(err)
	}
	if err := s.MarkApprovalVia(ctx, owner.ID, appr.ID, GrantVia); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteGrantAudit(ctx, owner.ID, appr.ID, "user", owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteGrantAudit(ctx, owner.ID, appr.ID, "user", owner.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListAudit(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Action != "approval.approved" {
			continue
		}
		n++
		if strings.Contains(row.Detail, "SECRETTKN-96") || !strings.Contains(row.Detail, GrantNote) {
			t.Fatalf("detail %s", row.Detail)
		}
	}
	if n != 1 {
		t.Fatalf("audits %d %#v", n, rows)
	}
	gotAppr, err := s.GetApproval(ctx, owner.ID, appr.ID)
	if err != nil || gotAppr.Via != GrantVia {
		t.Fatalf("via %#v %v", gotAppr, err)
	}

	hash := strings.Repeat("ab", 32)
	card, err := s.CreateConnectApproval(ctx, owner.ID, chief.ID, thread.ID, runRow.ID, connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/demo/issues", hash, s.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	decided, ok, err := s.GrantConnectApproval(ctx, owner.ID, card.ID)
	if err != nil || !ok || decided.Via != GrantVia || decided.Status != "approved" {
		t.Fatalf("connect %#v %v %v", decided, ok, err)
	}
	again, ok, err := s.GrantConnectApproval(ctx, owner.ID, card.ID)
	if err != nil || ok || again.Status != "approved" {
		t.Fatalf("second connect %#v %v %v", again, ok, err)
	}
	audits, err := s.ListAudit(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	connects := 0
	for _, row := range audits {
		if row.Action != "connection.approved" {
			continue
		}
		connects++
		if strings.Contains(row.Detail, "SECRETTKN-96") || !strings.Contains(row.Detail, GrantNote) {
			t.Fatalf("connect detail %s", row.Detail)
		}
	}
	if connects != 1 {
		t.Fatalf("connect audits %d", connects)
	}
	pending, err := s.ListPendingConnectForRuns(ctx, owner.ID, []string{otherRun.ID, ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("other pending %#v", pending)
	}
}

func hasID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
