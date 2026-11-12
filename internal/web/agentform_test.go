package web

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"archive/tar"
	"compress/gzip"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

func TestAgentFormRoundTrip(t *testing.T) {
	ip := "203.0.113.180:9"
	h := newHarness(t)
	user, secret := h.account("form@example.com", "Form Owner", "correct-horse-1")
	h.login("form@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	code, body, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Choose a model") || !strings.Contains(body, model) || strings.Contains(body, "\u2014") {
		t.Fatalf("empty form %d", code)
	}
	fields := map[string][]string{
		"form":          {"agent"},
		"name":          {"Atlas"},
		"title":         {"Helper"},
		"section":       {sec.ID},
		"model":         {model},
		"instructions":  {"Be concise."},
		"tool":          {"bash", "glob", "git_push"},
		"perm_bash":     {"ask"},
		"perm_glob":     {"always-allow"},
		"perm_git_push": {"ask"},
	}
	code, body, hdr := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("create %d %s", code, body)
	}
	loc := hdr.Get("Location")
	if !strings.HasPrefix(loc, "/agents/") || strings.Contains(loc, "/edit") || strings.Contains(loc, "/machine") {
		t.Fatal(loc)
	}
	code, body, _ = h.do(http.MethodGet, loc+"/edit", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Be concise.") || !strings.Contains(body, `value="Atlas"`) {
		t.Fatalf("saved page %d", code)
	}
	agent := onlyAgent(t, h, user.ID)
	bundle, err := h.st.LatestBundle(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bundle.YAML, "super-secret-value") || !strings.Contains(bundle.YAML, "bash: ask") || !strings.Contains(bundle.YAML, "git_push: ask") || !strings.Contains(bundle.YAML, "glob: always-allow") {
		t.Fatalf("yaml\n%s", bundle.YAML)
	}
	if bundle.Instructions != "Be concise." {
		t.Fatal(bundle.Instructions)
	}
	spec, err := castcfg.ParseYAML(bundle.YAML)
	if err != nil || spec.Name != "Atlas" || spec.Model != model {
		t.Fatalf("%+v %v", spec, err)
	}
	used, max, err := h.st.Quota(context.Background(), user.ID)
	if err != nil || used != 0 || max != 4 {
		t.Fatalf("quota %d %d %v", used, max, err)
	}

	fields["name"] = []string{""}
	fields["model"] = []string{"not-a-cast-model"}
	code, body, _ = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "A name is required.") || !strings.Contains(body, "That model is not in the provider manifest.") || !strings.Contains(body, "not-a-cast-model") || !strings.Contains(body, "Be concise.") {
		t.Fatalf("validation %d %s", code, snippet(body))
	}
	got, err := h.st.GetAgent(context.Background(), user.ID, agent.ID)
	if err != nil || got.Name != "Atlas" || got.Model != model {
		t.Fatalf("agent changed %+v %v", got, err)
	}

	bare := url.Values{"form": {"agent"}, "name": {"Atlas"}, "title": {"Helper"}, "section": {sec.ID}, "model": {model}, "instructions": {"Be concise."}}
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/edit", bare.Encode(), ip, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
		t.Fatalf("csrf %d %s", code, snippet(body))
	}
}

func TestImportReviewAndRejects(t *testing.T) {
	ip := "203.0.113.181:9"
	h := newHarness(t)
	user, secret := h.account("import@example.com", "Import Owner", "correct-horse-1")
	h.login("import@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	archive := testTarGz(map[string]string{
		"docs/agent.yaml":      "name: Imported\ndescription: From a folder\nversion: 3.1\nbrain:\n  model: " + model + "\n  instructions_file: instructions.md\ntools:\n  builtin: [read, bash]\nharness:\n  permissions:\n    bash: allow\nenv:\n  API_KEY: super-secret-value\n",
		"docs/instructions.md": "Imported instructions.\n",
		"docs/.env":            "API_KEY=super-secret-value\n",
		"docs/notes.txt":       "leave this out\n",
	})
	fields := map[string][]string{"form": {"agent"}, "section": {sec.ID}}
	code, body, _ := h.send(http.MethodPost, "/new-agent/import", ip, fields, map[string][]byte{"archive": archive})
	if code != 200 || (!strings.Contains(body, "<li>docs/.env</li>") && !strings.Contains(body, "<li>.env</li>")) || strings.Contains(body, "super-secret-value") || !strings.Contains(body, "Imported instructions.") || !strings.Contains(body, `value="Imported"`) {
		t.Fatalf("review %d %s", code, snippet(body))
	}
	if strings.Contains(body, "\u2014") {
		t.Fatal("em dash")
	}

	bad := []struct {
		name string
		raw  []byte
		want string
	}{
		{"traversal", testTar(map[string]string{"../agent.yaml": "name: x\n"}), "path that is not allowed"},
		{"symlink", testSymlink(), "contains a link"},
		{"absolute", testTar(map[string]string{"/etc/passwd": "x"}), "path that is not allowed"},
		{"toomany", testTooMany(), "too many files"},
		{"oversize", bytes.Repeat([]byte{'x'}, (4<<20)+1), "too large"},
	}
	for _, tc := range bad {
		code, body, _ = h.send(http.MethodPost, "/new-agent/import", ip, fields, map[string][]byte{"archive": tc.raw})
		if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
			t.Fatalf("%s %d %s", tc.name, code, snippet(body))
		}
	}

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "matching-refs") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[{"ref":"refs/heads/main"}]`))
			return
		}
		if r.URL.Path != "/acme/helper/tar.gz/main" {
			http.NotFound(w, r)
			return
		}
		w.Write(testTarGz(map[string]string{
			"helper-main/agents/docs/agent.yaml":      "name: Remote\ndescription: Public\nbrain:\n  model: " + model + "\ntools:\n  builtin: [read]\n",
			"helper-main/agents/docs/instructions.md": "From GitHub.\n",
			"helper-main/agents/docs/.env":            "TOKEN=super-secret-value\n",
		}))
	}))
	t.Cleanup(gh.Close)
	h.srv.SetGitHub(gh.URL, gh.Client())
	fields["github"] = []string{"https://github.com/acme/helper/tree/main/agents/docs"}
	code, body, _ = h.send(http.MethodPost, "/new-agent/import", ip, fields, nil)
	if code != 200 || !strings.Contains(body, "From GitHub.") || !strings.Contains(body, ".env") || strings.Contains(body, "super-secret-value") || !strings.Contains(body, `value="Remote"`) {
		t.Fatalf("github %d %s", code, snippet(body))
	}
	for _, raw := range []string{
		"http://github.com/acme/helper",
		"https://user:pass@github.com/acme/helper",
		"https://evil.example/acme/helper",
	} {
		fields["github"] = []string{raw}
		code, body, _ = h.send(http.MethodPost, "/new-agent/import", ip, fields, nil)
		if code != http.StatusBadRequest || !strings.Contains(body, "Private repositories are out of scope") {
			t.Fatalf("%s %d %s", raw, code, snippet(body))
		}
	}
	gh.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	fields["github"] = []string{"https://github.com/acme/private"}
	code, body, _ = h.send(http.MethodPost, "/new-agent/import", ip, fields, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Private repositories are out of scope") {
		t.Fatalf("private %d %s", code, snippet(body))
	}
}

func TestSaveReloadsRunningAndLeavesPaused(t *testing.T) {
	ip := "203.0.113.182:9"
	h, fake, agent := machineUser(t, ip)
	model := firstModel(t)
	if _, err := h.st.PutBundle(context.Background(), agent.UserID, agent.ID, "1", "name: Atlas\nbrain:\n  model: "+model+"\n  instructions_file: instructions.md\ntools:\n  builtin: []\n", "Be concise."); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), agent.UserID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Atlas"}, "title": {"Updated"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Be concise and kind."},
		"tool": {"bash"}, "perm_bash": {"ask"},
	}
	before, _ := fake.Snapshot()
	creates := len(fake.Creates)
	code, body, hdr := h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=reloaded") {
		t.Fatalf("save %d %s %s", code, hdr.Get("Location"), snippet(body))
	}
	after, _ := fake.Snapshot()
	if len(fake.Creates) != creates || !grew(before, after, "reload") || grew(before, after, "create") || grew(before, after, "wake") {
		t.Fatalf("calls before %v after %v creates %d", before, after, len(fake.Creates))
	}
	code, body, _ = h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != 200 || !strings.Contains(body, "The running agent was reloaded. No new machine was created.") {
		t.Fatalf("notice %d %s", code, snippet(body))
	}

	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/pause", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	fields["instructions"] = []string{"Stay paused."}
	before, _ = fake.Snapshot()
	creates = len(fake.Creates)
	code, _, hdr = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=paused") {
		t.Fatalf("paused save %d %s", code, hdr.Get("Location"))
	}
	after, _ = fake.Snapshot()
	if len(fake.Creates) != creates || grew(before, after, "wake") || grew(before, after, "reload") || grew(before, after, "create") {
		t.Fatalf("paused calls before %v after %v", before, after)
	}
	bundle, err := h.st.LatestBundle(context.Background(), agent.UserID, agent.ID)
	if err != nil || !strings.Contains(bundle.Instructions, "Stay paused.") || strings.Contains(bundle.YAML, "super-secret-value") {
		t.Fatalf("bundle %v %s", err, bundle.YAML)
	}
	before, _ = fake.Snapshot()
	if code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/wake", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	after, _ = fake.Snapshot()
	if countCall(before, after, "reload") != 1 || grew(before, after, "create") {
		t.Fatalf("wake reload before %v after %v", before, after)
	}
}

func TestSaveDoesNotWakeAnAutoPausedMachine(t *testing.T) {
	ip := "203.0.113.186:9"
	h, fake, agent := machineUser(t, ip)
	model := firstModel(t)
	if _, err := h.st.PutBundle(context.Background(), agent.UserID, agent.ID, "1", "name: Atlas\nbrain:\n  model: "+model+"\n  instructions_file: instructions.md\ntools:\n  builtin: []\n", "Be concise."); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	fake.SetForce("paused")
	sec, err := h.st.SystemSection(context.Background(), agent.UserID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Atlas"}, "title": {"Helper"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Stay paused."},
	}
	before, _ := fake.Snapshot()
	code, _, hdr := h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=paused") {
		t.Fatalf("save %d %s", code, hdr.Get("Location"))
	}
	after, _ := fake.Snapshot()
	if grew(before, after, "reload") || grew(before, after, "wake") || !grew(before, after, "status") {
		t.Fatalf("auto pause calls before %v after %v", before, after)
	}
}

func TestAgentFormAuthz(t *testing.T) {
	ip := "203.0.113.183:9"
	h := newHarness(t)
	owner, secret := h.account("owner-form@example.com", "Owner", "correct-horse-1")
	h.login("owner-form@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	png := tinyPNG()
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Atlas"}, "title": {"Helper"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Be concise."},
	}
	code, _, hdr := h.send(http.MethodPost, "/new-agent", ip, fields, map[string][]byte{"avatar": png})
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	agent := onlyAgent(t, h, owner.ID)
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/avatar", "", ip, nil)
	if code != 200 || !strings.HasPrefix(body, "\x89PNG") {
		t.Fatalf("avatar %d %d", code, len(body))
	}
	bUser, bSecret := h.account("b-form@example.com", "User B", "correct-horse-1")
	h.do(http.MethodPost, "/logout", h.form(url.Values{}), ip, nil)
	h.login("b-form@example.com", "correct-horse-1", bSecret, "203.0.113.184:9", false)
	bSec, err := h.st.SystemSection(context.Background(), owner.ID, "agents")
	if err != nil || bSec.ID != sec.ID {
		t.Fatal(err)
	}
	paths := []struct {
		method, path string
		files        map[string][]byte
	}{
		{http.MethodGet, "/agents/" + agent.ID + "/edit", nil},
		{http.MethodPost, "/agents/" + agent.ID + "/edit", nil},
		{http.MethodPost, "/agents/" + agent.ID + "/import", map[string][]byte{"archive": testTar(map[string]string{"agent.yaml": "name: x\n"})}},
		{http.MethodGet, "/agents/" + agent.ID + "/avatar", nil},
	}
	bip := "203.0.113.184:9"
	for _, p := range paths {
		var code int
		var body string
		if p.method == http.MethodPost {
			code, body, _ = h.send(p.method, p.path, bip, fields, p.files)
		} else {
			code, body, _ = h.do(p.method, p.path, "", bip, nil)
		}
		if code != http.StatusNotFound {
			t.Fatalf("B %s %s -> %d %s", p.method, p.path, code, snippet(body))
		}
	}
	fields["section"] = []string{sec.ID}
	fields["name"] = []string{"Borrowed"}
	code, body, _ = h.send(http.MethodPost, "/new-agent", bip, fields, nil)
	if code != http.StatusNotFound {
		t.Fatalf("create-for %d %s", code, snippet(body))
	}
	if _, _, err := h.st.GetAvatar(context.Background(), owner.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.GetAvatar(context.Background(), bUser.ID, agent.ID); err != store.ErrNotFound {
		t.Fatal(err)
	}
	_ = hdr
}

func (h *harness) send(method, path, ip string, fields map[string][]string, files map[string][]byte) (int, string, http.Header) {
	h.t.Helper()
	raw, header := h.multi(fields, files)
	return h.do(method, path, raw, ip, header)
}

func (h *harness) multi(fields map[string][]string, files map[string][]byte) (string, map[string]string) {
	h.t.Helper()
	vals, err := url.ParseQuery(h.form(url.Values{}))
	if err != nil {
		h.t.Fatal(err)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("csrf", vals.Get("csrf")); err != nil {
		h.t.Fatal(err)
	}
	for key, list := range fields {
		for _, value := range list {
			if err := w.WriteField(key, value); err != nil {
				h.t.Fatal(err)
			}
		}
	}
	for name, body := range files {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="`+name+`"; filename="`+name+`.bin"`)
		if name == "avatar" {
			hdr.Set("Content-Type", "image/png")
		} else {
			hdr.Set("Content-Type", "application/octet-stream")
		}
		part, err := w.CreatePart(hdr)
		if err != nil {
			h.t.Fatal(err)
		}
		if _, err := part.Write(body); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		h.t.Fatal(err)
	}
	return buf.String(), map[string]string{"Content-Type": w.FormDataContentType()}
}

func firstModel(t *testing.T) string {
	t.Helper()
	list, err := castcfg.Models()
	if err != nil || len(list) == 0 {
		t.Fatal(err)
	}
	return list[0].ID
}

func onlyAgent(t *testing.T, h *harness, userID string) store.Agent {
	t.Helper()
	list, err := h.st.ListAgents(context.Background(), userID)
	if err != nil || len(list) != 1 {
		t.Fatalf("agents %d %v", len(list), err)
	}
	return list[0]
}

func TestDuplicateHarnessIsRejected(t *testing.T) {
	ip := "203.0.113.188:9"
	h := newHarness(t)
	user, secret := h.account("dup@example.com", "Dup Owner", "correct-horse-1")
	h.login("dup@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	raw := "name: Atlas\nbrain:\n  model: " + model + "\n  instructions_file: instructions.md\ntools:\n  builtin: [bash]\nharness:\n  permissions:\n    bash: ask\n    git_push: ask\nharness:\n  permissions:\n    bash: always-allow\n    git_push: always-allow\n"
	archive := testTarGz(map[string]string{
		"docs/agent.yaml":      raw,
		"docs/instructions.md": "Be concise.\n",
	})
	fields := map[string][]string{"form": {"agent"}, "section": {sec.ID}}
	code, body, _ := h.send(http.MethodPost, "/new-agent/import", ip, fields, map[string][]byte{"archive": archive})
	if code != http.StatusBadRequest || !strings.Contains(body, "the key harness more than once") {
		t.Fatalf("duplicate %d %s", code, snippet(body))
	}
	list, err := h.st.ListAgents(context.Background(), user.ID)
	if err != nil || len(list) != 0 {
		t.Fatalf("agent was created %d %v", len(list), err)
	}
}

func TestSecretInstructionsAreSkipped(t *testing.T) {
	ip := "203.0.113.195:9"
	h := newHarness(t)
	user, secret := h.account("inst@example.com", "Inst Owner", "correct-horse-1")
	h.login("inst@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	archive := testTarGz(map[string]string{
		"docs/agent.yaml": "name: Atlas\nbrain:\n  instructions_file: .env\n",
		"docs/.env":       "API_KEY=nested-token-value\n",
	})
	code, body, _ := h.send(http.MethodPost, "/new-agent/import", ip, map[string][]string{"form": {"agent"}, "section": {sec.ID}}, map[string][]byte{"archive": archive})
	if code != 200 || strings.Contains(body, "nested-token-value") || !strings.Contains(body, "was skipped because it can hold secrets") || !strings.Contains(body, ".env") {
		t.Fatalf("instructions %d %s", code, snippet(body))
	}
}

func TestNestedSecretsStayOffThePageAndTheBundle(t *testing.T) {
	ip := "203.0.113.189:9"
	h := newHarness(t)
	user, secret := h.account("nested@example.com", "Nested Owner", "correct-horse-1")
	h.login("nested@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	token := "nested-token-value"
	yaml := "name: Atlas\ndescription: Helper\nbrain:\n  model: " + model + "\n  instructions_file: instructions.md\ntools:\n  builtin: [read]\n  mcp:\n    demo:\n      command: helper\n      env:\n        GITHUB_TOKEN: " + token + "\nruntime:\n  image: local\n  env:\n    API_KEY: " + token + "\nskills:\n  - review\n"
	archive := testTarGz(map[string]string{
		"docs/agent.yaml":      yaml,
		"docs/instructions.md": "Be concise.\n",
	})
	fields := map[string][]string{"form": {"agent"}, "section": {sec.ID}}
	code, body, _ := h.send(http.MethodPost, "/new-agent/import", ip, fields, map[string][]byte{"archive": archive})
	if code != 200 || strings.Contains(body, token) || !strings.Contains(body, "helper") || !strings.Contains(body, "review") || !strings.Contains(body, "MCP demo") {
		t.Fatalf("review leaked or hid the kept keys %d %s", code, snippet(body))
	}
	draft := draftValue(body)
	kept, err := h.st.ImportDraft(context.Background(), user.ID, "", draft)
	if err != nil || strings.Contains(kept, token) || !strings.Contains(kept, "review") {
		t.Fatalf("draft %v %s", err, kept)
	}
	fields = map[string][]string{
		"form": {"agent"}, "name": {"Atlas"}, "title": {"Helper"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Be concise."}, "draft": {draft},
		"tool": {"read"}, "perm_read": {"always-allow"},
		"kept": {"brain:\n  model: openrouter/not/in/manifest\ntools:\n  builtin: [not_a_tool]\n"},
	}
	code, body, hdr := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("save %d %s", code, snippet(body))
	}
	agent := onlyAgent(t, h, user.ID)
	bundle, err := h.st.LatestBundle(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := h.st.KeptYAML(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, page, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != 200 || strings.Contains(bundle.YAML, token) || strings.Contains(stored, token) || strings.Contains(page, token) || strings.Contains(bundle.YAML, "not_a_tool") || strings.Contains(bundle.YAML, "not/in/manifest") {
		t.Fatalf("saved secret or tamper\n%s\n%s", bundle.YAML, stored)
	}
	if !strings.Contains(bundle.YAML, model) || !strings.Contains(bundle.YAML, "review") || !strings.Contains(bundle.YAML, "helper") {
		t.Fatalf("dropped the form or the kept command\n%s", bundle.YAML)
	}
}

func TestTamperedPostCannotAddAModelOrTool(t *testing.T) {
	ip := "203.0.113.191:9"
	h := newHarness(t)
	user, secret := h.account("tamper@example.com", "Tamper Owner", "correct-horse-1")
	h.login("tamper@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Atlas"}, "title": {"Helper"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Be concise."},
		"tool": {"bash"}, "perm_bash": {"ask"},
		"kept": {"brain:\n  model: openrouter/not/in/manifest\ntools:\n  builtin: [not_a_tool]\nharness:\n  permissions:\n    bash: always-allow\n"},
	}
	code, body, _ := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("create %d %s", code, snippet(body))
	}
	agent := onlyAgent(t, h, user.ID)
	bundle, err := h.st.LatestBundle(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bundle.YAML, "not_a_tool") || strings.Contains(bundle.YAML, "not/in/manifest") || strings.Contains(bundle.YAML, "always-allow") || !strings.Contains(bundle.YAML, "bash: ask") || !strings.Contains(bundle.YAML, model) {
		t.Fatalf("tampered yaml\n%s", bundle.YAML)
	}
}

func TestPendingReloadDoesNotBlockAndKeepsANewerSave(t *testing.T) {
	ip := "203.0.113.192:9"
	h, fake, agent := machineUser(t, ip)
	model := firstModel(t)
	if _, err := h.st.PutBundle(context.Background(), agent.UserID, agent.ID, "1", "name: Atlas\nbrain:\n  model: "+model+"\n  instructions_file: instructions.md\ntools:\n  builtin: []\n", "Be concise."); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetPendingBundle(context.Background(), agent.UserID, agent.ID, "version-a"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	hold := make(chan struct{})
	fake.ReloadStarted = started
	fake.SetReloadWait(hold)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
		}()
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not start")
	}
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("page loads waited for reload")
	}
	select {
	case <-started:
		t.Fatal("a second reload started")
	case <-time.After(150 * time.Millisecond):
	}
	if err := h.st.SetPendingBundle(context.Background(), agent.UserID, agent.ID, "version-b"); err != nil {
		t.Fatal(err)
	}
	close(hold)
	fake.SetReloadWait(nil)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, err := h.st.PendingBundle(context.Background(), agent.UserID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got == "version-b" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending %s", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/wake", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	got, err := h.st.PendingBundle(context.Background(), agent.UserID, agent.ID)
	if err != nil || got != "" {
		t.Fatalf("after wake %q %v", got, err)
	}
	calls, _ := fake.Snapshot()
	n := 0
	for _, call := range calls {
		if call == "reload" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("reloads %d %v", n, calls)
	}
}

func TestFailedReloadBacksOff(t *testing.T) {
	ip := "203.0.113.193:9"
	h, fake, agent := machineUser(t, ip)
	model := firstModel(t)
	if _, err := h.st.PutBundle(context.Background(), agent.UserID, agent.ID, "1", "name: Atlas\nbrain:\n  model: "+model+"\n  instructions_file: instructions.md\ntools:\n  builtin: []\n", "Be concise."); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetPendingBundle(context.Background(), agent.UserID, agent.ID, "version-a"); err != nil {
		t.Fatal(err)
	}
	fake.ReloadErr = errors.New("reload failed")
	if code, _, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil); code != 200 {
		t.Fatal(code)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		calls, _ := fake.Snapshot()
		n := 0
		for _, call := range calls {
			if call == "reload" {
				n++
			}
		}
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reload did not run")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code, _, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil); code != 200 {
		t.Fatal(code)
	}
	time.Sleep(80 * time.Millisecond)
	calls, _ := fake.Snapshot()
	n := 0
	for _, call := range calls {
		if call == "reload" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("backoff did not hold %d %v", n, calls)
	}
}

func TestGitHubRateLimitIsExplained(t *testing.T) {
	ip := "203.0.113.194:9"
	h := newHarness(t)
	_, secret := h.account("rate@example.com", "Rate Owner", "correct-horse-1")
	h.login("rate@example.com", "correct-horse-1", secret, ip, false)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("API rate limit exceeded"))
	}))
	t.Cleanup(gh.Close)
	h.srv.SetGitHub(gh.URL, gh.Client())
	code, body, _ := h.send(http.MethodPost, "/new-agent/import", ip, map[string][]string{
		"form": {"agent"}, "github": {"https://github.com/acme/helper"},
	}, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "GitHub is rate limiting imports, try again later or add ?ref=") {
		t.Fatalf("rate %d %s", code, snippet(body))
	}
}

func draftValue(body string) string {
	const needle = `name="draft" value="`
	i := strings.Index(body, needle)
	if i < 0 {
		return ""
	}
	rest := body[i+len(needle):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func countCall(before, after []string, name string) int {
	if len(after) < len(before) {
		return 0
	}
	n := 0
	for _, call := range after[len(before):] {
		if call == name {
			n++
		}
	}
	return n
}

func grew(before, after []string, name string) bool {
	n := 0
	for _, call := range before {
		if call == name {
			n++
		}
	}
	m := 0
	for _, call := range after {
		if call == name {
			m++
		}
	}
	return m > n
}

func snippet(body string) string {
	if len(body) > 400 {
		return body[:400]
	}
	return body
}

func tinyPNG() []byte {
	var buf bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Pix = []byte{10, 20, 30, 255}
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func testTarGz(files map[string]string) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	writeTestTar(tw, files)
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func testTar(files map[string]string) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTestTar(tw, files)
	tw.Close()
	return buf.Bytes()
}

func writeTestTar(tw *tar.Writer, files map[string]string) {
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		tw.WriteHeader(hdr)
		tw.Write([]byte(body))
	}
}

func testSymlink() []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "link", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: "agent.yaml"})
	tw.Close()
	return buf.Bytes()
}

func testTooMany() []byte {
	files := map[string]string{"agent.yaml": "name: x\n"}
	for i := 0; i < 201; i++ {
		files[string(rune('a'+(i%26)))+string(rune('a'+((i/26)%26)))+string(rune('a'+(i%3)))+".txt"] = "x"
	}
	return testTar(files)
}
