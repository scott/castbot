package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/selftool"
	"github.com/scott/castbot/internal/store"
)

func TestMachineSelfReports(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("self-owner@example.com", "Ada Person", "correct-horse-1")
	other, _ := h.account("self-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Quill")
	otherAgent := seedCaller(t, h, other, "Bea")
	sibling := seedCaller(t, h, user, "Pip")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	sibToken := guestToken(t, h, sibling)
	ctx := context.Background()
	ip := "203.0.113.90:9"
	const planted = "vault-sample-value-xyz"

	sec, err := h.st.PutSecret(ctx, user.ID, "SAMPLE_KEY", planted)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(ctx, user.ID, caller.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, user.ID, caller.ID, "castbot-test", "example.invalid/runtime:1"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(ctx, user.ID, caller.ID, "vm-self", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AddVMEvent(ctx, user.ID, caller.ID, store.EventRunning, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PlantCached(ctx, user.ID, caller.ID, "notes.md", 12); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.InsertUpload(ctx, user.ID, caller.ID, "", "brief.txt", "", strings.Repeat("ab", 32), "workspace/uploads/brief.txt", "stored", 20); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.InsertUpload(ctx, user.ID, caller.ID, "", "waiting.txt", "", strings.Repeat("cd", 32), "workspace/uploads/waiting.txt", "held", 8); err != nil {
		t.Fatal(err)
	}
	_, plain, err := h.st.IssueCLIToken(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	prefix := plain
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}

	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := h.st.RecordPermission(ctx, user.ID, runRow.ID, []byte(`{"id":"req-bash","tool":"bash","arguments":{"command":"ls"}}`), h.st.Now().Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.RecordPermission(ctx, user.ID, runRow.ID, []byte(`{"id":"req-read","tool":"read","arguments":{"path":"notes.md"}}`), h.st.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := h.st.ClaimDecision(ctx, user.ID, approved.ID, runRow.ID, "approved", "user"); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(selftool.ToolDescribe, map[string]any{}))
	if code != 404 || strings.Contains(body, "Quill") || strings.Contains(body, planted) {
		t.Fatalf("other token %d", code)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolDescribe, map[string]any{"user_id": other.ID, "agent_id": sibling.ID}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Name: Quill") || !strings.Contains(body, "1 vCPU / 2 GiB") || strings.Contains(body, planted) {
		t.Fatalf("describe %d %s", code, body)
	}
	if model := defaultModelID(); model != "" && !strings.Contains(body, model) {
		t.Fatalf("model missing %s", body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolUsage, map[string]any{"user_id": other.ID}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "$") || !strings.Contains(body, "An estimate, not a bill.") || strings.Contains(body, planted) {
		t.Fatalf("usage %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolMachine, map[string]any{}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Machine running") || strings.Contains(body, planted) {
		t.Fatalf("machine %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolApprovals, map[string]any{}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Waiting") || !strings.Contains(body, "read") || !strings.Contains(body, "Approved") || !strings.Contains(body, "bash") || strings.Contains(body, planted) {
		t.Fatalf("approvals %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolSecrets, map[string]any{}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "SAMPLE_KEY") || strings.Contains(body, planted) {
		t.Fatal("secret value leaked")
	}

	code, body = machinePost(t, h, ip, sibToken, sibling.ID, "/agents", routinePayload(selftool.ToolSecrets, map[string]any{}))
	if code != 200 || strings.Contains(body, "SAMPLE_KEY") || strings.Contains(body, planted) {
		t.Fatal("sibling saw the secret")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolCLI, map[string]any{}))
	if code != 200 || strings.Contains(body, plain) || !strings.Contains(body, prefix) || !strings.Contains(body, "Not shown") {
		t.Fatal("cli token leaked")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolFiles, map[string]any{}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "notes.md") || !strings.Contains(body, "brief.txt") || !strings.Contains(body, "saved in castbot, copied when the machine wakes.") || strings.Contains(body, planted) {
		t.Fatalf("files %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolConnections, map[string]any{}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "GitHub") || !strings.Contains(body, "Not connected") || strings.Contains(body, planted) {
		t.Fatalf("connections %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolAccount, map[string]any{"user_id": other.ID}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Ada Person") || !strings.Contains(body, "Dark") || strings.Contains(body, "Bea") || strings.Contains(body, planted) {
		t.Fatalf("overview %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolSearch, map[string]any{}))
	if code != 200 || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Say what to search for.") {
		t.Fatalf("empty search %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolRuns, map[string]any{}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "No routines yet.") || strings.Contains(body, planted) {
		t.Fatalf("runs %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolRuns, map[string]any{"id": "missing"}))
	if code != 200 || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That routine was not found.") {
		t.Fatalf("missing routine %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolApprovals, map[string]any{"query": "zzzz-no-such"}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "No approvals match.") || strings.Contains(body, planted) {
		t.Fatalf("approval search %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(selftool.ToolSearch, map[string]any{"query": "notes"}))
	if code != 200 || strings.Contains(body, `"error":true`) || !strings.Contains(body, "notes.md") || strings.Contains(body, planted) {
		t.Fatalf("search %d %s", code, body)
	}
}

func TestSelfFenceAppendsUsage(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("self-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Quill")
	ctx := context.Background()
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	fence := "Here is the spend.\n\n```castbot-account\n{\"tool\":\"account_usage\"}\n```\n"
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, runRow.ID, fence, 0); err != nil {
		t.Fatal(err)
	}
	job := run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: runRow.ID}
	h.srv.settleTalk(job, "done")
	h.srv.settleTalk(job, "done")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-account") || !strings.Contains(body, "Here is the spend.") || !strings.Contains(body, "An estimate, not a bill.") || strings.Count(body, "An estimate, not a bill.") != 1 {
		t.Fatalf("fence %s", body)
	}
	invalid := "Leave this.\n\n```castbot-account\n{\"tool\":\"TOOL\"}\n```\n"
	replyRoutine(t, h, user.ID, caller, invalid)
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-account") || !strings.Contains(kept, "Leave this.") {
		t.Fatalf("invalid fence removed %s", kept)
	}
}
