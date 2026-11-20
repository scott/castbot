package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/settingstool"
)

func TestMachineSettingsNameAndTheme(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("settings-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("settings-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	sibToken := guestToken(t, h, sibling)
	ip := "203.0.113.92:9"

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(settingstool.ToolName, map[string]any{"name": "Other"}))
	if code != http.StatusNotFound || strings.Contains(body, "Name saved") {
		t.Fatalf("other token %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(settingstool.ToolName, map[string]any{"name": "Sibling path", "user_id": other.ID}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d %s", code, redactTokens(body))
	}
	if got := displayName(t, h, user.ID); got != "Ada" {
		t.Fatalf("name changed to %s", got)
	}

	code, body = machinePost(t, h, ip, sibToken, sibling.ID, "/agents", routinePayload(settingstool.ToolName, map[string]any{"name": "Ada M"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Name saved.") {
		t.Fatalf("sibling name %d %s", code, redactTokens(body))
	}
	if got := displayName(t, h, user.ID); got != "Ada M" {
		t.Fatalf("sibling name stored %s", got)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolName, map[string]any{"name": "Ada Lovelace", "user_id": other.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Name saved.") {
		t.Fatalf("name %d %s", code, redactTokens(body))
	}
	if got := displayName(t, h, user.ID); got != "Ada Lovelace" {
		t.Fatalf("name stored %s", got)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolName, map[string]any{"name": ""}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Enter a name up to 80 characters.") {
		t.Fatalf("empty name %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolName, map[string]any{"name": strings.Repeat("n", 81)}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Enter a name up to 80 characters.") || displayName(t, h, user.ID) != "Ada Lovelace" {
		t.Fatalf("long name %d %s", code, redactTokens(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolTheme, map[string]any{"theme": "Light", "agent_id": sibling.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Theme set to Light.") || themeOf(t, h, user.ID) != "light" {
		t.Fatalf("theme %d %s theme %s", code, redactTokens(body), themeOf(t, h, user.ID))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolTheme, map[string]any{"theme": "blue"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Choose dark, light, or system.") || themeOf(t, h, user.ID) != "light" {
		t.Fatalf("bad theme %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(settingstool.ToolTheme, map[string]any{"theme": "dark"}))
	if code != http.StatusNotFound || themeOf(t, h, user.ID) != "light" {
		t.Fatalf("other theme %d %s", code, redactTokens(body))
	}
	if displayName(t, h, other.ID) != "Bea" {
		t.Fatal("owner tools renamed the other account")
	}
}

func TestSettingsFenceSavesName(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("settings-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	invalid := "Leave this.\n\n```castbot-settings\n{\"action\":\"update_name\",\"name\":\"NAME\"}\n```\n"
	replyRoutine(t, h, user.ID, caller, invalid)
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-settings") || !strings.Contains(kept, "Leave this.") || displayName(t, h, user.ID) != "Ada" {
		t.Fatalf("invalid fence removed %s", redactTokens(kept))
	}
	replyRoutine(t, h, user.ID, caller, "Renaming.\n\n```castbot-settings\n{\"action\":\"update_name\",\"name\":\"Ada Lovelace\",\"user_id\":\"other\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-settings") || !strings.Contains(body, "Renaming.") || displayName(t, h, user.ID) != "Ada Lovelace" {
		t.Fatalf("fence name %s", redactTokens(body))
	}
	replyRoutine(t, h, user.ID, caller, "Theming.\n\n```castbot-settings\n{\"action\":\"set_theme\",\"theme\":\"dark\"}\n```\n")
	body = agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-settings") || !strings.Contains(body, "Theming.") || themeOf(t, h, user.ID) != "dark" {
		t.Fatalf("fence theme %s", redactTokens(body))
	}
}

func displayName(t *testing.T, h *harness, userID string) string {
	t.Helper()
	user, err := h.st.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return user.DisplayName
}

func themeOf(t *testing.T, h *harness, userID string) string {
	t.Helper()
	user, err := h.st.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return user.Theme
}

func redactTokens(body string) string {
	var b strings.Builder
	for {
		i := strings.Index(body, "cbi_")
		if i < 0 {
			b.WriteString(body)
			return b.String()
		}
		b.WriteString(body[:i])
		b.WriteString("cbi_redacted")
		body = body[i+4:]
		for len(body) > 0 && isHexByte(body[0]) {
			body = body[1:]
		}
	}
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
