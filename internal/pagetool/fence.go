package pagetool

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// Fields is one page action parsed from a fence or a tool call.
type Fields struct {
	Action     string
	Title      string
	Document   string
	HasDoc     bool
	Page       string
	Revision   int
	Visibility string
	People     []string
	Agents     []string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-page\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence, or a fence that still has a
// sample placeholder, is left in place.
func Parse(body string) (visible string, fields Fields, ok bool) {
	matches := fenceRE.FindAllStringSubmatchIndex(body, -1)
	for _, loc := range matches {
		if len(loc) < 4 {
			continue
		}
		raw := strings.TrimSpace(body[loc[2]:loc[3]])
		parsed, good := decodeFence(raw)
		if !good {
			continue
		}
		visible = strings.TrimSpace(body[:loc[0]] + body[loc[1]:])
		return visible, parsed, true
	}
	return body, Fields{}, false
}

func decodeFence(raw string) (Fields, bool) {
	var wire struct {
		Action     string          `json:"action"`
		Title      string          `json:"title"`
		Document   json.RawMessage `json:"document"`
		Page       string          `json:"page"`
		Revision   *int            `json:"revision"`
		Visibility string          `json:"visibility"`
		People     []string        `json:"people"`
		Agents     []string        `json:"agents"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Fields{}, false
	}
	fields, ok := fieldsFrom(wire.Action, wire.Title, wire.Document, wire.Page, wire.Revision, wire.Visibility, wire.People, wire.Agents)
	if !ok {
		return Fields{}, false
	}
	if strings.TrimSpace(fields.Title) == PlaceholderTitle || strings.TrimSpace(fields.Page) == PlaceholderPage {
		return Fields{}, false
	}
	if fields.HasDoc && strings.TrimSpace(fields.Document) == PlaceholderDocument {
		return Fields{}, false
	}
	return fields, true
}

// FieldsFromCall checks a tool payload. Placeholders are not special here.
func FieldsFromCall(action, title, page, visibility string, document json.RawMessage, revision *int, people, agents []string) (Fields, bool) {
	return fieldsFrom(action, title, document, page, revision, visibility, people, agents)
}

func fieldsFrom(action, title string, document json.RawMessage, page string, revision *int, visibility string, people, agents []string) (Fields, bool) {
	action = strings.TrimSpace(action)
	if !Known(action) {
		return Fields{}, false
	}
	doc, hasDoc := DocumentText(document)
	rev := 0
	if revision != nil {
		rev = *revision
	}
	fields := Fields{
		Action:     action,
		Title:      strings.TrimSpace(title),
		Document:   doc,
		HasDoc:     hasDoc,
		Page:       strings.TrimSpace(page),
		Revision:   rev,
		Visibility: strings.TrimSpace(visibility),
		People:     cleanNames(people),
		Agents:     cleanNames(agents),
	}
	switch action {
	case ToolCreate:
		if fields.Title == "" || !fields.HasDoc {
			return Fields{}, false
		}
	case ToolUpdate:
		if fields.Page == "" || !fields.HasDoc {
			return Fields{}, false
		}
	case ToolRename:
		if fields.Page == "" || fields.Title == "" {
			return Fields{}, false
		}
	case ToolDelete, ToolRead:
		if fields.Page == "" {
			return Fields{}, false
		}
	case ToolShare:
		if fields.Page == "" || fields.Visibility == "" {
			return Fields{}, false
		}
	}
	return fields, true
}

// DocumentText returns a JSON document from a string or an object.
func DocumentText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", false
		}
		return text, true
	}
	if !json.Valid(raw) {
		return "", false
	}
	return string(raw), true
}

func cleanNames(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, name := range in {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
