package routinetool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Request is one create, update, delete, or run asked for in a reply.
// A nil field was omitted. Create requires name, cron, and prompt.
type Request struct {
	Action   string
	ID       string
	Name     *string
	Cron     *string
	Timezone *string
	Prompt   *string
	Missed   *string
	Approval *string
	Enabled  *bool
}

var fenceRE = regexp.MustCompile("(?s)```castbot-routine\\s*\\n?(.*?)\\n?```")

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
		Action   string  `json:"action"`
		ID       string  `json:"id"`
		Name     *string `json:"name"`
		Cron     *string `json:"cron"`
		Timezone *string `json:"timezone"`
		Prompt   *string `json:"prompt"`
		Missed   *string `json:"missed"`
		Approval *string `json:"approval"`
		Enabled  *bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Request{}, false
	}
	req := Request{
		Action:   strings.TrimSpace(wire.Action),
		ID:       strings.TrimSpace(wire.ID),
		Name:     wire.Name,
		Cron:     wire.Cron,
		Timezone: wire.Timezone,
		Prompt:   wire.Prompt,
		Missed:   wire.Missed,
		Approval: wire.Approval,
		Enabled:  wire.Enabled,
	}
	switch req.Action {
	case "create":
		if !named(req.Name) || !named(req.Cron) || !named(req.Prompt) {
			return Request{}, false
		}
		if strings.TrimSpace(*req.Name) == PlaceholderName || strings.TrimSpace(*req.Cron) == PlaceholderCron || strings.TrimSpace(*req.Prompt) == PlaceholderPrompt {
			return Request{}, false
		}
	case "update", "delete", "run":
		if req.ID == "" {
			return Request{}, false
		}
	default:
		return Request{}, false
	}
	return req, true
}

func named(v *string) bool {
	return v != nil && strings.TrimSpace(*v) != ""
}
