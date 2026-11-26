// Package page checks the document an agent saves as a hosted page.
// The control plane renders the result. The document is not HTML.
package page

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"unicode/utf8"
)

const (
	// Version is the only document version this process accepts.
	Version = 1
	// MaxDocument is the largest JSON text that can be stored.
	// It fits the existing agent tool body limit with room for the envelope.
	MaxDocument = 32 << 10
	// MaxBlocks is the most blocks in one document.
	MaxBlocks = 40
	// MaxMarkdown is the largest prose block, in bytes.
	MaxMarkdown = 8 << 10
	// MaxCallout is the largest callout, in runes.
	MaxCallout = 500
	// MaxStats is the most figures in one stats block.
	MaxStats = 6
	// MaxColumns is the widest table.
	MaxColumns = 8
	// MaxRows is the longest table.
	MaxRows = 100
	// MaxSeries is the most lines or bar groups on one chart.
	MaxSeries = 4
	// MaxPoints is the most points on one series.
	MaxPoints = 60
	// MaxControls is the most switches and filters on one page.
	MaxControls = 6
	// MaxOptions is the most choices on one control.
	MaxOptions = 8
	// MaxTitle is the longest page title, in runes.
	MaxTitle = 80
)

// ErrInvalid means the document or title was rejected.
var ErrInvalid = errors.New("invalid page")

// Document is a checked page body.
type Document struct {
	Blocks []Block
}

// Block is one stacked region. Only the fields for Type are set.
type Block struct {
	Type    string
	When    map[string]string
	Text    string
	Tone    string
	Items   []Stat
	Caption string
	Columns []string
	Rows    []Row
	Kind    string
	Title   string
	XLabel  string
	YLabel  string
	Series  []Series
	ID      string
	Label   string
	Options []string
}

// Stat is one figure in a stats block.
type Stat struct {
	Label string
	Value string
	Note  string
}

// Row is one table row. When omits the row unless those controls match.
type Row struct {
	Cells []string
	When  map[string]string
}

// Series is one named line or bar group.
type Series struct {
	Name   string
	Points []Point
}

// Point is one chart category and its number.
type Point struct {
	X string
	Y float64
}

// CleanTitle trims a title and reports whether it can be stored.
func CleanTitle(title string) (string, bool) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > MaxTitle {
		return "", false
	}
	if strings.ContainsAny(title, "\r\n\t") {
		return "", false
	}
	return title, true
}

// Parse checks raw JSON and returns the document.
// The stored body should be the trimmed raw text, which is what the agent sent.
func Parse(raw string) (Document, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxDocument || !utf8.ValidString(raw) {
		return Document{}, ErrInvalid
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire struct {
		V      int               `json:"v"`
		Blocks []json.RawMessage `json:"blocks"`
	}
	if err := dec.Decode(&wire); err != nil {
		return Document{}, ErrInvalid
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Document{}, ErrInvalid
	}
	if wire.V != Version || len(wire.Blocks) == 0 || len(wire.Blocks) > MaxBlocks {
		return Document{}, ErrInvalid
	}
	doc := Document{Blocks: make([]Block, 0, len(wire.Blocks))}
	controls := map[string][]string{}
	for _, rawBlock := range wire.Blocks {
		block, err := decodeBlock(rawBlock)
		if err != nil {
			return Document{}, ErrInvalid
		}
		if block.Type == "control" {
			if _, ok := controls[block.ID]; ok {
				return Document{}, ErrInvalid
			}
			controls[block.ID] = block.Options
		}
		doc.Blocks = append(doc.Blocks, block)
	}
	if len(controls) > MaxControls {
		return Document{}, ErrInvalid
	}
	for i := range doc.Blocks {
		if err := checkWhen(doc.Blocks[i].When, controls); err != nil {
			return Document{}, ErrInvalid
		}
		if doc.Blocks[i].Type != "table" {
			continue
		}
		for _, row := range doc.Blocks[i].Rows {
			if err := checkWhen(row.When, controls); err != nil {
				return Document{}, ErrInvalid
			}
		}
	}
	return doc, nil
}

func decodeBlock(raw json.RawMessage) (Block, error) {
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &kind); err != nil {
		return Block{}, ErrInvalid
	}
	switch kind.Type {
	case "markdown":
		var w struct {
			Type string            `json:"type"`
			Text string            `json:"text"`
			When map[string]string `json:"when"`
		}
		if err := strict(raw, &w); err != nil || strings.TrimSpace(w.Text) == "" || len(w.Text) > MaxMarkdown {
			return Block{}, ErrInvalid
		}
		return Block{Type: "markdown", Text: w.Text, When: cleanWhen(w.When)}, nil
	case "callout":
		var w struct {
			Type string            `json:"type"`
			Tone string            `json:"tone"`
			Text string            `json:"text"`
			When map[string]string `json:"when"`
		}
		if err := strict(raw, &w); err != nil || (w.Tone != "note" && w.Tone != "warn") {
			return Block{}, ErrInvalid
		}
		if strings.TrimSpace(w.Text) == "" || utf8.RuneCountInString(w.Text) > MaxCallout {
			return Block{}, ErrInvalid
		}
		return Block{Type: "callout", Tone: w.Tone, Text: w.Text, When: cleanWhen(w.When)}, nil
	case "stats":
		var w struct {
			Type  string            `json:"type"`
			Items []statWire        `json:"items"`
			When  map[string]string `json:"when"`
		}
		if err := strict(raw, &w); err != nil || len(w.Items) == 0 || len(w.Items) > MaxStats {
			return Block{}, ErrInvalid
		}
		items := make([]Stat, 0, len(w.Items))
		for _, item := range w.Items {
			stat, ok := item.clean()
			if !ok {
				return Block{}, ErrInvalid
			}
			items = append(items, stat)
		}
		return Block{Type: "stats", Items: items, When: cleanWhen(w.When)}, nil
	case "table":
		var w struct {
			Type    string            `json:"type"`
			Caption string            `json:"caption"`
			Columns []string          `json:"columns"`
			Rows    []rowWire         `json:"rows"`
			When    map[string]string `json:"when"`
		}
		if err := strict(raw, &w); err != nil {
			return Block{}, ErrInvalid
		}
		block, ok := tableFrom(w.Caption, w.Columns, w.Rows, w.When)
		if !ok {
			return Block{}, ErrInvalid
		}
		return block, nil
	case "chart":
		var w chartWire
		if err := strict(raw, &w); err != nil {
			return Block{}, ErrInvalid
		}
		block, ok := w.clean()
		if !ok {
			return Block{}, ErrInvalid
		}
		return block, nil
	case "control":
		var w struct {
			Type    string   `json:"type"`
			ID      string   `json:"id"`
			Label   string   `json:"label"`
			Options []string `json:"options"`
		}
		if err := strict(raw, &w); err != nil {
			return Block{}, ErrInvalid
		}
		block, ok := cleanControl(w.ID, w.Label, w.Options)
		if !ok {
			return Block{}, ErrInvalid
		}
		return block, nil
	default:
		return Block{}, ErrInvalid
	}
}

type statWire struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

func (w statWire) clean() (Stat, bool) {
	label := strings.TrimSpace(w.Label)
	value := strings.TrimSpace(w.Value)
	note := strings.TrimSpace(w.Note)
	if label == "" || value == "" || utf8.RuneCountInString(label) > 40 || utf8.RuneCountInString(value) > 40 || utf8.RuneCountInString(note) > 80 {
		return Stat{}, false
	}
	if strings.ContainsAny(label, "\r\n") || strings.ContainsAny(value, "\r\n") || strings.ContainsAny(note, "\r\n") {
		return Stat{}, false
	}
	return Stat{Label: label, Value: value, Note: note}, true
}

type rowWire struct {
	Cells []string          `json:"cells"`
	When  map[string]string `json:"when"`
}

func tableFrom(caption string, columns []string, rows []rowWire, when map[string]string) (Block, bool) {
	caption = strings.TrimSpace(caption)
	if utf8.RuneCountInString(caption) > 120 || strings.ContainsAny(caption, "\r\n") {
		return Block{}, false
	}
	if len(columns) == 0 || len(columns) > MaxColumns || len(rows) == 0 || len(rows) > MaxRows {
		return Block{}, false
	}
	cleanCols := make([]string, len(columns))
	for i, col := range columns {
		col = strings.TrimSpace(col)
		if col == "" || utf8.RuneCountInString(col) > 40 || strings.ContainsAny(col, "\r\n") {
			return Block{}, false
		}
		cleanCols[i] = col
	}
	cleanRows := make([]Row, len(rows))
	for i, row := range rows {
		if len(row.Cells) != len(cleanCols) {
			return Block{}, false
		}
		cells := make([]string, len(row.Cells))
		for j, cell := range row.Cells {
			if utf8.RuneCountInString(cell) > 200 || strings.ContainsAny(cell, "\r\n") {
				return Block{}, false
			}
			cells[j] = cell
		}
		cleanRows[i] = Row{Cells: cells, When: cleanWhen(row.When)}
	}
	return Block{Type: "table", Caption: caption, Columns: cleanCols, Rows: cleanRows, When: cleanWhen(when)}, true
}

type chartWire struct {
	Type   string            `json:"type"`
	Kind   string            `json:"kind"`
	Title  string            `json:"title"`
	X      string            `json:"x"`
	Y      string            `json:"y"`
	Series []seriesWire      `json:"series"`
	When   map[string]string `json:"when"`
}

type seriesWire struct {
	Name   string      `json:"name"`
	Points []pointWire `json:"points"`
}

type pointWire struct {
	X string   `json:"x"`
	Y *float64 `json:"y"`
}

func (w chartWire) clean() (Block, bool) {
	if w.Kind != "bar" && w.Kind != "line" {
		return Block{}, false
	}
	title := strings.TrimSpace(w.Title)
	xLabel := strings.TrimSpace(w.X)
	yLabel := strings.TrimSpace(w.Y)
	if utf8.RuneCountInString(title) > 80 || utf8.RuneCountInString(xLabel) > 40 || utf8.RuneCountInString(yLabel) > 40 {
		return Block{}, false
	}
	if strings.ContainsAny(title, "\r\n") || strings.ContainsAny(xLabel, "\r\n") || strings.ContainsAny(yLabel, "\r\n") {
		return Block{}, false
	}
	if len(w.Series) == 0 || len(w.Series) > MaxSeries {
		return Block{}, false
	}
	series := make([]Series, len(w.Series))
	names := map[string]bool{}
	for i, raw := range w.Series {
		name := strings.TrimSpace(raw.Name)
		if name == "" || names[name] || utf8.RuneCountInString(name) > 40 || strings.ContainsAny(name, "\r\n") {
			return Block{}, false
		}
		names[name] = true
		if len(raw.Points) == 0 || len(raw.Points) > MaxPoints {
			return Block{}, false
		}
		points := make([]Point, len(raw.Points))
		for j, pt := range raw.Points {
			x := strings.TrimSpace(pt.X)
			if pt.Y == nil || x == "" || utf8.RuneCountInString(x) > 40 || strings.ContainsAny(x, "\r\n") || math.IsNaN(*pt.Y) || math.IsInf(*pt.Y, 0) {
				return Block{}, false
			}
			points[j] = Point{X: x, Y: *pt.Y}
		}
		series[i] = Series{Name: name, Points: points}
	}
	return Block{
		Type: "chart", Kind: w.Kind, Title: title, XLabel: xLabel, YLabel: yLabel,
		Series: series, When: cleanWhen(w.When),
	}, true
}

func cleanControl(id, label string, options []string) (Block, bool) {
	label = strings.TrimSpace(label)
	if !controlID(id) || label == "" || utf8.RuneCountInString(label) > 80 || strings.ContainsAny(label, "\r\n") {
		return Block{}, false
	}
	if len(options) < 2 || len(options) > MaxOptions {
		return Block{}, false
	}
	clean := make([]string, len(options))
	seen := map[string]bool{}
	for i, option := range options {
		option = strings.TrimSpace(option)
		if option == "" || seen[option] || utf8.RuneCountInString(option) > 40 || strings.ContainsAny(option, "\r\n") {
			return Block{}, false
		}
		seen[option] = true
		clean[i] = option
	}
	return Block{Type: "control", ID: id, Label: label, Options: clean}, true
}

func controlID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for i, r := range id {
		if i == 0 {
			if r < 'a' || r > 'z' {
				return false
			}
			continue
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

func cleanWhen(when map[string]string) map[string]string {
	if len(when) == 0 {
		return nil
	}
	return when
}

func checkWhen(when map[string]string, controls map[string][]string) error {
	for id, option := range when {
		choices, ok := controls[id]
		if !ok {
			return ErrInvalid
		}
		found := false
		for _, choice := range choices {
			if choice == option {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalid
		}
	}
	return nil
}

func strict(raw json.RawMessage, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}
