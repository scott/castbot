package page

import (
	"strings"
	"testing"
)

func TestParseSample(t *testing.T) {
	doc, err := Parse(sample())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Blocks) != 6 {
		t.Fatalf("blocks %d", len(doc.Blocks))
	}
	chart := doc.Blocks[4]
	if chart.Kind != "bar" || chart.Series[0].Points[0].Y != 3 {
		t.Fatalf("%+v", chart)
	}
	if doc.Blocks[0].When["range"] != "30 days" {
		t.Fatalf("when %#v", doc.Blocks[0].When)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []string{
		``,
		`{}`,
		`{"v":2,"blocks":[{"type":"markdown","text":"Hi"}]}`,
		`{"v":1,"blocks":[]}`,
		`{"v":1,"blocks":[{"type":"markdown","text":"Hi"}],"extra":1}`,
		`{"v":1,"blocks":[{"type":"markdown","text":"Hi","html":"<script>"}]}`,
		`{"v":1,"blocks":[{"type":"markdown","text":"   "}]}`,
		`{"v":1,"blocks":[{"type":"callout","tone":"loud","text":"Hi"}]}`,
		`{"v":1,"blocks":[{"type":"stats","items":[]}]}`,
		`{"v":1,"blocks":[{"type":"table","columns":["A"],"rows":[{"cells":["1","2"]}]}]}`,
		`{"v":1,"blocks":[{"type":"chart","kind":"pie","series":[{"name":"A","points":[{"x":"M","y":1}]}]}]}`,
		`{"v":1,"blocks":[{"type":"chart","kind":"line","series":[{"name":"A","points":[{"x":"M"}]}]}]}`,
		`{"v":1,"blocks":[{"type":"chart","kind":"line","series":[{"name":"A","points":[{"x":"M","y":1e309}]}]}]}`,
		`{"v":1,"blocks":[{"type":"control","id":"Range","label":"Range","options":["A","B"]}]}`,
		`{"v":1,"blocks":[{"type":"control","id":"range","label":"Range","options":["A"]}]}`,
		`{"v":1,"blocks":[{"type":"control","id":"range","label":"Range","options":["A","A"]}]}`,
		`{"v":1,"blocks":[{"type":"markdown","text":"Hi","when":{"missing":"A"}}]}`,
		`{"v":1,"blocks":[{"type":"control","id":"range","label":"Range","options":["A","B"]},{"type":"control","id":"range","label":"Other","options":["A","B"]}]}`,
		`{"v":1,"blocks":[{"type":"markdown","text":"Hi"}]}` + `{"v":1,"blocks":[{"type":"markdown","text":"Hi"}]}`,
		`{"v":1,"blocks":[{"type":"nope","text":"Hi"}]}`,
	}
	for _, raw := range cases {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	huge := `{"v":1,"blocks":[{"type":"markdown","text":"` + strings.Repeat("a", MaxDocument) + `"}]}`
	if _, err := Parse(huge); err == nil {
		t.Fatal("accepted a document over the cap")
	}
}

func TestCleanTitle(t *testing.T) {
	if got, ok := CleanTitle("  Signups  "); !ok || got != "Signups" {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := CleanTitle(" "); ok {
		t.Fatal("blank")
	}
	if _, ok := CleanTitle(strings.Repeat("a", MaxTitle+1)); ok {
		t.Fatal("long")
	}
	if _, ok := CleanTitle("two\nlines"); ok {
		t.Fatal("newline")
	}
}

func sample() string {
	return `{
	  "v": 1,
	  "blocks": [
	    {"type": "markdown", "text": "Trials beat the other quotes.", "when": {"range": "30 days"}},
	    {"type": "callout", "tone": "note", "text": "Figures are counts, not money."},
	    {"type": "stats", "items": [{"label": "Trials", "value": "12", "note": "30 days"}]},
	    {"type": "table", "caption": "By day", "columns": ["Day", "Trials"], "rows": [{"cells": ["Mon", "3"], "when": {"range": "7 days"}}, {"cells": ["Tue", "4"]}]},
	    {"type": "chart", "kind": "bar", "title": "Trials", "x": "Day", "y": "Count", "series": [{"name": "Trials", "points": [{"x": "Mon", "y": 3}, {"x": "Tue", "y": 4}]}]},
	    {"type": "control", "id": "range", "label": "Range", "options": ["7 days", "30 days"]}
	  ]
	}`
}
