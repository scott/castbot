package deletetool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Fields is one delete action.
type Fields struct {
	Action  string
	Confirm string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-delete\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence, or a fence that still has the
// sample name, is left in place.
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
		Action  string  `json:"action"`
		Confirm *string `json:"confirm"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Fields{}, false
	}
	action := strings.TrimSpace(wire.Action)
	if !Known(action) {
		return Fields{}, false
	}
	fields := Fields{Action: action}
	if wire.Confirm == nil {
		return fields, true
	}
	confirm := strings.TrimSpace(*wire.Confirm)
	if confirm == "" {
		return fields, true
	}
	if confirm == PlaceholderConfirm {
		return Fields{}, false
	}
	fields.Confirm = confirm
	return fields, true
}
