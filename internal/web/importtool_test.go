package web

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/importtool"
)

const importSecret = "super-secret-value"

func TestMachineImportSavesTheFolder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("import-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("import-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	ip := "203.0.113.99:9"
	archive := importArchive(caller.Model, importSecret)

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(importtool.ToolImport, map[string]any{
		"archive_b64": base64.StdEncoding.EncodeToString(archive),
		"user_id":     other.ID,
	}))
	if code != http.StatusNotFound {
		t.Fatalf("other token %d", code)
	}
	if importChanged(t, h, user.ID, caller.ID, "Ada") || importChanged(t, h, other.ID, otherAgent.ID, "Bea") {
		t.Fatal("other token imported an agent")
	}

	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(importtool.ToolImport, map[string]any{
		"archive_b64": base64.StdEncoding.EncodeToString(archive),
	}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d", code)
	}
	if importChanged(t, h, user.ID, caller.ID, "Ada") || importChanged(t, h, user.ID, sibling.ID, "Pip") {
		t.Fatal("sibling path imported an agent")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(importtool.ToolImport, map[string]any{}))
	importQuiet(t, "empty", body)
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Choose an archive or a public github.com URL.") {
		t.Fatalf("empty %d %s", code, snippet(body))
	}
	if importChanged(t, h, user.ID, caller.ID, "Ada") {
		t.Fatal("empty args imported")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(importtool.ToolImport, map[string]any{
		"archive_b64": "!!!!",
	}))
	importQuiet(t, "bad archive", body)
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That archive could not be read.") {
		t.Fatalf("bad archive %d %s", code, snippet(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(importtool.ToolImport, map[string]any{
		"url": "https://evil.example/acme/helper",
	}))
	importQuiet(t, "bad url", body)
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Private repositories are out of scope") {
		t.Fatalf("bad url %d %s", code, snippet(body))
	}
	if importChanged(t, h, user.ID, caller.ID, "Ada") {
		t.Fatal("bad url imported")
	}

	pad := strings.Repeat("a", (64<<10)+1)
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload("list_agents", map[string]any{"note": pad}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That request was rejected.") || len(body) > 2000 {
		t.Fatalf("oversized list %d len %d", code, len(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(importtool.ToolImport, map[string]any{
		"url": "https://evil.example/" + pad,
	}))
	importQuiet(t, "oversized import", body)
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Private repositories are out of scope") || strings.Contains(body, "That request was rejected.") || len(body) > 2000 {
		t.Fatalf("oversized import %d len %d", code, len(body))
	}
	if importChanged(t, h, user.ID, caller.ID, "Ada") {
		t.Fatal("oversized import changed the agent")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(importtool.ToolImport, map[string]any{
		"archive_b64": base64.StdEncoding.EncodeToString(archive),
		"url":         "https://evil.example/acme/helper",
		"user_id":     other.ID,
		"agent_id":    sibling.ID,
		"value":       importSecret,
	}))
	text := agentResult(t, body)
	importQuiet(t, "save", text)
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(text, "Saved. Name is Imported.") || !strings.Contains(text, "Skipped files") || !strings.Contains(text, ".env") || !strings.Contains(text, "These files can hold secrets, so they were not added.") || !strings.Contains(text, "Removed secret values") || !strings.Contains(text, "\nenv\n") || !strings.Contains(text, "Secret values were removed. A vault reference can stay.") || !strings.Contains(text, "Other files were left out.") || !strings.Contains(text, "widget") || strings.Contains(text, "Folder instructions stay on the agent.") || strings.Contains(text, other.ID) {
		t.Fatalf("save %d %s", code, snippet(text))
	}
	got, err := h.st.GetAgent(ctx, user.ID, caller.ID)
	if err != nil || got.Name != "Imported" || got.Title != "From a folder" || got.Model != caller.Model || got.SectionID != caller.SectionID || got.Description != caller.Description {
		t.Fatalf("saved agent %+v %v", got, err)
	}
	bundle, err := h.st.LatestBundle(ctx, user.ID, caller.ID)
	if err != nil || bundle.Instructions != "Folder instructions stay on the agent.\n" || strings.Contains(bundle.YAML, "widget") {
		t.Fatal("bundle did not keep the folder instructions")
	}
	importQuiet(t, "bundle", bundle.YAML)
	importQuiet(t, "instructions", bundle.Instructions)
	kept, err := h.st.KeptYAML(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	importQuiet(t, "kept", kept)
	if importChanged(t, h, user.ID, sibling.ID, "Pip") || importChanged(t, h, other.ID, otherAgent.ID, "Bea") {
		t.Fatal("import changed another agent")
	}

	locked := seedCaller(t, h, user, "Nia")
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, locked.ID, "vm-nia", "running", fake)
	if _, _, err := h.st.BeginReplacement(ctx, user.ID, locked.ID, "tool", "example.invalid/castbot-runtime:s09", false); err != nil {
		t.Fatal(err)
	}
	lockToken := guestToken(t, h, locked)
	code, body = machinePost(t, h, ip, lockToken, locked.ID, "/agents", routinePayload(importtool.ToolImport, map[string]any{
		"archive_b64": base64.StdEncoding.EncodeToString(importArchive(locked.Model, importSecret)),
	}))
	importQuiet(t, "replacing", body)
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, replaceSentence) {
		t.Fatalf("replacing %d %s", code, snippet(body))
	}
	if importChanged(t, h, user.ID, locked.ID, "Nia") {
		t.Fatal("replace lock imported")
	}
}

func TestImportFenceSavesTheFolder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("import-fence@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("import-fence-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	model := caller.Model

	replyRoutine(t, h, user.ID, caller, "Wait.\n\n```castbot-import\n{\"action\":\"import_agent\"}\n```\n")
	bare := agentReplyBody(t, h, user.ID, caller.ID)
	importQuiet(t, "bare fence", bare)
	if strings.Contains(bare, "castbot-import") || !strings.Contains(bare, "Wait.") || !strings.Contains(bare, "Choose an archive or a public github.com URL.") {
		t.Fatalf("bare fence %s", snippet(bare))
	}
	if importChanged(t, h, user.ID, caller.ID, "Ada") {
		t.Fatal("bare fence imported")
	}

	replyRoutine(t, h, user.ID, caller, "Leave this.\n\n```castbot-import\n{\"action\":\"import_agent\",\"url\":\"URL\"}\n```\n")
	held := agentReplyBody(t, h, user.ID, caller.ID)
	importQuiet(t, "placeholder url", held)
	if !strings.Contains(held, "castbot-import") || !strings.Contains(held, "Leave this.") || strings.Contains(held, "Saved. Name is") {
		t.Fatalf("placeholder url %s", snippet(held))
	}
	replyRoutine(t, h, user.ID, caller, "Leave the archive.\n\n```castbot-import\n{\"action\":\"import_agent\",\"archive_b64\":\"ARCHIVE\"}\n```\n")
	held = agentReplyBody(t, h, user.ID, caller.ID)
	importQuiet(t, "placeholder archive", held)
	if !strings.Contains(held, "ARCHIVE") || !strings.Contains(held, "Leave the archive.") || strings.Contains(held, "Saved. Name is") {
		t.Fatalf("placeholder archive %s", snippet(held))
	}
	if importChanged(t, h, user.ID, caller.ID, "Ada") {
		t.Fatal("placeholder imported")
	}

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "matching-refs") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"ref":"refs/heads/main"}]`))
			return
		}
		if r.URL.Path != "/acme/helper/tar.gz/main" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(testTarGz(map[string]string{
			"helper-main/agents/docs/agent.yaml":      "name: Remote\ndescription: Public\nbrain:\n  model: " + model + "\ntools:\n  builtin: [read]\n",
			"helper-main/agents/docs/instructions.md": "From GitHub.\n",
			"helper-main/agents/docs/.env":            "TOKEN=" + importSecret + "\n",
		}))
	}))
	t.Cleanup(gh.Close)
	h.srv.SetGitHub(gh.URL, gh.Client())
	replyRoutine(t, h, user.ID, caller, "Reading it.\n\n```castbot-import\n{\"action\":\"import_agent\",\"url\":\"https://github.com/acme/helper/tree/main/agents/docs\",\"user_id\":\""+other.ID+"\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	importQuiet(t, "fence", body)
	if strings.Contains(body, "castbot-import") || !strings.Contains(body, "Reading it.") || !strings.Contains(body, "Saved. Name is Remote.") || !strings.Contains(body, "Skipped files") || !strings.Contains(body, ".env") || !strings.Contains(body, "These files can hold secrets, so they were not added.") || strings.Contains(body, "From GitHub.") || strings.Contains(body, other.ID) {
		t.Fatalf("fence %s", snippet(body))
	}
	got, err := h.st.GetAgent(ctx, user.ID, caller.ID)
	if err != nil || got.Name != "Remote" || got.Title != "Public" || got.SectionID != caller.SectionID {
		t.Fatalf("fenced agent %+v %v", got, err)
	}
	bundle, err := h.st.LatestBundle(ctx, user.ID, caller.ID)
	if err != nil || bundle.Instructions != "From GitHub.\n" {
		t.Fatal("fence bundle")
	}
	importQuiet(t, "fence bundle", bundle.YAML)
	kept, err := h.st.KeptYAML(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	importQuiet(t, "fence kept", kept)
	if importChanged(t, h, user.ID, sibling.ID, "Pip") || importChanged(t, h, other.ID, otherAgent.ID, "Bea") {
		t.Fatal("fence changed another agent")
	}
}

func importArchive(model, secret string) []byte {
	return testTarGz(map[string]string{
		"docs/agent.yaml":      "name: Imported\ndescription: From a folder\nversion: 3.1\nbrain:\n  model: " + model + "\n  instructions_file: instructions.md\ntools:\n  builtin: [read, widget]\nharness:\n  permissions:\n    bash: allow\nenv:\n  API_KEY: " + secret + "\n",
		"docs/instructions.md": "Folder instructions stay on the agent.\n",
		"docs/.env":            "API_KEY=" + secret + "\n",
		"docs/notes.txt":       "leave this out\n",
	})
}

func importChanged(t *testing.T, h *harness, userID, agentID, name string) bool {
	t.Helper()
	agent, err := h.st.GetAgent(context.Background(), userID, agentID)
	if err != nil || agent.Name != name {
		return true
	}
	bundle, err := h.st.LatestBundle(context.Background(), userID, agentID)
	if err != nil || bundle.Instructions != "Be brief.\n" {
		return true
	}
	return false
}

func importQuiet(t *testing.T, label, body string) {
	t.Helper()
	if strings.Contains(body, importSecret) || strings.Contains(body, "nested-token-value") {
		t.Fatalf("%s included a secret value", label)
	}
}
