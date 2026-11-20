package replacetool

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Apply adds the replace skill and forces the replace tools to always-allow.
// It does not add an MCP server. Invalid YAML, aliases, and a non-mapping
// root are returned unchanged.
func Apply(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return text
	}
	if hasAlias(&doc) {
		return text
	}
	root := mappingRoot(&doc)
	if root == nil {
		return text
	}
	ensureSkill(root)
	for _, spec := range Specs() {
		ensurePerm(root, spec.Name)
	}
	raw, err := yaml.Marshal(&doc)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return text
	}
	return string(raw)
}

func skillNode() *yaml.Node {
	item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setScalar(item, "name", SkillName)
	setScalar(item, "description", SkillSummary)
	setScalar(item, "path", SkillDir)
	return item
}

func ensureSkill(root *yaml.Node) {
	skills := mapGet(root, "skills")
	if skills != nil && skills.Kind == yaml.MappingNode {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for i := 0; i+1 < len(skills.Content); i += 2 {
			name := strings.TrimSpace(skills.Content[i].Value)
			if name == "" || name == SkillName {
				continue
			}
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
		}
		seq.Content = append(seq.Content, skillNode())
		setKey(root, "skills", seq)
		return
	}
	if skills != nil && skills.Kind != yaml.SequenceNode {
		return
	}
	if skills == nil {
		skills = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setKey(root, "skills", skills)
	}
	for i, item := range skills.Content {
		if item.Kind == yaml.MappingNode && mapScalar(item, "name") == SkillName {
			setScalar(item, "name", SkillName)
			setScalar(item, "description", SkillSummary)
			setScalar(item, "path", SkillDir)
			return
		}
		if item.Kind == yaml.ScalarNode && strings.TrimSpace(item.Value) == SkillName {
			skills.Content[i] = skillNode()
			return
		}
	}
	skills.Content = append(skills.Content, skillNode())
}

func ensurePerm(root *yaml.Node, name string) {
	harness := mapChild(root, "harness")
	if harness == nil {
		harness = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setKey(root, "harness", harness)
	}
	perms := mapChild(harness, "permissions")
	if perms == nil {
		perms = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setKey(harness, "permissions", perms)
	}
	setScalar(perms, name, "always-allow")
}

func mappingRoot(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	n := doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) != 1 {
			return nil
		}
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

func hasAlias(n *yaml.Node) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.AliasNode || n.Alias != nil {
		return true
	}
	for _, child := range n.Content {
		if hasAlias(child) {
			return true
		}
	}
	return false
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
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		val,
	)
}
