package importtool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Fields is one import.
type Fields struct {
	Action     string
	URL        string
	ArchiveB64 string
}

var fenceRE = regexp.MustCompile("(?s)```castbot-import\\s*\\n?(.*?)\\n?```")

// Parse returns the reply without one usable fence. ok is false when the
// text has no usable fence. An invalid fence, or a fence that still has a
// sample URL or archive, is left in place.
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
		URL     *string `json:"url"`
		Archive *string `json:"archive_b64"`
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return Fields{}, false
	}
	action := strings.TrimSpace(wire.Action)
	if !Known(action) {
		return Fields{}, false
	}
	fields := Fields{Action: action}
	if wire.URL != nil {
		url := strings.TrimSpace(*wire.URL)
		if url == PlaceholderURL {
			return Fields{}, false
		}
		fields.URL = url
	}
	if wire.Archive != nil {
		archive := strings.TrimSpace(*wire.Archive)
		if archive == PlaceholderArchive {
			return Fields{}, false
		}
		fields.ArchiveB64 = archive
	}
	return fields, true
}
