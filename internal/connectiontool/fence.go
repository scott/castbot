package connectiontool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Fields is one connection action.
type Fields struct {
	Action     string
	Connection string
	Tool       string
	Level      string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-connection\\s*\\n?(.*?)\\n?```")

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
		Action     string  `json:"action"`
		Connection *string `json:"connection"`
		Tool       *string `json:"tool"`
		Level      *string `json:"level"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Fields{}, false
	}
	action := strings.TrimSpace(wire.Action)
	if !Known(action) {
		return Fields{}, false
	}
	fields := Fields{Action: action}
	if wire.Connection != nil {
		fields.Connection = strings.TrimSpace(*wire.Connection)
	}
	if wire.Tool != nil {
		fields.Tool = strings.TrimSpace(*wire.Tool)
	}
	if wire.Level != nil {
		fields.Level = strings.TrimSpace(*wire.Level)
	}
	if fields.Connection == PlaceholderConnection || fields.Tool == PlaceholderTool || fields.Level == PlaceholderLevel {
		return Fields{}, false
	}
	switch action {
	case ToolDisconnect:
		if fields.Connection == "" {
			return Fields{}, false
		}
	case ToolLevel:
		if fields.Connection == "" || fields.Tool == "" || fields.Level == "" {
			return Fields{}, false
		}
	default:
		return Fields{}, false
	}
	return fields, true
}
