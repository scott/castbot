package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/store"
)

func TestCommandOptInDefaultDeny(t *testing.T) {
	ip := "203.0.113.81:9"
	otherIP := "203.0.113.82:9"
	h := newHarness(t)
	user, secret := h.account("ada-tools@example.com", "Ada", "correct-horse-1")
	h.login("ada-tools@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.PutSecret(context.Background(), user.ID, "NOTES_TOKEN", "fixture-iso-notes")
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/settings/secrets/item/"+sec.ID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Available to commands") || strings.Contains(body, `name="commands" value="1" checked`) {
		t.Fatalf("detail %d", code)
	}
	if strings.Contains(body, "fixture-iso-notes") {
		t.Fatal("detail rendered a value")
	}
	ctx := context.Background()
	secRow, err := h.st.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: secRow.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(ctx, user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(ctx, agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	code, secrets, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK || !strings.Contains(secrets, "NOTES_TOKEN") || strings.Contains(secrets, `"tools"`) {
		t.Fatalf("secrets before opt-in %d %s", code, secrets)
	}
	if !strings.Contains(secrets, "fixture-iso-notes") {
		t.Fatal("runtime still needs the value")
	}
	code, _, hdr := h.do(http.MethodPost, "/settings/secrets/item/"+sec.ID+"/commands", h.form(url.Values{"commands": {"1"}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/settings/secrets/item/"+sec.ID {
		t.Fatalf("opt-in %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/settings/secrets/item/"+sec.ID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, `name="commands" value="1" checked`) || strings.Contains(body, "fixture-iso-notes") {
		t.Fatalf("checked %d", code)
	}
	code, secrets, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK || !strings.Contains(secrets, `"tools":true`) {
		t.Fatalf("secrets after opt-in %d %s", code, secrets)
	}
	model, err := h.st.PutSecret(ctx, user.ID, "OPENROUTER_API_KEY", "fixture-iso-model")
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodPost, "/settings/secrets/item/"+model.ID+"/commands", h.form(url.Values{"commands": {"1"}}), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Commands cannot use this secret.") || strings.Contains(body, "fixture-iso-model") {
		t.Fatalf("model name %d %s", code, body)
	}
	other, otherSecret := h.account("bea-tools@example.com", "Bea", "correct-horse-2")
	h.login("bea-tools@example.com", "correct-horse-2", otherSecret, otherIP, false)
	code, _, _ = h.do(http.MethodPost, "/settings/secrets/item/"+sec.ID+"/commands", h.form(url.Values{"commands": {"1"}}), otherIP, nil)
	if code != http.StatusNotFound {
		t.Fatalf("other user %d", code)
	}
	_ = other
	rows, err := h.st.SecretAudit(ctx, user.ID, sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Detail, "fixture-iso") {
			t.Fatal("audit stored a value")
		}
	}
}
