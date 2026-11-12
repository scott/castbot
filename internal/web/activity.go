package web

import (
	"html"
	"html/template"
	"strings"
	"time"
)

const (
	actShowDelay = 300 * time.Millisecond
	actMinDwell  = 600 * time.Millisecond
	actElapsedAt = 5 * time.Second
)

// activityIn is the run state the label is chosen from.
// ToolKind is set only when a tool event is open. Without tool events it stays empty.
type activityIn struct {
	Status          string
	HasText         bool
	PendingApproval bool
	Machine         string
	ToolKind        string
	Reconnecting    bool
}

// activityOut is the indicator for one moment.
type activityOut struct {
	Label      string
	Mode       string
	Hint       string
	HideThread bool
}

// decideActivity maps run state to a label. Unknown states are Working.
// It never returns Writing or Replying.
func decideActivity(in activityIn) activityOut {
	if in.Reconnecting {
		return activityOut{Label: "Reconnecting", Mode: "slow"}
	}
	if in.PendingApproval || in.Status == "waiting_approval" {
		return activityOut{Label: "Waiting for approval", Mode: "held"}
	}
	if kind := strings.ToLower(strings.TrimSpace(in.ToolKind)); kind != "" {
		switch kind {
		case "read", "list", "grep", "glob", "view":
			return activityOut{Label: "Reading files"}
		case "bash", "shell", "exec", "git", "python":
			return activityOut{Label: "Running command"}
		default:
			return activityOut{Label: "Working"}
		}
	}
	if !in.HasText && (in.Machine == "paused" || in.Machine == "starting") {
		hint := "its machine is starting"
		if in.Machine == "paused" {
			hint = "its machine was paused"
		}
		return activityOut{Label: "Waking up", Hint: hint}
	}
	if !in.HasText {
		return activityOut{Label: "Thinking"}
	}
	return activityOut{Label: "Working", HideThread: true}
}

// formatElapsed returns a clock like 14s or 1m 05s once the wait is at least 5s.
func formatElapsed(d time.Duration) (string, bool) {
	if d < actElapsedAt {
		return "", false
	}
	sec := int(d.Seconds())
	if sec < 60 {
		return itoa(sec) + "s", true
	}
	m := sec / 60
	s := sec % 60
	if s < 10 {
		return itoa(m) + "m 0" + itoa(s) + "s", true
	}
	return itoa(m) + "m " + itoa(s) + "s", true
}

// labelDwell keeps a label on screen for at least actMinDwell.
type labelDwell struct {
	label string
	at    time.Time
}

func (d *labelDwell) next(label string, now time.Time) string {
	if label == "" {
		label = "Working"
	}
	if d.label == "" || d.label == label {
		if d.label == "" {
			d.at = now
		}
		d.label = label
		return label
	}
	if now.Sub(d.at) < actMinDwell {
		return d.label
	}
	d.label = label
	d.at = now
	return label
}

func actIcons(mode string, small bool) string {
	cls := "actico"
	if mode == "held" || mode == "slow" {
		cls += " " + mode
	}
	if small {
		cls += " sm"
	}
	return `<span class="` + cls + `" aria-hidden="true"><i></i><i></i><i></i><i></i><i></i><i></i></span>`
}

func actRowHTML(out activityOut, elapsed string) template.HTML {
	var b strings.Builder
	b.WriteString(`<div class="act" role="status" aria-live="polite">`)
	b.WriteString(actIcons(out.Mode, false))
	b.WriteString(`<span class="lbl">`)
	b.WriteString(html.EscapeString(out.Label))
	b.WriteString(`</span>`)
	if elapsed != "" {
		b.WriteString(`<span class="el" aria-hidden="true">`)
		b.WriteString(html.EscapeString(elapsed))
		b.WriteString(`</span>`)
	}
	if out.Hint != "" {
		b.WriteString(`<span class="hint">`)
		b.WriteString(html.EscapeString("· " + out.Hint))
		b.WriteString(`</span>`)
	}
	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func actSlotHTML(runID string, inner template.HTML, hidden bool) string {
	var b strings.Builder
	b.WriteString(`<div class="act-hold" id="tail-`)
	b.WriteString(html.EscapeString(runID))
	b.WriteString(`" sse-swap="act,fail" hx-swap="outerHTML"`)
	if hidden {
		b.WriteString(` hidden`)
	}
	b.WriteString(`>`)
	b.WriteString(string(inner))
	b.WriteString(`</div>`)
	return b.String()
}

func sidebarActHTML(out activityOut) template.HTML {
	return template.HTML(actIcons(out.Mode, true) + `<span>` + html.EscapeString(out.Label) + `</span>`)
}

func tailWrap(runID, inner string) string {
	return `<div class="md-tail" id="md-tail-` + html.EscapeString(runID) + `" sse-swap="tail" hx-swap="outerHTML">` + inner + `</div>`
}

type calmCopy struct {
	Title, Line, Retry, Second, Href string
	Wake                             bool
}

func calmFor(agentName, errText string, hasText bool) calmCopy {
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = "The agent"
	}
	low := strings.ToLower(errText)
	if strings.Contains(low, "did not wake") {
		return calmCopy{
			Title:  name + "'s machine did not wake up.",
			Line:   "Your message is saved. Try again, or open the Machine panel to see what happened.",
			Retry:  "Try again",
			Second: "Machine",
			Wake:   true,
		}
	}
	line := strings.TrimSpace(errText)
	if line == "" {
		line = "Something went wrong."
	}
	if hasText {
		return calmCopy{
			Title:  "Reply stopped before it finished.",
			Line:   line + " What " + name + " wrote so far is kept above.",
			Retry:  "Retry",
			Second: "Details",
		}
	}
	return calmCopy{
		Title:  name + " could not reply.",
		Line:   line,
		Retry:  "Retry",
		Second: "Details",
	}
}
