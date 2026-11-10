package castcfg

import (
	"encoding/json"
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
