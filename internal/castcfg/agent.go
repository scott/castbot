package castcfg

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the agent form. It is the subset of agent.yaml the runtime reads
// plus the instructions body. Secret values are not a field.
type Spec struct {
	Name         string
	Title        string
	Version      string
	Model        string
	Instructions string
	Tools        []string
	Permissions  map[string]string
}

// ParseYAML reads name, brain.model, tools.builtin, and harness.permissions.
// Other keys, including anything that might hold a secret, are dropped.
func ParseYAML(text string) (Spec, error) {
	var raw map[string]any
	if err := yaml.Unmarshal([]byte(text), &raw); err != nil {
		return Spec{}, err
	}
	if raw == nil {
		return Spec{}, nil
	}
	spec := Spec{
		Name:        scalar(raw["name"]),
		Title:       scalar(raw["description"]),
		Version:     scalar(raw["version"]),
		Permissions: map[string]string{},
	}
	if brain, ok := raw["brain"].(map[string]any); ok {
		spec.Model = scalar(brain["model"])
	}
	if tools, ok := raw["tools"].(map[string]any); ok {
		spec.Tools = stringList(tools["builtin"])
	}
	if harness, ok := raw["harness"].(map[string]any); ok {
		if perms, ok := harness["permissions"].(map[string]any); ok {
			for key, val := range perms {
				key = strings.TrimSpace(key)
				if key == "" || key == "mcp" || key == "*" {
					continue
				}
				level, ok := NormalizeLevel(scalar(val))
				if !ok {
					spec.Permissions[key] = strings.TrimSpace(scalar(val))
					continue
				}
				spec.Permissions[key] = level
			}
		}
	}
	return spec, nil
}

// RenderYAML writes the agent.yaml the runtime loads. The bytes contain the
// form fields only. They do not contain secret values.
func RenderYAML(spec Spec) ([]byte, error) {
	version := strings.TrimSpace(spec.Version)
	if version == "" || strings.ContainsAny(version, " \t\r\n") {
		version = "1.0"
	}
	doc := yamlDoc{
		Name:        strings.TrimSpace(spec.Name),
		Description: strings.TrimSpace(spec.Title),
		Version:     version,
	}
	doc.Brain.Model = strings.TrimSpace(spec.Model)
	doc.Brain.InstructionsFile = "instructions.md"
	doc.Tools.Builtin = append([]string(nil), spec.Tools...)
	var perms permList
	for _, tool := range Tools() {
		level, ok := spec.Permissions[tool.Name]
		if !ok {
			continue
		}
		norm, good := NormalizeLevel(level)
		if !good {
			continue
		}
		perms = append(perms, permItem{Name: tool.Name, Level: norm})
	}
	if len(perms) > 0 {
		doc.Harness = &harnessDoc{Permissions: perms}
	}
	return yaml.Marshal(doc)
}

type yamlDoc struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description,omitempty"`
	Version     string      `yaml:"version"`
	Brain       brainDoc    `yaml:"brain"`
	Tools       toolsDoc    `yaml:"tools"`
	Harness     *harnessDoc `yaml:"harness,omitempty"`
}

type brainDoc struct {
	Model            string `yaml:"model"`
	InstructionsFile string `yaml:"instructions_file"`
}

type toolsDoc struct {
	Builtin []string `yaml:"builtin"`
}

type harnessDoc struct {
	Permissions permList `yaml:"permissions"`
}

type permItem struct {
	Name  string
	Level string
}

type permList []permItem

func (p permList) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for _, item := range p {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: item.Name},
			&yaml.Node{Kind: yaml.ScalarNode, Value: item.Level},
		)
	}
	return node, nil
}

func scalar(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func stringList(v any) []string {
	switch list := v.(type) {
	case []any:
		var out []string
		for _, item := range list {
			s := scalar(item)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return append([]string(nil), list...)
	default:
		return nil
	}
}
