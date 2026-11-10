package web

import (
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/scott/castbot/internal/store"
)

type cellView struct {
	Digit string
	Class string
}

type machineView struct {
	Lead, LastWake                                   string
	CanStart, CanPause, CanWake, CanRetry, CanDelete bool
}

type agentView struct {
	ID, Name, Title, Letter, Color, Dot, Sub, SubClass string
	Model, Status, ChipClass, ChipLabel, ChipMeta      string
	SizeLabel, IdleLabel, Created                      string
	Approvals, Unread                                  int
	On                                                 bool
}

type sectionView struct {
	ID, Name, Kind string
	Agents         []agentView
}

type msgView struct {
	ID, Author, Body, Color, Letter, Stamp, Day string
	ShowDay                                     bool
}

type liveView struct {
	RunID, Color, Letter, Text, Error, CSRF string
	Failed                                  bool
}

type sessView struct {
	CurrentLine string
	OtherLine   string
	OtherCount  int
	Confirm     string
}

type page struct {
	Title      string
	Theme      string
	CSRF       string
	BodyClass  string
	Page       string
	Nav        string
	ShowNav    bool
	Pane       bool
	Empty      bool
	User       store.User
	HasUser    bool
	Initials   string
	Sections   []sectionView
	Approvals  int
	Usage      string
	Agent      agentView
	HasAgent   bool
	Messages   []msgView
	Lives      []liveView
	Tab        string
	Notice     string
	EditName   bool
	Sessions   sessView
	Email      string
	Remember   bool
	Code       string
	Cells      []cellView
	OTPBad     bool
	Banner     bool
	BannerLead string
	BannerRest string
	BannerIcon string
	Hint       string
	Locked     bool
	LockRFC    string
	LockLabel  string
	StubTitle  string
	StubBody   string
	FormError  string
	Machine    machineView
	Text       string
	RoleLine   string
	PasswordOn string
}

func finish(p page) page {
	switch p.Page {
	case "home":
		p.BodyClass = "p-list"
		p.ShowNav = true
		p.Nav = "agents"
	case "first":
		p.BodyClass = "p-main"
		p.ShowNav = true
		p.Nav = "agents"
	case "chat":
		if p.Pane {
			p.BodyClass = "p-pane"
		} else {
			p.BodyClass = "p-chat"
		}
		p.Tab = "chat"
	case "machine", "agent-routines", "agent-files":
		p.BodyClass = "p-chat"
	case "settings", "password", "stub", "new":
		p.BodyClass = "p-main no-bnav"
	case "approvals", "routines", "files", "usage":
		p.BodyClass = "p-main"
		p.ShowNav = true
		p.Nav = p.Page
	case "auth":
		p.BodyClass = "p-auth"
	}
	if p.Theme == "" {
		p.Theme = "system"
	}
	if p.Usage == "" {
		p.Usage = "$0.00"
	}
	return p
}

func initials(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	if len(parts) == 1 {
		r := []rune(parts[0])
		if len(r) == 0 {
			return "?"
		}
		return strings.ToUpper(string(r[0]))
	}
	a := []rune(parts[0])
	b := []rune(parts[len(parts)-1])
	return strings.ToUpper(string(a[0]) + string(b[0]))
}

func letter(name string) string {
	r := []rune(strings.TrimSpace(name))
	if len(r) == 0 {
		return "?"
	}
	return strings.ToUpper(string(r[0]))
}

func presentAgent(a store.Agent, on bool, loc *time.Location) agentView {
	v := agentView{
		ID: a.ID, Name: a.Name, Title: a.Title, Letter: letter(a.Name), Color: a.Color,
		Model: a.Model, Status: a.Status, Approvals: a.Approvals, Unread: a.Unread, On: on,
		SizeLabel: sizeLabel(a.Size), IdleLabel: idleLabel(a.IdleTimeout),
		Created: monthDay(a.CreatedAt, loc),
	}
	if v.Color == "" {
		v.Color = "c0"
	}
	v.Sub, v.SubClass = agentSub(a)
	v.Dot, v.ChipClass, v.ChipLabel, v.ChipMeta = statusBits(a)
	return v
}

func agentSub(a store.Agent) (string, string) {
	switch a.Status {
	case "paused":
		return joinDot(a.Title, "Paused"), ""
	case "failed":
		return joinDot(a.Title, "Machine failed"), "err"
	case "provisioning":
		label := a.Activity
		if label == "" {
			label = "Resuming"
		}
		return joinDot(a.Title, label), "start"
	default:
		if a.Activity != "" {
			return joinDot(a.Title, a.Activity), ""
		}
		return a.Title, ""
	}
}

func joinDot(title, state string) string {
	if title == "" {
		return state
	}
	return title + " · " + state
}

func statusBits(a store.Agent) (dot, class, label, meta string) {
	meta = "mkc1 · " + sizeLabel(a.Size)
	switch a.Status {
	case "ready":
		return "run", "run", "Running", meta
	case "provisioning":
		label := a.Activity
		if label == "" {
			label = "Waking"
		}
		return "start", "start", label, "mkc1"
	case "paused":
		return "pause", "pause", "Paused", meta
	case "failed":
		return "err", "err", "Failed", "mkc1"
	default:
		return "pause", "pause", "Draft", "no machine yet"
	}
}

func sizeLabel(size string) string {
	switch size {
	case "1:2048":
		return "1 vCPU / 2 GiB"
	case "2:4096":
		return "2 vCPU / 4 GiB"
	case "8:16384":
		return "8 vCPU / 16 GiB"
	default:
		if size == "" {
			return "1 vCPU / 2 GiB"
		}
		return size
	}
}

func idleLabel(sec int) string {
	if sec <= 0 {
		sec = 900
	}
	if sec%3600 == 0 {
		h := sec / 3600
		if h == 1 {
			return "After 1 hour idle"
		}
		return fmt.Sprintf("After %d hours idle", h)
	}
	return fmt.Sprintf("After %d min idle", sec/60)
}

func userLocation(name string) *time.Location {
	if name == "" {
		name = "America/Denver"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc, err = time.LoadLocation("America/Denver")
		if err != nil {
			return time.UTC
		}
	}
	return loc
}

func inZone(stamp string, loc *time.Location) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}, false
	}
	if loc == nil {
		loc = userLocation("")
	}
	return t.In(loc), true
}

func monthDay(stamp string, loc *time.Location) string {
	t, ok := inZone(stamp, loc)
	if !ok {
		return ""
	}
	return t.Format("Jan 2")
}

func clockLabel(stamp string, loc *time.Location) string {
	t, ok := inZone(stamp, loc)
	if !ok {
		return ""
	}
	return t.Format("3:04 PM")
}

func dayLabel(stamp string, now time.Time, loc *time.Location) string {
	t, ok := inZone(stamp, loc)
	if !ok {
		return ""
	}
	if loc == nil {
		loc = userLocation("")
	}
	if t.Format("2006-01-02") == now.In(loc).Format("2006-01-02") {
		return "Today"
	}
	return t.Format("Jan 2")
}

func otpCells(code string) []cellView {
	out := make([]cellView, 6)
	for i := 0; i < 6; i++ {
		if i < len(code) {
			out[i] = cellView{Digit: string(code[i]), Class: "filled"}
		} else if i == len(code) {
			out[i] = cellView{Class: "now"}
		}
	}
	return out
}

func lockLabel(until, now time.Time) string {
	d := until.Sub(now)
	if d < 0 {
		d = 0
	}
	sec := int((d + time.Second - 1) / time.Second)
	return fmt.Sprintf("%d:%02d", sec/60, sec%60)
}

func triesHint(n int) string {
	if n == 1 {
		return "1 try left, then sign-in pauses for 15 minutes."
	}
	return fmt.Sprintf("%d tries left, then sign-in pauses for 15 minutes.", n)
}

func deviceLabel(ua string) string {
	browser := "Browser"
	switch {
	case strings.Contains(ua, "Edg/"):
		browser = "Edge"
	case strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	kind := "a laptop"
	switch {
	case strings.Contains(ua, "iPad"):
		kind = "a tablet"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "Mobile"):
		kind = "a phone"
	}
	return browser + " on " + kind
}

func roleLine(role, workspace string) string {
	if role == "member" {
		return "Member of " + workspace + " workspace"
	}
	return "Owner of " + workspace + " workspace"
}
