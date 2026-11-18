package agentskill

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Request is the description a reply asked to save.
type Request struct {
	Description string
	Name        string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-create\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence is left in place.
func Parse(body string) (visible string, req Request, ok bool) {
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
	return body, Request{}, false
}

func decodeFence(raw string) (Request, bool) {
	var wire struct {
		Description string `json:"description"`
		Name        string `json:"name"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Request{}, false
	}
	desc := strings.TrimSpace(wire.Description)
	if !UsableDescription(desc) {
		return Request{}, false
	}
	return Request{Description: desc, Name: strings.TrimSpace(wire.Name)}, true
}
