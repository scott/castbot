package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/secretstool"
	"github.com/scott/castbot/internal/store"
)

const vaultCanary = "vault-tool-canary"

func TestMachineVaultGrantsRevokesAndDeletes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("vault-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("vault-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	sibToken := guestToken(t, h, sibling)
	ip := "203.0.113.94:9"
	if _, err := h.st.PutSecret(ctx, user.ID, "NOTES_TOKEN", vaultCanary); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutSecret(ctx, other.ID, "NOTES_TOKEN", vaultCanary); err != nil {
		t.Fatal(err)
	}

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(secretstool.ToolGrant, map[string]any{
		"secret": "NOTES_TOKEN", "agent": "Ada", "value": vaultCanary,
	}))
	if code != http.StatusNotFound || secretHeld(t, h, user.ID, caller.ID, "NOTES_TOKEN") {
		t.Fatalf("other token %d %s", code, redactVault(body))
	}
	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(secretstool.ToolGrant, map[string]any{
		"secret": "NOTES_TOKEN", "agent": "Pip", "user_id": other.ID,
	}))
	if code != http.StatusNotFound || secretHeld(t, h, user.ID, sibling.ID, "NOTES_TOKEN") {
		t.Fatalf("sibling path %d %s", code, redactVault(body))
	}

	code, body = machinePost(t, h, ip, sibToken, sibling.ID, "/agents", routinePayload(secretstool.ToolGrant, map[string]any{
		"secret": "NOTES_TOKEN", "agent": "Pip", "value": vaultCanary, "user_id": other.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Granted NOTES_TOKEN to Pip. No new machine.") || strings.Contains(body, vaultCanary) {
		t.Fatalf("grant %d %s", code, redactVault(body))
	}
	if !secretHeld(t, h, user.ID, sibling.ID, "NOTES_TOKEN") || secretHeld(t, h, user.ID, caller.ID, "NOTES_TOKEN") {
		t.Fatal("grant scope")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolGrant, map[string]any{
		"secret": "NOTES_TOKEN", "agent": "",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Say which agent.") || secretHeld(t, h, user.ID, caller.ID, "NOTES_TOKEN") {
		t.Fatalf("empty agent %d %s", code, redactVault(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolGrant, map[string]any{
		"secret": "NOTES_TOKEN", "agent": "Ada",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Granted NOTES_TOKEN to Ada.") {
		t.Fatalf("grant ada %d %s", code, redactVault(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolRevoke, map[string]any{
		"secret": "NOTES_TOKEN", "agent": "Pip", "value": vaultCanary,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Revoked NOTES_TOKEN from Pip.") || !strings.Contains(body, "The secret stays in the vault.") || strings.Contains(body, vaultCanary) {
		t.Fatalf("revoke %d %s", code, redactVault(body))
	}
	if secretHeld(t, h, user.ID, sibling.ID, "NOTES_TOKEN") || !secretHeld(t, h, user.ID, caller.ID, "NOTES_TOKEN") {
		t.Fatal("revoke dropped the wrong grant")
	}
	if _, err := h.st.SecretByName(ctx, user.ID, "NOTES_TOKEN"); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolRevoke, map[string]any{
		"secret": "NOTES_TOKEN", "agent": "Pip",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That agent does not have this secret.") {
		t.Fatalf("again %d %s", code, redactVault(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolDelete, map[string]any{}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Say which secret.") {
		t.Fatalf("empty delete %d %s", code, redactVault(body))
	}
	if _, err := h.st.SecretByName(ctx, user.ID, "NOTES_TOKEN"); err != nil {
		t.Fatal("empty delete removed the secret")
	}
	code, body = machinePost(t, h, ip, otherToken, otherAgent.ID, "/agents", routinePayload(secretstool.ToolDelete, map[string]any{
		"secret": "NOTES_TOKEN",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Deleted NOTES_TOKEN.") || !strings.Contains(body, "No agent had it.") || strings.Contains(body, vaultCanary) {
		t.Fatalf("other delete %d %s", code, redactVault(body))
	}
	if _, err := h.st.SecretByName(ctx, user.ID, "NOTES_TOKEN"); err != nil {
		t.Fatal("other account deleted this secret")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolDelete, map[string]any{
		"secret": "NOTES_TOKEN", "value": vaultCanary, "user_id": other.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Deleted NOTES_TOKEN.") || !strings.Contains(body, "Ada lose it.") || strings.Contains(body, vaultCanary) {
		t.Fatalf("delete %d %s", code, redactVault(body))
	}
	if _, err := h.st.SecretByName(ctx, user.ID, "NOTES_TOKEN"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("secret stayed")
	}
	if secretHeld(t, h, user.ID, caller.ID, "NOTES_TOKEN") {
		t.Fatal("grant stayed after delete")
	}
}

func TestMachineVaultMovesConnectionToken(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("vault-move@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.95:9"
	if _, err := h.st.PutSecret(ctx, user.ID, "NOTES_TOKEN", vaultCanary); err != nil {
		t.Fatal(err)
	}
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolMove, map[string]any{
		"secret": "NOTES_TOKEN", "value": vaultCanary,
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "cannot move") || strings.Contains(body, vaultCanary) {
		t.Fatalf("plain %d %s", code, redactVault(body))
	}
	if _, err := h.st.SecretByName(ctx, user.ID, "NOTES_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutSecret(ctx, user.ID, "GITHUB_TOKEN", vaultCanary); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolMove, map[string]any{
		"secret": "GITHUB_TOKEN", "value": vaultCanary, "token": vaultCanary,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Moved GITHUB_TOKEN into the connection vault.") || !strings.Contains(body, "The agent does not see it.") || strings.Contains(body, vaultCanary) {
		t.Fatalf("move %d %s", code, redactVault(body))
	}
	if _, err := h.st.SecretByName(ctx, user.ID, "GITHUB_TOKEN"); err == nil {
		t.Fatal("alias stayed")
	}
	got, err := h.st.ConnectionValue(ctx, user.ID, connect.GitHub)
	if err != nil || got != vaultCanary {
		t.Fatal("connection vault")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(secretstool.ToolMove, map[string]any{
		"secret": connect.SecretNameGitHub,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "already in the connection vault") || strings.Contains(body, vaultCanary) {
		t.Fatalf("again %d %s", code, redactVault(body))
	}
}

func TestMachineVaultRefusesReplace(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("vault-replace@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	if _, err := h.st.PutSecret(ctx, user.ID, "NOTES_TOKEN", vaultCanary); err != nil {
		t.Fatal(err)
	}
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-vault", "running", fake)
	if _, _, err := h.st.BeginReplacement(ctx, user.ID, caller.ID, "web:test", "example.invalid/castbot-runtime:next", false); err != nil {
		t.Fatal(err)
	}
	code, body := machinePost(t, h, "203.0.113.96:9", token, caller.ID, "/agents", routinePayload(secretstool.ToolGrant, map[string]any{
		"secret": "NOTES_TOKEN", "agent": "Ada",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, replaceSentence) || secretHeld(t, h, user.ID, caller.ID, "NOTES_TOKEN") {
		t.Fatalf("replace %d %s", code, redactVault(body))
	}
}

func TestVaultFenceGrantsOneSecret(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("vault-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	if _, err := h.st.PutSecret(ctx, user.ID, "NOTES_TOKEN", vaultCanary); err != nil {
		t.Fatal(err)
	}
	invalid := "Leave this.\n\n```castbot-secrets\n{\"action\":\"grant_secret\",\"secret\":\"SECRET\",\"agent\":\"AGENT\"}\n```\n"
	replyRoutine(t, h, user.ID, caller, invalid)
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-secrets") || !strings.Contains(kept, "Leave this.") || secretHeld(t, h, user.ID, caller.ID, "NOTES_TOKEN") {
		t.Fatalf("invalid fence removed %s", redactVault(kept))
	}
	replyRoutine(t, h, user.ID, caller, "Giving it.\n\n```castbot-secrets\n{\"action\":\"grant_secret\",\"secret\":\"NOTES_TOKEN\",\"agent\":\"Ada\",\"value\":\""+vaultCanary+"\",\"user_id\":\"other\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-secrets") || !strings.Contains(body, "Giving it.") || strings.Contains(body, vaultCanary) || !secretHeld(t, h, user.ID, caller.ID, "NOTES_TOKEN") {
		t.Fatalf("fence %s", redactVault(body))
	}
}

func secretHeld(t *testing.T, h *harness, userID, agentID, name string) bool {
	t.Helper()
	rows, err := h.st.GrantedSecrets(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == name {
			return true
		}
	}
	return false
}

func redactVault(body string) string {
	return strings.ReplaceAll(body, vaultCanary, "redacted")
}
