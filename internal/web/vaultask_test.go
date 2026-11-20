package web

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/secretask"
	"github.com/scott/castbot/internal/store"
)

const (
	keyCanary  = "test-named-key-value"
	nextCanary = "test-named-key-next"
)

func TestNamedKeySaveCard(t *testing.T) {
	ip := "203.0.113.91:9"
	h := newHarness(t)
	user, secret := h.account("named-save@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("named-save-other@example.com", "Bea", "correct-horse-1")
	h.login("named-save@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	wren := vaultAgent(t, h.st, user.ID, "Wren")
	token := guestToken(t, h, agent)
	wrenToken := guestToken(t, h, wren)

	replyRoutine(t, h, user.ID, agent, "Connect it.\n\n"+secretFence("save", "x_api_key", "Monitor posts", "Watch posts", keyCanary))
	body := agentReplyBody(t, h, user.ID, agent.ID)
	if body != "Connect it." || strings.Contains(body, "castbot-secret") {
		t.Fatalf("reply %q", body)
	}
	assertNoKey(t, "reply", body)
	if _, err := h.st.SecretByName(context.Background(), user.ID, "X_API_KEY"); err != store.ErrNotFound {
		t.Fatal("fence stored a secret")
	}
	row := waitSecretCard(t, h, user.ID, agent.ID)
	if row.Connection != "X_API_KEY" || row.Label != "Monitor posts" {
		t.Fatalf("card %+v", row)
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	assertNoKey(t, "page", page)
	if code != 200 || !passwordField(page) || !strings.Contains(page, "Monitor posts") || !strings.Contains(page, "Watch posts") || !strings.Contains(page, "Save key") || !strings.Contains(page, "given to this agent") || !strings.Contains(page, "The agent does not see what you type.") || !strings.Contains(page, "expires in 60 minutes") || strings.Contains(page, "every agent you own") || strings.Contains(page, "Always approve") {
		t.Fatalf("card page %d", code)
	}

	code, card, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "empty", card)
	if code != 200 || !strings.Contains(card, "A key is required.") || !passwordField(card) {
		t.Fatalf("empty %d", code)
	}
	useSession(t, h, other.ID, ip)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{"value": {keyCanary}}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "other", card)
	if code != http.StatusNotFound {
		t.Fatalf("other user %d", code)
	}
	useSession(t, h, user.ID, ip)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", "value="+url.QueryEscape(keyCanary)+"&csrf=nope", ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "csrf", card)
	if code != http.StatusForbidden || !strings.Contains(card, "The form expired.") {
		t.Fatalf("csrf %d", code)
	}
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{"value": {keyCanary}}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "saved card", card)
	if code != 200 || !strings.Contains(card, "Saved. This agent can use it on the next turn.") || strings.Contains(card, `type="password"`) {
		t.Fatalf("save %d", code)
	}
	sec, err := h.st.SecretByName(context.Background(), user.ID, "X_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.st.DecryptSecret(context.Background(), user.ID, sec.ID)
	if err != nil || got != keyCanary {
		t.Fatal("saved value mismatch")
	}
	if !agentHasSecret(t, h, user.ID, agent.ID, "X_API_KEY") || agentHasSecret(t, h, user.ID, wren.ID, "X_API_KEY") {
		t.Fatal("grant scope")
	}
	code, page, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	assertNoKey(t, "chat", page)
	if code != 200 || strings.Contains(page, `type="password"`) || !strings.Contains(page, "Saved. This agent can use it on the next turn.") {
		t.Fatalf("settled page %d", code)
	}
	code, names, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/secrets", "", ip, nil)
	assertNoKey(t, "names", names)
	if code != 200 || !strings.Contains(names, "X_API_KEY") {
		t.Fatalf("names %d", code)
	}
	code, pull, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || !strings.Contains(pull, "X_API_KEY") || !strings.Contains(pull, keyCanary) {
		t.Fatal("machine pull missed the granted key")
	}
	code, settings, _ := h.do(http.MethodGet, "/settings/secrets", "", ip, nil)
	assertNoKey(t, "settings", settings)
	if code != 200 || !strings.Contains(settings, "X_API_KEY") {
		t.Fatalf("settings %d", code)
	}

	replyRoutine(t, h, user.ID, agent, secretFence("save", "X_API_KEY", "", "Again", ""))
	if agentReplyBody(t, h, user.ID, agent.ID) != "That key is already saved for this agent." || namedPending(t, h, user.ID, agent.ID, "X_API_KEY") != 0 {
		t.Fatal("second ask opened a card")
	}

	if err := h.st.RevokeSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	replyRoutine(t, h, user.ID, agent, secretFence("save", "X_API_KEY", "", "Give it to Atlas", ""))
	row = waitSecretCard(t, h, user.ID, agent.ID)
	code, page, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	assertNoKey(t, "saved key page", page)
	if code != 200 || !strings.Contains(page, "Use saved key") || !strings.Contains(page, "already in your vault") || !strings.Contains(page, "optional") {
		t.Fatalf("use saved %d", code)
	}
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "use saved", card)
	if code != 200 || !strings.Contains(card, "Saved. This agent can use it on the next turn.") {
		t.Fatalf("grant saved %d", code)
	}
	got, err = h.st.DecryptSecret(context.Background(), user.ID, sec.ID)
	if err != nil || got != keyCanary || !agentHasSecret(t, h, user.ID, agent.ID, "X_API_KEY") {
		t.Fatal("blank submit changed the key")
	}

	if err := h.st.GrantSecret(context.Background(), user.ID, wren.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.RevokeSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	replyRoutine(t, h, user.ID, agent, secretFence("save", "X_API_KEY", "", "Replace it", ""))
	row = waitSecretCard(t, h, user.ID, agent.ID)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{"value": {nextCanary}}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "replace card", card)
	if code != 200 || !strings.Contains(card, "Saved. This agent can use it on the next turn.") {
		t.Fatalf("replace %d", code)
	}
	got, err = h.st.DecryptSecret(context.Background(), user.ID, sec.ID)
	if err != nil || got != nextCanary {
		t.Fatal("replace did not store the new key")
	}
	for _, item := range []struct {
		id    string
		token string
	}{{agent.ID, token}, {wren.ID, wrenToken}} {
		code, pull, _ = h.do(http.MethodGet, "/api/castlet/agents/"+item.id+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + item.token})
		if code != 200 || !strings.Contains(pull, nextCanary) || strings.Contains(pull, keyCanary) {
			t.Fatal("pull after replace")
		}
	}
	code, page, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	assertNoKey(t, "chat after replace", page)
	if code != 200 {
		t.Fatalf("page %d", code)
	}
}

func TestNamedKeyDeleteCard(t *testing.T) {
	ip := "203.0.113.92:9"
	h := newHarness(t)
	user, secret := h.account("named-delete@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("named-delete-other@example.com", "Bea", "correct-horse-1")
	h.login("named-delete@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	wren := vaultAgent(t, h.st, user.ID, "Wren")
	token := guestToken(t, h, agent)
	wrenToken := guestToken(t, h, wren)
	sec, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{Name: "X_API_KEY", Value: keyCanary})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, wren.ID, sec.ID); err != nil {
		t.Fatal(err)
	}

	replyRoutine(t, h, user.ID, agent, secretFence("delete", "x_api_key", "", "Stop watching", ""))
	if agentReplyBody(t, h, user.ID, agent.ID) != "Approve the card to delete the key." {
		t.Fatal(agentReplyBody(t, h, user.ID, agent.ID))
	}
	row := waitDeleteCard(t, h, user.ID, agent.ID)
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	assertNoKey(t, "delete page", page)
	if code != 200 || !strings.Contains(page, "Delete X_API_KEY") || !strings.Contains(page, "Approving removes this key from your vault.") || !strings.Contains(page, "Every agent that has it loses it on the next turn.") || !strings.Contains(page, "10 minutes") || !strings.Contains(page, ">Approve<") || !strings.Contains(page, ">Deny<") || strings.Contains(page, "Always approve") || strings.Contains(page, `type="password"`) {
		t.Fatalf("delete card %d", code)
	}
	useSession(t, h, other.ID, ip)
	code, card, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-deletes/"+row.ID+"/approve", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "other delete", card)
	if code != http.StatusNotFound {
		t.Fatalf("other user %d", code)
	}
	useSession(t, h, user.ID, ip)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-deletes/"+row.ID+"/approve", "csrf=nope", ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "csrf delete", card)
	if code != http.StatusForbidden || !strings.Contains(card, "The form expired.") {
		t.Fatalf("csrf %d", code)
	}
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-deletes/"+row.ID+"/deny", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "deny", card)
	if code != 200 || !strings.Contains(card, "Denied") {
		t.Fatalf("deny %d", code)
	}
	if _, err := h.st.SecretByName(context.Background(), user.ID, "X_API_KEY"); err != nil {
		t.Fatal("deny removed the key")
	}
	code, pull, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || !strings.Contains(pull, keyCanary) {
		t.Fatal("deny dropped the pull")
	}

	replyRoutine(t, h, user.ID, agent, secretFence("delete", "X_API_KEY", "", "Stop watching", ""))
	row = waitDeleteCard(t, h, user.ID, agent.ID)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-deletes/"+row.ID+"/approve", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "deleted", card)
	if code != 200 || !strings.Contains(card, "Deleted") {
		t.Fatalf("approve %d", code)
	}
	if _, err := h.st.SecretByName(context.Background(), user.ID, "X_API_KEY"); err != store.ErrNotFound {
		t.Fatal("approve left the key")
	}
	for _, item := range []string{token, wrenToken} {
		_, pull, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + item})
		if strings.Contains(pull, keyCanary) || strings.Contains(pull, "X_API_KEY") {
			t.Fatal("pull kept the deleted key")
		}
	}
	_, wrenPull, _ := h.do(http.MethodGet, "/api/castlet/agents/"+wren.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + wrenToken})
	if strings.Contains(wrenPull, keyCanary) || strings.Contains(wrenPull, "X_API_KEY") {
		t.Fatal("other agent kept the deleted key")
	}

	replyRoutine(t, h, user.ID, agent, secretFence("delete", "GONE_API_KEY", "", "Missing", ""))
	if agentReplyBody(t, h, user.ID, agent.ID) != "There is no saved key with that name." || deletePending(t, h, user.ID, agent.ID) != 0 {
		t.Fatal("missing name opened a card")
	}

	sec, err = h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{Name: "RACE_API_KEY", Value: keyCanary})
	if err != nil {
		t.Fatal(err)
	}
	replyRoutine(t, h, user.ID, agent, secretFence("delete", "RACE_API_KEY", "", "Stop", ""))
	row = waitDeleteCard(t, h, user.ID, agent.ID)
	if err := h.st.DeleteSecret(context.Background(), user.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{Name: "RACE_API_KEY", Provider: "a", Value: keyCanary}); err != nil {
		t.Fatal(err)
	}
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-deletes/"+row.ID+"/approve", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "model approve", card)
	if code != 200 || !strings.Contains(card, "Denied") {
		t.Fatalf("model delete %d", code)
	}
	kept, err := h.st.SecretByName(context.Background(), user.ID, "RACE_API_KEY")
	if err != nil || kept.Provider != "a" {
		t.Fatal("approve removed a model key")
	}
}

func TestNamedKeyRefusals(t *testing.T) {
	ip := "203.0.113.93:9"
	h := newHarness(t)
	user, secret := h.account("named-refuse@example.com", "Ada", "correct-horse-1")
	h.login("named-refuse@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{Name: "PROVIDER_A_MAIN", Provider: "a", Value: keyCanary}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PROVIDER_A_MAIN", "GITHUB_CONNECTION", "GITHUB_TOKEN", "1API_KEY"} {
		replyRoutine(t, h, user.ID, agent, secretFence("save", name, "", "Watch posts", keyCanary))
		body := agentReplyBody(t, h, user.ID, agent.ID)
		assertNoKey(t, name, body)
		if body != "That name cannot be saved from chat." || namedPending(t, h, user.ID, agent.ID, strings.ToUpper(name)) != 0 {
			t.Fatalf("%s save %q", name, body)
		}
		replyRoutine(t, h, user.ID, agent, secretFence("delete", name, "", "Stop", ""))
		body = agentReplyBody(t, h, user.ID, agent.ID)
		if body != "That name cannot be saved from chat." || deletePending(t, h, user.ID, agent.ID) != 0 {
			t.Fatalf("%s delete %q", name, body)
		}
	}
	replyRoutine(t, h, user.ID, agent, secretFence("save", secretask.SampleName, "Service API", secretask.SampleReason, ""))
	if agentReplyBody(t, h, user.ID, agent.ID) != "Say which API to connect and I will ask for the key." || namedPending(t, h, user.ID, agent.ID, secretask.SampleName) != 0 {
		t.Fatal("sample fence opened a card")
	}
	replyRoutine(t, h, user.ID, agent, "Leave this.\n\n```castbot-secret\nnot json\n```\n")
	kept := agentReplyBody(t, h, user.ID, agent.ID)
	if !strings.Contains(kept, "Leave this.") || !strings.Contains(kept, "castbot-secret") || namedPending(t, h, user.ID, agent.ID, "X_API_KEY") != 0 {
		t.Fatalf("invalid fence %q", kept)
	}
	exp := h.st.Now().Add(time.Hour)
	for i := 0; i < store.SecretRequestLimit; i++ {
		row, _, err := h.st.CreateNamedSecretRequest(context.Background(), user.ID, agent.ID, "", "", "LIMIT_KEY", "", "n", exp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.ResolveSecretRequest(context.Background(), user.ID, row.ID, "declined"); err != nil {
			t.Fatal(err)
		}
	}
	replyRoutine(t, h, user.ID, agent, secretFence("save", "LIMIT_KEY", "", "Again", ""))
	if agentReplyBody(t, h, user.ID, agent.ID) != "That request was declined." || namedPending(t, h, user.ID, agent.ID, "LIMIT_KEY") != 0 {
		t.Fatal("limit opened a card")
	}
}

func TestNamedKeyMachineWaitsForTheCard(t *testing.T) {
	ip := "203.0.113.94:9"
	h := newHarness(t)
	user, secret := h.account("named-machine@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("named-machine-other@example.com", "Bea", "correct-horse-1")
	h.login("named-machine@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	otherAgent := vaultAgent(t, h.st, other.ID, "Bea")
	token := guestToken(t, h, agent)
	otherToken := guestToken(t, h, otherAgent)
	sec, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{Name: "READY_API_KEY", Value: "test-ready-key-value"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{Name: "PROVIDER_A_MAIN", Provider: "a", Value: keyCanary}); err != nil {
		t.Fatal(err)
	}
	code, status := machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"action":"save","name":"READY_API_KEY","reason":"Again"}`)
	assertNoKey(t, "already", status)
	if code != 200 || !strings.Contains(status, "already_configured") || namedPending(t, h, user.ID, agent.ID, "READY_API_KEY") != 0 {
		t.Fatalf("already %d", code)
	}
	code, status = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"action":"save","name":"PROVIDER_A_MAIN","reason":"model"}`)
	assertNoKey(t, "model", status)
	if code != 200 || !strings.Contains(status, "rejected") {
		t.Fatalf("model %d %s", code, status)
	}
	code, status = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"action":"delete","name":"GONE_API_KEY","reason":"missing"}`)
	if code != 200 || !strings.Contains(status, "not_found") {
		t.Fatalf("missing %d %s", code, status)
	}
	code, status = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"connection":"aws","reason":"no"}`)
	if code != http.StatusForbidden || !strings.Contains(status, "bad_request") {
		t.Fatalf("aws %d %s", code, status)
	}
	code, status = machinePost(t, h, ip, otherToken, agent.ID, "/egress/secret-request", `{"action":"save","name":"X_API_KEY","reason":"no"}`)
	if code != http.StatusNotFound {
		t.Fatalf("other token %d %s", code, status)
	}

	var saveStatus string
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, saveStatus = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"action":"save","name":"X_API_KEY","label":"Monitor posts","reason":"Watch posts","value":"`+keyCanary+`"}`)
	}()
	row := waitSecretCard(t, h, user.ID, agent.ID)
	if _, err := h.st.SecretByName(context.Background(), user.ID, "X_API_KEY"); err != store.ErrNotFound {
		t.Fatal("tool body stored a secret")
	}
	replyRoutine(t, h, user.ID, agent, secretFence("save", "X_API_KEY", "", "Watch posts", keyCanary))
	if namedPending(t, h, user.ID, agent.ID, "X_API_KEY") != 1 {
		t.Fatal("tool and fence opened two cards")
	}
	code, card, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || !strings.Contains(card, "A key is required.") {
		t.Fatalf("empty %d", code)
	}
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{"value": {keyCanary}}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "machine save", card)
	if code != 200 || !strings.Contains(card, "Saved. This agent can use it on the next turn.") {
		t.Fatalf("submit %d", code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("save wait")
	}
	assertNoKey(t, "tool result", saveStatus)
	if !strings.Contains(saveStatus, `"saved"`) {
		t.Fatal("tool result")
	}
	code, pull, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || !strings.Contains(pull, keyCanary) {
		t.Fatal("pull after the tool card")
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	assertNoKey(t, "chat", page)
	if code != 200 {
		t.Fatalf("page %d", code)
	}

	done = make(chan struct{})
	go func() {
		defer close(done)
		_, saveStatus = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"action":"delete","name":"X_API_KEY","reason":"Stop watching"}`)
	}()
	del := waitDeleteCard(t, h, user.ID, agent.ID)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-deletes/"+del.ID+"/approve", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	assertNoKey(t, "machine delete", card)
	if code != 200 || !strings.Contains(card, "Deleted") {
		t.Fatalf("delete %d", code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("delete wait")
	}
	assertNoKey(t, "delete result", saveStatus)
	if !strings.Contains(saveStatus, `"deleted"`) {
		t.Fatal("delete result")
	}
	_, pull, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if strings.Contains(pull, keyCanary) || strings.Contains(pull, "X_API_KEY") {
		t.Fatal("pull kept the deleted key")
	}
}

func TestServedBundleCarriesAPIKeySkill(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("named-bundle@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.95:9"
	code, packed, hdr := h.do(http.MethodGet, "/api/castlet/agents/"+caller.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK {
		t.Fatalf("bundle %d", code)
	}
	if hdr.Get("X-Castbot-Bundle-SHA256") != bundle.Sum([]byte(packed)) {
		t.Fatal("served sha does not match the body")
	}
	stored, err := h.st.LatestBundle(context.Background(), user.ID, caller.ID)
	if err != nil || strings.Contains(stored.Instructions, secretask.Heading) {
		t.Fatal("stored bundle was rewritten")
	}
	dir := t.TempDir()
	if err := bundle.Install(dir, []byte(packed)); err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(dir, secretask.SkillFile))
	if err != nil || !strings.Contains(string(skill), secretask.SkillName) || !strings.Contains(string(skill), secretask.Fence) {
		t.Fatal("api key skill was not installed")
	}
	notes, err := os.ReadFile(filepath.Join(dir, "instructions.md"))
	if err != nil || !strings.Contains(string(notes), secretask.Heading) || !strings.Contains(string(notes), "Be brief.") {
		t.Fatal("guide missing")
	}
	tr := tar.NewReader(bytes.NewReader([]byte(packed)))
	saw := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "agent.yaml" {
			body, _ := io.ReadAll(tr)
			text := string(body)
			for _, name := range []string{secretask.ToolSave, secretask.ToolDelete, agentskill.ToolCreate, routinetool.ToolCreate} {
				if !strings.Contains(text, name) || !strings.Contains(text, "always-allow") {
					t.Fatalf("served yaml missing %s\n%s", name, text)
				}
			}
			saw = true
		}
	}
	if !saw {
		t.Fatal("served tar has no agent.yaml")
	}
}

func secretFence(action, name, label, reason, value string) string {
	fields := map[string]string{"action": action, "name": name}
	if label != "" {
		fields["label"] = label
	}
	if reason != "" {
		fields["reason"] = reason
	}
	if value != "" {
		fields["value"] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return "```castbot-secret\n" + string(raw) + "\n```"
}

func assertNoKey(t *testing.T, where, text string) {
	t.Helper()
	for _, key := range []string{keyCanary, nextCanary} {
		if strings.Contains(text, key) {
			t.Fatalf("%s held the key", where)
		}
	}
}

func passwordField(page string) bool {
	i := strings.Index(page, `type="password"`)
	if i < 0 {
		return false
	}
	end := strings.Index(page[i:], ">")
	if end < 0 {
		return true
	}
	return !strings.Contains(page[i:i+end], "value=")
}

func namedPending(t *testing.T, h *harness, userID, agentID, name string) int {
	t.Helper()
	rows, err := h.st.ListSecretRequests(context.Background(), userID, agentID, h.st.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Status == "pending" && row.Connection == name {
			n++
		}
	}
	return n
}

func deletePending(t *testing.T, h *harness, userID, agentID string) int {
	t.Helper()
	rows, err := h.st.ListSecretDeletes(context.Background(), userID, agentID, h.st.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Status == "pending" {
			n++
		}
	}
	return n
}

func waitDeleteCard(t *testing.T, h *harness, userID, agentID string) store.SecretDelete {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := h.st.ListSecretDeletes(context.Background(), userID, agentID, h.st.Now().Add(-24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Status == "pending" {
				return row
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("no delete card")
	return store.SecretDelete{}
}

func agentHasSecret(t *testing.T, h *harness, userID, agentID, name string) bool {
	t.Helper()
	rows, err := h.st.GrantedMeta(context.Background(), userID, agentID)
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
