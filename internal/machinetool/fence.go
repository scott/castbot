package machinetool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Request is one machine change asked for in a reply.
type Request struct {
	Action string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-machine\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence, or a fence that still has
// ACTION, is left in place.
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
		Action string `json:"action"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Request{}, false
	}
	req := Request{Action: strings.TrimSpace(wire.Action)}
	if req.Action == "" || req.Action == PlaceholderAction || !Known(req.Action) {
		return Request{}, false
	}
	return req, true
}
