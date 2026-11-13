// Package castcfg reads Cast's provider manifest and writes the agent.yaml
// fields the Cast runtime actually loads.
//
// The model list is the manifest copied from scott/castagent
// python/cast/providers/manifest.json at ad6b1ee21462ce4c6a917ace7b8a15b3d99ef077.
// DigitalOcean default and fallback ids were updated in this repo after that copy.
// Builtin tool names match python/cast/agent/runtime.py BUILTIN_TOOLS at that commit.
// Permission levels match python/cast/harness/permissions.py.
package castcfg

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed manifest.json
var manifestJSON []byte

// ManifestJSON is the embedded provider manifest. Tests compare against it
// so a model list typed into Go cannot drift from the file.
func ManifestJSON() []byte { return manifestJSON }

type provider struct {
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	DisplayName    string   `json:"displayName"`
	Default        bool     `json:"default"`
	Prefixes       []string `json:"prefixes"`
	DefaultModel   string   `json:"defaultModel"`
	KnownModels    []string `json:"knownModels"`
	FallbackModels []string `json:"fallbackModels"`
	LLMDefault     string   `json:"llmDefaultModel"`
	Impl           string   `json:"impl"`
	EnvVar         string   `json:"envVar"`
	BaseURL        string   `json:"baseUrl"`
	Keyless        bool     `json:"keyless"`
}

// Model is one brain.model value named by the manifest.
type Model struct {
	ID string
}

// Models returns every model id the manifest names, in manifest order.
// A provider that accepts any remote model still only contributes the ids
// written in the file. An unknown id is not filled in from a default.
func Models() ([]Model, error) {
	var list []provider
	if err := json.Unmarshal(manifestJSON, &list); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Model
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, Model{ID: id})
	}
	for _, p := range list {
		if p.Impl == "reserved" {
			continue
		}
		add(p.DefaultModel)
		prefix := ""
		if len(p.Prefixes) > 0 {
			prefix = p.Prefixes[0]
		}
		for _, name := range p.KnownModels {
			add(withPrefix(prefix, name))
		}
		for _, name := range p.FallbackModels {
			add(withPrefix(prefix, name))
		}
		add(withPrefix(prefix, p.LLMDefault))
	}
	return out, nil
}

func withPrefix(prefix, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if prefix == "" || strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

// KnownModel reports whether id is named by the manifest.
func KnownModel(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	list, err := Models()
	if err != nil {
		return false
	}
	for _, m := range list {
		if m.ID == id {
			return true
		}
	}
	return false
}

// Tool is one tools.builtin name the runtime loads.
type Tool struct {
	Name  string
	Write bool
}

// Tools is the runtime builtin catalog. Write tools default to ask.
func Tools() []Tool {
	return []Tool{
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
}

// KnownTool reports whether name is a builtin the runtime loads.
func KnownTool(name string) bool {
	for _, t := range Tools() {
		if t.Name == name {
			return true
		}
	}
	return false
}

// WriteTool reports whether a missing permission should be ask.
// bash can change files. git add, commit, and push publish those changes.
// Cast already asks for git_push when the key is missing. castbot asks for
// the other write tools as well, and writes the level into agent.yaml so the
// runtime does not fall through to always-allow.
func WriteTool(name string) bool {
	for _, t := range Tools() {
		if t.Name == name {
			return t.Write
		}
	}
	return false
}

// DefaultLevel is ask for a write tool and always-allow for a read tool.
func DefaultLevel(name string) string {
	if WriteTool(name) {
		return "ask"
	}
	return "always-allow"
}

// NormalizeLevel maps Cast's level names, including the older aliases, onto
// always-allow, ask, or deny. An unknown level is not allowed.
func NormalizeLevel(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "always-allow", "allow":
		return "always-allow", true
	case "ask", "always-ask", "approve-once":
		return "ask", true
	case "deny", "block":
		return "deny", true
	default:
		return "", false
	}
}
