package md

import (
	"strings"
	"testing"
)

const featureMD = `
## Pricing tiers

I read ` + "`docs/pricing.md`" + ` and the FAQ is **out of date** and *still* used. ~~old price~~

### Tiers

| Tier | Price |
| --- | ---: |
| **Starter** | $0 |
| Team | $24 |

> Note: pricing.md is the source of truth.

1. Update docs/faq.md
   - "How much does it cost?"
   - [ ] not a live checkbox
2. Run the lint

` + "```bash\n" + `git checkout -b docs/pricing-faq
echo hi
` + "```" + `

---

See [pricing.md](https://example.org/docs/pricing) and write to <a href="mailto:alex@example.com">alex@example.com</a>.

![diagram](https://example.org/diagram.png)

##### small heading
###### smaller
`

func TestMarkdownFeatures(t *testing.T) {
	got := Render(featureMD)
	wants := []string{
		"<h2>", "Pricing tiers",
		"<h3>", "Tiers",
		"<h4>", "small heading",
		"<h4>", "smaller",
		"<strong>out of date</strong>",
		"<em>still</em>",
		"<del>old price</del>",
		"<code>docs/pricing.md</code>",
		`class="md-code"`,
		">bash</span>",
		`aria-label="Copy code"`,
		"git checkout -b docs/pricing-faq",
		`class="md-table"`,
		`role="region"`,
		`class="num"`,
		"<blockquote>",
		"<ol>",
		"<ul>",
		`class="task"`,
		"☐",
		`href="https://example.org/docs/pricing"`,
		`rel="noopener noreferrer nofollow"`,
		`target="_blank"`,
		`href="mailto:alex@example.com"`,
		`href="https://example.org/diagram.png"`,
		"Image: diagram",
		"<hr",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "<img") {
		t.Fatalf("image loaded\n%s", got)
	}
	if strings.Contains(got, "<h5") || strings.Contains(got, "<h6") {
		t.Fatalf("deep heading kept\n%s", got)
	}
	if strings.Contains(got, "<input") {
		t.Fatalf("live checkbox\n%s", got)
	}
}

func TestMarkdownXSS(t *testing.T) {
	payloads := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`[x](javascript:alert(1))`,
		`<a href="data:text/html,hi">click</a>`,
		`[x](vbscript:alert(1))`,
		`<a href="https://example.org" onerror="alert(1)">ok</a>`,
		"```" + `"><script>` + "\ncode\n```",
	}
	for _, payload := range payloads {
		got := Render(payload)
		low := strings.ToLower(got)
		if strings.Contains(low, "<script") || strings.Contains(low, "<img") || strings.Contains(low, "javascript:") || strings.Contains(low, "vbscript:") || strings.Contains(low, "data:text") || strings.Contains(low, "onerror=\"") {
			t.Fatalf("payload survived %q\n%s", payload, got)
		}
	}
}

func TestMarkdownHTMLInCode(t *testing.T) {
	fence := "```\n<script>alert(1)</script>\n<img src=x onerror=alert(1)>\n```"
	got := Render(fence)
	if strings.Contains(got, "<script>") || strings.Contains(got, "<img") {
		t.Fatalf("fence rendered tags\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") && !strings.Contains(got, "&lt;script&gt;alert") && !strings.Contains(got, "script") {
		t.Fatalf("fence dropped the text\n%s", got)
	}
	inline := Render("See `<img src=x onerror=alert(1)>` please")
	if strings.Contains(inline, "<img") || strings.Contains(inline, "onerror=\"") {
		t.Fatalf("inline code rendered tags\n%s", inline)
	}
	if !strings.Contains(inline, "&lt;img") {
		t.Fatalf("inline code dropped the text\n%s", inline)
	}
}

func TestMarkdownLinkSafety(t *testing.T) {
	got := Render("[docs](https://example.org/a) and [mail](mailto:a@example.com) and [bad](javascript:alert(1)) and [data](data:text/html,hi)")
	if !strings.Contains(got, `href="https://example.org/a"`) || !strings.Contains(got, `rel="noopener noreferrer nofollow"`) || !strings.Contains(got, `target="_blank"`) {
		t.Fatalf("https link\n%s", got)
	}
	if !strings.Contains(got, `href="mailto:a@example.com"`) {
		t.Fatalf("mailto\n%s", got)
	}
	if strings.Contains(strings.ToLower(got), "javascript:") || strings.Contains(strings.ToLower(got), "data:text") {
		t.Fatalf("bad scheme kept\n%s", got)
	}
	if !strings.Contains(got, ">bad<") && !strings.Contains(got, ">bad</") && !strings.Contains(got, "bad") {
		t.Fatalf("bad link text dropped\n%s", got)
	}
}

func TestMarkdownNoInlineScriptOrStyle(t *testing.T) {
	got := Render(featureMD + "\n\n<script>bad()</script>\n\n[x](javascript:alert(1))")
	if strings.Contains(strings.ToLower(got), "<script") || strings.Contains(got, "style=") || strings.Contains(got, "onclick") {
		t.Fatalf("inline\n%s", got)
	}
}

func TestStreamHalfBoldDoesNotFlip(t *testing.T) {
	_, tail := Split("Keep **bol")
	got := RenderTail(tail)
	if strings.Contains(got, "<strong>") || strings.Contains(got, "**") || strings.Contains(got, "bol") {
		t.Fatalf("half bold leaked\n%s", got)
	}
	if !strings.Contains(got, "Keep") || !strings.Contains(got, "caret") {
		t.Fatalf("tail\n%s", got)
	}
	full := RenderTail(Block{Kind: "para", Raw: "Keep **bold**"})
	if !strings.Contains(full, "<strong>bold</strong>") || !strings.Contains(full, "Keep") {
		t.Fatalf("closed bold\n%s", full)
	}
}

func TestStreamHalfLink(t *testing.T) {
	_, tail := Split("See [docs](https://exa")
	got := RenderTail(tail)
	if strings.Contains(got, "https://exa") || strings.Contains(got, "<a ") {
		t.Fatalf("half link\n%s", got)
	}
	if !strings.Contains(got, "docs") {
		t.Fatalf("link text dropped\n%s", got)
	}
}

func TestStreamOpenFenceWriting(t *testing.T) {
	closed, tail := Split("Here\n\n```bash\necho hi")
	if len(closed) != 1 || !strings.Contains(closed[0].Raw, "Here") {
		t.Fatalf("closed %#v", closed)
	}
	if tail.Kind != "fence" || !tail.Open || tail.Lang != "bash" {
		t.Fatalf("tail %#v", tail)
	}
	got := RenderTail(tail)
	if !strings.Contains(got, `class="md-code open"`) || !strings.Contains(got, ">Writing<") || !strings.Contains(got, ">bash<") || !strings.Contains(got, "echo hi") || !strings.Contains(got, "caret") {
		t.Fatalf("open fence\n%s", got)
	}
	if strings.Contains(got, "Copy") {
		t.Fatalf("copy showed early\n%s", got)
	}
}

func TestStreamFenceCloseBecomesCopy(t *testing.T) {
	closed, tail := Split("```bash\necho hi\n```\n")
	if tail.Raw != "" {
		t.Fatalf("tail still open %#v", tail)
	}
	if len(closed) != 1 {
		t.Fatalf("closed %#v", closed)
	}
	got := Render(closed[0].Raw)
	if !strings.Contains(got, "Copy") || strings.Contains(got, "Writing") || !strings.Contains(got, "echo hi") {
		t.Fatalf("closed fence\n%s", got)
	}
}

func TestStreamTableWaitsForDelim(t *testing.T) {
	_, tail := Split("| A | B |")
	got := RenderTail(tail)
	if strings.Contains(got, "<table") {
		t.Fatalf("table early\n%s", got)
	}
	if !strings.Contains(got, "A") {
		t.Fatalf("plain header\n%s", got)
	}
	_, ready := Split("| A | B |\n| --- | ---: |\n| 1 | 2 |")
	got = RenderTail(ready)
	if !strings.Contains(got, "<table") || !strings.Contains(got, `class="num"`) {
		t.Fatalf("table\n%s", got)
	}
}

func TestStreamListHalfItem(t *testing.T) {
	_, tail := Split("1. one\n2. Run the docs li")
	if tail.Kind != "list" {
		t.Fatalf("kind %s %q", tail.Kind, tail.Raw)
	}
	got := RenderTail(tail)
	if !strings.Contains(got, "<ol>") || !strings.Contains(got, "Run the docs li") || !strings.Contains(got, "caret") {
		t.Fatalf("list\n%s", got)
	}
}

func TestStreamSplitClosedOnce(t *testing.T) {
	src := "## Title\n\nHello **world**\n\n1. one\n2. two\n\n```bash\necho hi\n```\n\nAfter\n\n"
	var prev []string
	for i := 1; i <= len(src); i++ {
		closed, _ := Split(src[:i])
		raws := make([]string, len(closed))
		for j, b := range closed {
			raws[j] = b.Raw
		}
		if len(raws) < len(prev) {
			t.Fatalf("shrank at %d", i)
		}
		for j := range prev {
			if raws[j] != prev[j] {
				t.Fatalf("block %d changed at %d\nwas %q\nnow %q", j, i, prev[j], raws[j])
			}
		}
		prev = raws
	}
	closed, tail := Split(src)
	if tail.Raw != "" {
		t.Fatalf("finished doc still has tail %q", tail.Raw)
	}
	if len(closed) < 4 {
		t.Fatalf("closed %d %#v", len(closed), closed)
	}
}

func TestStreamResumeNoDuplicateBlocks(t *testing.T) {
	src := "Hello\n\n```go\nfmt.Println(1)\n```\n\nNext line"
	cut := strings.Index(src, "fmt")
	before, _ := Split(src[:cut])
	all, tail := Split(src)
	if len(all) < len(before) {
		t.Fatal("resume lost blocks")
	}
	for i := range before {
		if all[i].Raw != before[i].Raw {
			t.Fatalf("duplicate or rewrite %d\n%q\n%q", i, before[i].Raw, all[i].Raw)
		}
	}
	joined := ""
	for _, b := range all[len(before):] {
		joined += b.Raw
	}
	if strings.Contains(joined, before[0].Raw) && before[0].Raw != "" {
		t.Fatalf("closed block repeated in the new suffix")
	}
	_ = tail
}

func TestUserPlain(t *testing.T) {
	samples := []string{
		"Before you push, check:\n- the **pricing** lines in docs/faq.md\n- the GITHUB_TOKEN note in docs/setup_guide.md\n- links like https://example.org/docs/pricing\n\nThanks!",
		"The lint said:\n\n  docs/faq.md:41  heading not sentence case\n  docs/faq.md:58  trailing space\n\n# fix these too",
	}
	for _, sample := range samples {
		got := LinkPlain(sample)
		if strings.Contains(got, "<strong>") || strings.Contains(got, "<em>") || strings.Contains(got, "<h1") || strings.Contains(got, "<li") || strings.Contains(got, "<code") {
			t.Fatalf("user markdown rendered\n%s", got)
		}
	}
	if !strings.Contains(LinkPlain(samples[0]), "**pricing**") || !strings.Contains(LinkPlain(samples[0]), "GITHUB_TOKEN") {
		t.Fatal("literal text lost")
	}
	second := LinkPlain(samples[1])
	if !strings.Contains(second, "# fix these too") || !strings.Contains(second, "  docs/faq.md:41") {
		t.Fatalf("indent lost\n%s", second)
	}
	got := LinkPlain(samples[0])
	if !strings.Contains(got, `href="https://example.org/docs/pricing"`) || !strings.Contains(got, `rel="noopener noreferrer nofollow"`) || !strings.Contains(got, `target="_blank"`) {
		t.Fatalf("url\n%s", got)
	}
	trailed := LinkPlain("see https://example.org/a.")
	if !strings.Contains(trailed, `href="https://example.org/a"`) || !strings.Contains(trailed, "</a>.") {
		t.Fatalf("trail\n%s", trailed)
	}
	if strings.Contains(LinkPlain("[x](javascript:alert(1))"), "javascript:") && strings.Contains(LinkPlain("[x](javascript:alert(1))"), "<a ") {
		t.Fatal("javascript linked")
	}
}

func TestFenceInfoIsSafe(t *testing.T) {
	got := Render("```\"><script>\nalert(1)\n```")
	if strings.Contains(strings.ToLower(got), "<script") || strings.Contains(got, `class="lang">`) && strings.Contains(got, `class="lang">script`) {
		t.Fatalf("info string\n%s", got)
	}
	if !strings.Contains(got, `<span class="lang"></span>`) {
		t.Fatalf("info string kept junk\n%s", got)
	}
	if !strings.Contains(got, "alert(1)") {
		t.Fatalf("code dropped\n%s", got)
	}
}
