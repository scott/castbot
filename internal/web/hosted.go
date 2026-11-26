package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/scott/castbot/internal/md"
	pagedoc "github.com/scott/castbot/internal/page"
	"github.com/scott/castbot/internal/store"
)

type hostedView struct {
	Title   string
	Missing bool
	Blocks  []hostedBlock
}

type hostedBlock struct {
	Kind    string
	Label   string
	Text    string
	Tone    string
	HTML    template.HTML
	SVG     template.HTML
	Caption string
	When    string
	Hidden  bool
	Columns []string
	Items   []hostedStat
	Rows    []hostedRow
	Options []hostedOption
}

type hostedStat struct {
	Label, Value, Note string
}

type hostedRow struct {
	Cells  []string
	When   string
	Hidden bool
}

type hostedOption struct {
	Name, Value string
	Checked     bool
}

func (s *Server) hostedPage(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if row, err := s.store.PageByLink(r.Context(), token); err == nil {
		s.writeHosted(w, row)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	need, err := s.store.PageRequiresSignIn(r.Context(), token)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if !need {
		s.renderHosted(w, http.StatusNotFound, hostedView{Title: "Page", Missing: true})
		return
	}
	st := from(r)
	if st.User == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	row, err := s.store.PageForUser(r.Context(), st.User.ID, token)
	if errors.Is(err, store.ErrNotFound) {
		s.renderHosted(w, http.StatusNotFound, hostedView{Title: "Page", Missing: true})
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.writeHosted(w, row)
}

func (s *Server) writeHosted(w http.ResponseWriter, row store.Page) {
	doc, err := pagedoc.Parse(row.Body)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.renderHosted(w, http.StatusOK, hostedView{Title: row.Title, Blocks: hostedBlocks(doc)})
}

func (s *Server) renderHosted(w http.ResponseWriter, status int, v hostedView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	w.WriteHeader(status)
	if err := views.ExecuteTemplate(w, "hosted", v); err != nil {
		log.Printf("render hosted failed")
	}
}

func hostedBlocks(doc pagedoc.Document) []hostedBlock {
	defaults := map[string]string{}
	for _, block := range doc.Blocks {
		if block.Type == "control" && len(block.Options) > 0 {
			defaults[block.ID] = block.Options[0]
		}
	}
	out := make([]hostedBlock, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		switch block.Type {
		case "markdown":
			out = append(out, hostedBlock{
				Kind: "markdown", HTML: template.HTML(md.Render(block.Text)),
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "callout":
			out = append(out, hostedBlock{
				Kind: "callout", Tone: block.Tone, Text: block.Text,
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "stats":
			items := make([]hostedStat, len(block.Items))
			for i, item := range block.Items {
				items[i] = hostedStat{Label: item.Label, Value: item.Value, Note: item.Note}
			}
			out = append(out, hostedBlock{
				Kind: "stats", Items: items,
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "table":
			rows := make([]hostedRow, len(block.Rows))
			for i, row := range block.Rows {
				rows[i] = hostedRow{Cells: row.Cells, When: whenAttr(row.When), Hidden: hideWhen(row.When, defaults)}
			}
			out = append(out, hostedBlock{
				Kind: "table", Caption: block.Caption, Columns: block.Columns, Rows: rows,
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "chart":
			out = append(out, hostedBlock{
				Kind: "chart", Caption: block.Title, SVG: chartSVG(block),
				When: whenAttr(block.When), Hidden: hideWhen(block.When, defaults),
			})
		case "control":
			options := make([]hostedOption, len(block.Options))
			for i, option := range block.Options {
				options[i] = hostedOption{Name: block.ID, Value: option, Checked: i == 0}
			}
			out = append(out, hostedBlock{Kind: "control", Label: block.Label, Options: options})
		}
	}
	return out
}

func whenAttr(when map[string]string) string {
	if len(when) == 0 {
		return ""
	}
	raw, err := json.Marshal(when)
	if err != nil {
		return ""
	}
	return string(raw)
}

func hideWhen(when map[string]string, defaults map[string]string) bool {
	for key, want := range when {
		if defaults[key] != want {
			return true
		}
	}
	return false
}

func chartSVG(block pagedoc.Block) template.HTML {
	const (
		width  = 640
		height = 240
		left   = 44
		right  = 12
		top    = 16
		bottom = 36
	)
	cats := chartCats(block.Series)
	if len(cats) == 0 {
		return ""
	}
	minY, maxY := 0.0, 0.0
	seen := false
	for _, series := range block.Series {
		for _, pt := range series.Points {
			if !seen || pt.Y < minY {
				minY = pt.Y
			}
			if !seen || pt.Y > maxY {
				maxY = pt.Y
			}
			seen = true
		}
	}
	if minY > 0 {
		minY = 0
	}
	if maxY < 0 {
		maxY = 0
	}
	if maxY == minY {
		maxY = minY + 1
	}
	plotW := float64(width - left - right)
	plotH := float64(height - top - bottom)
	yAt := func(y float64) float64 {
		return float64(top) + (maxY-y)/(maxY-minY)*plotH
	}
	base := yAt(0)
	label := block.Title
	if label == "" {
		label = "Chart"
	}
	var b strings.Builder
	b.WriteString(`<svg viewBox="0 0 640 240" role="img" aria-label="`)
	b.WriteString(template.HTMLEscapeString(label))
	b.WriteString(`">`)
	fills := []string{"var(--acc)", "var(--start)", "var(--ok)", "var(--warn)"}
	slot := plotW / float64(len(cats))
	if block.Kind == "bar" {
		inner := slot / float64(len(block.Series)+1)
		for si, series := range block.Series {
			vals := map[string]float64{}
			for _, pt := range series.Points {
				vals[pt.X] = pt.Y
			}
			fill := fills[si%len(fills)]
			for ci, cat := range cats {
				y, ok := vals[cat]
				if !ok {
					continue
				}
				y1 := yAt(y)
				y0 := base
				if y1 > y0 {
					y0, y1 = y1, y0
				}
				x := float64(left) + float64(ci)*slot + inner*float64(si+1)
				h := y0 - y1
				if h < 1 {
					h = 1
				}
				b.WriteString(`<rect x="`)
				b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
				b.WriteString(`" y="`)
				b.WriteString(strconv.FormatFloat(y1, 'f', 1, 64))
				b.WriteString(`" width="`)
				b.WriteString(strconv.FormatFloat(inner*0.8, 'f', 1, 64))
				b.WriteString(`" height="`)
				b.WriteString(strconv.FormatFloat(h, 'f', 1, 64))
				b.WriteString(`" fill="`)
				b.WriteString(fill)
				b.WriteString(`"></rect>`)
			}
		}
	} else {
		for si, series := range block.Series {
			vals := map[string]float64{}
			for _, pt := range series.Points {
				vals[pt.X] = pt.Y
			}
			b.WriteString(`<polyline fill="none" stroke="`)
			b.WriteString(fills[si%len(fills)])
			b.WriteString(`" stroke-width="2" points="`)
			first := true
			for ci, cat := range cats {
				y, ok := vals[cat]
				if !ok {
					continue
				}
				if !first {
					b.WriteByte(' ')
				}
				first = false
				x := float64(left) + float64(ci)*slot + slot/2
				b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
				b.WriteByte(',')
				b.WriteString(strconv.FormatFloat(yAt(y), 'f', 1, 64))
			}
			b.WriteString(`"></polyline>`)
		}
	}
	for ci, cat := range cats {
		x := float64(left) + float64(ci)*slot + slot/2
		b.WriteString(`<text x="`)
		b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
		b.WriteString(`" y="`)
		b.WriteString(strconv.Itoa(height - 12))
		b.WriteString(`" text-anchor="middle" fill="currentColor" font-size="12">`)
		b.WriteString(template.HTMLEscapeString(chartLabel(cat)))
		b.WriteString(`</text>`)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func chartCats(series []pagedoc.Series) []string {
	var cats []string
	seen := map[string]bool{}
	for _, series := range series {
		for _, pt := range series.Points {
			if seen[pt.X] {
				continue
			}
			seen[pt.X] = true
			cats = append(cats, pt.X)
		}
	}
	return cats
}

func chartLabel(s string) string {
	if utf8.RuneCountInString(s) <= 12 {
		return s
	}
	n := 0
	for i := range s {
		if n == 12 {
			return s[:i]
		}
		n++
	}
	return s
}
