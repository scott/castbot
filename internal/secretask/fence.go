package secretask

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Request is one save or delete asked for in a reply.
// Skip means the fence was the sample and must not open a card.
type Request struct {
	Action string
	Name   string
	Label  string
	Reason string
	Skip   bool
}

var fenceRE = regexp.MustCompile("(?s)```castbot-secret\\s*\\n?(.*?)\\n?```")

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
		Action string `json:"action"`
		Name   string `json:"name"`
		Label  string `json:"label"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Request{}, false
	}
	req := Request{
		Action: strings.ToLower(strings.TrimSpace(wire.Action)),
		Name:   strings.ToUpper(strings.TrimSpace(wire.Name)),
		Label:  strings.TrimSpace(wire.Label),
		Reason: strings.TrimSpace(wire.Reason),
	}
	switch req.Action {
	case "save", "delete":
	default:
		return Request{}, false
	}
	if req.Name == "" || len(req.Name) > 64 || strings.ContainsAny(req.Name, " \t\r\n") {
		return Request{}, false
	}
	for _, r := range req.Name {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return Request{}, false
		}
	}
	if req.Name == "NAME" || (req.Name == SampleName && req.Reason == SampleReason) {
		req.Skip = true
	}
	return req, true
}
