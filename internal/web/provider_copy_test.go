package web

import (
	"context"
	"html"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/castcfg"
)

func TestPagesDoNotSayProviderA(t *testing.T) {
	const secret = "server-key-sample-not-a-real-token"
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), secret)
	ip := "203.0.113.40:9"
	h, _, desc := createLab(t)
	user, session := h.account("copy@example.com", "Ada", "correct-horse-1")
	h.login("copy@example.com", "correct-horse-1", session, ip, false)
	_ = desc

	slot, ok := slotForModel(defaultModelID())
	if !ok || slot.ID != "a" || slot.Title != "OpenRouter" || slot.Manifest != "openrouter" || slot.Letter != "OR" {
		t.Fatalf("slot %+v %v", slot, ok)
	}
	if _, ok := slotForModel("Provider A / model-large"); ok {
		t.Fatal("placeholder still selected a slot")
	}
	if _, ok := slotForModel("Provider B / model-small"); ok {
		t.Fatal("provider b still selected a slot")
	}

	pages := []string{"/settings", "/settings/secrets", "/new-agent", "/new-agent?advanced=1"}
	for _, path := range pages {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusOK {
			t.Fatalf("%s %d", path, code)
		}
		assertNoPlaceholder(t, path, body, secret)
	}
	code, settings, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(settings, "OpenRouter") || !strings.Contains(settings, ">OR<") {
		t.Fatal("settings summary did not name OpenRouter")
	}
	code, secrets, _ := h.do(http.MethodGet, "/settings/secrets", "", ip, nil)
	shown := html.UnescapeString(secrets)
	if code != http.StatusOK || !strings.Contains(shown, serverKeyLabel) || !strings.Contains(shown, "OpenRouter") || !strings.Contains(shown, "DigitalOcean") || !strings.Contains(shown, "Google Gemini") || !strings.Contains(shown, "xAI") {
		t.Fatal("secrets page did not explain the server key")
	}

	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Make me an agent called Scout that checks example-org/docs for broken links.")), ip, nil)
	card := follow(t, h, ip, code, hdr)
	assertNoPlaceholder(t, "card", card, secret)
	label := modelLabel(defaultModelID())
	if !strings.Contains(card, label) || !strings.Contains(card, "OpenRouter") || strings.Contains(html.UnescapeString(card), "Provider A") {
		t.Fatal("card did not name the model")
	}
	if strings.Contains(card, "Add a model key first") || strings.Contains(card, "your own OpenRouter key") || strings.Contains(card, "disabled") {
		t.Fatal("server key still blocked the card")
	}

	agent := vaultAgent(t, h.st, user.ID, "Scout")
	model := defaultModelID()
	if err := h.st.SetAgentModel(context.Background(), user.ID, agent.ID, model); err != nil {
		t.Fatal(err)
	}
	code, chat, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(chat, model) {
		t.Fatalf("chat %d", code)
	}
	assertNoPlaceholder(t, "chat", chat, secret)
	for _, path := range []string{
		"/agents/" + agent.ID + "/edit",
		"/agents/" + agent.ID + "?panel=secrets",
		"/agents/" + agent.ID + "?panel=agent",
	} {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusOK {
			t.Fatalf("%s %d", path, code)
		}
		assertNoPlaceholder(t, path, body, secret)
		if !strings.Contains(html.UnescapeString(body), "OpenRouter") {
			t.Fatalf("%s did not name OpenRouter", path)
		}
	}
	code, pane, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=agent", "", ip, nil)
	if !strings.Contains(html.UnescapeString(pane), serverKeyLabel) {
		t.Fatal("agent panel did not explain the server key")
	}
}

func assertNoPlaceholder(t *testing.T, where, body, secret string) {
	t.Helper()
	shown := html.UnescapeString(body)
	if strings.Contains(shown, "Provider A") || strings.Contains(shown, "Provider B") || strings.Contains(shown, "From the server") {
		t.Fatalf("%s still has placeholder copy", where)
	}
	if secret != "" && strings.Contains(body, secret) {
		t.Fatalf("%s showed the server key", where)
	}
}
