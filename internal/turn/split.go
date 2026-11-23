// Package turn separates a streamed reply into progress lines and the answer.
// The Cast runtime sends each model turn as text chunks with no boundary
// between them. A short status ends on punctuation and the next turn starts
// on the next letter, so a plain join draws one wall of text.
//
// Split is lossless: joining the beats and the answer returns the chunks.
// Echo word frames stay one answer. A dotted call such as fmt.Println stays
// intact. The last turn is the answer. Earlier turns are the progress log.
package turn

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Split joins chunks the way the stream received them, then cuts progress
// lines off the front. The answer is the last turn. A reply with one turn
// has no beats.
func Split(chunks []string) (beats []string, answer string) {
	var s Splitter
	for _, chunk := range chunks {
		s.Add(chunk)
	}
	s.Flush()
	return s.Beats(), s.Answer()
}

// Visible returns the answer when beats are a prefix of body.
// Otherwise it returns body unchanged.
func Visible(body string, beats []string) string {
	if len(beats) == 0 {
		return body
	}
	prefix := strings.Join(beats, "")
	if prefix == "" || !strings.HasPrefix(body, prefix) {
		return body
	}
	rest := body[len(prefix):]
	if strings.TrimSpace(rest) == "" {
		return body
	}
	return rest
}

// DecodeSteps reads the JSON list stored beside a message.
// Empty and invalid values mean there is no progress log.
func DecodeSteps(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}
	var lines []string
	if json.Unmarshal([]byte(raw), &lines) != nil {
		return nil
	}
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Splitter is the incremental form of Split.
// Hold keeps a single capital token after sentence punctuation until the
// next chunk shows whether it is a new sentence or the rest of a name.
type Splitter struct {
	turns []string
	cur   strings.Builder
	line  strings.Builder
	fence string
	hold  string
}

// Add consumes one text chunk.
func (s *Splitter) Add(chunk string) {
	if chunk == "" {
		return
	}
	if s.hold != "" {
		held := s.hold
		s.hold = ""
		if !s.inCode() && isUpperIdent(held) && opensTurn(chunk) && (s.sentenceBoundary() || s.endsSingleNL()) {
			s.commit()
		}
		s.appendRaw(held)
	}
	s.write(chunk)
}

// Flush appends a token that was waiting for the next chunk.
// Call it when the stream ends so the last word is not dropped.
func (s *Splitter) Flush() {
	if s.hold == "" {
		return
	}
	held := s.hold
	s.hold = ""
	s.appendRaw(held)
}

// Beats returns the finished progress lines. The answer is not included.
func (s *Splitter) Beats() []string {
	if len(s.turns) == 0 {
		return nil
	}
	out := make([]string, len(s.turns))
	copy(out, s.turns)
	return out
}

// Answer returns the current turn, not including a token still on hold.
func (s *Splitter) Answer() string {
	return s.cur.String()
}

// Text returns every byte accepted so far, including a held token.
func (s *Splitter) Text() string {
	var b strings.Builder
	for _, turn := range s.turns {
		b.WriteString(turn)
	}
	b.WriteString(s.cur.String())
	b.WriteString(s.hold)
	return b.String()
}

func (s *Splitter) write(chunk string) {
	atStart := true
	for len(chunk) > 0 {
		if !s.inCode() && s.canBreak() {
			if rest, ok := s.breakAt(chunk, atStart); ok {
				chunk = rest
				atStart = false
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(chunk)
		if size <= 0 {
			return
		}
		s.appendRaw(chunk[:size])
		chunk = chunk[size:]
		atStart = false
	}
}

// breakAt starts a new turn when chunk is clearly the next sentence.
// A space after punctuation only counts at the start of a chunk.
// The same space inside one chunk is one message, so a denial stays whole.
// ok is false when the caller should append the next rune instead.
func (s *Splitter) breakAt(chunk string, atStart bool) (rest string, ok bool) {
	if chunk == "" {
		return chunk, false
	}
	r, _ := utf8.DecodeRuneInString(chunk)
	if r == ' ' || r == '\t' {
		if !atStart || !s.sentenceBoundary() {
			return chunk, false
		}
		trimmed := strings.TrimLeft(chunk, " \t")
		if trimmed == "" || !startsSentence(trimmed) && !blockStart(trimmed) {
			return chunk, false
		}
		s.appendRaw(chunk[:len(chunk)-len(trimmed)])
		s.commit()
		return trimmed, true
	}
	if s.endsSingleNL() && (startsSentence(chunk) || blockStart(chunk)) {
		if tok, after, ok := cutIdent(chunk); ok && isUpperIdent(tok) && after == "" {
			s.hold = tok
			return "", true
		}
		s.commit()
		return chunk, true
	}
	if !s.sentenceBoundary() {
		return chunk, false
	}
	if tok, after, ok := cutIdent(chunk); ok && isUpperIdent(tok) {
		if after == "" {
			s.hold = tok
			return "", true
		}
		if continuesCall(after) {
			return chunk, false
		}
		s.commit()
		s.appendRaw(tok)
		return after, true
	}
	if blockStart(chunk) {
		s.commit()
		return chunk, true
	}
	return chunk, false
}

func (s *Splitter) commit() {
	if s.cur.Len() == 0 {
		return
	}
	s.turns = append(s.turns, s.cur.String())
	s.cur.Reset()
	s.line.Reset()
}

func (s *Splitter) appendRaw(str string) {
	for _, r := range str {
		if r == '\n' {
			s.finishLine(s.line.String())
			s.cur.WriteRune('\n')
			s.line.Reset()
			continue
		}
		s.line.WriteRune(r)
		s.cur.WriteRune(r)
	}
}

func (s *Splitter) finishLine(line string) {
	if s.fence != "" {
		if fenceClose(line, s.fence) {
			s.fence = ""
		}
		return
	}
	if mark, ok := fenceOpen(line); ok {
		s.fence = mark
	}
}

func (s *Splitter) inCode() bool {
	if s.fence != "" {
		return true
	}
	n := strings.Count(s.line.String(), "`")
	return n%2 == 1
}

func (s *Splitter) canBreak() bool {
	if s.inCode() || s.cur.Len() == 0 {
		return false
	}
	return s.endsSingleNL() || s.sentenceBoundary()
}

func (s *Splitter) endsSingleNL() bool {
	t := s.cur.String()
	return strings.HasSuffix(t, "\n") && !strings.HasSuffix(t, "\n\n")
}

func (s *Splitter) sentenceBoundary() bool {
	t := s.cur.String()
	if !endsPunct(t) {
		return false
	}
	word := wordBeforePunct(t)
	if word == "" || isAbbrev(word) || strings.Contains(word, ".") {
		return false
	}
	return true
}

func endsPunct(t string) bool {
	if t == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(t)
	return r == '.' || r == '!' || r == '?'
}

func wordBeforePunct(t string) string {
	if t == "" {
		return ""
	}
	i := len(t) - 1
	_, size := utf8.DecodeLastRuneInString(t)
	i -= size
	end := i + 1
	for i >= 0 {
		r, n := utf8.DecodeLastRuneInString(t[:i+1])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' {
			i -= n
			continue
		}
		break
	}
	return t[i+1 : end]
}

func isAbbrev(word string) bool {
	switch strings.ToLower(word) {
	case "mr", "mrs", "ms", "dr", "st", "jr", "sr", "vs", "etc", "eg", "ie", "inc", "ltd":
		return true
	default:
		return false
	}
}

func opensTurn(chunk string) bool {
	if chunk == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(chunk)
	if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
		return true
	}
	return blockStart(chunk)
}

func startsSentence(s string) bool {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsUpper(r)
}

func blockStart(s string) bool {
	if strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~") || strings.HasPrefix(s, "#") {
		return true
	}
	if strings.HasPrefix(s, "> ") || s == ">" {
		return true
	}
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") || strings.HasPrefix(s, "+ ") {
		return true
	}
	return false
}

func cutIdent(s string) (string, string, bool) {
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_') {
			break
		}
		i += size
	}
	if i == 0 {
		return "", s, false
	}
	return s[:i], s[i:], true
}

func isUpperIdent(tok string) bool {
	if tok == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(tok)
	if !unicode.IsUpper(r) {
		return false
	}
	for _, c := range tok {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			return false
		}
	}
	return true
}

func continuesCall(rest string) bool {
	if rest == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return r == '(' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func fenceOpen(line string) (string, bool) {
	trim := strings.TrimLeft(line, " ")
	if len(line)-len(trim) > 3 {
		return "", false
	}
	if len(trim) < 3 {
		return "", false
	}
	ch := trim[0]
	if ch != '`' && ch != '~' {
		return "", false
	}
	n := 0
	for n < len(trim) && trim[n] == ch {
		n++
	}
	if n < 3 {
		return "", false
	}
	return strings.Repeat(string(ch), n), true
}

func fenceClose(line, mark string) bool {
	if mark == "" {
		return false
	}
	trim := strings.TrimSpace(line)
	if len(trim) < len(mark) {
		return false
	}
	for i := 0; i < len(trim); i++ {
		if trim[i] != mark[0] {
			return false
		}
	}
	return true
}
