package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/deletetool"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestMachineDeleteWaitsForTheName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("delete-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("delete-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	fake := host.NewFake()
	ip := "203.0.113.97:9"

	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "This agent has no machine.") {
		t.Fatalf("no machine %d %s", code, body)
	}
	if _, err := h.st.GetAgent(ctx, user.ID, caller.ID); err != nil {
		t.Fatal(err)
	}

	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-ada", "running", fake)

	code, body = machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{
		"confirm": caller.Name,
	}))
	if code != http.StatusNotFound {
		t.Fatalf("other token %d %s", code, body)
	}
	if !agentLive(t, h, user.ID, caller.ID) || len(fake.Deletes) != 0 {
		t.Fatal("other token deleted the agent")
	}

	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{
		"confirm": sibling.Name,
	}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d %s", code, body)
	}
	if _, err := h.st.GetAgent(ctx, user.ID, sibling.ID); err != nil {
		t.Fatal(err)
	}
	if !agentLive(t, h, user.ID, caller.ID) || len(fake.Deletes) != 0 {
		t.Fatal("sibling path deleted the caller")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, nil))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Type Ada to delete this machine.") || !strings.Contains(body, deletetool.Warning) {
		t.Fatalf("explain %d %s", code, body)
	}
	if !agentLive(t, h, user.ID, caller.ID) || len(fake.Deletes) != 0 {
		t.Fatal("explain deleted the agent")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{
		"confirm": caller.Name, "user_id": other.ID, "value": "secret-token",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, deletetool.Warning) || strings.Contains(body, other.ID) || strings.Contains(body, "secret-token") {
		t.Fatalf("early confirm %d %s", code, body)
	}
	if !agentLive(t, h, user.ID, caller.ID) || len(fake.Deletes) != 0 {
		t.Fatal("early confirm deleted the agent")
	}

	say(t, h, user, caller, "user", "please delete Ada")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{"confirm": caller.Name}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Type Ada to delete this machine.") {
		t.Fatalf("question %d %s", code, body)
	}
	say(t, h, user, caller, "user", "yes")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{"confirm": caller.Name}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, deletetool.Warning) {
		t.Fatalf("yes %d %s", code, body)
	}
	say(t, h, user, caller, "user", caller.Name)
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{"confirm": caller.Name}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || strings.Contains(body, "Deleted Ada.") {
		t.Fatalf("name before the warning %d %s", code, body)
	}
	if !agentLive(t, h, user.ID, caller.ID) || len(fake.Deletes) != 0 {
		t.Fatal("early name deleted the agent")
	}

	say(t, h, user, caller, "agent", deleteExplain(caller.Name))
	say(t, h, user, caller, "user", caller.Name)
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{"confirm": "ada"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Type the agent name to delete this machine.") {
		t.Fatalf("case %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{"confirm": "Pip"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Type the agent name to delete this machine.") {
		t.Fatalf("wrong confirm %d %s", code, body)
	}
	if !agentLive(t, h, user.ID, caller.ID) || len(fake.Deletes) != 0 {
		t.Fatal("wrong confirm deleted the agent")
	}

	say(t, h, user, caller, "user", "wait")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{"confirm": caller.Name}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || strings.Contains(body, "Deleted Ada.") {
		t.Fatalf("cleared %d %s", code, body)
	}
	if !agentLive(t, h, user.ID, caller.ID) {
		t.Fatal("a later message deleted the agent")
	}
	say(t, h, user, caller, "user", caller.Name)

	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{
		"confirm": caller.Name, "user_id": other.ID, "agent_id": sibling.ID, "value": "secret-token",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Deleted Ada.") || !strings.Contains(body, deletetool.Warning) || strings.Contains(body, other.ID) || strings.Contains(body, "secret-token") {
		t.Fatalf("delete %d %s", code, body)
	}
	if _, err := h.st.GetAgent(ctx, user.ID, caller.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("agent row %v", err)
	}
	if _, err := h.st.LiveVM(ctx, user.ID, caller.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("vm row %v", err)
	}
	if !containsID(fake.Deletes, "vm-ada") {
		t.Fatalf("host deletes %v", fake.Deletes)
	}
	if _, err := h.st.GetAgent(ctx, user.ID, sibling.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetAgent(ctx, other.ID, otherAgent.ID); err != nil {
		t.Fatal(err)
	}

	locked := seedCaller(t, h, user, "Nia")
	seedPower(t, h, user.ID, locked.ID, "vm-nia", "running", fake)
	say(t, h, user, locked, "agent", deleteExplain(locked.Name))
	say(t, h, user, locked, "user", locked.Name)
	if _, _, err := h.st.BeginReplacement(ctx, user.ID, locked.ID, "tool", "example.invalid/castbot-runtime:s09", false); err != nil {
		t.Fatal(err)
	}
	lockToken := guestToken(t, h, locked)
	code, body = machinePost(t, h, ip, lockToken, locked.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{"confirm": locked.Name}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, replaceSentence) {
		t.Fatalf("replacing %d %s", code, body)
	}
	if !agentLive(t, h, user.ID, locked.ID) || containsID(fake.Deletes, "vm-nia") {
		t.Fatal("replace lock deleted the agent")
	}

	held := seedCaller(t, h, user, "Kai")
	seedPower(t, h, user.ID, held.ID, "vm-kai", "running", fake)
	say(t, h, user, held, "agent", deleteExplain(held.Name))
	say(t, h, user, held, "user", held.Name)
	fake.DeleteErr = errors.New("delete failed")
	heldToken := guestToken(t, h, held)
	code, body = machinePost(t, h, ip, heldToken, held.ID, "/agents", routinePayload(deletetool.ToolDelete, map[string]any{"confirm": held.Name}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "The machine could not be deleted.") {
		t.Fatalf("host delete %d %s", code, body)
	}
	if !agentLive(t, h, user.ID, held.ID) || containsID(fake.Deletes, "vm-kai") {
		t.Fatal("failed host delete removed the agent")
	}
}

func TestDeleteFenceWaitsForTheName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("delete-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-ada", "running", fake)
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}

	replyRoutine(t, h, user.ID, caller, "Leave this.\n\n```castbot-delete\n{\"action\":\"delete_agent\",\"confirm\":\"NAME\"}\n```\n")
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-delete") || !strings.Contains(kept, "Leave this.") || strings.Contains(kept, "Deleted Ada.") {
		t.Fatalf("placeholder %s", kept)
	}
	if !agentLive(t, h, user.ID, caller.ID) || len(fake.Deletes) != 0 {
		t.Fatal("placeholder deleted the agent")
	}

	say(t, h, user, caller, "user", caller.Name)
	replyRoutine(t, h, user.ID, caller, deletetool.Warning+"\n\n```castbot-delete\n{\"action\":\"delete_agent\",\"confirm\":\"Ada\"}\n```\n")
	early := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(early, "castbot-delete") || strings.Contains(early, "Deleted Ada.") || !strings.Contains(early, deletetool.Warning) {
		t.Fatalf("early fence %s", early)
	}
	if !agentLive(t, h, user.ID, caller.ID) || len(fake.Deletes) != 0 {
		t.Fatal("warning in the same reply deleted the agent")
	}

	say(t, h, user, caller, "user", caller.Name)
	replyRoutine(t, h, user.ID, caller, "Removing it.\n\n```castbot-delete\n{\"action\":\"delete_agent\",\"confirm\":\"Ada\"}\n```\n")
	body := lastAgentOnThread(t, h, user.ID, th.ID)
	if strings.Contains(body, "castbot-delete") || !strings.Contains(body, "Removing it.") || !strings.Contains(body, "Deleted Ada.") || !strings.Contains(body, deletetool.Warning) {
		t.Fatalf("fence %s", body)
	}
	if _, err := h.st.GetAgent(ctx, user.ID, caller.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("agent row %v", err)
	}
	if _, err := h.st.LiveVM(ctx, user.ID, caller.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("vm row %v", err)
	}
	if !containsID(fake.Deletes, "vm-ada") {
		t.Fatalf("host deletes %v", fake.Deletes)
	}
}

func lastAgentOnThread(t *testing.T, h *harness, userID, threadID string) string {
	t.Helper()
	msgs, err := h.st.ListMessages(context.Background(), userID, threadID)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, msg := range msgs {
		if msg.AuthorKind == "agent" {
			body = msg.Body
		}
	}
	return body
}

func agentLive(t *testing.T, h *harness, userID, agentID string) bool {
	t.Helper()
	if _, err := h.st.GetAgent(context.Background(), userID, agentID); err != nil {
		return false
	}
	_, err := h.st.LiveVM(context.Background(), userID, agentID)
	return err == nil
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
