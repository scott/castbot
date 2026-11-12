package md

import (
	"html"
	"regexp"
	"strings"
)

// urlRe matches a bare http or https URL in plain user text.
var urlRe = regexp.MustCompile(`https?://[^\s<]+`)

// LinkPlain escapes user text and links bare http and https URLs.
// Trailing punctuation stays outside the link. Nothing else is formatted.
func LinkPlain(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range urlRe.FindAllStringIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:m[0]]))
		raw := s[m[0]:m[1]]
		link, trail := trimURLTrail(raw)
		if link == "" {
			b.WriteString(html.EscapeString(raw))
		} else {
			b.WriteString(`<a href="`)
			b.WriteString(html.EscapeString(link))
			b.WriteString(`" target="_blank" rel="noopener noreferrer nofollow">`)
			b.WriteString(html.EscapeString(link))
			b.WriteString(`</a>`)
			b.WriteString(html.EscapeString(trail))
		}
		last = m[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	return b.String()
}

func trimURLTrail(s string) (string, string) {
	trail := ""
	for len(s) > 0 {
		r := s[len(s)-1]
		if !strings.ContainsRune(".,;:!?)]}>\"'", rune(r)) {
			break
		}
		// Keep a closing paren when the URL itself opened one.
		if r == ')' && strings.Count(s, "(") >= strings.Count(s, ")") {
			break
		}
		trail = string(r) + trail
		s = s[:len(s)-1]
	}
	return s, trail
}
