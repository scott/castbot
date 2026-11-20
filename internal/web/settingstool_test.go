package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/settingstool"
)

func TestMachineSettingsNameThemeAndInvites(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
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

	code, body = machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Pip Lane", "email": "pip-set@example.com"}))
	if code != http.StatusNotFound || strings.Contains(body, "invite link") {
		t.Fatalf("other invite %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Pip Lane", "email": "pip-set@example.com", "user_id": other.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Copy this invite link now. It is shown once.") {
		t.Fatalf("invite %d %s", code, redactTokens(body))
	}
	token1 := oneInviteToken(t, body)
	if _, err := h.st.PeekInvite(ctx, token1); err != nil {
		t.Fatal("invite link did not resolve")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Pip Lane", "email": "pip-set@example.com"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) {
		t.Fatalf("refresh %d %s", code, redactTokens(body))
	}
	token2 := oneInviteToken(t, body)
	if token2 == token1 {
		t.Fatal("refresh kept the same link")
	}
	if _, err := h.st.PeekInvite(ctx, token1); err == nil {
		t.Fatal("old link still resolved")
	}
	if _, err := h.st.PeekInvite(ctx, token2); err != nil {
		t.Fatal("new link did not resolve")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Bea", "email": "settings-other@example.com"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That email already has an account.") {
		t.Fatalf("existing %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Pip", "email": "not-an-email"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Enter a name and an email address.") {
		t.Fatalf("bad email %d %s", code, redactTokens(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Sam", "email": "sam-a@example.com"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) {
		t.Fatalf("sam a %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Sam", "email": "sam-b@example.com"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) {
		t.Fatalf("sam b %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolRevoke, map[string]any{}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Say which invite to revoke.") {
		t.Fatalf("empty revoke %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolRevoke, map[string]any{"invite": "Sam"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "More than one invite matches.") {
		t.Fatalf("two sam %d %s", code, redactTokens(body))
	}
	if n := pendingInvites(t, h, user.ID); n != 3 {
		t.Fatalf("pending %d", n)
	}
	rows, err := h.st.ListInvites(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var samBID string
	for _, row := range rows {
		if row.Email == "sam-b@example.com" {
			samBID = row.ID
		}
	}
	if samBID == "" {
		t.Fatal("sam b missing")
	}
	code, body = machinePost(t, h, ip, otherToken, otherAgent.ID, "/agents", routinePayload(settingstool.ToolRevoke, map[string]any{"invite": samBID}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That invite was not found.") {
		t.Fatalf("other revoke %d %s", code, redactTokens(body))
	}
	if n := pendingInvites(t, h, user.ID); n != 3 {
		t.Fatal("other account revoked an invite")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolRevoke, map[string]any{"invite": "sam-a@example.com", "user_id": other.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Revoked Sam.") {
		t.Fatalf("revoke a %d %s", code, redactTokens(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolRevoke, map[string]any{"invite": samBID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Revoked Sam.") {
		t.Fatalf("revoke b %d %s", code, redactTokens(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Quinn Hale", "email": "quinn-set@example.com"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) {
		t.Fatalf("quinn %d %s", code, redactTokens(body))
	}
	member, err := h.st.AcceptInvite(ctx, oneInviteToken(t, body), "hash", "totp")
	if err != nil || member.Role != "member" {
		t.Fatalf("accept %v %+v", err, member.Role)
	}
	memberAgent := seedCaller(t, h, member, "Quinn")
	memberToken := guestToken(t, h, memberAgent)
	code, body = machinePost(t, h, ip, memberToken, memberAgent.ID, "/agents", routinePayload(settingstool.ToolInvite, map[string]any{"name": "Pat", "email": "pat-set@example.com"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "The owner invites people to this workspace.") {
		t.Fatalf("member invite %d %s", code, redactTokens(body))
	}
	if n := pendingInvites(t, h, user.ID); n != 1 {
		t.Fatalf("member created an invite, pending %d", n)
	}
	if n := pendingInvites(t, h, member.ID); n != 0 {
		t.Fatalf("member pending %d", n)
	}
	code, body = machinePost(t, h, ip, memberToken, memberAgent.ID, "/agents", routinePayload(settingstool.ToolName, map[string]any{"name": "Quinn"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Name saved.") || displayName(t, h, member.ID) != "Quinn" {
		t.Fatalf("member name %d %s", code, redactTokens(body))
	}
	if displayName(t, h, user.ID) != "Ada Lovelace" {
		t.Fatal("member renamed the owner")
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
	h.srv.SetHost(nil, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	replyRoutine(t, h, user.ID, caller, "Inviting.\n\n```castbot-settings\n{\"action\":\"create_invite\",\"name\":\"Pip Lane\",\"email\":\"pip-fence@example.com\",\"user_id\":\"other\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-settings") || !strings.Contains(body, "Inviting.") || !strings.Contains(body, "Copy this invite link now. It is shown once.") || !strings.Contains(body, "https://castbot.example/invite/cbi_") {
		t.Fatalf("fence invite %s", redactTokens(body))
	}
	token := fenceInviteToken(t, body)
	if _, err := h.st.PeekInvite(context.Background(), token); err != nil {
		t.Fatal("fence link did not resolve")
	}
}

func fenceInviteToken(t *testing.T, body string) string {
	t.Helper()
	const key = "/invite/cbi_"
	if strings.Count(body, key) != 1 {
		t.Fatalf("invite link count %d", strings.Count(body, key))
	}
	rest := body[strings.Index(body, key)+len("/invite/"):]
	n := 0
	for n < len(rest) && (n < 4 || isHexByte(rest[n])) {
		n++
	}
	token := rest[:n]
	if !strings.HasPrefix(token, "cbi_") || n < 8 {
		t.Fatal("invite link was not a token")
	}
	return token
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

func pendingInvites(t *testing.T, h *harness, userID string) int {
	t.Helper()
	rows, err := h.st.ListInvites(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
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
