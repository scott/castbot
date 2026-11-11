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
	bearerValue = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/=-]{16,})`)
	basicValue  = regexp.MustCompile(`(?i)\bbasic\s+([A-Za-z0-9+/=]+)`)
	curlUser    = regexp.MustCompile(`(?i)(^|[\s])(--user|-u)(=|\s+)(\S+)`)
	urlUserinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^\s/@]+):([^\s/@]+)@`)
	querySecret = regexp.MustCompile(`(?i)([?&](?:access_token|api_key|password|secret|token|key)=)([^&#\s]*)`)
	assignment  = regexp.MustCompile(`(?:^|[\s;])((?i:export[ \t]+)?)([A-Za-z_][A-Za-z0-9_]*)=([^\s;&]+)`)
)

// scrubString hides credential values inside a string and keeps the rest.
// A bearer word is a secret only when the value looks like a token.
func scrubString(s string) (string, bool) {
	next := scrubBearer(s)
	next = scrubBasic(next)
	next = curlUser.ReplaceAllString(next, `${1}${2}${3}Hidden`)
	next = urlUserinfo.ReplaceAllString(next, `${1}Hidden@`)
	next = querySecret.ReplaceAllString(next, `${1}Hidden`)
	next = scrubAssignments(next)
	return next, next != s
}

func scrubBearer(s string) string {
	return bearerValue.ReplaceAllStringFunc(s, func(m string) string {
		sub := bearerValue.FindStringSubmatch(m)
		if len(sub) < 2 || !tokenLike(sub[1]) {
			return m
		}
		return "Bearer Hidden"
	})
}

// tokenLike reports a bearer value that is long and not a plain word.
func tokenLike(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return true
		}
	}
	return false
}

func scrubBasic(s string) string {
	return basicValue.ReplaceAllStringFunc(s, func(m string) string {
		sub := basicValue.FindStringSubmatch(m)
		if len(sub) < 2 || !basicCredential(sub[1]) {
			return m
		}
		return "Basic Hidden"
	})
}

func basicCredential(s string) bool {
	if len(s) < 8 {
		return false
	}
	var digit, symbol, upper, lower bool
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r == '+' || r == '/' || r == '=':
			symbol = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		default:
			return false
		}
	}
	if digit || symbol {
		return true
	}
	return len(s) >= 12 && upper && lower
}

func scrubAssignments(s string) string {
	locs := assignment.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		name := s[loc[4]:loc[5]]
		if !SecretKey(name) {
			continue
		}
		b.WriteString(s[last:loc[6]])
		b.WriteString("Hidden")
		last = loc[7]
	}
	b.WriteString(s[last:])
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

func scrubArray(raw json.RawMessage, path string, redacted *[]string) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, errBadJSON
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	first := true
	for dec.More() {
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		cleaned, err := scrubJSON(val, path, redacted)
		if err != nil {
			return nil, err
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
