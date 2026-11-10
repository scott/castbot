package castcfg

import (
	"bytes"
	"encoding/json"
	"errors"
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
		if !json.Valid(raw) {
			return nil, errBadJSON
		}
		return raw, nil
	}
}

var errBadJSON = errors.New("bad json")

// bearerValue reports a string that starts with "Bearer ".
// Those arguments are treated as secrets even when the key is ordinary.
func bearerValue(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '"' {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return false
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "bearer ")
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
		if SecretKey(key) || bearerValue(val) {
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
