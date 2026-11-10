package castcfg

import (
	"errors"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxKept = 64 << 10

// DuplicateKeyError means agent.yaml names one mapping key twice.
// Cast keeps the last copy. castbot rejects the file so the review
// and the runtime cannot disagree.
type DuplicateKeyError struct {
	Key string
}

func (e *DuplicateKeyError) Error() string { return "duplicate key " + e.Key }

// ErrTooLarge means the keys kept beside the form are over the cap.
var ErrTooLarge = errors.New("agent file is too large")

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
	Removed          []string
}

// KeptItem is one read-only line for the review page.
type KeptItem struct {
	Label string
	Value string
}

// ParseYAML reads name, brain.model, tools.builtin, and harness.permissions.
// Duplicate keys are an error. Secret-shaped values are removed at every
// depth. Keys the form does not own stay in Kept.
func ParseYAML(text string) (Spec, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return Spec{}, err
	}
	if err := rejectDuplicates(&doc, ""); err != nil {
		return Spec{}, err
	}
	root := mappingRoot(&doc)
	var removed []string
	if root != nil {
		scrubNode(root, "", &removed)
	}
	spec := Spec{Permissions: map[string]string{}, Removed: removed}
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
		stripOwned(root)
	}
	if root != nil && len(root.Content) > 0 {
		raw, err := yaml.Marshal(&doc)
		if err != nil {
			return Spec{}, err
		}
		if len(raw) > maxKept {
			return Spec{}, ErrTooLarge
		}
		spec.Kept = raw
	}
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
// in Kept are copied through. Secret values are never written.
func RenderYAML(spec Spec) ([]byte, error) {
	var doc yaml.Node
	if len(strings.TrimSpace(string(spec.Kept))) > 0 {
		if err := yaml.Unmarshal(spec.Kept, &doc); err != nil {
			return nil, err
		}
		if err := rejectDuplicates(&doc, ""); err != nil {
			return nil, err
		}
	}
	root := ensureMapping(&doc)
	scrubNode(root, "", nil)
	stripOwned(root)
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

// Agree reports whether a re-parse of the rendered file matches the form.
// Cast loads agent.yaml with last-key-wins. Duplicate keys are rejected, so
// one copy is the copy Cast runs.
func Agree(form, parsed Spec) bool {
	if strings.TrimSpace(form.Name) != parsed.Name || strings.TrimSpace(form.Model) != parsed.Model {
		return false
	}
	if len(form.Tools) != len(parsed.Tools) {
		return false
	}
	for i := range form.Tools {
		name := form.Tools[i]
		if parsed.Tools[i] != name || form.Permissions[name] != parsed.Permissions[name] {
			return false
		}
	}
	return len(form.Permissions) == len(parsed.Permissions)
}

// SummarizeKept lists the skills, runtime, and MCP commands a save will keep.
func SummarizeKept(raw []byte) []KeptItem {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	root := mappingRoot(&doc)
	if root == nil {
		return nil
	}
	var out []KeptItem
	if skills := mapGet(root, "skills"); skills != nil {
		names := skillNames(skills)
		if len(names) > 0 {
			out = append(out, KeptItem{Label: "Skills", Value: strings.Join(names, ", ")})
		}
	}
	if runtime := mapGet(root, "runtime"); runtime != nil {
		if text := runtimeText(runtime); text != "" {
			out = append(out, KeptItem{Label: "Runtime", Value: text})
		}
	}
	if tools := mapChild(root, "tools"); tools != nil {
		if mcp := mapGet(tools, "mcp"); mcp != nil {
			out = append(out, mcpItems(mcp)...)
		}
	}
	return out
}

func skillNames(n *yaml.Node) []string {
	if n.Kind == yaml.SequenceNode {
		return seqStrings(n)
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		if name := strings.TrimSpace(n.Content[i].Value); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func runtimeText(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return clip(strings.TrimSpace(n.Value))
	}
	if n.Kind != yaml.MappingNode {
		return ""
	}
	var parts []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := strings.TrimSpace(n.Content[i].Value)
		val := n.Content[i+1]
		if key == "" || secretKey(key) || val.Kind != yaml.ScalarNode {
			continue
		}
		parts = append(parts, key+" "+strings.TrimSpace(val.Value))
	}
	return clip(strings.Join(parts, ", "))
}

func mcpItems(n *yaml.Node) []KeptItem {
	var out []KeptItem
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			name := strings.TrimSpace(n.Content[i].Value)
			if name == "" || secretKey(name) {
				continue
			}
			out = append(out, mcpLine(name, n.Content[i+1]))
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if item.Kind != yaml.MappingNode {
				continue
			}
			name := mapScalar(item, "name")
			if name == "" {
				name = mapScalar(item, "server")
			}
			if name == "" {
				name = "server"
			}
			out = append(out, mcpLine(name, item))
		}
	}
	return out
}

func mcpLine(name string, n *yaml.Node) KeptItem {
	cmd := ""
	if n != nil && n.Kind == yaml.MappingNode {
		cmd = commandOf(n)
	}
	if cmd == "" {
		return KeptItem{Label: "MCP " + name, Value: "no command"}
	}
	return KeptItem{Label: "MCP " + name, Value: clip(cmd)}
}

func commandOf(m *yaml.Node) string {
	parts := seqOrScalar(mapGet(m, "command"))
	parts = append(parts, seqOrScalar(mapGet(m, "args"))...)
	return strings.Join(parts, " ")
}

func seqOrScalar(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		s := strings.TrimSpace(n.Value)
		if s == "" {
			return nil
		}
		return []string{s}
	}
	return seqStrings(n)
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 180 {
		return s[:180]
	}
	return s
}

func rejectDuplicates(n *yaml.Node, path string) error {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			if err := rejectDuplicates(child, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			name := key
			if path != "" {
				name = path + "." + key
			}
			if seen[key] {
				return &DuplicateKeyError{Key: name}
			}
			seen[key] = true
			if err := rejectDuplicates(n.Content[i+1], name); err != nil {
				return err
			}
		}
	}
	return nil
}

func secretKey(key string) bool {
	k := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	switch k {
	case "env", "headers", "token", "secret", "password", "api_key", "apikey",
		"access_token", "refresh_token", "authorization", "private_key",
		"client_secret", "credentials", "auth_token", "secret_key":
		return true
	default:
		return false
	}
}

func vaultRef(s string) bool {
	s = strings.TrimSpace(s)
	var rest string
	switch {
	case strings.HasPrefix(s, "vault:"):
		rest = strings.TrimPrefix(s, "vault:")
	case strings.HasPrefix(s, "secret:"):
		rest = strings.TrimPrefix(s, "secret:")
	default:
		return false
	}
	if rest == "" || len(rest) > 64 {
		return false
	}
	for _, c := range rest {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
		default:
			return false
		}
	}
	return true
}

func scrubNode(n *yaml.Node, path string, removed *[]string) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			scrubNode(child, path, removed)
		}
	case yaml.MappingNode:
		next := n.Content[:0]
		for i := 0; i+1 < len(n.Content); i += 2 {
			keyNode := n.Content[i]
			val := n.Content[i+1]
			child := keyNode.Value
			if path != "" {
				child = path + "." + keyNode.Value
			}
			if secretKey(keyNode.Value) {
				kept, ok := scrubSecret(val, child, removed)
				if ok {
					next = append(next, keyNode, kept)
				}
				continue
			}
			scrubNode(val, child, removed)
			next = append(next, keyNode, val)
		}
		n.Content = next
	}
}

func scrubSecret(val *yaml.Node, path string, removed *[]string) (*yaml.Node, bool) {
	if val != nil && val.Kind == yaml.ScalarNode && vaultRef(val.Value) {
		return val, true
	}
	if val != nil && val.Kind == yaml.MappingNode {
		next := val.Content[:0]
		var dropped []string
		for i := 0; i+1 < len(val.Content); i += 2 {
			k := val.Content[i]
			v := val.Content[i+1]
			if v.Kind == yaml.ScalarNode && vaultRef(v.Value) {
				next = append(next, k, v)
				continue
			}
			dropped = append(dropped, path+"."+k.Value)
		}
		if len(next) == 0 {
			noteRemoved(removed, path)
			return nil, false
		}
		val.Content = next
		for _, name := range dropped {
			noteRemoved(removed, name)
		}
		return val, true
	}
	noteRemoved(removed, path)
	return nil, false
}

func noteRemoved(removed *[]string, path string) {
	if removed == nil || path == "" {
		return
	}
	*removed = append(*removed, path)
}

func stripOwned(root *yaml.Node) {
	if root == nil {
		return
	}
	dropKey(root, "name")
	dropKey(root, "description")
	dropKey(root, "version")
	if brain := mapChild(root, "brain"); brain != nil {
		dropKey(brain, "model")
		dropKey(brain, "instructions_file")
		if len(brain.Content) == 0 {
			dropKey(root, "brain")
		}
	}
	if tools := mapChild(root, "tools"); tools != nil {
		dropKey(tools, "builtin")
		if len(tools.Content) == 0 {
			dropKey(root, "tools")
		}
	}
	if harness := mapChild(root, "harness"); harness != nil {
		dropKey(harness, "permissions")
		if len(harness.Content) == 0 {
			dropKey(root, "harness")
		}
	}
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
