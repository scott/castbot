package castcfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// ScrubJSON removes values stored under secret keys.
// redacted lists those keys, including nested ones, and never includes a value.
// The returned JSON keeps the other keys in their original order.
// A value that is not a JSON object or array is returned unchanged.
func ScrubJSON(raw json.RawMessage) (json.RawMessage, []string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return json.RawMessage("{}"), nil, nil
	}
	var redacted []string
	clean, err := scrubJSON(raw, "", &redacted)
	if err != nil {
		return nil, nil, err
	}
	return clean, redacted, nil
}

func scrubJSON(raw json.RawMessage, path string, redacted *[]string) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return json.RawMessage("null"), nil
	}
	switch raw[0] {
	case '{':
		return scrubObject(raw, path, redacted)
	case '[':
		return scrubArray(raw, path, redacted)
	default:
		return scrubScalar(raw, path, redacted)
	}
}

var errBadJSON = errors.New("bad json")

// shellMeta is the one set of characters that ends a secret value.
// Whitespace is space, tab, CR, LF, VT, and FF. A redirect, a quote, a
// command substitution, or a shell break stays in the command.
const shellMeta = " \t\n\r\v\f;|&<>'\"`()$"

// shellToken is one credential token: every byte except shellMeta.
var shellToken = shellValueClass()

func shellValueClass() string {
	var b strings.Builder
	b.WriteString("[^")
	for i := 0; i < len(shellMeta); i++ {
		switch shellMeta[i] {
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\v':
			b.WriteString(`\v`)
		case '\f':
			b.WriteString(`\f`)
		default:
			b.WriteByte(shellMeta[i])
		}
	}
	b.WriteString("]+")
	return b.String()
}

func shellStop(c byte) bool {
	return strings.IndexByte(shellMeta, c) >= 0
}

func shellSpace(c byte) bool {
	return shellStop(c) && unicode.IsSpace(rune(c))
}

func writeShellSpace(b *strings.Builder) {
	for i := 0; i < len(shellMeta); i++ {
		if !shellSpace(shellMeta[i]) {
			continue
		}
		switch shellMeta[i] {
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\v':
			b.WriteString(`\v`)
		case '\f':
			b.WriteString(`\f`)
		default:
			b.WriteByte(shellMeta[i])
		}
	}
}

// shellSpaces is the whitespace in shellMeta, as a regex class.
func shellSpaceClass() string {
	var b strings.Builder
	b.WriteByte('[')
	writeShellSpace(&b)
	b.WriteByte(']')
	return b.String()
}

// shellIntroClass is where a flag may start: shell whitespace, or ; | &.
func shellIntroClass() string {
	var b strings.Builder
	b.WriteByte('[')
	writeShellSpace(&b)
	b.WriteString(`;|&`)
	b.WriteByte(']')
	return b.String()
}

var (
	shellSpaces = shellSpaceClass()
	shellIntro  = shellIntroClass()
)

var (
	// Any value after Bearer or Basic is hidden, including short and letters-only tokens.
	// The gap is any whitespace in shellMeta. A URL is not a credential.
	bearerAny = regexp.MustCompile(`(?i)\bbearer` + shellSpaces + `+` + shellToken)
	basicAny  = regexp.MustCompile(`(?i)\bbasic` + shellSpaces + `+` + shellToken)
	// authorization: token VALUE hides one token. Bearer and Basic are above.
	authTokenScheme = regexp.MustCompile(`(?i)((?:proxy-)?authorization` + shellSpaces + `*:` + shellSpaces + `*token` + shellSpaces + `+)(` + shellToken + `)`)
	// A private key body. The BEGIN and END lines stay so the command is recognizable.
	pemPrivate = regexp.MustCompile(`(?s)(-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----)(.*?)(-----END [A-Z0-9 ]*PRIVATE KEY-----)`)
	// Token shapes that are secrets even with no name beside them.
	knownToken = regexp.MustCompile(`\b(?:ghp_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|sk-[A-Za-z0-9_-]{8,})`)
	// --name value and --name=value. The value uses shellToken. A quoted value stays whole.
	longFlag = regexp.MustCompile(`(?i)(^|` + shellIntro + `)(--)([A-Za-z0-9][A-Za-z0-9-]*)(=` + shellSpaces + `*|` + shellSpaces + `+)("[^"]*"|'[^']*'|` + shellToken + `)`)
	// Short flags. Only a known credential tool uses these. The value uses shellToken.
	shortFlag = regexp.MustCompile(`(?i)(^|` + shellIntro + `)(-u|-p|-a)(` + shellSpaces + `*)(` + shellToken + `)`)
)

// scrubString hides credential values inside a string and keeps the rest.
// Only the secret token is replaced. The rest of the command stays visible.
func scrubScheme(s string, re *regexp.Regexp, label string) string {
	locs := re.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		if loc[0] < last {
			continue
		}
		end := loc[1]
		valStart := end
		for valStart > loc[0] && !shellSpace(s[valStart-1]) {
			valStart--
		}
		end = cutRedirectFD(s, valStart, end)
		if strings.Contains(s[valStart:end], "://") {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(label)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// cutRedirectFD leaves a file-descriptor digit run on the tail of `n>&` and `n<&`.
// `echo Bearer x12>&1` keeps the `12`. A plain `>` redirect does not, so
// `token=t1>f` still hides the whole value.
func cutRedirectFD(s string, valStart, valEnd int) int {
	if valEnd <= valStart || valEnd+1 >= len(s) {
		return valEnd
	}
	redir := s[valEnd]
	if (redir != '>' && redir != '<') || s[valEnd+1] != '&' {
		return valEnd
	}
	i := valEnd
	for i > valStart && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	if i == valEnd || i == valStart {
		return valEnd
	}
	return i
}

func scrubString(s string) (string, bool) {
	next := pemPrivate.ReplaceAllString(s, "${1}\nHidden\n${3}")
	next = knownToken.ReplaceAllString(next, "Hidden")
	next = scrubScheme(next, bearerAny, "Bearer Hidden")
	next = scrubScheme(next, basicAny, "Basic Hidden")
	next = authTokenScheme.ReplaceAllString(next, "${1}Hidden")
	next = scrubToolFlags(next)
	next = scrubURLUserinfo(next)
	next = scrubLabeled(next)
	return next, next != s
}

func commandSubst(v string) bool {
	return strings.Contains(v, "$(") || strings.Contains(v, "`")
}

// scrubURLUserinfo hides user:password and token userinfo.
// The user and password stop at shellMeta. @ still ends the authority.
// A redirect after the password stays. A host:port redirect is not userinfo
// unless @ appears before the next space.
func scrubURLUserinfo(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], "://")
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		j += i
		start, ok := schemeStart(s, j)
		if !ok || start < i {
			b.WriteString(s[i : j+3])
			i = j + 3
			continue
		}
		b.WriteString(s[i:start])
		scheme := s[start : j+3]
		p := j + 3
		userEnd := scanURLPart(s, p)
		passAt := -1
		if userEnd == p && p < len(s) && s[p] == ':' {
			// scheme://:password@host has an empty user. The colon starts the password.
			passAt = p + 1
		} else if userEnd > p && userEnd < len(s) && s[userEnd] == ':' {
			passAt = userEnd + 1
		} else if userEnd > p && userEnd < len(s) && s[userEnd] == '@' {
			b.WriteString(scheme)
			b.WriteString("Hidden@")
			i = userEnd + 1
			continue
		}
		if passAt >= 0 {
			if repl, next, ok := takePassword(s, scheme, passAt); ok {
				b.WriteString(repl)
				i = next
				continue
			}
		}
		b.WriteString(scheme)
		i = j + 3
	}
	return b.String()
}

// takePassword hides the password that starts at i, just after ':'.
// Whitespace there is a separator. @ ends the authority. A shell mark ends
// the password and stays in the result when @ still follows.
func takePassword(s, scheme string, i int) (string, int, bool) {
	for i < len(s) && shellSpace(s[i]) {
		i++
	}
	start := i
	end := scanURLPart(s, start)
	if end < len(s) && s[end] == '@' {
		return scheme + "Hidden@", end + 1, true
	}
	if end > start && end < len(s) && shellStop(s[end]) && atSignBeforeSpace(s, end+1) {
		cut := cutRedirectFD(s, start, end)
		if cut == start {
			cut = end
		}
		return scheme + "Hidden", cut, true
	}
	return "", 0, false
}

func schemeStart(s string, colon int) (int, bool) {
	i := colon
	for i > 0 {
		c := s[i-1]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '+' || c == '.' || c == '-' {
			i--
			continue
		}
		break
	}
	if i == colon {
		return 0, false
	}
	c := s[i]
	if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
		return 0, false
	}
	return i, true
}

func scanURLPart(s string, i int) int {
	for i < len(s) && !shellStop(s[i]) && s[i] != ':' && s[i] != '@' && s[i] != '/' && s[i] != '?' && s[i] != '#' {
		i++
	}
	return i
}

func atSignBeforeSpace(s string, i int) bool {
	for i < len(s) {
		if s[i] == '@' {
			return true
		}
		if shellSpace(s[i]) {
			return false
		}
		i++
	}
	return false
}

func commandBefore(s string, pos int) string {
	start := pos
	for start > 0 {
		switch s[start-1] {
		case ';', '|', '&', '\n', '\r':
			goto scan
		}
		start--
	}
scan:
	fields := strings.Fields(s[start:pos])
	if len(fields) == 0 {
		return ""
	}
	cmd := fields[0]
	if i := strings.LastIndex(cmd, "/"); i >= 0 {
		cmd = cmd[i+1:]
	}
	cmd = strings.ToLower(cmd)
	if strings.HasPrefix(cmd, "-") {
		return ""
	}
	if cmd == "docker" {
		for _, f := range fields[1:] {
			if strings.EqualFold(f, "login") {
				return "docker login"
			}
		}
	}
	return cmd
}

func scrubToolFlags(s string) string {
	s = replaceFlagValues(s, longFlag, func(m []string, cmd string) (string, bool) {
		name := m[3]
		val := m[5]
		if commandSubst(val) {
			return "", false
		}
		if (cmd == "curl" || cmd == "wget") && strings.EqualFold(name, "user") && strings.Contains(val, ":") {
			return m[1] + m[2] + name + m[4] + "Hidden", true
		}
		if valueSecretName(name) {
			return m[1] + m[2] + name + m[4] + "Hidden", true
		}
		return "", false
	})
	return replaceFlagValues(s, shortFlag, func(m []string, cmd string) (string, bool) {
		flag := strings.ToLower(m[2])
		space := m[3]
		val := m[4]
		switch flag {
		case "-u":
			if (cmd == "curl" || cmd == "wget") && strings.Contains(val, ":") {
				return m[1] + flag + space + "Hidden", true
			}
		case "-p":
			stuck := space == ""
			switch cmd {
			case "mysql", "mariadb", "mysqldump":
				if stuck && val != "" {
					return m[1] + flag + "Hidden", true
				}
			case "sshpass", "docker login":
				return m[1] + flag + space + "Hidden", true
			}
		case "-a":
			if cmd == "redis-cli" {
				return m[1] + flag + space + "Hidden", true
			}
		}
		return "", false
	})
}

func replaceFlagValues(s string, re *regexp.Regexp, decide func(m []string, cmd string) (string, bool)) string {
	locs := re.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		if loc[0] < last {
			continue
		}
		full := s[loc[0]:loc[1]]
		m := re.FindStringSubmatch(full)
		if m == nil {
			continue
		}
		end := loc[1]
		val := m[len(m)-1]
		valStart := end - len(val)
		if cut := cutRedirectFD(s, valStart, end); cut != end {
			m[len(m)-1] = s[valStart:cut]
			end = cut
		}
		cmd := commandBefore(s, loc[0])
		repl, ok := decide(m, cmd)
		if !ok {
			continue
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(repl)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// valueSecretName reports a name whose value must be hidden inside a string.
// JSON keys still use SecretKey. This is wider on purpose: query names, flags,
// assignments, and header labels such as X-Api-Key and X-Amz-Signature.
func valueSecretName(name string) bool {
	k := normalizeSecretKey(name)
	// --secret-file and --password-file name a path. --token-limit names a count.
	if strings.HasSuffix(k, "_file") || strings.HasSuffix(k, "_limit") {
		return false
	}
	if SecretKey(name) {
		return true
	}
	if k == "password" || strings.HasSuffix(k, "password") {
		return true
	}
	switch k {
	case "key", "keys", "signature", "sig", "apikey", "api_key":
		return true
	}
	parts := strings.Split(k, "_")
	if len(parts) == 0 {
		return false
	}
	switch parts[len(parts)-1] {
	case "signature", "sig":
		return true
	}
	return false
}

func isLabelChar(c byte) bool {
	return c == '_' || c == '-' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

func isLabelStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// scrubLabeled hides the value of NAME=value and NAME: value when the name
// is a secret. The value may be quoted. The name may follow a quote, &, or &&.
func scrubLabeled(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		sep := -1
		for j := i; j < len(s); j++ {
			if s[j] == '=' || s[j] == ':' {
				sep = j
				break
			}
		}
		if sep < 0 {
			b.WriteString(s[i:])
			break
		}
		if s[sep] == ':' && sep+1 < len(s) && s[sep+1] == '/' {
			b.WriteString(s[i : sep+1])
			i = sep + 1
			continue
		}
		raw := sep
		for raw > i && (shellSpace(s[raw-1]) || s[raw-1] == '"' || s[raw-1] == '\'') {
			raw--
		}
		for raw > i && isLabelChar(s[raw-1]) {
			raw--
		}
		nameStart := raw
		for nameStart < sep && s[nameStart] == '-' {
			nameStart++
		}
		nameEnd := sep
		for nameEnd > nameStart && (shellSpace(s[nameEnd-1]) || s[nameEnd-1] == '"' || s[nameEnd-1] == '\'') {
			nameEnd--
		}
		if nameStart >= nameEnd || !isLabelStart(s[nameStart]) {
			b.WriteString(s[i : sep+1])
			i = sep + 1
			continue
		}
		name := s[nameStart:nameEnd]
		vStart := sep + 1
		for vStart < len(s) && shellSpace(s[vStart]) {
			vStart++
		}
		vEnd := vStart
		if vStart < len(s) && (s[vStart] == '"' || s[vStart] == '\'') {
			q := s[vStart]
			vEnd = vStart + 1
			for vEnd < len(s) && s[vEnd] != q {
				vEnd++
			}
			if vEnd < len(s) && s[vEnd] == q {
				vEnd++
			}
		} else {
			for vEnd < len(s) && !shellStop(s[vEnd]) {
				vEnd++
			}
			vEnd = cutRedirectFD(s, vStart, vEnd)
		}
		if vEnd < i {
			vEnd = i
		}
		if !labelIsSecret(name, s[sep]) {
			if vEnd <= i {
				b.WriteByte(s[sep])
				i = sep + 1
				continue
			}
			b.WriteString(s[i:vEnd])
			i = vEnd
			continue
		}
		if vEnd == vStart || strings.Contains(s[vStart:vEnd], "$(") || strings.Contains(s[vStart:vEnd], "`") {
			b.WriteString(s[i:vEnd])
			if vEnd <= i {
				i = sep + 1
			} else {
				i = vEnd
			}
			continue
		}
		b.WriteString(s[i:sep])
		b.WriteByte(s[sep])
		if vStart > sep+1 {
			b.WriteString(s[sep+1 : vStart])
		}
		b.WriteString("Hidden")
		i = vEnd
	}
	return b.String()
}

// labelIsSecret reports a NAME=value or NAME: value whose value is a secret.
// A bare key: label is prose. key= in a query string is still a secret.
func labelIsSecret(name string, sep byte) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization":
		// Bearer, Basic, and token schemes hide one value. Other words stay.
		return false
	}
	if sep == ':' {
		switch strings.ToLower(name) {
		case "key", "keys":
			return false
		}
	}
	return valueSecretName(name)
}

func scrubScalar(raw json.RawMessage, path string, redacted *[]string) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if !json.Valid(raw) {
		return nil, errBadJSON
	}
	if len(raw) < 2 || raw[0] != '"' {
		return raw, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	cleaned, ok := scrubString(s)
	if !ok {
		return raw, nil
	}
	if path != "" {
		*redacted = append(*redacted, path)
	}
	b, err := json.Marshal(cleaned)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func scrubObject(raw json.RawMessage, path string, redacted *[]string) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errBadJSON
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, errBadJSON
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		child := key
		if path != "" {
			child = path + "." + key
		}
		if SecretKey(key) {
			*redacted = append(*redacted, child)
			continue
		}
		cleaned, err := scrubJSON(val, child, redacted)
		if err != nil {
			return nil, err
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		kb, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(cleaned)
	}
	end, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := end.(json.Delim); !ok || d != '}' {
		return nil, errBadJSON
	}
	buf.WriteByte('}')
	return json.RawMessage(buf.Bytes()), nil
}

func argvCommand(words []string, upto int) string {
	if upto > len(words) {
		upto = len(words)
	}
	if upto < 1 {
		return ""
	}
	cmd := words[0]
	if i := strings.LastIndex(cmd, "/"); i >= 0 {
		cmd = cmd[i+1:]
	}
	cmd = strings.ToLower(cmd)
	if cmd == "docker" {
		for _, w := range words[1:upto] {
			if strings.EqualFold(w, "login") {
				return "docker login"
			}
		}
	}
	return cmd
}

func stuckArgv(cmd, word string) (string, bool) {
	low := strings.ToLower(word)
	switch cmd {
	case "mysql", "mariadb", "mysqldump":
		if strings.HasPrefix(low, "-p") && len(word) > 2 {
			return "-pHidden", true
		}
	case "curl", "wget":
		if strings.HasPrefix(low, "-u") && len(word) > 2 && strings.Contains(word, ":") {
			return "-uHidden", true
		}
	case "redis-cli":
		if strings.HasPrefix(low, "-a") && len(word) > 2 {
			return "-aHidden", true
		}
	case "sshpass", "docker login":
		if strings.HasPrefix(low, "-p") && len(word) > 2 {
			return "-pHidden", true
		}
	}
	return "", false
}

func argvHidesNext(cmd, flag, value string) bool {
	switch strings.ToLower(flag) {
	case "-u", "--user":
		return (cmd == "curl" || cmd == "wget") && strings.Contains(value, ":")
	case "-p":
		return cmd == "sshpass" || cmd == "docker login"
	case "-a":
		return cmd == "redis-cli"
	}
	name := strings.TrimLeft(flag, "-")
	if strings.HasPrefix(flag, "--") && valueSecretName(name) && !commandSubst(value) {
		return true
	}
	return false
}

func scrubArray(raw json.RawMessage, path string, redacted *[]string) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, errBadJSON
	}
	var elems []json.RawMessage
	for dec.More() {
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		elems = append(elems, val)
	}
	var words []string
	stringAt := map[int]bool{}
	for i, val := range elems {
		var word string
		if len(val) > 0 && val[0] == '"' && json.Unmarshal(val, &word) == nil {
			words = append(words, word)
			stringAt[i] = true
		} else {
			words = append(words, "")
		}
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	for i, val := range elems {
		var cleaned json.RawMessage
		if repl, ok := stuckArgv(argvCommand(words, i), words[i]); stringAt[i] && ok {
			b, err := json.Marshal(repl)
			if err != nil {
				return nil, err
			}
			cleaned = b
			if path != "" {
				*redacted = append(*redacted, path)
			}
		} else if stringAt[i] && i > 0 && stringAt[i-1] && argvHidesNext(argvCommand(words, i), words[i-1], words[i]) {
			b, err := json.Marshal("Hidden")
			if err != nil {
				return nil, err
			}
			cleaned = b
			if path != "" {
				*redacted = append(*redacted, path)
			}
		} else {
			var err error
			cleaned, err = scrubJSON(val, path, redacted)
			if err != nil {
				return nil, err
			}
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(cleaned)
	}
	end, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := end.(json.Delim); !ok || d != ']' {
		return nil, errBadJSON
	}
	buf.WriteByte(']')
	return json.RawMessage(buf.Bytes()), nil
}

// JSONString reads one string field from an object.
func JSONString(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	for _, key := range keys {
		val, ok := obj[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(val, &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
