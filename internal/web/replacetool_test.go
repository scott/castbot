package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/replacetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func TestMachineReplaceWaitsForYes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("replace-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("replace-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	fake := host.NewFake()
	fake.CreateErr = errors.New("held")
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-ada", "running", fake)
	ip := "203.0.113.97:9"

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(replacetool.ToolReplace, map[string]any{
		"confirm": "yes", "user_id": other.ID,
	}))
	if code != http.StatusNotFound {
		t.Fatalf("other token %d %s", code, body)
	}
	noMove(t, h, user.ID, caller.ID)

	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(replacetool.ToolReplace, map[string]any{
		"confirm": "yes",
	}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d %s", code, body)
	}
	noMove(t, h, user.ID, caller.ID)

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(replacetool.ToolReplace, map[string]any{}))
	text := agentResult(t, body)
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !replaceLists(text) {
		t.Fatalf("explain %d %s", code, body)
	}
	noMove(t, h, user.ID, caller.ID)

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(replacetool.ToolReplace, map[string]any{
		"confirm": "yes", "user_id": other.ID, "value": "secret-token",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !replaceLists(agentResult(t, body)) || strings.Contains(body, other.ID) || strings.Contains(body, "secret-token") {
		t.Fatalf("early confirm %d %s", code, body)
	}
	noMove(t, h, user.ID, caller.ID)

	say(t, h, user, caller, "user", "please replace this machine")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(replacetool.ToolReplace, map[string]any{
		"confirm": "yes",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !replaceLists(agentResult(t, body)) {
		t.Fatalf("question %d %s", code, body)
	}
	noMove(t, h, user.ID, caller.ID)

	say(t, h, user, caller, "agent", text)
	say(t, h, user, caller, "user", "yes")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(replacetool.ToolReplace, map[string]any{
		"confirm": "yes", "user_id": other.ID, "value": "secret-token",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Replacing this machine.") || !strings.Contains(body, "Chat is paused until it is done.") || strings.Contains(body, other.ID) || strings.Contains(body, "secret-token") {
		t.Fatalf("start %d %s", code, body)
	}
	row := waitMove(t, h, user.ID, caller.ID)
	if row.AgentID != caller.ID {
		t.Fatalf("replaced %s", row.AgentID)
	}
	if _, err := h.st.LatestReplacement(ctx, user.ID, sibling.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("sibling machine changed")
	}
}

func TestReplaceDefersUntilTheReplyIsDone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("replace-defer@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	fake := host.NewFake()
	fake.CreateErr = errors.New("held")
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-defer", "running", fake)
	code, body := machinePost(t, h, "203.0.113.98:9", token, caller.ID, "/agents", routinePayload(replacetool.ToolReplace, nil))
	text := agentResult(t, body)
	if code != http.StatusOK || !replaceLists(text) {
		t.Fatalf("explain %d %s", code, body)
	}
	say(t, h, user, caller, "agent", text)
	say(t, h, user, caller, "user", "yes")
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, "203.0.113.98:9", token, caller.ID, "/agents", routinePayload(replacetool.ToolReplace, map[string]any{
		"confirm": "yes",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Replacing this machine.") {
		t.Fatalf("deferred %d %s", code, body)
	}
	if _, err := h.st.LatestReplacement(ctx, user.ID, caller.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("started during the reply")
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, runRow.ID, "On it.", 0); err != nil {
		t.Fatal(err)
	}
	h.srv.settleTalk(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: runRow.ID}, "done")
	waitMove(t, h, user.ID, caller.ID)
}

func TestReplaceFenceStartsAfterYes(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("replace-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	fake := host.NewFake()
	fake.CreateErr = errors.New("held")
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-fence", "running", fake)

	replyRoutine(t, h, user.ID, caller, "Leave this.\n\n```castbot-replace\n{\"action\":\"replace_machine\",\"confirm\":\"YES\"}\n```\n")
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-replace") || !strings.Contains(kept, "Leave this.") {
		t.Fatalf("placeholder removed %s", kept)
	}
	noMove(t, h, user.ID, caller.ID)

	say(t, h, user, caller, "user", "yes")
	replyRoutine(t, h, user.ID, caller, "Workspace files (copied)\nMachine logs\n\n```castbot-replace\n{\"action\":\"replace_machine\",\"confirm\":\"yes\",\"user_id\":\"other\"}\n```\n")
	early := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(early, "castbot-replace") || strings.Contains(early, "Replacing this machine.") || !strings.Contains(early, replacetool.KeptWorkspace) {
		t.Fatalf("early fence %s", early)
	}
	noMove(t, h, user.ID, caller.ID)

	say(t, h, user, caller, "user", "yes")
	replyRoutine(t, h, user.ID, caller, "Starting.\n\n```castbot-replace\n{\"action\":\"replace_machine\",\"confirm\":\"yes\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-replace") || !strings.Contains(body, "Starting.") {
		t.Fatalf("fence %s", body)
	}
	waitMove(t, h, user.ID, caller.ID)
}

func TestMachineRollbackWaitsForYes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("rollback-tool@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	fake := host.NewFake()
	fake.CreateErr = errors.New("held")
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-roll", "running", fake)
	ip := "203.0.113.99:9"

	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(replacetool.ToolRollback, map[string]any{"confirm": "yes"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "There is no previous runtime to roll back to.") {
		t.Fatalf("none %d %s", code, body)
	}
	noMove(t, h, user.ID, caller.ID)

	if err := h.st.NoteRuntimeRollback(ctx, user.ID, caller.ID, "offer", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(replacetool.ToolRollback, nil))
	text := agentResult(t, body)
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(text, "previous runtime (7b5e414)") || !strings.Contains(text, replacetool.RollbackCopy) || !strings.Contains(text, "for 7 days") || strings.Contains(text, replacetool.LostLogs) {
		t.Fatalf("explain %d %s", code, body)
	}
	noMove(t, h, user.ID, caller.ID)
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(replacetool.ToolRollback, map[string]any{"confirm": "yes"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "previous runtime (7b5e414)") {
		t.Fatalf("early %d %s", code, body)
	}
	noMove(t, h, user.ID, caller.ID)

	say(t, h, user, caller, "agent", text)
	say(t, h, user, caller, "user", "yes, roll it back")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(replacetool.ToolRollback, map[string]any{
		"confirm": "yes", "user_id": "other",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Rolling back this runtime.") || strings.Contains(body, "other") {
		t.Fatalf("start %d %s", code, body)
	}
	waitMove(t, h, user.ID, caller.ID)
}

func replaceLists(text string) bool {
	if !strings.Contains(text, "current runtime (s09)") || !strings.Contains(text, "Chat is paused until it is done.") {
		return false
	}
	if !strings.Contains(text, replacetool.ReplacePromise) {
		return false
	}
	for _, line := range append(replacetool.KeptLines(), replacetool.LostLines()...) {
		if !strings.Contains(text, line) {
			return false
		}
	}
	return true
}

func agentResult(t *testing.T, body string) string {
	t.Helper()
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Text
}

func say(t *testing.T, h *harness, user store.User, agent store.Agent, kind, body string) {
	t.Helper()
	th, err := h.st.DMThread(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	author := user.ID
	if kind == "agent" {
		author = agent.ID
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, th.ID, kind, author, body); err != nil {
		t.Fatal(err)
	}
}

func noMove(t *testing.T, h *harness, userID, agentID string) {
	t.Helper()
	time.Sleep(40 * time.Millisecond)
	_, err := h.st.LatestReplacement(context.Background(), userID, agentID)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replacement started %v", err)
	}
}

func waitMove(t *testing.T, h *harness, userID, agentID string) store.Replacement {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last store.Replacement
	var err error
	for time.Now().Before(deadline) {
		last, err = h.st.LatestReplacement(context.Background(), userID, agentID)
		if err == nil && last.State != "" && last.State != "running" {
			return last
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("replacement %v state %s", err, last.State)
	return last
}
