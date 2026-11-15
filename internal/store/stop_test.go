package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
)

func TestStopRunKeepsTextAndSettlesCards(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "stop@example.com", "Ada")
	other := makeUser(t, s, "other-stop@example.com", "Bea")
	sec, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, changed, err := s.StopRun(ctx, other.ID, queued.ID, false); err != ErrNotFound || changed {
		t.Fatalf("other user %v %v", err, changed)
	}
	payload, err := json.Marshal(map[string]string{"text": "Half a reply"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendRunEvent(ctx, owner.ID, queued.ID, "text", string(payload), 1); err != nil {
		t.Fatal(err)
	}
	stopped, text, changed, err := s.StopRun(ctx, owner.ID, queued.ID, false)
	if err != nil || !changed || stopped.Status != "stopped" || text != "Half a reply" {
		t.Fatalf("queued stop %+v %q %v %v", stopped, text, changed, err)
	}
	again, _, changed, err := s.StopRun(ctx, owner.ID, queued.ID, false)
	if err != nil || changed || again.Status != "stopped" {
		t.Fatalf("second %v %v %+v", changed, err, again)
	}
	msgs, err := s.ListMessages(ctx, owner.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	notes := 0
	kept := 0
	for _, msg := range msgs {
		if msg.Body == StoppedLine && msg.AuthorKind == "system" && msg.Origin == "notice" {
			notes++
		}
		if msg.Body == "Half a reply" && msg.AuthorKind == "agent" {
			kept++
		}
	}
	if notes != 1 || kept != 1 {
		t.Fatalf("messages notes %d kept %d", notes, kept)
	}

	live, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkStreaming(ctx, owner.ID, live.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, changed, err := s.StopRun(ctx, owner.ID, live.ID, false); err != nil || changed {
		t.Fatalf("queued update on a live run %v %v", changed, err)
	}
	if _, err := s.RecordPermission(ctx, owner.ID, live.ID, json.RawMessage(`{"id":"req-stop","tool":"bash","arguments":{"command":"ls"}}`), s.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	mine, err := s.CreateConnectApproval(ctx, owner.ID, agent.ID, thread.ID, live.ID, connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/demo/issues", hash, s.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.DecideConnectApproval(ctx, owner.ID, mine.ID, "approved"); err != nil || !ok {
		t.Fatal("approve card")
	}
	otherRun, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash2 := strings.Repeat("cd", 32)
	otherCard, err := s.CreateConnectApproval(ctx, owner.ID, agent.ID, thread.ID, otherRun.ID, connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/other", hash2, s.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	hash3 := strings.Repeat("ef", 32)
	loose, err := s.CreateConnectApproval(ctx, owner.ID, agent.ID, thread.ID, "", connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/loose", hash3, s.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	stopped, text, changed, err = s.StopRun(ctx, owner.ID, live.ID, true)
	if err != nil || !changed || stopped.Status != "stopped" {
		t.Fatalf("live stop %+v %q %v %v", stopped, text, changed, err)
	}
	rows, err := s.ListRunApprovals(ctx, owner.ID, live.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("approvals %v %+v", err, rows)
	}
	if rows[0].Status != "inactive" || rows[0].DecidedBy != "system:stop" || !rows[0].Sent {
		t.Fatalf("approval %+v", rows[0])
	}
	for _, row := range mustAudit(t, s, owner.ID) {
		if row.Action == "approval.approved" {
			t.Fatal("approved audit")
		}
	}
	denials, err := s.ListRestartDenials(ctx)
	if err != nil || len(denials) != 0 {
		t.Fatalf("restart denials %v %+v", err, denials)
	}
	got, err := s.GetConnectApproval(ctx, owner.ID, mine.ID)
	if err != nil || got.Status != "expired" {
		t.Fatalf("card %s %v", got.Status, err)
	}
	ok, err := s.ConsumeConnectApproval(ctx, owner.ID, mine.ID)
	if err != nil || ok {
		t.Fatal("expired card was consumed")
	}
	otherGot, err := s.GetConnectApproval(ctx, owner.ID, otherCard.ID)
	if err != nil || otherGot.Status != "pending" {
		t.Fatalf("other card %s %v", otherGot.Status, err)
	}
	looseGot, err := s.GetConnectApproval(ctx, owner.ID, loose.ID)
	if err != nil || looseGot.Status != "pending" {
		t.Fatalf("loose card %s %v", looseGot.Status, err)
	}
}

func mustAudit(t *testing.T, s *Store, userID string) []Audit {
	t.Helper()
	rows, err := s.ListAudit(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
