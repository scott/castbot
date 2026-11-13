package castcfg

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestModelsComeFromTheManifest(t *testing.T) {
	raw := ManifestJSON()
	if !strings.Contains(string(raw), `"knownModels"`) {
		t.Fatal("manifest has no knownModels")
	}
	var list []provider
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	models, err := Models()
	if err != nil || len(models) == 0 {
		t.Fatalf("models %d %v", len(models), err)
	}
	known := map[string]bool{}
	for _, model := range models {
		known[model.ID] = true
		if !KnownModel(model.ID) {
			t.Fatalf("catalog rejected %s", model.ID)
		}
	}
	var named int
	for _, p := range list {
		if p.Impl == "reserved" {
			continue
		}
		prefix := ""
		if len(p.Prefixes) > 0 {
			prefix = p.Prefixes[0]
		}
		for _, name := range p.KnownModels {
			id := withPrefix(prefix, name)
			if id != "" && !known[id] {
				t.Fatalf("manifest model %s missing from the catalog", id)
			}
			named++
		}
	}
	if named == 0 {
		t.Fatal("manifest named no models")
	}
	if KnownModel("not-a-cast-model") || KnownModel("") {
		t.Fatal("unknown model was accepted")
	}
}

func TestCatalogWriteToolsAsk(t *testing.T) {
	want := []Tool{
		{Name: "bash", Write: true},
		{Name: "read"},
		{Name: "glob"},
		{Name: "git_status"},
		{Name: "git_commit", Write: true},
		{Name: "git_push", Write: true},
		{Name: "git_log"},
		{Name: "git_diff"},
		{Name: "git_add", Write: true},
	}
	got := Tools()
	if len(got) != len(want) {
		t.Fatalf("%d tools", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool %d got %+v", i, got[i])
		}
		if got[i].Write && DefaultLevel(got[i].Name) != "ask" {
			t.Fatalf("%s default", got[i].Name)
		}
		if !got[i].Write && DefaultLevel(got[i].Name) != "always-allow" {
			t.Fatalf("%s default", got[i].Name)
		}
	}
}

func TestRenderDropsSecretsAndAsksForWrites(t *testing.T) {
	spec, err := ParseYAML("name: Atlas\ndescription: Helper\nversion: 2.0\nbrain:\n  model: openrouter/not-real\n  instructions_file: instructions.md\ntools:\n  builtin: [bash, glob, git_push, custom_tool]\nharness:\n  permissions:\n    bash: allow\n    git_push: always-ask\nenv:\n  API_KEY: super-secret-value\n")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Model != "openrouter/not-real" || spec.Permissions["bash"] != "always-allow" || spec.Permissions["git_push"] != "ask" {
		t.Fatalf("spec %+v", spec)
	}
	raw, err := RenderYAML(Spec{
		Name: spec.Name, Title: spec.Title, Version: spec.Version, Model: spec.Model,
		Tools: []string{"bash", "glob", "git_push"},
		Permissions: map[string]string{
			"bash": "ask", "glob": DefaultLevel("glob"), "git_push": DefaultLevel("git_push"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "super-secret-value") || strings.Contains(text, "API_KEY") || strings.Contains(text, "custom_tool") {
		t.Fatalf("yaml kept a secret or an unknown tool\n%s", text)
	}
	if !strings.Contains(text, "bash: ask") || !strings.Contains(text, "git_push: ask") || !strings.Contains(text, "glob: always-allow") {
		t.Fatalf("permissions\n%s", text)
	}
	if DefaultLevel("bash") != "ask" || DefaultLevel("read") != "always-allow" {
		t.Fatal("defaults")
	}
}

func TestRenderKeepsKeysTheFormDoesNotOwn(t *testing.T) {
	raw := "name: Atlas\ndescription: Helper\nversion: 2\nbrain:\n  model: openrouter/not-real\n  model_override: fast\n  instructions_file: prompts/system.md\ntools:\n  builtin: [read]\n  mcp:\n    demo: {}\nskills:\n  - review\nruntime:\n  image: local\nenv:\n  API_KEY: super-secret-value\n"
	spec, err := ParseYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	if spec.InstructionsFile != "prompts/system.md" {
		t.Fatal(spec.InstructionsFile)
	}
	if strings.Contains(string(spec.Kept), "super-secret-value") || strings.Contains(string(spec.Kept), "API_KEY") {
		t.Fatal("kept a secret")
	}
	out, err := RenderYAML(spec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, needle := range []string{"model_override: fast", "mcp:", "skills:", "runtime:", "image: local"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("missing %s\n%s", needle, text)
		}
	}
	if strings.Contains(text, "super-secret-value") || strings.Contains(text, "API_KEY") {
		t.Fatalf("secret returned\n%s", text)
	}
}

func TestAliasToScrubbedSecretIsRejected(t *testing.T) {
	raw := "name: Atlas\ntools:\n  mcp:\n    - name: demo\n      env: &s\n        TOKEN: nested-token-value\nruntime:\n  copied: *s\n"
	_, err := ParseYAML(raw)
	if !errors.Is(err, ErrAlias) {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "nested-token-value") || strings.Contains(err.Error(), "TOKEN") {
		t.Fatal("secret leaked")
	}
}

func TestCompoundSecretNamesAreDropped(t *testing.T) {
	raw := "name: Atlas\ngithub_token: compound-token-value\nopenai_api_key: compound-key-value\nnote: hello\n"
	spec, err := ParseYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec.Kept)
	if strings.Contains(text, "compound-token-value") || strings.Contains(text, "compound-key-value") || strings.Contains(text, "github_token") {
		t.Fatalf("kept a secret\n%s", text)
	}
	if !strings.Contains(text, "hello") {
		t.Fatalf("dropped a plain note\n%s", text)
	}
}

func TestPluralTokenNamesStaySecret(t *testing.T) {
	raw := "name: Atlas\nmax_tokens: 16\nmax_output_tokens: 32\nmax_input_tokens: 8\napi_tokens: api-token-value\ngithub_tokens: gh-token-value\nrefresh_tokens: refresh-token-value\nnote: hello\n"
	spec, err := ParseYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec.Kept)
	for _, needle := range []string{"max_tokens", "16", "max_output_tokens", "32", "max_input_tokens", "hello"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("missing %s\n%s", needle, text)
		}
	}
	for _, needle := range []string{"api_tokens", "api-token-value", "github_tokens", "gh-token-value", "refresh_tokens", "refresh-token-value"} {
		if strings.Contains(text, needle) {
			t.Fatalf("kept %s\n%s", needle, text)
		}
	}
}

func TestCountKeysAreNotSecrets(t *testing.T) {
	raw := "name: Atlas\nmax_tokens: 16\nsort_key: name\nprompt_tokens: 3\ngithub_token: compound-token-value\nnote: hello\n"
	spec, err := ParseYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec.Kept)
	for _, needle := range []string{"max_tokens", "16", "sort_key", "prompt_tokens", "hello"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("missing %s\n%s", needle, text)
		}
	}
	if strings.Contains(text, "compound-token-value") || strings.Contains(text, "github_token") {
		t.Fatalf("kept a secret\n%s", text)
	}
}

func TestSummarizeKeptListsLeftovers(t *testing.T) {
	raw := "name: Atlas\nbrain:\n  model: openrouter/x\n  model_override: fast\nrepository: github.com/acme/helper\ndeployment:\n  region: mkc1\nskills:\n  - review\n"
	spec, err := ParseYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	lines := SummarizeKept(spec.Kept)
	joined := ""
	for _, line := range lines {
		joined += line.Label + " " + line.Value + "\n"
	}
	for _, needle := range []string{"Skills", "review", "Brain model_override", "fast", "Repository", "github.com/acme/helper", "Deployment", "mkc1"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("missing %s in %s", needle, joined)
		}
	}
}

func TestDuplicateKeysAreRejected(t *testing.T) {
	raw := "name: Atlas\nharness:\n  permissions:\n    bash: ask\nharness:\n  permissions:\n    bash: always-allow\n"
	_, err := ParseYAML(raw)
	var dup *DuplicateKeyError
	if !errors.As(err, &dup) || dup.Key != "harness" {
		t.Fatalf("%v", err)
	}
	quoted := "name: Atlas\nharness:\n  permissions:\n    bash: ask\n\"harness\":\n  permissions:\n    bash: always-allow\n"
	_, err = ParseYAML(quoted)
	if !errors.As(err, &dup) || dup.Key != "harness" {
		t.Fatalf("quoted %v", err)
	}
}

func TestNestedSecretsAreDropped(t *testing.T) {
	raw := "name: Atlas\nbrain:\n  model: openrouter/not-real\ntools:\n  mcp:\n    demo:\n      command: helper\n      env:\n        GITHUB_TOKEN: nested-token-value\n        SAFE: vault:GITHUB_TOKEN\nruntime:\n  image: local\n  env:\n    API_KEY: nested-api-key-value\nskills:\n  - review\n"
	spec, err := ParseYAML(raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(spec.Kept)
	if strings.Contains(text, "nested-token-value") || strings.Contains(text, "nested-api-key-value") {
		t.Fatalf("kept a nested secret\n%s", text)
	}
	if !strings.Contains(text, "vault:GITHUB_TOKEN") || !strings.Contains(text, "helper") || !strings.Contains(text, "review") {
		t.Fatalf("dropped a vault reference or a command\n%s", text)
	}
	out, err := RenderYAML(Spec{
		Name: "Atlas", Title: "Helper", Version: "1", Model: spec.Model,
		Tools: []string{"read"}, Permissions: map[string]string{"read": "always-allow"},
		Kept: spec.Kept,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "nested-token-value") || strings.Contains(string(out), "nested-api-key-value") {
		t.Fatalf("render kept a nested secret\n%s", out)
	}
	lines := SummarizeKept(spec.Kept)
	joined := ""
	for _, line := range lines {
		joined += line.Label + " " + line.Value + "\n"
	}
	if !strings.Contains(joined, "Skills") || !strings.Contains(joined, "review") || !strings.Contains(joined, "helper") || strings.Contains(joined, "nested-token-value") {
		t.Fatalf("summary %s", joined)
	}
}
