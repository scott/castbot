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

// ErrAlias means the file uses a YAML alias or merge key.
// Aliases can point at a node that scrubbing removed, and the save would fail.
var ErrAlias = errors.New("yaml aliases are not allowed")

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
	if err := rejectAliases(&doc); err != nil {
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
		if err := rejectAliases(&doc); err != nil {
			return nil, err
		}
		if err := rejectDuplicates(&doc, ""); err != nil {
			return nil, err
		}
		dropAliases(&doc)
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
	shown := map[string]bool{"skills": true, "runtime": true, "tools": true}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := strings.TrimSpace(root.Content[i].Value)
		if key == "" || shown[key] || secretKey(key) {
			continue
		}
		val := root.Content[i+1]
		if key == "brain" {
			out = append(out, brainLeftovers(val)...)
			continue
		}
		if text := keptValue(val); text != "" {
			out = append(out, KeptItem{Label: labelize(key), Value: text})
		}
	}
	return out
}

func brainLeftovers(n *yaml.Node) []KeptItem {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var out []KeptItem
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := strings.TrimSpace(n.Content[i].Value)
		if key == "" || key == "model" || key == "instructions_file" || secretKey(key) {
			continue
		}
		if text := keptValue(n.Content[i+1]); text != "" {
			out = append(out, KeptItem{Label: "Brain " + key, Value: text})
		}
	}
	return out
}

func keptValue(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind {
	case yaml.ScalarNode:
		return clip(strings.TrimSpace(n.Value))
	case yaml.MappingNode:
		var parts []string
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := strings.TrimSpace(n.Content[i].Value)
			if key == "" || secretKey(key) || n.Content[i+1].Kind != yaml.ScalarNode {
				continue
			}
			parts = append(parts, key+" "+strings.TrimSpace(n.Content[i+1].Value))
		}
		if len(parts) == 0 {
			return "set"
		}
		return clip(strings.Join(parts, ", "))
	default:
		return "set"
	}
}

func labelize(key string) string {
	if key == "" {
		return key
	}
	return strings.ToUpper(key[:1]) + key[1:]
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

// SecretKey reports whether a config or argument key must not be shown.
func SecretKey(key string) bool { return secretKey(key) }

func secretKey(key string) bool {
	k := normalizeSecretKey(key)
	if k == "" {
		return false
	}
	parts := strings.Split(k, "_")
	// A trailing "keys" reads like "key", so api_keys matches api_key.
	if len(parts) >= 2 && parts[len(parts)-1] == "keys" {
		parts[len(parts)-1] = "key"
	}
	// A key with a version, number, encoding or environment after it is
	// still a key: ssh_key2, signing_key_v2, deploy_key_pem, stripe_key_live.
	parts = trimKeySuffix(parts)
	k = strings.Join(parts, "_")
	if ordinaryKey(k, parts) {
		return false
	}
	switch k {
	case "env", "headers", "token", "secret", "password", "api_key", "apikey",
		"access_token", "refresh_token", "authorization", "private_key",
		"client_secret", "credentials", "auth_token", "secret_key",
		"cookie", "cookies", "passphrase", "pwd", "access_key", "session_key",
		"auth", "pass", "jwt", "pat", "bearer", "basic_auth", "auth_header",
		"authorization_code":
		return true
	}
	// Names that end in _key are secrets unless ordinaryKey allowed them.
	if len(parts) >= 2 && parts[len(parts)-1] == "key" {
		return true
	}
	for i, p := range parts {
		if secretWord(p) {
			return true
		}
		if p == "pass" && passIsSecret(parts, i) {
			return true
		}
		if p == "cookie" || p == "cookies" {
			// set_cookie and session_cookie hold the value. cookie_name and
			// cookie_domain only describe it.
			if i == len(parts)-1 {
				return true
			}
			switch parts[i+1] {
			case "value", "values", "header", "string", "data", "jar":
				return true
			}
		}
		if i+1 < len(parts) {
			switch parts[i] + "_" + parts[i+1] {
			case "api_key", "private_key", "secret_key", "access_key", "session_key",
				"client_secret", "basic_auth", "auth_header":
				return true
			}
		}
	}
	return false
}

// secretWord is a segment that hides any name it appears in.
func secretWord(p string) bool {
	switch p {
	case "token", "tokens", "secret", "secrets", "password", "passwords",
		"passwd", "passwds", "pw", "pwd", "passphrase",
		"credential", "credentials", "auth", "jwt", "pat", "bearer", "assertion":
		return true
	}
	return false
}

// passIsSecret reports whether the "pass" segment at i is a password.
// Trailing version, number, encoding and environment words are dropped
// first, so db_pass_prod reads as db_pass and pass_2 as pass. pass alone,
// or at the end of a name (db_pass, user_pass, DB_PASS), is a password
// unless an ordinal or step word comes before it (first_pass, last_pass).
// Any word after it is a password too (pass_word, pass_env) unless it is a
// known non-secret word (pass_rate, pass_count, pass_through).
func passIsSecret(parts []string, i int) bool {
	end := len(parts)
	for end > i+1 && keySuffix(parts[end-1]) {
		end--
	}
	if i+1 < end {
		switch parts[i+1] {
		case "rate", "rates", "count", "counts", "through", "thru", "fail", "fails",
			"failed", "number", "num", "index", "idx", "ratio", "percent", "pct",
			"total", "threshold", "status", "result", "results", "time", "duration":
			return false
		}
		return true
	}
	if i == 0 {
		return true
	}
	switch parts[i-1] {
	case "first", "second", "third", "last", "next", "final", "initial", "single",
		"multi", "dry", "each", "every", "per", "one", "two", "render", "compile",
		"build", "lint", "check", "review", "by":
		return false
	}
	return true
}

// trimKeySuffix drops version, number, encoding and environment words that
// follow a "key" segment, so the name is judged as the key itself.
func trimKeySuffix(parts []string) []string {
	end := len(parts)
	for end > 1 && keySuffix(parts[end-1]) {
		end--
	}
	if end == len(parts) || end < 2 || (parts[end-1] != "key" && parts[end-1] != "keys") {
		return parts
	}
	out := append([]string(nil), parts[:end]...)
	if out[len(out)-1] == "keys" {
		out[len(out)-1] = "key"
	}
	return out
}

// keySuffix is a word that can follow "key" without changing what it holds.
// Digits split off by normalizeSecretKey (v2 becomes v_2, b64 becomes b_64)
// leave "v", "b" and "base" on their own, so those count too.
func keySuffix(p string) bool {
	if p == "" {
		return false
	}
	digits := true
	for _, r := range p {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		return true
	}
	switch p {
	case "v", "ver", "version", "old", "new", "prev", "next", "current", "alt",
		"primary", "secondary", "b", "b64", "base", "base64", "hex", "pem", "der", "raw", "bytes",
		"encoded", "json", "live", "test", "prod", "production", "dev", "staging",
		"sandbox", "local":
		return true
	}
	return false
}

// ordinaryKey keeps names that only look like secrets.
// Token counts stay visible. A short list of ordinary *_key names stays visible.
// Public keys stay visible unless a secret word comes before them.
// max_ and count do not keep max_secret or secret_count.
func ordinaryKey(k string, parts []string) bool {
	switch k {
	case "tokens", "tokenizer",
		"sort_key", "cache_key", "object_key", "partition_key", "primary_key",
		"idempotency_key", "host_key", "public_key":
		return true
	}
	if tokenCountKey(parts) {
		return true
	}
	// A public key is not a secret, so client_public_key and ssh_public_key
	// stay. secret_public_key and private_public_key do not.
	if n := len(parts); n >= 2 && parts[n-2] == "public" && parts[n-1] == "key" {
		for i, p := range parts[:n-2] {
			if secretWord(p) || p == "private" || p == "secrets" || (p == "pass" && passIsSecret(parts[:n-2], i)) {
				return false
			}
		}
		return true
	}
	// auth_mode and auth_type name a scheme, not a credential.
	if len(parts) == 2 && parts[0] == "auth" {
		switch parts[1] {
		case "mode", "type", "method", "scheme", "kind", "provider", "required", "enabled":
			return true
		}
	}
	return false
}

// tokenCountKey reports names built only from token-count words, such as
// max_tokens, input_token_count, cached_tokens or max_tokens_to_sample.
// Any other word (access, api, github, secret) keeps the name hidden.
// read, write and new are not enough on their own, so read_tokens,
// write_tokens and new_tokens stay hidden while max_new_tokens and
// cache_read_input_tokens show. A rate such as tokens_per_second shows.
func tokenCountKey(parts []string) bool {
	plural, single, count, strong := false, false, false, false
	per, unit := false, false
	for _, p := range parts {
		switch p {
		case "per":
			per = true
		case "second", "seconds", "sec", "minute", "minutes", "hour", "hours":
			// A rate such as tokens_per_second, only after "per".
			unit = true
			strong = true
		case "tokens":
			plural = true
		case "token":
			single = true
		case "count", "counts":
			count = true
			strong = true
		case "max", "min", "num", "total", "input", "output", "prompt", "completion",
			"cached", "cache", "reasoning", "thinking", "audio", "text", "image",
			"accepted", "rejected", "prediction", "limit", "budget", "used", "remaining":
			strong = true
		case "new", "to", "sample", "read", "write", "creation":
		default:
			return false
		}
	}
	if unit && !per {
		return false
	}
	if len(parts) == 1 {
		return plural
	}
	return strong && (plural || (single && count))
}

// normalizeSecretKey folds camelCase, acronyms, digits and dashes into
// lower-case words joined by "_". privateKey becomes private_key, APIToken
// becomes api_token, SSHKey becomes ssh_key and password2 becomes password_2.
func normalizeSecretKey(key string) string {
	rs := []rune(strings.TrimSpace(key))
	var b strings.Builder
	sep := func() {
		s := b.String()
		if s != "" && !strings.HasSuffix(s, "_") {
			b.WriteByte('_')
		}
	}
	isUpper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
	isLower := func(r rune) bool { return r >= 'a' && r <= 'z' }
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' }
	for i, r := range rs {
		var prev, next rune
		if i > 0 {
			prev = rs[i-1]
		}
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		switch {
		case r == '-' || r == '_' || r == '.' || r == ' ':
			sep()
		case isUpper(r):
			// Split lower-to-upper (apiKey), digit-to-upper (v2Key) and the end
			// of an acronym run before a capitalised word (APIToken).
			if isLower(prev) || isDigit(prev) || (isUpper(prev) && isLower(next)) {
				sep()
			}
			b.WriteRune(r - 'A' + 'a')
		case isDigit(r):
			if i > 0 && !isDigit(prev) {
				sep()
			}
			b.WriteRune(r)
		default:
			if isDigit(prev) && (isLower(r) || isUpper(r)) {
				sep()
			}
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "_")
}

func rejectAliases(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode || n.Alias != nil {
		return ErrAlias
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if strings.TrimSpace(n.Content[i].Value) == "<<" {
				return ErrAlias
			}
			if err := rejectAliases(n.Content[i+1]); err != nil {
				return err
			}
		}
		return nil
	}
	for _, child := range n.Content {
		if err := rejectAliases(child); err != nil {
			return err
		}
	}
	return nil
}

func dropAliases(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.AliasNode || n.Alias != nil {
		n.Kind = yaml.ScalarNode
		n.Alias = nil
		n.Value = ""
		n.Tag = "!!str"
		n.Content = nil
		return
	}
	if n.Kind == yaml.MappingNode {
		next := n.Content[:0]
		for i := 0; i+1 < len(n.Content); i += 2 {
			if strings.TrimSpace(n.Content[i].Value) == "<<" || n.Content[i+1].Kind == yaml.AliasNode || n.Content[i+1].Alias != nil {
				continue
			}
			dropAliases(n.Content[i+1])
			next = append(next, n.Content[i], n.Content[i+1])
		}
		n.Content = next
		return
	}
	for _, child := range n.Content {
		dropAliases(child)
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
