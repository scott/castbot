package approvetool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Fields is one decision. Card empty means the only waiting card.
type Fields struct {
	Action string
	Card   string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-approval\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence, or a fence that still has the
// sample card, is left in place.
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
		Card   *string `json:"card"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Fields{}, false
	}
	action := strings.TrimSpace(wire.Action)
	if !Known(action) {
		return Fields{}, false
	}
	card := ""
	if wire.Card != nil {
		card = strings.TrimSpace(*wire.Card)
		if card == PlaceholderCard {
			return Fields{}, false
		}
	}
	return Fields{Action: action, Card: card}, true
}
