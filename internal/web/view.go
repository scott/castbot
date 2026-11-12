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
	Lead, LastWake, FirstRunning                     string
	ShowFirst                                        bool
	CanStart, CanPause, CanWake, CanRetry, CanDelete bool
}

type agentView struct {
	ID, Name, Title, Letter, Color, Dot, Sub, SubClass string
	Model, Status, ChipClass, ChipLabel, ChipMeta      string
	SizeLabel, IdleLabel, Created                      string
	Approvals, Unread                                  int
	On, Avatar                                         bool
}

type sectionView struct {
	ID, Name, Kind string
	Agents         []agentView
}

type chipView struct {
	Name string
}

type fileLinkView struct {
	Name, Meta, Open, Download string
}

type msgView struct {
	ID, Author, Body, Text, Color, Letter, Stamp, Day, At string
	Origin                                                string
	ShowDay                                               bool
	HasCard                                               bool
	Card                                                  cardView
	Prompt, Marker, Hide                                  bool
	Href, Pill, PillClass, Sub                            string
	Chips                                                 []chipView
	PathLines                                             []string
	File                                                  *fileLinkView
}

type threadView struct {
	Title, Href string
	On          bool
}

type cliTokView struct {
	ID, Prefix, Created, LastUsed, Status string
	Revoked, Unseen                       bool
}

type liveView struct {
	RunID, Color, Letter, Text, Error, CSRF, Status string
	Failed                                          bool
	Cards                                           []cardView
}

type cardView struct {
	ID, RunID, Tool, Line, CSRF, When, At string
	Extra, From                           string
	Args                                  []argView
	Pending, ReadOnly, Approved           bool
}

type chromeView struct {
	AgentID        string
	RunID          string
	AgentApprovals int
	Total          int
	Unread         int
	Tail           string
}

type argView struct {
	Key, Value, Short string
	Hidden, Long      bool
}

type sessView struct {
	CurrentLine string
	OtherLine   string
	OtherCount  int
	Confirm     string
}

type page struct {
	Title            string
	Theme            string
	CSRF             string
	BodyClass        string
	Page             string
	Nav              string
	ShowNav          bool
	Pane             bool
	Empty            bool
	User             store.User
	HasUser          bool
	Initials         string
	Sections         []sectionView
	Approvals        int
	OtherApprovals   int
	AgentCount       int
	Usage            string
	Glance           []glanceRow
	ModelTitle       string
	ModelSub         string
	ApprovalsWaiting []cardView
	ApprovalsEarlier []earlierRow
	Toasts           []toastView
	Agent            agentView
	HasAgent         bool
	Messages         []msgView
	Lives            []liveView
	Tab              string
	Notice           string
	EditName         bool
	Sessions         sessView
	Email            string
	Remember         bool
	Code             string
	Cells            []cellView
	OTPBad           bool
	Banner           bool
	BannerLead       string
	BannerRest       string
	BannerIcon       string
	Hint             string
	Locked           bool
	LockRFC          string
	LockLabel        string
	StubTitle        string
	StubBody         string
	FormError        string
	AgentForm        agentForm
	Machine          machineView
	Replace          replaceView
	StartMachine     bool
	CLIBlob          string
	CLINote          string
	CLIName          string
	CLITokens        []cliTokView
	Threads          []threadView
	ThreadID         string
	Cards            []cardView
	Text             string
	RoleLine         string
	PasswordOn       string
	Routines         routinePanel
	Run              runBar
	PendingTool      string
	Vault            vaultView
	Panel            string
	LowSpace         bool
	Files            filesPanel
	SearchQuery      string
	SearchAgents     []agentView
	FileHits         []fileHitView
}

func finish(p page) page {
	switch p.Page {
	case "home":
		p.BodyClass = "p-list no-bnav"
		p.ShowNav = false
		p.Nav = ""
	case "first":
		p.BodyClass = "p-main"
		p.ShowNav = true
		p.Nav = "agents"
	case "chat":
		p.BodyClass = "p-chat"
		if (p.Pane || p.Panel != "" || p.Vault.Panel) && !p.Run.On {
			p.BodyClass += " p-slide"
		}
		if p.Panel == "files" || p.Files.View != "" {
			p.BodyClass += " p-files"
			if p.Files.Detail != nil {
				p.BodyClass += " p-file"
			}
		}
		if p.Vault.Panel {
			p.BodyClass += " vault-open"
		}
		if p.Run.On {
			p.BodyClass += " p-run"
		}
		if p.Vault.Drawer {
			p.BodyClass += " p-drawer"
		}
		if p.Vault.ModalOpen {
			p.BodyClass += " p-modal"
		}
		if p.Replace.Confirm {
			p.BodyClass += " p-confirm"
		}
		p.Tab = "chat"
	case "secrets":
		p.BodyClass = "p-main no-bnav"
		if p.Vault.Drawer {
			p.BodyClass += " p-drawer"
		}
		if p.Vault.ModalOpen {
			p.BodyClass += " p-modal"
		}
	case "machine", "agent-routines", "agent-files":
		p.BodyClass = "p-chat"
		if p.Replace.Confirm {
			p.BodyClass += " p-confirm"
		}
	case "settings", "password", "stub", "new", "edit":
		p.BodyClass = "p-main no-bnav"
	case "approvals", "routines", "files", "usage":
		p.BodyClass = "p-main"
		p.ShowNav = true
		p.Nav = p.Page
	case "auth":
		p.BodyClass = "p-auth"
	}
	if p.Routines.On {
		if !strings.Contains(p.BodyClass, "p-chat") {
			p.BodyClass = "p-chat " + p.BodyClass
		}
		if !strings.Contains(p.BodyClass, "panel-routines") {
			p.BodyClass += " panel-routines"
		}
		if p.Run.On {
			if !strings.Contains(p.BodyClass, "p-run") {
				p.BodyClass += " p-run"
			}
		} else if !strings.Contains(p.BodyClass, "p-slide") {
			p.BodyClass += " p-slide"
		}
	}
	if p.Theme == "" {
		p.Theme = "system"
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
		Avatar:    a.AvatarPath != "",
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
			label = "Starting"
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
			label = "Starting"
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

func clockStamp(stamp string, loc *time.Location) string {
	t, ok := inZone(stamp, loc)
	if !ok {
		return ""
	}
	return t.Format("Jan 2, 3:04 PM")
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
