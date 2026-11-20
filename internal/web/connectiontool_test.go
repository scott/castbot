package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/connectiontool"
	"github.com/scott/castbot/internal/store"
)

func TestMachineConnectionDisconnectsAndSetsLevels(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("conn-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("conn-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	seedModelBundle(t, h, user, caller)
	seedModelBundle(t, h, user, sibling)
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	sibToken := guestToken(t, h, sibling)
	ip := "203.0.113.93:9"

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(connectiontool.ToolLevel, map[string]any{
		"connection": "shell", "tool": "bash", "level": "deny",
	}))
	if code != http.StatusNotFound || strings.Contains(body, "Saved") {
		t.Fatalf("other token %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(connectiontool.ToolLevel, map[string]any{
		"connection": "shell", "tool": "bash", "level": "deny", "user_id": other.ID,
	}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d %s", code, body)
	}
	if got := toolLevel(t, h, user.ID, connect.Shell, "bash"); got != "" {
		t.Fatalf("level stored early %s", got)
	}

	code, body = machinePost(t, h, ip, sibToken, sibling.ID, "/agents", routinePayload(connectiontool.ToolLevel, map[string]any{
		"connection": "Shell Actions", "tool": "bash", "level": "Always allow", "token": connectCanary,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Saved. Every agent you own uses these choices.") || strings.Contains(body, connectCanary) {
		t.Fatalf("allow %d %s", code, strings.ReplaceAll(body, connectCanary, "redacted"))
	}
	if toolLevel(t, h, user.ID, connect.Shell, "bash") != "always-allow" || toolLevel(t, h, user.ID, connect.Shell, "read") != "always-allow" {
		t.Fatal("shell levels")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(connectiontool.ToolLevel, map[string]any{
		"connection": "shell", "tool": "bash", "level": "deny", "user_id": other.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Saved. Every agent you own uses these choices.") {
		t.Fatalf("deny %d %s", code, body)
	}
	if toolLevel(t, h, user.ID, connect.Shell, "bash") != "deny" || toolLevel(t, h, user.ID, connect.Shell, "read") != "always-allow" {
		t.Fatal("deny wiped another tool")
	}
	bundle, err := h.st.LatestBundle(ctx, user.ID, caller.ID)
	if err != nil || !strings.Contains(bundle.YAML, "bash") || !strings.Contains(bundle.YAML, "deny") {
		t.Fatalf("bundle did not reload %v", err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(connectiontool.ToolLevel, map[string]any{
		"connection": "shell", "tool": "git", "level": "ask",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "More than one tool matches.") || toolLevel(t, h, user.ID, connect.Shell, "bash") != "deny" {
		t.Fatalf("many tools %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(connectiontool.ToolLevel, map[string]any{
		"connection": "github", "tool": "get_me", "level": "deny",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "needs a token") || !strings.Contains(body, "The token stays in your vault.") {
		t.Fatalf("needs token %d %s", code, body)
	}
	if toolLevel(t, h, user.ID, connect.GitHub, "get_me") != "" {
		t.Fatal("github level saved without a token")
	}
	if _, err := h.st.SaveConnectionSecret(ctx, user.ID, connect.GitHub, connectCanary); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(connectiontool.ToolLevel, map[string]any{
		"connection": "GitHub", "tool": "get_me", "level": "deny", "token": connectCanary,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Saved. GitHub stays disconnected.") || strings.Contains(body, connectCanary) {
		t.Fatalf("stay off %d %s", code, strings.ReplaceAll(body, connectCanary, "redacted"))
	}
	on, err := h.st.ConnectionSwitchedOn(ctx, user.ID, connect.GitHub)
	if err != nil || on || toolLevel(t, h, user.ID, connect.GitHub, "get_me") != "deny" {
		t.Fatalf("switch %v on %v", err, on)
	}
	if got, err := h.st.ConnectionValue(ctx, user.ID, connect.GitHub); err != nil || got != connectCanary {
		t.Fatal("token changed")
	}
	if err := h.st.SetConnectionSwitch(ctx, user.ID, connect.GitHub, true); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(connectiontool.ToolLevel, map[string]any{
		"connection": "github", "tool": "list_repos", "level": "ask",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Saved. Every agent you own uses these choices.") {
		t.Fatalf("repos %d %s", code, body)
	}
	if toolLevel(t, h, user.ID, connect.GitHub, "get_me") != "deny" || toolLevel(t, h, user.ID, connect.GitHub, "list_repos") != "ask" {
		t.Fatal("github levels")
	}
	code, body = machinePost(t, h, ip, otherToken, otherAgent.ID, "/agents", routinePayload(connectiontool.ToolDisconnect, map[string]any{"connection": "github"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Already disconnected.") {
		t.Fatalf("other disconnect %d %s", code, body)
	}
	on, err = h.st.ConnectionSwitchedOn(ctx, user.ID, connect.GitHub)
	if err != nil || !on {
		t.Fatal("other account disconnected this connection")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(connectiontool.ToolDisconnect, map[string]any{"connection": "GitHub", "user_id": other.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Disconnected. Tool choices stay.") || strings.Contains(body, connectCanary) {
		t.Fatalf("disconnect %d %s", code, strings.ReplaceAll(body, connectCanary, "redacted"))
	}
	on, err = h.st.ConnectionSwitchedOn(ctx, user.ID, connect.GitHub)
	if err != nil || on || toolLevel(t, h, user.ID, connect.GitHub, "get_me") != "deny" {
		t.Fatal("disconnect dropped the choice")
	}
	if got, err := h.st.ConnectionValue(ctx, user.ID, connect.GitHub); err != nil || got != connectCanary {
		t.Fatal("disconnect removed the token")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(connectiontool.ToolDisconnect, map[string]any{"connection": "github"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Already disconnected.") {
		t.Fatalf("again %d %s", code, body)
	}
}

func TestConnectionFenceSetsOneLevel(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("conn-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	seedModelBundle(t, h, user, caller)
	invalid := "Leave this.\n\n```castbot-connection\n{\"action\":\"set_tool_level\",\"connection\":\"CONNECTION\",\"tool\":\"TOOL\",\"level\":\"LEVEL\"}\n```\n"
	replyRoutine(t, h, user.ID, caller, invalid)
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-connection") || !strings.Contains(kept, "Leave this.") || toolLevel(t, h, user.ID, connect.Shell, "bash") != "" {
		t.Fatalf("invalid fence removed %s", kept)
	}
	replyRoutine(t, h, user.ID, caller, "Setting it.\n\n```castbot-connection\n{\"action\":\"set_tool_level\",\"connection\":\"shell\",\"tool\":\"bash\",\"level\":\"deny\",\"token\":\"secret\",\"user_id\":\"other\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-connection") || !strings.Contains(body, "Setting it.") || strings.Contains(body, "could not be saved") || toolLevel(t, h, user.ID, connect.Shell, "bash") != "deny" {
		t.Fatalf("fence %s level %s", body, toolLevel(t, h, user.ID, connect.Shell, "bash"))
	}
}

func seedModelBundle(t *testing.T, h *harness, user store.User, agent store.Agent) {
	t.Helper()
	model := defaultModelID()
	if model == "" {
		t.Fatal("no model")
	}
	yaml := "name: " + agent.Name + "\ndescription: Helper\nversion: 1\nbrain:\n  model: " + model + "\n  instructions_file: instructions.md\n"
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "2", yaml, "Be brief.\n"); err != nil {
		t.Fatal(err)
	}
}

func toolLevel(t *testing.T, h *harness, userID, connection, tool string) string {
	t.Helper()
	levels, err := h.st.ConnectionLevels(context.Background(), userID, connection)
	if err != nil {
		t.Fatal(err)
	}
	return levels[tool]
}
