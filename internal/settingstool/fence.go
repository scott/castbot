package settingstool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Fields is one settings action.
type Fields struct {
	Action string
	Name   string
	Theme  string
	Email  string
	Invite string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-settings\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence, or a fence that still has a
// sample value, is left in place.
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
		Action string  `json:"action"`
		Name   *string `json:"name"`
		Theme  *string `json:"theme"`
		Email  *string `json:"email"`
		Invite *string `json:"invite"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Fields{}, false
	}
	action := strings.TrimSpace(wire.Action)
	if !Known(action) {
		return Fields{}, false
	}
	fields := Fields{Action: action}
	if wire.Name != nil {
		fields.Name = strings.TrimSpace(*wire.Name)
	}
	if wire.Theme != nil {
		fields.Theme = strings.TrimSpace(*wire.Theme)
	}
	if wire.Email != nil {
		fields.Email = strings.TrimSpace(*wire.Email)
	}
	if wire.Invite != nil {
		fields.Invite = strings.TrimSpace(*wire.Invite)
	}
	if fields.Name == PlaceholderName || fields.Theme == PlaceholderTheme || fields.Email == PlaceholderEmail || fields.Invite == PlaceholderInvite {
		return Fields{}, false
	}
	switch action {
	case ToolName:
		if fields.Name == "" {
			return Fields{}, false
		}
	case ToolTheme:
		if fields.Theme == "" {
			return Fields{}, false
		}
	case ToolInvite:
		if fields.Name == "" || fields.Email == "" {
			return Fields{}, false
		}
	case ToolRevoke:
		if fields.Invite == "" {
			return Fields{}, false
		}
	default:
		return Fields{}, false
	}
	return fields, true
}
