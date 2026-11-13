package connect

import (
	"regexp"
	"strings"
)

const reasonMax = 240

var secretShape = regexp.MustCompile(`(?i)(github_pat_|ghp_|gho_|dop_v1_)[a-z0-9_\-]{8,}|[a-z0-9_\-]{32,}`)

// ScrubReason keeps a short explanation and removes secret-shaped text.
func ScrubReason(reason string) string {
	reason = strings.TrimSpace(reason)
	reason = strings.ReplaceAll(reason, "\r", " ")
	reason = strings.ReplaceAll(reason, "\n", " ")
	reason = secretShape.ReplaceAllString(reason, "[redacted]")
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > reasonMax {
		reason = reason[:reasonMax]
	}
	return reason
}
