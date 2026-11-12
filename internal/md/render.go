package md

import (
	"bytes"
	"html"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// engine renders GFM with raw HTML left off. The HTML renderer escapes raw
// tags unless unsafe mode is enabled, and this engine does not enable it.
var engine = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
)

var policy = newPolicy()

func newPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(
		"p", "h1", "h2", "h3", "h4", "h5", "h6",
		"strong", "em", "del", "code", "pre", "blockquote",
		"ul", "ol", "li", "table", "thead", "tbody", "tr", "th", "td",
		"a", "hr", "br", "input",
	)
	p.AllowAttrs("href").OnElements("a")
	p.AllowURLSchemes("http", "https", "mailto")
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(false)
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("disabled").OnElements("input")
	p.AllowAttrs("checked").OnElements("input")
	p.AllowAttrs("align").Matching(regexp.MustCompile(`^(left|right|center)$`)).OnElements("th", "td")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^language-[A-Za-z0-9+#._-]{0,40}$`)).OnElements("code")
	p.AllowNoAttrs().OnElements(
		"p", "h1", "h2", "h3", "h4", "h5", "h6",
		"strong", "em", "del", "code", "pre", "blockquote",
		"ul", "ol", "li", "table", "thead", "tbody", "tr", "th", "td",
		"a", "hr", "br", "input",
	)
	return p
}

// Render turns a finished agent message into safe HTML. The result is a
// fragment, not a full document. Classes are added after the allowlist runs.
func Render(src string) string {
	raw, err := goldmarkHTML(src)
	if err != nil {
		return "<p>" + html.EscapeString(src) + "</p>"
	}
	safe := policy.Sanitize(prepareHTML(raw))
	return decorate(safe, false)
}

// RenderTail renders the open tail, including the caret.
// An unclosed fence is a code block with Writing in place of Copy.
func RenderTail(b Block) string {
	if strings.TrimSpace(b.Raw) == "" && b.Kind == "" {
		return ""
	}
	if b.Kind == "fence" && (b.Open || !strings.Contains(b.Raw, "\n"+b.Mark)) {
		return openFence(b)
	}
	raw := b.Raw
	if b.Kind != "fence" {
		raw = soften(raw)
	}
	if strings.TrimSpace(raw) == "" {
		if strings.TrimSpace(b.Raw) == "" {
			return ""
		}
		return caret
	}
	if b.Kind == "table" && !tableReady(raw) {
		return plainPara(raw)
	}
	body := Render(raw)
	if strings.TrimSpace(body) == "" {
		return caret
	}
	return insertCaret(body)
}

const caret = `<span class="caret" aria-hidden="true"></span>`

func goldmarkHTML(src string) (string, error) {
	var buf bytes.Buffer
	if err := engine.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func prepareHTML(src string) string {
	nodes := parseFragment(src)
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == nethtml.ElementNode {
				if c.Data == "th" || c.Data == "td" {
					promoteAlign(c)
				}
				if c.Data == "img" {
					replaceImage(c)
				} else {
					walk(c)
				}
			}
			c = next
		}
	}
	root := &nethtml.Node{Type: nethtml.ElementNode, Data: "div"}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	walk(root)
	return renderChildren(root)
}

func promoteAlign(n *nethtml.Node) {
	style := strings.ToLower(strings.ReplaceAll(attr(n, "style"), " ", ""))
	switch {
	case strings.Contains(style, "text-align:right"):
		setAttr(n, "align", "right")
	case strings.Contains(style, "text-align:center"):
		setAttr(n, "align", "center")
	case strings.Contains(style, "text-align:left"):
		setAttr(n, "align", "left")
	}
	dropAttr(n, "style")
}

func replaceImage(n *nethtml.Node) {
	alt := attr(n, "alt")
	src := attr(n, "src")
	label := "Image"
	if alt != "" {
		label = "Image: " + alt
	}
	text := &nethtml.Node{Type: nethtml.TextNode, Data: label}
	if schemeOK(src) {
		a := &nethtml.Node{Type: nethtml.ElementNode, Data: "a"}
		a.Attr = []nethtml.Attribute{{Key: "href", Val: src}}
		a.AppendChild(text)
		n.Parent.InsertBefore(a, n)
	} else if alt != "" {
		n.Parent.InsertBefore(text, n)
	} else if src != "" {
		n.Parent.InsertBefore(&nethtml.Node{Type: nethtml.TextNode, Data: "Image"}, n)
	}
	n.Parent.RemoveChild(n)
}

func decorate(src string, open bool) string {
	nodes := parseFragment(src)
	root := &nethtml.Node{Type: nethtml.ElementNode, Data: "div"}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	transform(root, open)
	return strings.TrimSpace(renderChildren(root))
}

func transform(n *nethtml.Node, open bool) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == nethtml.ElementNode {
			switch c.Data {
			case "h5", "h6":
				c.Data = "h4"
			case "a":
				fixLink(c)
			case "input":
				replaceTask(c)
			case "pre":
				wrapCode(c, open)
			case "table":
				wrapTable(c)
			}
			if c.Parent != nil {
				transform(c, open)
			}
		}
		c = next
	}
	// alignment classes after the walk so new wrappers are not missed
	markNums(n)
}

func fixLink(n *nethtml.Node) {
	href := attr(n, "href")
	if !schemeOK(href) {
		unwrap(n)
		return
	}
	n.Attr = []nethtml.Attribute{
		{Key: "href", Val: href},
		{Key: "target", Val: "_blank"},
		{Key: "rel", Val: "noopener noreferrer nofollow"},
	}
}

func schemeOK(raw string) bool {
	// Decode entities and drop spaces so "&#106;avascript:" cannot stay a link.
	raw = html.UnescapeString(strings.TrimSpace(raw))
	raw = strings.Map(func(r rune) rune {
		if r <= 32 || r == 0x7f {
			return -1
		}
		return r
	}, raw)
	low := strings.ToLower(raw)
	return strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") || strings.HasPrefix(low, "mailto:")
}

func replaceTask(n *nethtml.Node) {
	if strings.ToLower(attr(n, "type")) != "checkbox" {
		unwrap(n)
		return
	}
	glyph := "☐"
	if attrExists(n, "checked") {
		glyph = "☑"
	}
	span := &nethtml.Node{Type: nethtml.ElementNode, Data: "span"}
	span.Attr = []nethtml.Attribute{{Key: "class", Val: "task"}, {Key: "aria-hidden", Val: "true"}}
	span.AppendChild(&nethtml.Node{Type: nethtml.TextNode, Data: glyph})
	n.Parent.InsertBefore(span, n)
	n.Parent.RemoveChild(n)
}

func wrapCode(pre *nethtml.Node, open bool) {
	if pre.Parent != nil && pre.Parent.Data == "figure" {
		return
	}
	lang := ""
	var code *nethtml.Node
	for c := pre.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == nethtml.ElementNode && c.Data == "code" {
			code = c
			lang = langFromClass(attr(c, "class"))
			stripClass(c)
		}
	}
	figure := &nethtml.Node{Type: nethtml.ElementNode, Data: "figure"}
	cls := "md-code"
	if open {
		cls += " open"
	}
	label := "code"
	if lang != "" {
		label = lang + " code"
	}
	figure.Attr = []nethtml.Attribute{{Key: "class", Val: cls}, {Key: "aria-label", Val: label}}
	cap := &nethtml.Node{Type: nethtml.ElementNode, Data: "figcaption"}
	cap.Attr = []nethtml.Attribute{{Key: "class", Val: "md-code-h"}}
	langSpan := &nethtml.Node{Type: nethtml.ElementNode, Data: "span"}
	langSpan.Attr = []nethtml.Attribute{{Key: "class", Val: "lang"}}
	langSpan.AppendChild(&nethtml.Node{Type: nethtml.TextNode, Data: lang})
	cap.AppendChild(langSpan)
	if open {
		wait := &nethtml.Node{Type: nethtml.ElementNode, Data: "span"}
		wait.Attr = []nethtml.Attribute{{Key: "class", Val: "wait"}}
		wait.AppendChild(&nethtml.Node{Type: nethtml.TextNode, Data: "Writing"})
		cap.AppendChild(wait)
	} else {
		cap.AppendChild(copyButton())
	}
	figure.AppendChild(cap)
	setAttr(pre, "tabindex", "0")
	pre.Parent.InsertBefore(figure, pre)
	pre.Parent.RemoveChild(pre)
	figure.AppendChild(pre)
	_ = code
}

func copyButton() *nethtml.Node {
	btn := &nethtml.Node{Type: nethtml.ElementNode, Data: "button"}
	btn.Attr = []nethtml.Attribute{
		{Key: "class", Val: "copy"},
		{Key: "type", Val: "button"},
		{Key: "data-copy", Val: "code"},
		{Key: "aria-label", Val: "Copy code"},
	}
	svg := &nethtml.Node{Type: nethtml.ElementNode, Data: "svg"}
	svg.Attr = []nethtml.Attribute{
		{Key: "class", Val: "i"},
		{Key: "viewBox", Val: "0 0 24 24"},
		{Key: "aria-hidden", Val: "true"},
	}
	rect := &nethtml.Node{Type: nethtml.ElementNode, Data: "rect"}
	rect.Attr = []nethtml.Attribute{{Key: "x", Val: "8"}, {Key: "y", Val: "8"}, {Key: "width", Val: "12"}, {Key: "height", Val: "12"}, {Key: "rx", Val: "2"}}
	path := &nethtml.Node{Type: nethtml.ElementNode, Data: "path"}
	path.Attr = []nethtml.Attribute{{Key: "d", Val: "M16 8V5H4v11h4"}}
	svg.AppendChild(rect)
	svg.AppendChild(path)
	btn.AppendChild(svg)
	span := &nethtml.Node{Type: nethtml.ElementNode, Data: "span"}
	span.AppendChild(&nethtml.Node{Type: nethtml.TextNode, Data: "Copy"})
	btn.AppendChild(span)
	return btn
}

func wrapTable(table *nethtml.Node) {
	if table.Parent != nil && table.Parent.Data == "div" && attr(table.Parent, "class") == "md-table" {
		return
	}
	div := &nethtml.Node{Type: nethtml.ElementNode, Data: "div"}
	div.Attr = []nethtml.Attribute{
		{Key: "class", Val: "md-table"},
		{Key: "tabindex", Val: "0"},
		{Key: "role", Val: "region"},
		{Key: "aria-label", Val: "Table"},
	}
	table.Parent.InsertBefore(div, table)
	table.Parent.RemoveChild(table)
	div.AppendChild(table)
}

func markNums(n *nethtml.Node) {
	if n.Type == nethtml.ElementNode && (n.Data == "th" || n.Data == "td") {
		if strings.EqualFold(attr(n, "align"), "right") {
			setAttr(n, "class", "num")
			dropAttr(n, "align")
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		markNums(c)
	}
}

func langFromClass(cls string) string {
	for _, part := range strings.Fields(cls) {
		if strings.HasPrefix(part, "language-") {
			return cleanLang(strings.TrimPrefix(part, "language-"))
		}
	}
	return ""
}

func openFence(b Block) string {
	lang := cleanLang(b.Lang)
	code := fenceBody(b)
	var buf strings.Builder
	buf.WriteString(`<figure class="md-code open" aria-label="`)
	if lang != "" {
		buf.WriteString(html.EscapeString(lang))
		buf.WriteString(` code`)
	} else {
		buf.WriteString(`code`)
	}
	buf.WriteString(`"><figcaption class="md-code-h"><span class="lang">`)
	buf.WriteString(html.EscapeString(lang))
	buf.WriteString(`</span><span class="wait">Writing</span></figcaption><pre tabindex="0"><code>`)
	buf.WriteString(html.EscapeString(code))
	buf.WriteString(caret)
	buf.WriteString(`</code></pre></figure>`)
	return buf.String()
}

func fenceBody(b Block) string {
	raw := b.Raw
	if i := strings.Index(raw, "\n"); i >= 0 {
		raw = raw[i+1:]
	} else {
		return ""
	}
	if b.Mark != "" {
		if j := strings.LastIndex(raw, "\n"); j >= 0 && fenceClose(raw[j+1:], b.Mark) {
			raw = raw[:j]
		} else if fenceClose(raw, b.Mark) && !strings.Contains(raw, "\n") {
			return ""
		}
	}
	return raw
}

func plainPara(raw string) string {
	text := soften(raw)
	if strings.TrimSpace(text) == "" {
		return caret
	}
	return "<p>" + html.EscapeString(text) + caret + "</p>"
}

func insertCaret(fragment string) string {
	nodes := parseFragment(fragment)
	if len(nodes) == 0 {
		return caret
	}
	root := &nethtml.Node{Type: nethtml.ElementNode, Data: "div"}
	for _, n := range nodes {
		root.AppendChild(n)
	}
	target := lastTextHost(root)
	if target == nil {
		target = root
	}
	target.AppendChild(parseOne(caret))
	return strings.TrimSpace(renderChildren(root))
}

func lastTextHost(n *nethtml.Node) *nethtml.Node {
	var found *nethtml.Node
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode && n.Data != "button" && n.Data != "svg" && n.Data != "figcaption" {
			if n.Data == "p" || n.Data == "li" || n.Data == "td" || n.Data == "th" || n.Data == "code" || n.Data == "h1" || n.Data == "h2" || n.Data == "h3" || n.Data == "h4" || n.Data == "blockquote" {
				found = n
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

func parseOne(fragment string) *nethtml.Node {
	nodes := parseFragment(fragment)
	if len(nodes) == 0 {
		return &nethtml.Node{Type: nethtml.TextNode, Data: ""}
	}
	return nodes[0]
}

func parseFragment(s string) []*nethtml.Node {
	ctx := &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := nethtml.ParseFragment(strings.NewReader(s), ctx)
	if err != nil {
		return []*nethtml.Node{{Type: nethtml.TextNode, Data: s}}
	}
	return nodes
}

func renderChildren(n *nethtml.Node) string {
	var buf bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = nethtml.Render(&buf, c)
	}
	return buf.String()
}

func unwrap(n *nethtml.Node) {
	parent := n.Parent
	if parent == nil {
		return
	}
	for n.FirstChild != nil {
		child := n.FirstChild
		n.RemoveChild(child)
		parent.InsertBefore(child, n)
	}
	parent.RemoveChild(n)
}

func attr(n *nethtml.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func attrExists(n *nethtml.Node, key string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}

func setAttr(n *nethtml.Node, key, val string) {
	for i := range n.Attr {
		if strings.EqualFold(n.Attr[i].Key, key) {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, nethtml.Attribute{Key: key, Val: val})
}

func dropAttr(n *nethtml.Node, key string) {
	out := n.Attr[:0]
	for _, a := range n.Attr {
		if !strings.EqualFold(a.Key, key) {
			out = append(out, a)
		}
	}
	n.Attr = out
}

func stripClass(n *nethtml.Node) {
	dropAttr(n, "class")
}

// soften hides a trailing unclosed inline mark so a half marker does not
// flash the earlier text into bold, italic, strike, or code.
// A half link keeps its text and drops the unfinished URL.
func soften(s string) string {
	s = openLink(s)
	return trimMarks(s)
}

var halfLink = regexp.MustCompile(`\[([^\[\]\n]*)\]\([^)\n]*$`)

func openLink(s string) string {
	return halfLink.ReplaceAllString(s, "$1")
}

type mark struct {
	kind string
	pos  int
}

func trimMarks(s string) string {
	var stack []mark
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			i += 2
			continue
		}
		if s[i] == '`' {
			n := 1
			for i+n < len(s) && s[i+n] == '`' {
				n++
			}
			closer := findRun(s, i+n, n, '`')
			if closer < 0 {
				return s[:i]
			}
			i = closer + n
			continue
		}
		if strings.HasPrefix(s[i:], "**") || strings.HasPrefix(s[i:], "__") || strings.HasPrefix(s[i:], "~~") {
			kind := s[i : i+2]
			stack = toggle(stack, kind, i)
			i += 2
			continue
		}
		if s[i] == '*' {
			stack = toggle(stack, "*", i)
			i++
			continue
		}
		if s[i] == '_' && underscoreMark(s, i) {
			stack = toggle(stack, "_", i)
			i++
			continue
		}
		if s[i] == '[' {
			stack = append(stack, mark{kind: "[", pos: i})
			i++
			continue
		}
		if s[i] == ']' {
			stack = popKind(stack, "[")
			i++
			continue
		}
		i++
	}
	if len(stack) == 0 {
		return s
	}
	return s[:stack[0].pos]
}

func toggle(stack []mark, kind string, pos int) []mark {
	if len(stack) > 0 && stack[len(stack)-1].kind == kind {
		return stack[:len(stack)-1]
	}
	return append(stack, mark{kind: kind, pos: pos})
}

func popKind(stack []mark, kind string) []mark {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].kind == kind {
			return append(stack[:i], stack[i+1:]...)
		}
	}
	return stack
}

func underscoreMark(s string, i int) bool {
	prevOK := i == 0 || !isWord(rune(s[i-1]))
	nextOK := i+1 >= len(s) || !isWord(rune(s[i+1]))
	return prevOK || nextOK
}

func isWord(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func findRun(s string, from, n int, ch byte) int {
	want := strings.Repeat(string(ch), n)
	idx := strings.Index(s[from:], want)
	if idx < 0 {
		return -1
	}
	return from + idx
}
