package connect

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// SameHash reports whether two request hashes are identical.
func SameHash(a, b string) bool {
	if len(a) != 64 || len(b) != 64 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// RequestHash binds an approval to one HTTP call: method, path, raw query, and body.
// The upstream host is not part of the hash. The provider is stored beside it.
func RequestHash(method, rawURL string, body []byte) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	path, query := "/", ""
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && u != nil {
		if u.EscapedPath() != "" {
			path = u.EscapedPath()
		} else if u.Path != "" {
			path = u.Path
		}
		query = u.RawQuery
	}
	sum := sha256.New()
	writePart(sum, method)
	writePart(sum, path)
	writePart(sum, query)
	_, _ = sum.Write(body)
	return hex.EncodeToString(sum.Sum(nil))
}

// RequestSummary is the short text shown on an approval card.
// It is not the hash, and secret-shaped text is removed.
func RequestSummary(method, rawURL string, body []byte) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	path := "/"
	query := ""
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil && u != nil {
		if u.Path != "" {
			path = u.Path
		}
		query = u.RawQuery
	}
	text := method + " " + path
	if query != "" {
		if len(query) > 60 {
			query = query[:60]
		}
		text += "?" + query
	}
	if len(body) > 0 {
		preview := body
		if len(preview) > 80 {
			preview = preview[:80]
		}
		text += " " + string(preview)
	}
	return ScrubReason(text)
}

// MCPHash binds an approval to one tool call.
// Arguments are canonical JSON with sorted object keys, so key order does not
// change the hash and a different value does.
func MCPHash(tool string, args json.RawMessage) (string, error) {
	canon, err := canonicalJSON(args)
	if err != nil {
		return "", err
	}
	sum := sha256.New()
	writePart(sum, "mcp")
	writePart(sum, strings.TrimSpace(tool))
	_, _ = sum.Write(canon)
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// MCPSummary is the card text for a tool call. Arguments are scrubbed.
func MCPSummary(tool string, args json.RawMessage) string {
	canon, err := canonicalJSON(args)
	if err != nil {
		canon = []byte("{}")
	}
	text := strings.TrimSpace(tool)
	if len(canon) > 2 {
		text += " " + string(canon)
	}
	return ScrubReason(text)
}

func writePart(sum interface{ Write([]byte) (int, error) }, part string) {
	_, _ = sum.Write([]byte(part))
	_, _ = sum.Write([]byte{0})
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("{}"), nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return encodeCanonical(v)
}

func encodeCanonical(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for key := range t {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var buf bytes.Buffer
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(key)
			if err != nil {
				return nil, err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			vb, err := encodeCanonical(t[key])
			if err != nil {
				return nil, err
			}
			buf.Write(vb)
		}
		buf.WriteByte('}')
		return buf.Bytes(), nil
	case []any:
		var buf bytes.Buffer
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			vb, err := encodeCanonical(item)
			if err != nil {
				return nil, err
			}
			buf.Write(vb)
		}
		buf.WriteByte(']')
		return buf.Bytes(), nil
	default:
		return json.Marshal(t)
	}
}
