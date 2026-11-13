package web

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestPickerListsOnlyConfiguredProviders(t *testing.T) {
	const key = "fixture-or-only"
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), key)
	h := newHarness(t)
	user, secret := h.account("or-only@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.61:9"
	h.login("or-only@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if strings.Contains(body, key) || selectDisabled(t, body, "provider") || selectDisabled(t, body, "model") {
		t.Fatal("advanced page showed a key or a disabled control")
	}
	opts := selectValues(t, body, "provider")
	if len(opts) != 1 || opts[0] != "openrouter" {
		t.Fatalf("providers %v", opts)
	}
	if !strings.Contains(body, firstModel(t)) || !strings.Contains(body, "Choose a model") {
		t.Fatal("model list missing")
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Scout", Title: "Helper", Model: firstModel(t), Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	code, edit, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != http.StatusOK || strings.Contains(edit, key) {
		t.Fatalf("edit %d", code)
	}
	if opts = selectValues(t, edit, "provider"); len(opts) != 1 || opts[0] != "openrouter" {
		t.Fatalf("edit providers %v", opts)
	}
	raw, err := json.Marshal(draftDoc{Editing: true, Card: &setupState{
		Name: "Scout", Title: "Helper", Job: "Check the docs", Instructions: "Check the docs.",
		Model: firstModel(t), Size: createSize, Idle: createIdle,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SaveCreateDraft(context.Background(), user.ID, string(raw)); err != nil {
		t.Fatal(err)
	}
	code, setup, _ := h.do(http.MethodGet, "/new-agent", "", ip, nil)
	if code != http.StatusOK || strings.Contains(setup, key) || selectDisabled(t, setup, "provider") || selectDisabled(t, setup, "model") {
		t.Fatalf("setup %d", code)
	}
	if opts = selectValues(t, setup, "provider"); len(opts) != 1 || opts[0] != "openrouter" {
		t.Fatalf("setup providers %v", opts)
	}
	if !strings.Contains(setup, `name="model"`) || !strings.Contains(setup, `id="model-key-line"`) {
		t.Fatal("setup card did not use the picker")
	}
}

func TestDigitalOceanPickerUsesCurrentIDs(t *testing.T) {
	const key = "fixture-do-picker"
	t.Setenv(castcfg.ProviderKeyEnv("digitalocean"), key)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.String(), key) || r.Header.Get("Authorization") != "Bearer "+key {
			t.Fatal("key was not kept in the header")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-v4.1-flash"},{"id":"deepseek-3.2"},{"id":"flux-image"}],"owned_by":"hidden"}`)
	}))
	t.Cleanup(upstream.Close)
	h := newHarness(t)
	h.srv.ModelCatalog().SetBase("digitalocean", upstream.URL)
	_, secret := h.account("do-pick@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.62:9"
	h.login("do-pick@example.com", "correct-horse-1", secret, ip, false)
	code, page, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != http.StatusOK || calls.Load() != 0 {
		t.Fatalf("page dialed %d status %d", calls.Load(), code)
	}
	if opts := selectValues(t, page, "provider"); len(opts) != 1 || opts[0] != "digitalocean" {
		t.Fatalf("providers %v", opts)
	}
	models := selectValues(t, page, "model")
	if !containsAll(models, "digitalocean/deepseek-v4.1-flash", "digitalocean/deepseek-3.2") {
		t.Fatalf("fallback models %v", models)
	}
	if strings.Contains(page, "llama3.3-70b-instruct") || strings.Contains(page, "deepseek-v3.2") || strings.Contains(page, key) {
		t.Fatal("page listed a retired id or a key")
	}
	code, frag, _ := h.do(http.MethodGet, "/model-picker?provider=digitalocean&model=openrouter/minimax/minimax-m2.5&surface=form", "", ip, nil)
	if code != http.StatusOK || calls.Load() == 0 {
		t.Fatalf("fragment %d calls %d", code, calls.Load())
	}
	if strings.Contains(frag, key) || strings.Contains(frag, "owned_by") || strings.Contains(frag, "flux-image") || strings.Contains(frag, "llama3.3") || strings.Contains(frag, "load once") {
		t.Fatal("fragment leaked upstream data or looped")
	}
	if selectedOption(t, frag, "model") != "digitalocean/deepseek-v4.1-flash" {
		t.Fatalf("preselect %s", selectedOption(t, frag, "model"))
	}
	if !strings.Contains(frag, "digitalocean/deepseek-3.2") {
		t.Fatal("live list missing deepseek-3.2")
	}
	code, line, _ := h.do(http.MethodGet, "/model-picker?provider=digitalocean&surface=setup", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(line, `id="model-key-line"`) || !strings.Contains(line, "Server key") || strings.Contains(line, key) {
		t.Fatalf("key line %d %s", code, line)
	}
}

func TestRetiredModelStaysSelected(t *testing.T) {
	const key = "fixture-do-retired"
	const kept = "digitalocean/llama3.3-70b-instruct"
	t.Setenv(castcfg.ProviderKeyEnv("digitalocean"), key)
	h := newHarness(t)
	user, secret := h.account("retired@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.63:9"
	h.login("retired@example.com", "correct-horse-1", secret, ip, false)
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
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Scout\nbrain:\n  model: "+kept+"\n", "Check the docs.\n"); err != nil {
		t.Fatal(err)
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != http.StatusOK || selectedOption(t, page, "model") != kept || !strings.Contains(page, "This model is no longer listed. You can keep it, or pick another.") || strings.Contains(page, key) {
		t.Fatalf("retired page %d", code)
	}
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Scout"}, "title": {"Helper"}, "section": {sec.ID},
		"instructions": {"Check the docs."}, "model": {kept},
	}
	code, body, _ := h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("keep %d %s", code, snippet(body))
	}
	assertModel(t, h, user.ID, agent.ID, kept)
	fields["model"] = []string{"digitalocean/not-a-listed-model"}
	code, body, _ = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "That model is not in the provider manifest.") {
		t.Fatalf("other %d %s", code, snippet(body))
	}
	assertModel(t, h, user.ID, agent.ID, kept)
}

func TestNoKeyDisablesThePicker(t *testing.T) {
	h := newHarness(t)
	_, secret := h.account("nokey-pick@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.64:9"
	h.login("nokey-pick@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if !strings.Contains(body, "Add a model key first") || !strings.Contains(body, `href="/settings/secrets"`) {
		t.Fatal("missing secrets path")
	}
	if !selectDisabled(t, body, "provider") || !selectDisabled(t, body, "model") {
		t.Fatal("selects stayed enabled")
	}
	if opts := selectValues(t, body, "provider"); len(opts) != 1 || opts[0] != "" {
		t.Fatalf("providers %v", opts)
	}
}

func TestPickerFragmentIsScopedToTheCaller(t *testing.T) {
	const keyA = "fixture-pick-a"
	const keyB = "fixture-pick-b"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), keyA) || strings.Contains(r.URL.String(), keyB) {
			t.Fatal("key in the upstream URL")
		}
		switch r.Header.Get("Authorization") {
		case "Bearer " + keyA:
			_, _ = io.WriteString(w, `{"data":[{"id":"only-a"}]}`)
		case "Bearer " + keyB:
			_, _ = io.WriteString(w, `{"data":[{"id":"only-b"}]}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(upstream.Close)
	h := newHarness(t)
	h.srv.ModelCatalog().SetBase("openrouter", upstream.URL)
	ip := "203.0.113.65:9"
	code, _, hdr := h.do(http.MethodGet, "/model-picker?provider=openrouter", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" {
		t.Fatalf("anon %d %s", code, hdr.Get("Location"))
	}
	userA, secretA := h.account("pick-a@example.com", "Ada", "correct-horse-1")
	userB, secretB := h.account("pick-b@example.com", "Bea", "correct-horse-1")
	addProviderKey(t, h, userA.ID, "OPENROUTER_A", keyA)
	addProviderKey(t, h, userB.ID, "OPENROUTER_B", keyB)
	h.login("pick-a@example.com", "correct-horse-1", secretA, ip, false)
	code, body, _ := h.do(http.MethodGet, "/model-picker?provider=openrouter&surface=form", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "openrouter/only-a") || strings.Contains(body, "only-b") || strings.Contains(body, keyA) || strings.Contains(body, keyB) {
		t.Fatalf("A %d %s", code, body)
	}
	sec, err := h.st.SystemSection(context.Background(), userA.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{
		"form": {"agent"}, "confirm": {"advanced"}, "name": {"Relay"}, "title": {"Helper"},
		"section": {sec.ID}, "model": {"openrouter/only-a"}, "instructions": {"Check the docs."},
	}
	code, body, hdr = h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("live create %d %s", code, snippet(body))
	}
	_ = hdr
	h.login("pick-b@example.com", "correct-horse-1", secretB, ip, false)
	secB, err := h.st.SystemSection(context.Background(), userB.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/model-picker?provider=openrouter&user="+userA.ID, "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, "only-a") || !strings.Contains(body, "openrouter/only-b") || strings.Contains(body, keyA) || strings.Contains(body, keyB) {
		t.Fatalf("B %d %s", code, body)
	}
	fields["model"] = []string{"openrouter/only-a"}
	fields["name"] = []string{"Other"}
	fields["section"] = []string{secB.ID}
	code, body, _ = h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "That model is not in the provider manifest.") {
		t.Fatalf("B create %d %s", code, snippet(body))
	}
}

func TestHTMLStillDoesNotDialWhenAKeyIsSet(t *testing.T) {
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "fixture-page-key")
	h := newHarness(t)
	var calls atomic.Int32
	h.srv.ModelCatalog().SetClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(2 * time.Second)
		return nil, errors.New("pages must not dial")
	})})
	_, secret := h.account("page-key@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.66:9"
	h.login("page-key@example.com", "correct-horse-1", secret, ip, false)
	start := time.Now()
	code, body, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != http.StatusOK || time.Since(start) > time.Second || calls.Load() != 0 {
		t.Fatalf("page %d dials %d", code, calls.Load())
	}
	if !strings.Contains(body, "load once") || strings.Contains(body, "fixture-page-key") {
		t.Fatal("first paint did not keep a later refresh, or it showed the key")
	}
}

func selectValues(t *testing.T, body, name string) []string {
	t.Helper()
	block := regexp.MustCompile(`(?s)<select[^>]*name="` + regexp.QuoteMeta(name) + `"[^>]*>(.*?)</select>`).FindStringSubmatch(body)
	if block == nil {
		t.Fatalf("no select %s", name)
	}
	var out []string
	for _, row := range regexp.MustCompile(`<option value="([^"]*)"`).FindAllStringSubmatch(block[1], -1) {
		out = append(out, html.UnescapeString(row[1]))
	}
	return out
}

func selectDisabled(t *testing.T, body, name string) bool {
	t.Helper()
	tag := regexp.MustCompile(`<select[^>]*name="` + regexp.QuoteMeta(name) + `"[^>]*>`).FindString(body)
	if tag == "" {
		t.Fatalf("no select %s", name)
	}
	return strings.Contains(tag, " disabled")
}

func containsAll(got []string, want ...string) bool {
	have := map[string]bool{}
	for _, id := range got {
		have[id] = true
	}
	for _, id := range want {
		if !have[id] {
			return false
		}
	}
	return true
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
