package selftool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Request is one read asked for in a reply.
type Request struct {
	Tool  string
	Query string
	ID    string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-account\\s*\\n?(.*?)\\n?```")

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
		Tool  string `json:"tool"`
		Query string `json:"query"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Request{}, false
	}
	req := Request{
		Tool:  strings.TrimSpace(wire.Tool),
		Query: strings.TrimSpace(wire.Query),
		ID:    strings.TrimSpace(wire.ID),
	}
	if req.Tool == "" || req.Tool == PlaceholderTool {
		return Request{}, false
	}
	spec, ok := Lookup(req.Tool)
	if !ok {
		return Request{}, false
	}
	if spec.QueryRequired && (req.Query == "" || req.Query == PlaceholderQuery) {
		return Request{}, false
	}
	if !spec.Query {
		req.Query = ""
	}
	if !spec.ID {
		req.ID = ""
	}
	return req, true
}
