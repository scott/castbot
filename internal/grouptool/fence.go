package grouptool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Request is one group or section change asked for in a reply.
type Request struct {
	Action  string
	Group   string
	Title   string
	Name    string
	Section string
	Agents  []string
	People  []string
	Out     bool
}

var fenceRE = regexp.MustCompile("(?s)```castbot-organize\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence, or a fence that still has
// TITLE, NAME, or PERSON, is left in place.
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
		Action  string   `json:"action"`
		Group   string   `json:"group"`
		Title   string   `json:"title"`
		Name    string   `json:"name"`
		Section string   `json:"section"`
		Agents  []string `json:"agents"`
		People  []string `json:"people"`
		Out     bool     `json:"out"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Request{}, false
	}
	req := Request{
		Action:  strings.TrimSpace(wire.Action),
		Group:   strings.TrimSpace(wire.Group),
		Title:   strings.TrimSpace(wire.Title),
		Name:    strings.TrimSpace(wire.Name),
		Section: strings.TrimSpace(wire.Section),
		Out:     wire.Out,
	}
	for _, item := range wire.Agents {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		req.Agents = append(req.Agents, item)
	}
	for _, item := range wire.People {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		req.People = append(req.People, item)
	}
	if tainted(req) {
		return Request{}, false
	}
	switch req.Action {
	case ToolCreateGroup:
		if req.Title == "" || len(req.Agents) == 0 {
			return Request{}, false
		}
	case ToolUpdateGroup:
		if req.Group == "" || req.Title == "" {
			return Request{}, false
		}
	case ToolAddMembers, ToolRemoveMembers:
		if req.Group == "" || (len(req.Agents) == 0 && len(req.People) == 0) {
			return Request{}, false
		}
	case ToolDeleteGroup, ToolPinGroup, ToolUnpinGroup:
		if req.Group == "" {
			return Request{}, false
		}
	case ToolCreateSection:
		if req.Name == "" {
			return Request{}, false
		}
	case ToolRenameSection:
		if req.Section == "" || req.Name == "" {
			return Request{}, false
		}
	case ToolDeleteSection:
		if req.Section == "" {
			return Request{}, false
		}
	case ToolMoveAgents:
		if len(req.Agents) == 0 {
			return Request{}, false
		}
	case ToolPlaceGroup:
		if req.Group == "" {
			return Request{}, false
		}
	default:
		return Request{}, false
	}
	return req, true
}

func tainted(req Request) bool {
	for _, item := range []string{req.Group, req.Title, req.Name, req.Section} {
		if placeholder(item) {
			return true
		}
	}
	for _, item := range req.Agents {
		if placeholder(item) {
			return true
		}
	}
	for _, item := range req.People {
		if placeholder(item) {
			return true
		}
	}
	return false
}

func placeholder(item string) bool {
	switch strings.TrimSpace(item) {
	case PlaceholderTitle, PlaceholderName, PlaceholderPerson:
		return true
	default:
		return false
	}
}
