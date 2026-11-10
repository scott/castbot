package castcfg

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is the agent form. It is the subset of agent.yaml the runtime reads
// plus the instructions body. Kept is the rest of the document, with secret
// values removed, so a save does not drop keys the form does not edit.
type Spec struct {
	Name             string
	Title            string
	Version          string
	Model            string
	Instructions     string
	InstructionsFile string
	Tools            []string
	Permissions      map[string]string
	Kept             []byte
}

// ParseYAML reads name, brain.model, tools.builtin, and harness.permissions.
// Keys the form does not own stay in Kept. env is not kept.
func ParseYAML(text string) (Spec, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return Spec{}, err
	}
	root := mappingRoot(&doc)
	spec := Spec{Permissions: map[string]string{}}
	if root != nil {
		spec.Name = mapScalar(root, "name")
		spec.Title = mapScalar(root, "description")
		spec.Version = mapScalar(root, "version")
		if brain := mapChild(root, "brain"); brain != nil {
			spec.Model = mapScalar(brain, "model")
			spec.InstructionsFile = mapScalar(brain, "instructions_file")
		}
		if tools := mapChild(root, "tools"); tools != nil {
			spec.Tools = seqStrings(mapGet(tools, "builtin"))
		}
		if harness := mapChild(root, "harness"); harness != nil {
			if perms := mapChild(harness, "permissions"); perms != nil {
				for i := 0; i+1 < len(perms.Content); i += 2 {
					key := strings.TrimSpace(perms.Content[i].Value)
					if key == "" || key == "mcp" || key == "*" {
						continue
					}
					val := strings.TrimSpace(perms.Content[i+1].Value)
					level, ok := NormalizeLevel(val)
					if !ok {
						spec.Permissions[key] = val
						continue
					}
					spec.Permissions[key] = level
				}
			}
		}
	}
	kept := doc
	if kroot := mappingRoot(&kept); kroot != nil {
		dropKey(kroot, "env")
	}
	raw, err := yaml.Marshal(&kept)
	if err != nil {
		return Spec{}, err
	}
	spec.Kept = raw
	return spec, nil
}

// InstructionsFile is the brain.instructions_file value, or empty.
func InstructionsFile(text string) string {
	spec, err := ParseYAML(text)
	if err != nil {
		return ""
	}
	return spec.InstructionsFile
}

// RenderYAML writes agent.yaml. Owned fields come from the form. Other keys
// in Kept are copied through. env is never written.
func RenderYAML(spec Spec) ([]byte, error) {
	var doc yaml.Node
	if len(strings.TrimSpace(string(spec.Kept))) > 0 {
		if err := yaml.Unmarshal(spec.Kept, &doc); err != nil {
			doc = yaml.Node{}
		}
	}
	root := ensureMapping(&doc)
	dropKey(root, "env")
	version := strings.TrimSpace(spec.Version)
	if version == "" || strings.ContainsAny(version, " \t\r\n") {
		version = "1.0"
	}
	setScalar(root, "name", strings.TrimSpace(spec.Name))
	if title := strings.TrimSpace(spec.Title); title == "" {
		dropKey(root, "description")
	} else {
		setScalar(root, "description", title)
	}
	setScalar(root, "version", version)
	brain := ensureChild(root, "brain")
	setScalar(brain, "model", strings.TrimSpace(spec.Model))
	setScalar(brain, "instructions_file", "instructions.md")
	tools := ensureChild(root, "tools")
	setSeq(tools, "builtin", spec.Tools)
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
	if len(perms) == 0 {
		if harness := mapChild(root, "harness"); harness != nil {
			dropKey(harness, "permissions")
			if len(harness.Content) == 0 {
				dropKey(root, "harness")
			}
		}
	} else {
		harness := ensureChild(root, "harness")
		setKey(harness, "permissions", permNode(perms))
	}
	return yaml.Marshal(&doc)
}

func mappingRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	n := doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 || n.Content[0].Kind != yaml.MappingNode {
			return nil
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

func ensureMapping(doc *yaml.Node) *yaml.Node {
	if root := mappingRoot(doc); root != nil {
		return root
	}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	*doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}
	return root
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapChild(m *yaml.Node, key string) *yaml.Node {
	n := mapGet(m, key)
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

func mapScalar(m *yaml.Node, key string) string {
	n := mapGet(m, key)
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(n.Value)
}

func ensureChild(m *yaml.Node, key string) *yaml.Node {
	if n := mapChild(m, key); n != nil {
		return n
	}
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setKey(m, key, n)
	return n
}

func setScalar(m *yaml.Node, key, value string) {
	setKey(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func setKey(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

func dropKey(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	next := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			continue
		}
		next = append(next, m.Content[i], m.Content[i+1])
	}
	m.Content = next
}

func setSeq(m *yaml.Node, key string, items []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, item := range items {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item})
	}
	setKey(m, key, seq)
}

func seqStrings(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for _, item := range n.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		s := strings.TrimSpace(item.Value)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func permNode(perms permList) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, item := range perms {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item.Name},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item.Level},
		)
	}
	return node
}

type permItem struct {
	Name  string
	Level string
}

type permList []permItem
