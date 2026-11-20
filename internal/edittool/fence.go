package edittool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Fields is one edit. A nil pointer means that field stays as it is.
type Fields struct {
	Name         *string
	Title        *string
	Job          *string
	Instructions *string
	Color        *string
	Model        *string
	Delegate     *bool
}

// Empty reports whether the person named no field.
func (f Fields) Empty() bool {
	return f.Name == nil && f.Title == nil && f.Job == nil && f.Instructions == nil && f.Color == nil && f.Model == nil && f.Delegate == nil
}

var fenceRE = regexp.MustCompile("(?s)```castbot-edit\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence, or a fence that still has a
// sample word, is left in place.
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
		Action       string  `json:"action"`
		Name         *string `json:"name"`
		Title        *string `json:"title"`
		Job          *string `json:"job"`
		Instructions *string `json:"instructions"`
		Color        *string `json:"color"`
		Model        *string `json:"model"`
		Delegate     *bool   `json:"delegate"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Fields{}, false
	}
	if strings.TrimSpace(wire.Action) != ToolUpdate {
		return Fields{}, false
	}
	fields := Fields{
		Name: wire.Name, Title: wire.Title, Job: wire.Job, Instructions: wire.Instructions,
		Color: wire.Color, Model: wire.Model, Delegate: wire.Delegate,
	}
	if fields.Empty() || placeholder(fields) {
		return Fields{}, false
	}
	return fields, true
}

func placeholder(fields Fields) bool {
	return isSample(fields.Name, PlaceholderName) ||
		isSample(fields.Title, PlaceholderTitle) ||
		isSample(fields.Job, PlaceholderJob) ||
		isSample(fields.Instructions, PlaceholderNotes) ||
		isSample(fields.Color, PlaceholderColor) ||
		isSample(fields.Model, PlaceholderModel)
}

func isSample(value *string, sample string) bool {
	return value != nil && strings.TrimSpace(*value) == sample
}
