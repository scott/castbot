package castcfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
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

var (
	// Any value after Bearer or Basic is hidden, including short and letters-only tokens.
	// A closing quote is not part of the value.
	bearerAny = regexp.MustCompile(`(?i)\bbearer\s+\S+`)
	basicAny  = regexp.MustCompile(`(?i)\bbasic\s+\S+`)
	// A password or user stuck to -p or -u, as in mysql -pSECRET or curl -uuser:pass.
	flagStick = regexp.MustCompile(`(?i)(^|[\s])(-u|-p)(\S+)`)
	// A credential flag, then a separate value. sort -u names.txt is hidden too.
	flagNext = regexp.MustCompile(`(?i)(^|[\s])(--user|--password|--token|--api-key|--apikey|--passphrase|--pass|-u)(\s+)("[^"]*"|'[^']*'|\S+)`)
	// --user= is a secret even though the name "user" is not.
	flagEq = regexp.MustCompile(`(?i)(^|[\s])(--user|--password|--token|--api-key|--apikey|--passphrase|--pass)(=)("[^"]*"|'[^']*'|\S+)`)
	// scheme://user:pass@host and scheme://token@host.
	urlUserinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^\s/@]+(?::[^\s/@]*)?@`)
)

// scrubString hides credential values inside a string and keeps the rest.
// A false positive is preferred to a leaked secret.
func scrubScheme(s string, re *regexp.Regexp, label string) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		trimmed := strings.TrimRight(m, `"'`)
		return label + m[len(trimmed):]
	})
}

func scrubString(s string) (string, bool) {
	next := scrubScheme(s, bearerAny, "Bearer Hidden")
	next = scrubScheme(next, basicAny, "Basic Hidden")
	next = flagStick.ReplaceAllString(next, "${1}${2}Hidden")
	next = flagNext.ReplaceAllString(next, "${1}${2}${3}Hidden")
	next = flagEq.ReplaceAllString(next, "${1}${2}${3}Hidden")
	next = urlUserinfo.ReplaceAllString(next, "${1}Hidden@")
	next = scrubLabeled(next)
	return next, next != s
}

// valueSecretName reports a name whose value must be hidden inside a string.
// JSON keys still use SecretKey. This is wider on purpose: query names, flags,
// assignments, and header labels such as X-Api-Key and X-Amz-Signature.
func valueSecretName(name string) bool {
	if SecretKey(name) {
		return true
	}
	k := normalizeSecretKey(name)
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
		for raw > i && (s[raw-1] == ' ' || s[raw-1] == '\t' || s[raw-1] == '"' || s[raw-1] == '\'') {
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
		for nameEnd > nameStart && (s[nameEnd-1] == ' ' || s[nameEnd-1] == '\t' || s[nameEnd-1] == '"' || s[nameEnd-1] == '\'') {
			nameEnd--
		}
		if nameStart >= nameEnd || !isLabelStart(s[nameStart]) {
			b.WriteString(s[i : sep+1])
			i = sep + 1
			continue
		}
		name := s[nameStart:nameEnd]
		vStart := sep + 1
		for vStart < len(s) && (s[vStart] == ' ' || s[vStart] == '\t') {
			vStart++
		}
		vEnd := vStart
		if strings.EqualFold(name, "authorization") {
			for vEnd < len(s) && s[vEnd] != '\n' && s[vEnd] != '\r' {
				vEnd++
			}
			if valueSecretName(name) {
				b.WriteString(s[i:sep])
				b.WriteByte(s[sep])
				if vStart > sep+1 {
					b.WriteString(s[sep+1 : vStart])
				}
				b.WriteString("Hidden")
				i = vEnd
				continue
			}
		}
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
			for vEnd < len(s) && s[vEnd] != ' ' && s[vEnd] != '\t' && s[vEnd] != '\n' && s[vEnd] != '\r' && s[vEnd] != ';' && s[vEnd] != '&' && s[vEnd] != ',' {
				vEnd++
			}
		}
		if vEnd < i {
			vEnd = i
		}
		if !valueSecretName(name) {
			if vEnd <= i {
				b.WriteByte(s[sep])
				i = sep + 1
				continue
			}
			b.WriteString(s[i:vEnd])
			i = vEnd
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

func flagTakesNext(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "-u", "--user", "--token", "--password", "--api-key", "--apikey", "--pass", "--passphrase", "-p":
		return true
	default:
		return false
	}
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
	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	hideNext := false
	for _, val := range elems {
		var word string
		isString := len(val) > 0 && val[0] == '"' && json.Unmarshal(val, &word) == nil
		var cleaned json.RawMessage
		if hideNext && isString {
			b, err := json.Marshal("Hidden")
			if err != nil {
				return nil, err
			}
			cleaned = b
			if path != "" {
				*redacted = append(*redacted, path)
			}
			hideNext = false
		} else {
			hideNext = isString && flagTakesNext(word)
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
