package web

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/scott/castbot/internal/castcfg"
)

// Proposal is the setup a describe step returns. It never carries a key.
type Proposal struct {
	Name, Title, Instructions, Job, Schedule, Reason string
	Unclear                                          bool
}

// DescribeClient turns a redacted description into a setup.
// Tests and screenshots pass a fake. The default client calls the provider.
type DescribeClient interface {
	Describe(ctx context.Context, text string) (Proposal, error)
}

var errDescribeDown = errors.New("describe unavailable")

func serverProviderKeySet() bool {
	for _, slot := range modelSlots() {
		if strings.TrimSpace(os.Getenv(slot.Env)) != "" {
			return true
		}
		if strings.TrimSpace(os.Getenv(castcfg.ProviderKeyEnv(slot.Manifest))) != "" {
			return true
		}
	}
	return false
}

func (s *Server) describer() DescribeClient {
	s.mu.Lock()
	c := s.describe
	s.mu.Unlock()
	if c != nil {
		return c
	}
	return providerDescribe{}
}

// SetDescribe installs the client that reads a new-agent description.
// Tests pass a fake so CI does not call a provider.
func (s *Server) SetDescribe(c DescribeClient) {
	s.mu.Lock()
	s.describe = c
	s.mu.Unlock()
}

var (
	nameCalled = regexp.MustCompile(`(?i)\b(?:called|named)\s+([A-Za-z][A-Za-z0-9]{0,30})`)
	nameCallIt = regexp.MustCompile(`(?i)\b(?:call it|name it)\s+([A-Za-z][A-Za-z0-9]{0,30})`)
	nameMake   = regexp.MustCompile(`^Make\s+([A-Z][A-Za-z0-9]{0,30})\b`)
	repoRef    = regexp.MustCompile(`\b[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\b`)
)

func nameFromText(text string) string {
	for _, re := range []*regexp.Regexp{nameCalled, nameCallIt} {
		if m := re.FindStringSubmatch(text); len(m) == 2 {
			return cleanName(m[1])
		}
	}
	if m := nameMake.FindStringSubmatch(strings.TrimSpace(text)); len(m) == 2 {
		return cleanName(m[1])
	}
	return ""
}

func cleanName(name string) string {
	name = strings.Trim(name, ".,;:!?\"'")
	if name == "" || len(name) > 40 {
		return ""
	}
	lower := strings.ToLower(name)
	switch lower {
	case "me", "an", "a", "the", "it", "my", "agent":
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func unclearText(text string) bool {
	t := strings.TrimSpace(strings.ToLower(text))
	t = strings.Trim(t, ".!?")
	switch t {
	case "", "hi", "hello", "hey", "ok", "okay", "thanks", "thank you", "yo", "sup", "help", "test":
		return true
	}
	return len([]rune(t)) < 8
}

func scheduleFrom(text string) string {
	lower := strings.ToLower(text)
	for _, phrase := range []string{
		"every weekday morning",
		"every weekday",
		"each morning",
		"every morning",
		"every friday",
		"every monday",
		"every day",
		"each week",
	} {
		if strings.Contains(lower, phrase) {
			return phrase
		}
	}
	return ""
}

func localProposal(text string) Proposal {
	text = strings.TrimSpace(text)
	name := nameFromText(text)
	schedule := scheduleFrom(text)
	job := jobLine(text, name)
	title := titleFor(job)
	if job == "" {
		title = "New agent"
	}
	reason := reasonFor(text, job)
	return Proposal{
		Name: name, Title: title, Job: job, Schedule: schedule, Reason: reason,
		Instructions: instructionsFor(name, job, text),
	}
}

func jobLine(text, name string) string {
	lower := strings.ToLower(text)
	repo := firstRepo(text)
	if strings.Contains(lower, "broken link") && repo != "" {
		return "Check " + repo + " for broken links and open a branch with fixes."
	}
	if strings.Contains(lower, "release note") && repo != "" {
		return "Write release notes from merged PRs in " + repo + "."
	}
	if strings.Contains(lower, "inbox") {
		return "Sort the support inbox and draft replies to check."
	}
	rest := stripNameClause(text)
	rest = strings.TrimSpace(rest)
	if rest == "" || len([]rune(rest)) < 12 || onlyNameWish(lower) {
		return ""
	}
	if len(rest) > 160 {
		rest = rest[:160]
	}
	return rest
}

func onlyNameWish(lower string) bool {
	if strings.Contains(lower, "tell it what to do") || strings.Contains(lower, "what to do later") {
		return true
	}
	// "I want an agent named Juno." has no job after the name clause.
	return false
}

func stripNameClause(text string) string {
	out := nameCalled.ReplaceAllString(text, "")
	out = nameCallIt.ReplaceAllString(out, "")
	out = nameMake.ReplaceAllString(out, "")
	out = strings.TrimSpace(out)
	out = strings.Trim(out, " .,")
	// Drop a leading "Make me an agent" or "I want an agent" shell.
	lower := strings.ToLower(out)
	for _, prefix := range []string{
		"make me an agent",
		"i want an agent",
		"an agent",
		"make me",
		"that ",
	} {
		if strings.HasPrefix(lower, prefix) {
			out = strings.TrimSpace(out[len(prefix):])
			lower = strings.ToLower(out)
		}
	}
	if strings.EqualFold(out, "i will tell it what to do later") || strings.Contains(strings.ToLower(out), "tell it what to do later") {
		return ""
	}
	return strings.Trim(out, " .")
}

func firstRepo(text string) string {
	m := repoRef.FindString(text)
	return m
}

func titleFor(job string) string {
	lower := strings.ToLower(job)
	switch {
	case strings.Contains(lower, "link"):
		return "Link checker"
	case strings.Contains(lower, "release"):
		return "Release notes"
	case strings.Contains(lower, "inbox"):
		return "Inbox triage"
	case job == "":
		return "New agent"
	}
	words := strings.Fields(job)
	if len(words) > 4 {
		words = words[:4]
	}
	title := strings.Trim(strings.Join(words, " "), ".")
	if len(title) > 80 {
		title = title[:80]
	}
	return title
}

func reasonFor(text, job string) string {
	lower := strings.ToLower(text + " " + job)
	switch {
	case strings.Contains(lower, "release") || strings.Contains(lower, "ship") || strings.Contains(lower, "merged"):
		return "it passes along what shipped"
	case strings.Contains(lower, "link") || strings.Contains(lower, "check"):
		return "it goes looking"
	case strings.Contains(lower, "inbox") || strings.Contains(lower, "draft"):
		return "it drafts for you"
	default:
		return "that fits what you described"
	}
}

func instructionsFor(name, job, text string) string {
	who := name
	if who == "" {
		who = "this agent"
	}
	repo := firstRepo(text)
	lower := strings.ToLower(text + " " + job)
	if job == "" {
		return "You are " + who + ", a helpful assistant. Ask what your job is before you start."
	}
	if strings.Contains(lower, "broken link") && repo != "" {
		return "You are " + who + ". Check the docs in " + repo + " for broken links. Fix them on a new branch and ask before you push. Keep a short list of what you changed."
	}
	if strings.Contains(lower, "release") && repo != "" {
		return "You are " + who + ". Read the PRs merged in " + repo + " since the last notes and write short release notes, grouped by area."
	}
	if strings.Contains(lower, "spanish") && repo != "" {
		return "You are a docs helper for the Spanish docs in " + repo + ". Ask before you push."
	}
	return "You are " + who + ". " + job + " Ask before you push. Do not put keys in your notes."
}

var autoRoster = []struct{ Name, Reason string }{
	{"Relay", "it passes along what shipped"},
	{"Scout", "it goes looking"},
	{"Quill", "it drafts for you"},
	{"Lark", "it writes things up"},
	{"Harbor", "it keeps a watch"},
	{"Cedar", "it stays with the work"},
	{"Brook", "it keeps things moving"},
	{"Nova", "it starts something new"},
	{"Maple", "it keeps a record"},
	{"Piper", "it sends things on"},
}

func autoName(text, job string, used map[string]bool) (string, string) {
	want := ""
	lower := strings.ToLower(text + " " + job)
	switch {
	case strings.Contains(lower, "release") || strings.Contains(lower, "ship") || strings.Contains(lower, "merged") || strings.Contains(lower, "pass"):
		want = "Relay"
	case strings.Contains(lower, "link") || strings.Contains(lower, "check") || strings.Contains(lower, "watch"):
		want = "Scout"
	case strings.Contains(lower, "inbox") || strings.Contains(lower, "draft") || strings.Contains(lower, "write"):
		want = "Quill"
	}
	if want != "" && !used[strings.ToLower(want)] {
		for _, row := range autoRoster {
			if row.Name == want {
				return row.Name, row.Reason
			}
		}
	}
	for _, row := range autoRoster {
		if !used[strings.ToLower(row.Name)] {
			return row.Name, row.Reason
		}
	}
	return "North", "that fits what you described"
}

func redactSecrets(in string) (string, bool) {
	if strings.TrimSpace(in) == "" {
		return in, false
	}
	parts := strings.Fields(in)
	changed := false
	for i, part := range parts {
		bare, prefix, suffix := trimEdge(part)
		if bare == "" || !secretToken(bare) {
			continue
		}
		parts[i] = prefix + "********" + suffix
		changed = true
	}
	if !changed {
		return in, false
	}
	return strings.Join(parts, " "), true
}

func trimEdge(s string) (bare, prefix, suffix string) {
	i := 0
	j := len(s)
	for i < j && strings.ContainsRune("([<\"'`", rune(s[i])) {
		i++
	}
	for j > i && strings.ContainsRune(".,;:!?)]>\"'`", rune(s[j-1])) {
		j--
	}
	return s[i:j], s[:i], s[j:]
}

func secretToken(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	prefixes := []string{
		"sk-", "sk-ant-", "ghp_", "github_pat_", "gho_",
		"xoxb-", "xoxp-", "xoxa-", "akia", "glpat-",
		"dop_v1_", "doo_v1_", "bearer",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) && len(s) >= len(p)+8 {
			return true
		}
	}
	return highEntropy(s)
}

func highEntropy(s string) bool {
	if len(s) < 24 {
		return false
	}
	var lower, upper, digit bool
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		case r == '-' || r == '_' || r == '+' || r == '=' || r == '/':
		default:
			return false
		}
	}
	classes := 0
	if lower {
		classes++
	}
	if upper {
		classes++
	}
	if digit {
		classes++
	}
	return classes >= 2 && (digit || (lower && upper))
}

func scrubProposal(p Proposal) Proposal {
	p.Name, _ = redactSecrets(p.Name)
	p.Title, _ = redactSecrets(p.Title)
	p.Instructions, _ = redactSecrets(p.Instructions)
	p.Job, _ = redactSecrets(p.Job)
	p.Schedule, _ = redactSecrets(p.Schedule)
	p.Reason, _ = redactSecrets(p.Reason)
	return p
}
