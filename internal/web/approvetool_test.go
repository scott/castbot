package web

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/approvetool"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestApproveCardIsScoped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("approve-owner@example.com", "Ada Owner", "correct-horse-1")
	other, _ := h.account("approve-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	ip := "203.0.113.150:9"
	bash := pendingCard(t, h, user, caller, "req-bash", "bash")
	git := pendingCard(t, h, user, caller, "req-git", "git")
	siblingCard := pendingCard(t, h, user, sibling, "req-rm", "rm")
	otherCard := pendingCard(t, h, other, otherAgent, "req-other", "curl")
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	secret, created, err := h.st.CreateSecretRequest(ctx, user.ID, caller.ID, th.ID, "", "github", "Save a token", time.Now().Add(time.Hour))
	if err != nil || !created || secret.Status != "pending" {
		t.Fatalf("secret %#v %v %v", secret, created, err)
	}
	if _, err := h.st.IngestJournal(ctx, user.ID, caller.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-10T18:00:00Z", SessionID: "sess-approve", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "request", Text: "from the cli"},
		{Seq: 2, At: "2026-11-10T18:00:01Z", SessionID: "sess-approve", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "permission", Tool: "bash", RequestID: "req-cli"},
	}); err != nil {
		t.Fatal(err)
	}

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(approvetool.ToolApprove, map[string]any{"card": "bash", "user_id": user.ID}))
	if code != http.StatusNotFound || strings.Contains(body, "Approved") {
		t.Fatalf("other token %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(approvetool.ToolApprove, map[string]any{"card": "rm"}))
	if code != http.StatusNotFound || strings.Contains(body, "Approved") {
		t.Fatalf("sibling path %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", `{"name":"`+approvetool.ToolApprove+`","arguments":["nope"]}`)
	if code != http.StatusOK || !strings.Contains(body, "That request was rejected.") {
		t.Fatalf("bad args %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolDeny, map[string]any{"card": 1}))
	if code != http.StatusOK || !strings.Contains(body, "That request was rejected.") {
		t.Fatalf("bad card %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolApprove, map[string]any{"user_id": other.ID, "agent_id": sibling.ID}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "More than one approval is waiting. Say which one.") || !strings.Contains(body, "bash") || !strings.Contains(body, "git") {
		t.Fatalf("ambiguous %d %s", code, body)
	}
	if approvalStatus(t, h, user.ID, bash.ID) != "pending" || approvalStatus(t, h, user.ID, git.ID) != "pending" || approvalStatus(t, h, user.ID, siblingCard.ID) != "pending" || approvalStatus(t, h, other.ID, otherCard.ID) != "pending" {
		t.Fatal("a card was decided before one was named")
	}

	runningGuest(t, h, user.ID, caller)
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolApprove, map[string]any{
		"card": "git", "user_id": other.ID, "agent_id": sibling.ID, "value": "sk-abcdefghijklmnop",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || strings.Contains(body, "sk-abcdefghijklmnop") || !strings.Contains(body, "Approved. git.") {
		t.Fatalf("git %d %s", code, body)
	}
	if approvalStatus(t, h, user.ID, git.ID) != "approved" || approvalStatus(t, h, user.ID, bash.ID) != "pending" || approvalStatus(t, h, user.ID, siblingCard.ID) != "pending" || approvalStatus(t, h, other.ID, otherCard.ID) != "pending" {
		t.Fatal("approve changed the wrong card")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolApprove, map[string]any{"card": "rm"}))
	if code != http.StatusOK || !strings.Contains(body, "No waiting approval matches that.") || approvalStatus(t, h, user.ID, siblingCard.ID) != "pending" || approvalStatus(t, h, user.ID, bash.ID) != "pending" {
		t.Fatalf("sibling tool %d %s", code, body)
	}
	got, err := h.st.GetSecretRequest(ctx, user.ID, secret.ID)
	if err != nil || got.Status != "pending" {
		t.Fatalf("secret card %#v %v", got, err)
	}
	if !cliAsked(t, h, user.ID, caller.ID) {
		t.Fatal("cli card was removed")
	}
}

func TestApproveAlwaysSweepsThePrompt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("approve-always@example.com", "Ada Owner", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token, fake := runningGuest(t, h, user.ID, caller)
	ip := "203.0.113.151:9"
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := h.st.CreateMessage(ctx, user.ID, th.ID, "user", user.ID, "Run both commands")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, msg.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	bash := mustPermission(t, h.st, user.ID, runRow.ID, "req-bash", "bash", "")
	git := mustPermission(t, h.st, user.ID, runRow.ID, "req-git", "git", "")
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolAlways, map[string]any{"card": "bash", "user_id": "other"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Approved for this prompt. bash.") || !strings.Contains(body, "Also approved for this prompt: git.") {
		t.Fatalf("always %d %s", code, body)
	}
	assertApprovedVia(t, h, user.ID, bash.ID)
	assertApprovedVia(t, h, user.ID, git.ID)
	_, approvals := fake.Decisions()
	if len(approvals) != 2 || !approvals[0].Allow || !approvals[1].Allow {
		t.Fatalf("host %#v", approvals)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolApprove, map[string]any{"card": "bash"}))
	if code != http.StatusOK || !strings.Contains(body, "Nothing is waiting.") {
		t.Fatalf("second %d %s", code, body)
	}
}

func TestApproveWhenTheMachineCannotTakeIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("approve-closed@example.com", "Ada Owner", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.152:9"
	missing := pendingCard(t, h, user, caller, "req-missing", "bash")
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolApprove, nil))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "This request is no longer active.") || strings.Contains(body, "Approved.") {
		t.Fatalf("no machine %d %s", code, body)
	}
	if approvalStatus(t, h, user.ID, missing.ID) != "inactive" || len(fake.Approvals) != 0 {
		t.Fatalf("status %s approvals %#v", approvalStatus(t, h, user.ID, missing.ID), fake.Approvals)
	}

	h = newHarness(t)
	user, _ = h.account("approve-replace@example.com", "Ada Owner", "correct-horse-1")
	caller = seedCaller(t, h, user, "Ada")
	token, _ = runningGuest(t, h, user.ID, caller)
	if _, _, err := h.st.BeginReplacement(ctx, user.ID, caller.ID, "web:test", "example.invalid/castbot-runtime:next", false); err != nil {
		t.Fatal(err)
	}
	held := pendingCard(t, h, user, caller, "req-held", "bash")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolDeny, nil))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "This machine is being replaced.") || approvalStatus(t, h, user.ID, held.ID) != "pending" {
		t.Fatalf("replace %d %s %s", code, body, approvalStatus(t, h, user.ID, held.ID))
	}
}

func TestConnectCardDenyAndSingleAlways(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("approve-connect@example.com", "Ada Owner", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.153:9"
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	push, err := h.st.CreateConnectApproval(ctx, user.ID, caller.ID, th.ID, "", "github", "Push a branch", "POST", "/repos/acme/app/git/refs", hash, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolDeny, map[string]any{"card": "Push", "value": "ghp_shouldnotpass"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || strings.Contains(body, "ghp_shouldnotpass") || !strings.Contains(body, "Denied. Push a branch.") {
		t.Fatalf("deny %d %s", code, body)
	}
	denied, err := h.st.GetConnectApproval(ctx, user.ID, push.ID)
	if err != nil || denied.Status != "denied" {
		t.Fatalf("connect %#v %v", denied, err)
	}
	drop, err := h.st.CreateConnectApproval(ctx, user.ID, caller.ID, th.ID, "", "github", "Delete a repo", "DELETE", "/repos/acme/app", hash, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(approvetool.ToolAlways, nil))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || strings.Contains(body, "for this prompt") || !strings.Contains(body, "Approved. Delete a repo.") {
		t.Fatalf("always %d %s", code, body)
	}
	row, err := h.st.GetConnectApproval(ctx, user.ID, drop.ID)
	if err != nil || row.Status != "approved" || row.Via != "" {
		t.Fatalf("single %#v %v", row, err)
	}
}

func TestApprovalFenceStripsOneDecision(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("approve-fence@example.com", "Ada Owner", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	runningGuest(t, h, user.ID, caller)
	bash := pendingCard(t, h, user, caller, "req-bash", "bash")
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg, ev, err := h.st.FinishRun(ctx, user.ID, runRow.ID, "Taking that one.\n\n```castbot-approval\n{\"action\":\"approve_card\",\"card\":\"bash\",\"user_id\":\"other\"}\n```", 0)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err = h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	fw := &frameWriter{w: &buf, seq: ev.Seq, from: -1}
	if err := h.srv.writeDone(ctx, fw, user, runRow, msg.ID, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	frame := buf.String()
	if strings.Contains(frame, "castbot-approval") || !strings.Contains(frame, "Taking that one.") || !strings.Contains(frame, "hx-swap-oob") || !strings.Contains(frame, "appr-"+bash.ID) || !strings.Contains(frame, "Approved") {
		t.Fatalf("fence frame %s", frame)
	}
	if approvalStatus(t, h, user.ID, bash.ID) != "approved" {
		t.Fatal("fence did not approve")
	}

	git := pendingCard(t, h, user, caller, "req-git", "git")
	kept, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	keptMsg, keptEv, err := h.st.FinishRun(ctx, user.ID, kept.ID, "Leave this.\n```castbot-approval\n{\"action\":\"approve_card\",\"card\":\"CARD\"}\n```", 0)
	if err != nil {
		t.Fatal(err)
	}
	kept, err = h.st.GetRun(ctx, user.ID, kept.ID)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	fw = &frameWriter{w: &buf, seq: keptEv.Seq, from: -1}
	if err := h.srv.writeDone(ctx, fw, user, kept, keptMsg.ID, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	frame = buf.String()
	if !strings.Contains(frame, "CARD") || !strings.Contains(frame, "Leave this.") || approvalStatus(t, h, user.ID, git.ID) != "pending" {
		t.Fatalf("placeholder %s status %s", frame, approvalStatus(t, h, user.ID, git.ID))
	}
}

func pendingCard(t *testing.T, h *harness, user store.User, agent store.Agent, requestID, tool string) store.Approval {
	t.Helper()
	ctx := context.Background()
	th, err := h.st.DMThread(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return mustPermission(t, h.st, user.ID, runRow.ID, requestID, tool, "")
}

func runningGuest(t *testing.T, h *harness, userID string, agent store.Agent) (string, *host.Fake) {
	t.Helper()
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, userID, agent.ID, "vm-"+agent.Name, "running", fake)
	token := guestToken(t, h, agent)
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	return token, fake
}

func approvalStatus(t *testing.T, h *harness, userID, id string) string {
	t.Helper()
	row, err := h.st.GetApproval(context.Background(), userID, id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Status
}

func cliAsked(t *testing.T, h *harness, userID, agentID string) bool {
	t.Helper()
	threads, err := h.st.ListThreadsByAgent(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.Kind != "cli" {
			continue
		}
		msgs, err := h.st.ListMessages(context.Background(), userID, th.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range msgs {
			if strings.Contains(msg.Body, "Answer it in the Cast CLI.") {
				return true
			}
		}
	}
	return false
}
