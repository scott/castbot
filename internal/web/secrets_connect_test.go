package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

func TestSecretsConnectionSaveSatisfiesPendingCard(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.79:9"
	user, secret := h.account("secrets-connect@example.com", "Ada", "correct-horse-1")
	h.login("secrets-connect@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), user.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	yaml := "name: Atlas\nbrain:\n  model: " + model + "\n  instructions_file: instructions.md\ntools:\n  builtin: []\n"
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", yaml, "Be concise."); err != nil {
		t.Fatal(err)
	}
	row, _, err := h.st.CreateSecretRequest(context.Background(), user.ID, agent.ID, thread.ID, runRow.ID, connect.DigitalOcean, "List droplets", h.st.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	const canary = "vault-sample-token"
	code, body, _ := h.do(http.MethodPost, "/settings/secrets", h.form(url.Values{
		"name": {"DIGITALOCEAN_API_TOKEN"}, "value": {"  " + canary + "\n"},
		"agent": {agent.ID},
	}), ip, nil)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, connectionStayCopy) || strings.Contains(body, "Use capital letters") || strings.Contains(body, canary) {
		t.Fatalf("offer %d", code)
	}
	if _, err := h.st.SecretByName(context.Background(), user.ID, "DIGITALOCEAN_API_TOKEN"); err == nil {
		t.Fatal("offer created an env row")
	}
	code, body, _ = h.do(http.MethodPost, "/settings/secrets", h.form(url.Values{
		"name": {"DIGITALOCEAN_CONNECTION"}, "value": {canary},
	}), ip, nil)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, connectionStayCopy) || strings.Contains(body, "Use capital letters") || strings.Contains(body, canary) {
		t.Fatalf("vault name offer %d", code)
	}

	code, _, _ = h.do(http.MethodPost, "/settings/secrets", h.form(url.Values{
		"connection": {"digitalocean"}, "value": {"  " + canary + "\n"},
		"agent": {agent.ID},
	}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("save %d", code)
	}
	saved, err := h.st.ConnectionSecret(context.Background(), user.ID, connect.DigitalOcean)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != connect.SecretNameDigitalOcean {
		t.Fatal("catalog name")
	}
	kind, err := h.st.SecretKind(context.Background(), user.ID, saved.ID)
	if err != nil || kind != "connection" {
		t.Fatalf("kind %s", kind)
	}
	got, err := h.st.ConnectionValue(context.Background(), user.ID, connect.DigitalOcean)
	if err != nil || got != canary {
		t.Fatal("trimmed value")
	}
	on, err := h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, connect.DigitalOcean)
	if err != nil || !on {
		t.Fatal("enabled")
	}
	resolved, err := h.st.GetSecretRequest(context.Background(), user.ID, row.ID)
	if err != nil || resolved.Status != "saved" {
		t.Fatal("request")
	}
	user, err = h.st.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	cards, err := h.srv.cardsHTML(context.Background(), user, runRow.ID, "csrf")
	if err != nil || !strings.Contains(cards, "Saved") || strings.Contains(cards, canary) || strings.Contains(cards, "secret-in-") {
		t.Fatal("card did not flip to saved")
	}
	token := guestToken(t, h, agent)
	code, pull, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || strings.Contains(pull, canary) || strings.Contains(pull, "DIGITALOCEAN_CONNECTION") || strings.Contains(pull, "DIGITALOCEAN_API_TOKEN") {
		t.Fatalf("pull %d", code)
	}
	code, packed, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || strings.Contains(packed, canary) || strings.Contains(packed, "DIGITALOCEAN_CONNECTION") {
		t.Fatalf("bundle %d", code)
	}
	b, err := h.st.LatestBundle(context.Background(), user.ID, agent.ID)
	if err != nil || strings.Contains(b.YAML, canary) || strings.Contains(b.YAML, "DIGITALOCEAN_CONNECTION") {
		t.Fatal("stored bundle")
	}
	for _, extra := range b.Extras {
		if strings.Contains(extra, canary) || strings.Contains(extra, "DIGITALOCEAN_CONNECTION") {
			t.Fatal("shim")
		}
	}
	code, status, _ := h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/egress/secret-request", `{"connection":"digitalocean","reason":"again"}`, ip, map[string]string{
		"Authorization": "Bearer " + token, "Content-Type": "application/json",
	})
	if code != 200 || !strings.Contains(status, "already_configured") || strings.Contains(status, canary) {
		t.Fatalf("already %d", code)
	}

	_, otherSecret := h.account("secrets-other@example.com", "Bea", "correct-horse-1")
	h.login("secrets-other@example.com", "correct-horse-1", otherSecret, ip, false)
	code, _, _ = h.do(http.MethodPost, "/settings/secrets/item/"+saved.ID+"/move", h.form(nil), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("other user move %d", code)
	}
	if _, err := h.st.ConnectionSecret(context.Background(), user.ID, connect.DigitalOcean); err != nil {
		t.Fatal("owner row")
	}
}

func TestPlainProviderEnvDoesNotConnect(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.80:9"
	user, secret := h.account("secrets-env@example.com", "Ada", "correct-horse-1")
	h.login("secrets-env@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	const canary = "vault-sample-token"
	env, err := h.st.PutSecret(context.Background(), user.ID, "DIGITALOCEAN_API_TOKEN", canary)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, env.ID); err == nil {
		t.Fatal("grant")
	}
	on, err := h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, connect.DigitalOcean)
	if err != nil || on {
		t.Fatal("enabled")
	}
	code, page, _ := h.do(http.MethodGet, "/settings/secrets", "", ip, nil)
	if code != 200 || !strings.Contains(page, "Move to the connection vault") || !strings.Contains(page, connectionStayCopy) || strings.Contains(page, canary) {
		t.Fatalf("move offer %d", code)
	}
	token := guestToken(t, h, agent)
	code, pull, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || strings.Contains(pull, canary) || strings.Contains(pull, "DIGITALOCEAN_API_TOKEN") {
		t.Fatalf("pull %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/settings/secrets/item/"+env.ID+"/move", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("move %d", code)
	}
	if _, err := h.st.SecretByName(context.Background(), user.ID, "DIGITALOCEAN_API_TOKEN"); err == nil {
		t.Fatal("env row remained")
	}
	moved, err := h.st.ConnectionSecret(context.Background(), user.ID, connect.DigitalOcean)
	if err != nil || moved.Name != connect.SecretNameDigitalOcean {
		t.Fatal("moved")
	}
	kind, err := h.st.SecretKind(context.Background(), user.ID, moved.ID)
	if err != nil || kind != "connection" {
		t.Fatal("kind")
	}
	got, err := h.st.ConnectionValue(context.Background(), user.ID, connect.DigitalOcean)
	if err != nil || got != canary {
		t.Fatal("value")
	}
	on, err = h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, connect.DigitalOcean)
	if err != nil || on {
		t.Fatal("move enabled an agent that was not granted and had no card")
	}
	code, pull, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || strings.Contains(pull, canary) || strings.Contains(pull, "DIGITALOCEAN_CONNECTION") {
		t.Fatalf("pull after move %d", code)
	}
}

func TestSecretSubmitTrimsWhitespace(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.81:9"
	user, secret := h.account("secret-trim@example.com", "Ada", "correct-horse-1")
	h.login("secret-trim@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	row, _, err := h.st.CreateSecretRequest(context.Background(), user.ID, agent.ID, "", "", connect.GitHub, "Read issues", h.st.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	const canary = "vault-sample-token"
	code, card, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{
		"value": {" \t" + canary + "\n"},
	}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || strings.Contains(card, canary) || !strings.Contains(card, "Saved") {
		t.Fatalf("submit %d", code)
	}
	got, err := h.st.ConnectionValue(context.Background(), user.ID, connect.GitHub)
	if err != nil || got != canary {
		t.Fatal("trimmed")
	}
}
