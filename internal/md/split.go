package md

import (
	"strings"
)

// Block is one markdown block. Open is set on a tail that is still growing,
// including an unclosed fence.
type Block struct {
	Kind string
	Raw  string
	Lang string
	Mark string
	Open bool
}

// Split divides src into blocks that can no longer change and one open tail.
// Growing src only appends to the closed list. A closed block's raw text stays put.
func Split(src string) (closed []Block, tail Block) {
	if src == "" {
		return nil, Block{}
	}
	ended := strings.HasSuffix(src, "\n")
	lines := strings.Split(src, "\n")
	if ended {
		lines = lines[:len(lines)-1]
	}
	var complete []string
	partial := ""
	hasPartial := false
	if ended {
		complete = lines
	} else if len(lines) > 0 {
		complete = lines[:len(lines)-1]
		partial = lines[len(lines)-1]
		hasPartial = true
	}

	var cur *Block
	commit := func() {
		if cur == nil {
			return
		}
		closed = append(closed, *cur)
		cur = nil
	}
	add := func(line string) {
		if cur.Raw == "" {
			cur.Raw = line
			return
		}
		cur.Raw += "\n" + line
	}

	for _, line := range complete {
		if cur != nil && cur.Kind == "fence" {
			add(line)
			if fenceClose(line, cur.Mark) {
				cur.Open = false
				commit()
			}
			continue
		}
		if isBlank(line) {
			commit()
			continue
		}
		if ok, mark, lang := fenceOpen(line); ok {
			commit()
			cur = &Block{Kind: "fence", Raw: line, Lang: lang, Mark: mark, Open: true}
			continue
		}
		if isATX(line) {
			commit()
			closed = append(closed, Block{Kind: "heading", Raw: line})
			continue
		}
		if isRule(line) {
			commit()
			closed = append(closed, Block{Kind: "rule", Raw: line})
			continue
		}
		if cur != nil && cur.Kind == "list" && (isList(line) || isCont(line)) {
			add(line)
			continue
		}
		if cur != nil && cur.Kind == "quote" && isQuote(line) {
			add(line)
			continue
		}
		if cur != nil && cur.Kind == "table" && isTable(line) {
			add(line)
			continue
		}
		if cur != nil && cur.Kind == "para" && !blockStart(line) {
			add(line)
			continue
		}
		commit()
		switch {
		case isList(line):
			cur = &Block{Kind: "list", Raw: line}
		case isQuote(line):
			cur = &Block{Kind: "quote", Raw: line}
		case isTable(line):
			cur = &Block{Kind: "table", Raw: line}
		default:
			cur = &Block{Kind: "para", Raw: line}
		}
	}

	if !hasPartial {
		if cur != nil {
			if cur.Kind == "fence" {
				cur.Open = true
			}
			return closed, *cur
		}
		return closed, Block{}
	}

	if cur != nil && cur.Kind == "fence" {
		if fenceClose(partial, cur.Mark) {
			add(partial)
			cur.Open = false
			commit()
			return closed, Block{}
		}
		add(partial)
		cur.Open = true
		return closed, *cur
	}
	if ok, mark, lang := fenceOpen(partial); ok {
		commit()
		return closed, Block{Kind: "fence", Raw: partial, Lang: lang, Mark: mark, Open: true}
	}
	if cur != nil && sameKind(cur, partial) {
		add(partial)
		out := *cur
		if out.Kind == "fence" {
			out.Open = true
		}
		return closed, out
	}
	if blockStart(partial) || cur == nil || (cur.Kind == "para" && blockStart(partial)) {
		commit()
		return closed, partialBlock(partial)
	}
	add(partial)
	return closed, *cur
}

func sameKind(cur *Block, line string) bool {
	switch cur.Kind {
	case "list":
		return isList(line) || isCont(line)
	case "quote":
		return isQuote(line)
	case "table":
		return isTable(line)
	case "para":
		return !blockStart(line)
	default:
		return false
	}
}

func partialBlock(line string) Block {
	switch {
	case isList(line):
		return Block{Kind: "list", Raw: line}
	case isQuote(line):
		return Block{Kind: "quote", Raw: line}
	case isTable(line):
		return Block{Kind: "table", Raw: line}
	case isATX(line):
		return Block{Kind: "heading", Raw: line}
	case isRule(line):
		return Block{Kind: "rule", Raw: line}
	default:
		return Block{Kind: "para", Raw: line}
	}
}

func blockStart(line string) bool {
	if ok, _, _ := fenceOpen(line); ok {
		return true
	}
	return isATX(line) || isRule(line) || isList(line) || isQuote(line) || isTable(line)
}

func isBlank(line string) bool {
	return strings.TrimSpace(line) == ""
}

func fenceOpen(line string) (bool, string, string) {
	trim := stripIndent(line, 3)
	if trim == line && strings.HasPrefix(line, "    ") {
		return false, "", ""
	}
	if len(trim) < 3 {
		return false, "", ""
	}
	ch := trim[0]
	if ch != '`' && ch != '~' {
		return false, "", ""
	}
	n := 0
	for n < len(trim) && trim[n] == ch {
		n++
	}
	if n < 3 {
		return false, "", ""
	}
	rest := trim[n:]
	if ch == '`' && strings.Contains(rest, "`") {
		return false, "", ""
	}
	return true, strings.Repeat(string(ch), n), fenceLang(rest)
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

func fenceLang(rest string) string {
	rest = strings.TrimSpace(rest)
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		rest = rest[:i]
	}
	return cleanLang(rest)
}

func cleanLang(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("+#._-", r) {
			b.WriteRune(r)
		}
		if b.Len() >= 20 {
			break
		}
	}
	return b.String()
}

func isATX(line string) bool {
	trim := stripIndent(line, 3)
	if trim == "" || trim[0] != '#' {
		return false
	}
	n := 0
	for n < len(trim) && trim[n] == '#' {
		n++
	}
	if n == 0 || n > 6 {
		return false
	}
	return n == len(trim) || trim[n] == ' ' || trim[n] == '\t'
}

func isRule(line string) bool {
	trim := strings.TrimSpace(line)
	if len(trim) < 3 {
		return false
	}
	ch := rune(0)
	count := 0
	for _, r := range trim {
		if r == ' ' || r == '\t' {
			continue
		}
		if r != '-' && r != '*' && r != '_' {
			return false
		}
		if ch == 0 {
			ch = r
		}
		if r != ch {
			return false
		}
		count++
	}
	return count >= 3
}

func isList(line string) bool {
	trim := stripIndent(line, 7)
	if trim == "" {
		return false
	}
	if trim[0] == '-' || trim[0] == '*' || trim[0] == '+' {
		return len(trim) == 1 || trim[1] == ' ' || trim[1] == '\t'
	}
	i := 0
	for i < len(trim) && i < 9 && trim[i] >= '0' && trim[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(trim) {
		return false
	}
	if trim[i] != '.' && trim[i] != ')' {
		return false
	}
	return i+1 == len(trim) || trim[i+1] == ' ' || trim[i+1] == '\t'
}

func isQuote(line string) bool {
	trim := stripIndent(line, 3)
	return strings.HasPrefix(trim, ">")
}

func isTable(line string) bool {
	trim := strings.TrimSpace(line)
	return strings.HasPrefix(trim, "|")
}

func isDelim(line string) bool {
	trim := strings.TrimSpace(line)
	trim = strings.Trim(trim, "|")
	if trim == "" || !strings.Contains(trim, "-") {
		return false
	}
	for _, part := range strings.Split(trim, "|") {
		cell := strings.TrimSpace(part)
		if cell == "" {
			continue
		}
		cell = strings.Trim(cell, ":")
		if cell == "" || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return strings.Contains(trim, "-")
}

func tableReady(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		if isDelim(line) {
			return true
		}
	}
	return false
}

func isCont(line string) bool {
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "\t") {
		return true
	}
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n >= 2 && n < len(line)
}

func stripIndent(line string, max int) string {
	n := 0
	for n < len(line) && n < max && line[n] == ' ' {
		n++
	}
	return line[n:]
}
