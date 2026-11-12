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
	"github.com/scott/castbot/internal/store"
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

func TestEditKeepsAModelOutsideTheCatalog(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("qa02@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.42:9"
	h.login("qa02@example.com", "correct-horse-1", secret, ip, false)
	const kept = "legacy-model-not-in-catalog"
	if castcfg.KnownModel(kept) {
		t.Fatal("fixture model is in the catalog")
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Scout", Title: "Helper", Model: kept, Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, bare, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != http.StatusOK || selectedOption(t, bare, "model") != kept {
		t.Fatalf("edit without a bundle selected %q (%d)", selectedOption(t, bare, "model"), code)
	}
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", ":\n", "Check the docs.\n"); err != nil {
		t.Fatal(err)
	}
	code, broken, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != http.StatusOK || selectedOption(t, broken, "model") != kept {
		t.Fatalf("edit with a broken file selected %q", selectedOption(t, broken, "model"))
	}
	yaml := "name: Scout\nbrain:\n  model: " + kept + "\n"
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "2", yaml, "Check the docs.\n"); err != nil {
		t.Fatal(err)
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != http.StatusOK || selectedOption(t, page, "model") != kept {
		t.Fatalf("edit did not preselect %s", kept)
	}
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Scout"}, "title": {"Helper"}, "section": {sec.ID},
		"instructions": {"Check the docs."},
	}
	code, body, _ := h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("empty model post %d %s", code, snippet(body))
	}
	assertModel(t, h, user.ID, agent.ID, kept)
	fields["model"] = []string{kept}
	code, body, _ = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("same model post %d %s", code, snippet(body))
	}
	assertModel(t, h, user.ID, agent.ID, kept)
	fields["model"] = []string{"another-legacy-model"}
	code, body, _ = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "That model is not in the provider manifest.") {
		t.Fatalf("other model %d %s", code, snippet(body))
	}
	assertModel(t, h, user.ID, agent.ID, kept)

	fields = map[string][]string{
		"form": {"agent"}, "confirm": {"advanced"}, "name": {"Relay"}, "title": {"Helper"},
		"section": {sec.ID}, "model": {"another-legacy-model"}, "instructions": {"Check the docs."},
	}
	code, body, _ = h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "That model is not in the provider manifest.") {
		t.Fatalf("new agent %d %s", code, snippet(body))
	}
	rows, err := h.st.ListAgents(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != agent.ID || rows[0].Model != kept {
		t.Fatalf("new agent wrote a model: %+v", rows)
	}
}

func assertModel(t *testing.T, h *harness, userID, agentID, model string) {
	t.Helper()
	got, err := h.st.GetAgent(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != model {
		t.Fatalf("agent model %q", got.Model)
	}
	bundle, err := h.st.LatestBundle(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := castcfg.ParseYAML(bundle.YAML)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Model != model {
		t.Fatalf("bundle model %q", spec.Model)
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
