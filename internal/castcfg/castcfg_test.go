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
