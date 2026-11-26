// Package page checks the document an agent saves as a hosted page.
// The control plane renders the result. The document is not HTML.
package page

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
// A rejected document wraps this error, and Error returns the reason.
var ErrInvalid = errors.New("invalid page")

// reject is one document problem. It unwraps to ErrInvalid.
type reject struct{ msg string }

func (e *reject) Error() string { return e.msg }

func (e *reject) Unwrap() error { return ErrInvalid }

func invalidf(format string, args ...any) error {
	return &reject{msg: fmt.Sprintf(format, args...)}
}

// Example is one document that uses every block type.
func Example() string {
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

// Shape is the document contract: the fields, the limits, and one valid example.
func Shape() string {
	return `The document is one JSON object. v is 1. blocks has 1 to 40 objects. No other keys are allowed.

markdown has text, up to 8 KiB. callout has tone (note or warn) and text, up to 500 characters. stats has items, 1 to 6, each with label and value (up to 40 characters) and an optional note (up to 80). table has an optional caption, columns (1 to 8 names), and rows (1 to 100). Each row has cells, one string per column, and no other keys. chart has kind (bar or line), an optional title, x, and y, and series (1 to 4). Each series has name and points (1 to 60). Each point has x and a number y. control has id, label, and options (2 to 8 different strings). id starts with a lowercase letter and then uses lowercase letters, digits, or underscores. A page has at most 6 controls, and each id is unique.

when, on a block or a table row, maps a control id to one of that control's options. Omit when to always show it. The control is on the same page.

Pass this object as document. A JSON string of the same object is also accepted. Keep it under 32 KiB.

Example:
` + "```json\n" + Example() + "\n```"
}

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
	if raw == "" {
		return Document{}, invalidf("The document is empty.")
	}
	if len(raw) > MaxDocument {
		return Document{}, invalidf("The document is over 32 KiB.")
	}
	if !utf8.ValidString(raw) {
		return Document{}, invalidf("The document is not valid text.")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var wire struct {
		V      int               `json:"v"`
		Blocks []json.RawMessage `json:"blocks"`
	}
	if err := dec.Decode(&wire); err != nil {
		return Document{}, rejectJSON(err, false)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Document{}, invalidf("The document is not one JSON object.")
	}
	if wire.V != Version {
		return Document{}, invalidf("Set v to 1.")
	}
	if len(wire.Blocks) == 0 {
		return Document{}, invalidf("Add at least one block.")
	}
	if len(wire.Blocks) > MaxBlocks {
		return Document{}, invalidf("Use at most 40 blocks.")
	}
	doc := Document{Blocks: make([]Block, 0, len(wire.Blocks))}
	controls := map[string][]string{}
	for i, rawBlock := range wire.Blocks {
		block, err := decodeBlock(rawBlock)
		if err != nil {
			return Document{}, invalidf("Block %d: %s", i+1, err.Error())
		}
		if block.Type == "control" {
			if _, ok := controls[block.ID]; ok {
				return Document{}, invalidf("Block %d: each control id is used once.", i+1)
			}
			controls[block.ID] = block.Options
		}
		doc.Blocks = append(doc.Blocks, block)
	}
	if len(controls) > MaxControls {
		return Document{}, invalidf("Use at most 6 controls.")
	}
	for i := range doc.Blocks {
		if err := checkWhen(doc.Blocks[i].When, controls); err != nil {
			return Document{}, invalidf("Block %d: %s", i+1, err.Error())
		}
		if doc.Blocks[i].Type != "table" {
			continue
		}
		for _, row := range doc.Blocks[i].Rows {
			if err := checkWhen(row.When, controls); err != nil {
				return Document{}, invalidf("Block %d: %s", i+1, err.Error())
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
		return Block{}, invalidf("This block is not valid JSON.")
	}
	switch kind.Type {
	case "markdown":
		var w struct {
			Type string            `json:"type"`
			Text string            `json:"text"`
			When map[string]string `json:"when"`
		}
		if err := strict(raw, &w); err != nil {
			return Block{}, err
		}
		if strings.TrimSpace(w.Text) == "" {
			return Block{}, invalidf("A markdown block needs text.")
		}
		if len(w.Text) > MaxMarkdown {
			return Block{}, invalidf("A markdown block is over 8 KiB.")
		}
		return Block{Type: "markdown", Text: w.Text, When: cleanWhen(w.When)}, nil
	case "callout":
		var w struct {
			Type string            `json:"type"`
			Tone string            `json:"tone"`
			Text string            `json:"text"`
			When map[string]string `json:"when"`
		}
		if err := strict(raw, &w); err != nil {
			return Block{}, err
		}
		if w.Tone != "note" && w.Tone != "warn" {
			return Block{}, invalidf("A callout tone is note or warn.")
		}
		if strings.TrimSpace(w.Text) == "" || utf8.RuneCountInString(w.Text) > MaxCallout {
			return Block{}, invalidf("A callout needs text, at most 500 characters.")
		}
		return Block{Type: "callout", Tone: w.Tone, Text: w.Text, When: cleanWhen(w.When)}, nil
	case "stats":
		var w struct {
			Type  string            `json:"type"`
			Items []statWire        `json:"items"`
			When  map[string]string `json:"when"`
		}
		if err := strict(raw, &w); err != nil {
			return Block{}, err
		}
		if len(w.Items) == 0 || len(w.Items) > MaxStats {
			return Block{}, invalidf("A stats block needs 1 to 6 items.")
		}
		items := make([]Stat, 0, len(w.Items))
		for _, item := range w.Items {
			stat, ok := item.clean()
			if !ok {
				return Block{}, invalidf("Each stat needs a label and a value, up to 40 characters, and an optional note up to 80. No line breaks.")
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
			return Block{}, err
		}
		return tableFrom(w.Caption, w.Columns, w.Rows, w.When)
	case "chart":
		var w chartWire
		if err := strict(raw, &w); err != nil {
			return Block{}, err
		}
		return w.clean()
	case "control":
		var w struct {
			Type    string   `json:"type"`
			ID      string   `json:"id"`
			Label   string   `json:"label"`
			Options []string `json:"options"`
		}
		if err := strict(raw, &w); err != nil {
			return Block{}, err
		}
		return cleanControl(w.ID, w.Label, w.Options)
	default:
		return Block{}, invalidf("A block type is markdown, callout, stats, table, chart, or control.")
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

func tableFrom(caption string, columns []string, rows []rowWire, when map[string]string) (Block, error) {
	caption = strings.TrimSpace(caption)
	if utf8.RuneCountInString(caption) > 120 || strings.ContainsAny(caption, "\r\n") {
		return Block{}, invalidf("A table caption is at most 120 characters, on one line.")
	}
	if len(columns) == 0 || len(columns) > MaxColumns {
		return Block{}, invalidf("A table needs 1 to 8 columns.")
	}
	if len(rows) == 0 || len(rows) > MaxRows {
		return Block{}, invalidf("A table needs 1 to 100 rows.")
	}
	cleanCols := make([]string, len(columns))
	for i, col := range columns {
		col = strings.TrimSpace(col)
		if col == "" || utf8.RuneCountInString(col) > 40 || strings.ContainsAny(col, "\r\n") {
			return Block{}, invalidf("Each column name is 1 to 40 characters, on one line.")
		}
		cleanCols[i] = col
	}
	cleanRows := make([]Row, len(rows))
	for i, row := range rows {
		if len(row.Cells) != len(cleanCols) {
			word := "cells"
			if len(cleanCols) == 1 {
				word = "cell"
			}
			return Block{}, invalidf("Each table row needs %d %s, one per column.", len(cleanCols), word)
		}
		cells := make([]string, len(row.Cells))
		for j, cell := range row.Cells {
			if utf8.RuneCountInString(cell) > 200 || strings.ContainsAny(cell, "\r\n") {
				return Block{}, invalidf("A table cell is at most 200 characters, on one line.")
			}
			cells[j] = cell
		}
		cleanRows[i] = Row{Cells: cells, When: cleanWhen(row.When)}
	}
	return Block{Type: "table", Caption: caption, Columns: cleanCols, Rows: cleanRows, When: cleanWhen(when)}, nil
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

func (w chartWire) clean() (Block, error) {
	if w.Kind != "bar" && w.Kind != "line" {
		return Block{}, invalidf("A chart kind is bar or line.")
	}
	title := strings.TrimSpace(w.Title)
	xLabel := strings.TrimSpace(w.X)
	yLabel := strings.TrimSpace(w.Y)
	if utf8.RuneCountInString(title) > 80 || utf8.RuneCountInString(xLabel) > 40 || utf8.RuneCountInString(yLabel) > 40 || strings.ContainsAny(title, "\r\n") || strings.ContainsAny(xLabel, "\r\n") || strings.ContainsAny(yLabel, "\r\n") {
		return Block{}, invalidf("A chart title is at most 80 characters, and x and y are at most 40, on one line.")
	}
	if len(w.Series) == 0 || len(w.Series) > MaxSeries {
		return Block{}, invalidf("A chart needs 1 to 4 series.")
	}
	series := make([]Series, len(w.Series))
	names := map[string]bool{}
	for i, raw := range w.Series {
		name := strings.TrimSpace(raw.Name)
		if name == "" || names[name] || utf8.RuneCountInString(name) > 40 || strings.ContainsAny(name, "\r\n") {
			return Block{}, invalidf("Each series name is 1 to 40 characters, on one line, and used once.")
		}
		names[name] = true
		if len(raw.Points) == 0 || len(raw.Points) > MaxPoints {
			return Block{}, invalidf("Each series needs 1 to 60 points.")
		}
		points := make([]Point, len(raw.Points))
		for j, pt := range raw.Points {
			x := strings.TrimSpace(pt.X)
			if pt.Y == nil || x == "" || utf8.RuneCountInString(x) > 40 || strings.ContainsAny(x, "\r\n") || math.IsNaN(*pt.Y) || math.IsInf(*pt.Y, 0) {
				return Block{}, invalidf("Each chart point needs x and a finite number y.")
			}
			points[j] = Point{X: x, Y: *pt.Y}
		}
		series[i] = Series{Name: name, Points: points}
	}
	return Block{
		Type: "chart", Kind: w.Kind, Title: title, XLabel: xLabel, YLabel: yLabel,
		Series: series, When: cleanWhen(w.When),
	}, nil
}

func cleanControl(id, label string, options []string) (Block, error) {
	label = strings.TrimSpace(label)
	if !controlID(id) {
		return Block{}, invalidf("A control id starts with a lowercase letter and uses only lowercase letters, digits, and underscores.")
	}
	if label == "" || utf8.RuneCountInString(label) > 80 || strings.ContainsAny(label, "\r\n") {
		return Block{}, invalidf("A control needs a label, at most 80 characters, on one line.")
	}
	if len(options) < 2 || len(options) > MaxOptions {
		return Block{}, invalidf("A control needs 2 to 8 different options.")
	}
	clean := make([]string, len(options))
	seen := map[string]bool{}
	for i, option := range options {
		option = strings.TrimSpace(option)
		if option == "" || seen[option] || utf8.RuneCountInString(option) > 40 || strings.ContainsAny(option, "\r\n") {
			return Block{}, invalidf("Each control option is 1 to 40 characters, on one line, and used once.")
		}
		seen[option] = true
		clean[i] = option
	}
	return Block{Type: "control", ID: id, Label: label, Options: clean}, nil
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
			return invalidf("when can only name a control on this page.")
		}
		found := false
		for _, choice := range choices {
			if choice == option {
				found = true
				break
			}
		}
		if !found {
			return invalidf("when must use one of that control's options.")
		}
	}
	return nil
}

func strict(raw json.RawMessage, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return rejectJSON(err, true)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return invalidf("This block is not one JSON object.")
	}
	return nil
}

func rejectJSON(err error, block bool) error {
	if err != nil {
		const prefix = `json: unknown field `
		msg := err.Error()
		if strings.HasPrefix(msg, prefix) {
			name := strings.Trim(strings.TrimPrefix(msg, prefix), `"`)
			if name != "" {
				return invalidf("Remove the unknown field %s.", name)
			}
		}
	}
	if block {
		return invalidf("This block is not valid JSON.")
	}
	return invalidf("The document is not one JSON object.")
}
