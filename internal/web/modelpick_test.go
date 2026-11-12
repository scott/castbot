package web

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/castcfg"
)

func TestAdvancedKeepsTheCardModel(t *testing.T) {
	h, _, _ := createLab(t)
	user, secret := h.account("qa01@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.41:9"
	h.login("qa01@example.com", "correct-horse-1", secret, ip, false)
	models, err := castcfg.Models()
	if err != nil || len(models) < 2 {
		t.Fatal(err)
	}
	second := models[1].ID
	outside := "legacy-model-not-in-catalog"
	if castcfg.KnownModel(outside) {
		t.Fatal("fixture model is in the catalog")
	}
	for _, model := range []string{second, outside} {
		saveCard(t, h, user.ID, model)
		code, body, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
		if code != http.StatusOK {
			t.Fatalf("%s %d", model, code)
		}
		got := selectedOption(t, body, "model")
		if got != model {
			t.Fatalf("advanced selected %q, card model %q", got, model)
		}
		if got == models[0].ID {
			t.Fatal("advanced used the first catalog entry")
		}
	}
}

func saveCard(t *testing.T, h *harness, userID, model string) {
	t.Helper()
	doc := draftDoc{Card: &setupState{
		Name: "Scout", Title: "Helper", Job: "Check the docs",
		Instructions: "Check the docs.", Model: model,
		Size: createSize, Idle: createIdle,
	}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SaveCreateDraft(context.Background(), userID, string(raw)); err != nil {
		t.Fatal(err)
	}
}

func selectedOption(t *testing.T, body, name string) string {
	t.Helper()
	block := regexp.MustCompile(`(?s)<select[^>]*name="` + regexp.QuoteMeta(name) + `"[^>]*>(.*?)</select>`).FindStringSubmatch(body)
	if block == nil {
		t.Fatalf("no select %s", name)
	}
	var got string
	n := 0
	for _, row := range regexp.MustCompile(`<option value="([^"]*)"([^>]*)>`).FindAllStringSubmatch(block[1], -1) {
		if strings.Contains(row[2], "selected") {
			got = html.UnescapeString(row[1])
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s selected %d times", name, n)
	}
	return got
}
