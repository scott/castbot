package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/scott/castbot/internal/cron"
	"github.com/scott/castbot/internal/store"
)

const routineHistory = 30

type routinePanel struct {
	On        bool
	Wide      bool
	Mode      string
	Count     int
	Rows      []routineListRow
	Editor    routineEditor
	Detail    routineDetail
	Delete    routineDelete
	Fine      string
	EmptyLead string
	EmptyBody string
	Ideas     []routineIdea
}

type routineListRow struct {
	Name, Schedule, Next, NextClass, Pill, PillClass, Href, Toggle string
	On, Off                                                        bool
}

type routineIdea struct {
	Name, Sub, Href string
}

type routineEditor struct {
	ID                                                             string
	Title, Back, Name, Cron, Prompt, Zone, Help, Save, Action, Err string
	PreviewURL                                                     string
	Presets                                                        []presetChoice
	Zones                                                          []cron.Zone
	MissedOnce, MissedSkip, ApprovalWait, ApprovalDeny             bool
	Preview                                                        routinePreview
	SaveOff                                                        bool
}

type presetChoice struct {
	ID, Label, Cron string
	On              bool
}

type routinePreview struct {
	OK             bool
	Say, Next, Err string
}

type routineDetail struct {
	Name, When, Next, Missed, Approval, Runs, Prompt string
	On                                               bool
	RunNowOff                                        bool
	RunNowWhy, RunHref, EditHref, DeleteHref, Toggle string
	History                                          []historyRow
	SeeAll                                           bool
	AllHref                                          string
	Note, NoteClass                                  string
	Siblings                                         []siblingView
}

type historyRow struct {
	Pill, Class, When, Detail, Href string
	Current                         bool
}

type siblingView struct {
	Heading, Note string
	History       []historyRow
}

type routineDelete struct {
	On                        bool
	Name, Agent, Action, Back string
}

type runBar struct {
	On                            bool
	Back, Name, When, Pill, Class string
	Wake, Done                    string
	Fail                          bool
	FailLead, FailRest            string
	RunNow, Machine               string
}

var routinePresets = []presetChoice{
	{ID: "hourly", Label: "Hourly", Cron: "0 * * * *"},
	{ID: "daily", Label: "Daily", Cron: "0 8 * * *"},
	{ID: "weekdays", Label: "Weekdays", Cron: "0 8 * * 1-5"},
	{ID: "weekly", Label: "Weekly", Cron: "0 13 * * 3"},
	{ID: "monthly", Label: "Monthly", Cron: "0 9 1 * *"},
	{ID: "custom", Label: "Custom", Cron: ""},
}

func (s *Server) attachRoutines(r *http.Request, user store.User, agent store.Agent, current store.Thread, p *page) {
	if p.PendingTool == "" {
		p.PendingTool = pendingTool(p.Messages, p.Lives)
	}
	loc := userLocation(user.Timezone)
	now := s.store.Now()
	for i := range p.Messages {
		if p.Messages[i].Origin == "routine" && p.Messages[i].Author == "user" {
			p.Messages[i].Prompt = true
		}
	}
	panel := r.URL.Query().Get("panel") == "routines"
	var linked store.RoutineRun
	if current.Kind == "routine" && current.ID != "" {
		if row, err := s.store.RoutineRunByThread(r.Context(), user.ID, current.ID); err == nil {
			linked = row
			panel = true
		}
	}
	if current.Kind != "routine" && current.ID != "" {
		p.Messages = s.mergeMarkers(r, user, agent, p.Messages, now, loc)
	}
	if !panel {
		return
	}
	p.Routines.On = true
	p.Routines.Fine = "Only " + agent.Name + "'s routines. Times are in " + cron.Friendly(user.Timezone) + " unless a routine says otherwise. Each run wakes " + agent.Name + "'s machine if it is paused, runs in its own thread in the chat, and lets the machine pause again after 15 min idle."
	p.Routines.EmptyLead = "No routines yet"
	p.Routines.EmptyBody = "Give " + agent.Name + " a job to do on its own, like a weekday morning check. castbot wakes the machine for each run, and each run gets its own thread in the chat."
	p.Routines.Ideas = routineIdeas(agent.ID)
	id := r.URL.Query().Get("routine")
	if linked.RoutineID != "" && id == "" && r.URL.Query().Get("edit") != "1" {
		id = linked.RoutineID
	}
	switch {
	case id == "new" || (id != "" && r.URL.Query().Get("edit") == "1"):
		p.Routines.Mode = "edit"
		p.Routines.Wide = true
		p.Routines.Editor = s.editorView(r, user, agent, id)
	case id != "":
		p.Routines.Mode = "detail"
		p.Routines.Detail, p.Routines.Delete = s.detailView(r, user, agent, id, linked, now)
		if p.Routines.Detail.Name == "" {
			p.Routines.Mode = "list"
			p.Routines.Rows, p.Routines.Count = s.listRows(r, user, agent, now, loc)
		}
	default:
		rows, n := s.listRows(r, user, agent, now, loc)
		p.Routines.Count = n
		if n == 0 {
			p.Routines.Mode = "empty"
		} else {
			p.Routines.Mode = "list"
			p.Routines.Rows = rows
		}
	}
	if linked.ID != "" {
		p.Run = s.runBar(r, user, agent, linked, now)
		if p.Run.Fail {
			for i := range p.Messages {
				if p.Messages[i].Prompt {
					p.Messages[i].Hide = true
				}
			}
		}
	}
}

func pendingTool(msgs []msgView, lives []liveView) string {
	for _, m := range msgs {
		if m.HasCard && m.Card.Pending && m.Card.Tool != "" {
			return m.Card.Tool
		}
	}
	for _, lv := range lives {
		for _, c := range lv.Cards {
			if c.Pending && c.Tool != "" {
				return c.Tool
			}
		}
	}
	return ""
}

func routineIdeas(agentID string) []routineIdea {
	base := "/agents/" + url.PathEscape(agentID) + "?panel=routines&routine=new&idea="
	return []routineIdea{
		{Name: "Morning docs check", Sub: "Every weekday at 8:00 AM", Href: base + "morning"},
		{Name: "Weekly link sweep", Sub: "Every Monday at 7:30 AM", Href: base + "weekly"},
		{Name: "Month-end report", Sub: "On the 1st of every month", Href: base + "month"},
	}
}

func ideaDraft(idea string) (name, cronExpr, prompt string) {
	switch idea {
	case "morning":
		return "Morning docs check", "0 8 * * 1-5", "Check docs/ for broken links and pages that disagree with docs/pricing.md. Fix what you can on a branch and tell me what you changed."
	case "weekly":
		return "Weekly link sweep", "30 7 * * 1", "Find broken external links across docs/, fix them on a branch, and push it for review."
	case "month":
		return "Month-end report", "0 9 1 * *", "Write the month-end docs report and leave it in the thread."
	default:
		return "", "0 8 * * 1-5", ""
	}
}

func (s *Server) editorView(r *http.Request, user store.User, agent store.Agent, id string) routineEditor {
	ed := routineEditor{
		Title:        "New routine",
		Back:         "/agents/" + url.PathEscape(agent.ID) + "?panel=routines",
		Save:         "Save routine",
		Action:       "/agents/" + url.PathEscape(agent.ID) + "/routines",
		PreviewURL:   "/agents/" + url.PathEscape(agent.ID) + "/routines/preview",
		Zone:         user.Timezone,
		MissedOnce:   true,
		ApprovalWait: true,
		Zones:        cron.Zones(user.Timezone),
	}
	name, cronExpr, prompt := ideaDraft(r.URL.Query().Get("idea"))
	ed.Name, ed.Cron, ed.Prompt = name, cronExpr, prompt
	if id != "" && id != "new" {
		if cur, err := s.store.GetLiveRoutine(r.Context(), user.ID, id); err == nil {
			ed.ID = cur.ID
			ed.Title = "Edit: " + cur.Name
			ed.Back = routineDetailPath(agent.ID, cur.ID)
			ed.Name = cur.Name
			ed.Cron = cur.Cron
			ed.Prompt = cur.Prompt
			ed.Zone = cur.Timezone
			ed.MissedOnce = cur.MissedPolicy != store.MissedSkip
			ed.MissedSkip = cur.MissedPolicy == store.MissedSkip
			ed.ApprovalWait = cur.ApprovalPolicy != store.ApprovalDeny
			ed.ApprovalDeny = cur.ApprovalPolicy == store.ApprovalDeny
			ed.Action = "/agents/" + url.PathEscape(agent.ID) + "/routines"
		}
	}
	if r.FormValue("draft_name") != "" || r.FormValue("draft_cron") != "" || r.FormValue("routine_error") != "" {
		ed.Name = r.FormValue("draft_name")
		ed.Cron = r.FormValue("draft_cron")
		ed.Prompt = r.FormValue("draft_prompt")
		if r.FormValue("draft_tz") != "" {
			ed.Zone = r.FormValue("draft_tz")
		}
		ed.MissedOnce = r.FormValue("draft_missed") != store.MissedSkip
		ed.MissedSkip = r.FormValue("draft_missed") == store.MissedSkip
		ed.ApprovalWait = r.FormValue("draft_approval") != store.ApprovalDeny
		ed.ApprovalDeny = r.FormValue("draft_approval") == store.ApprovalDeny
		ed.Err = r.FormValue("routine_error")
	}
	ed.Presets = append([]presetChoice(nil), routinePresets...)
	matched := false
	for i := range ed.Presets {
		if ed.Presets[i].Cron != "" && ed.Presets[i].Cron == strings.TrimSpace(ed.Cron) {
			ed.Presets[i].On = true
			matched = true
		}
	}
	if !matched {
		for i := range ed.Presets {
			if ed.Presets[i].ID == "custom" {
				ed.Presets[i].On = true
			}
		}
	}
	if ed.Zone == user.Timezone {
		ed.Help = "Your zone. 8:00 AM stays 8:00 AM when daylight saving time changes."
	}
	ed.Preview = previewSchedule(ed.Cron, ed.Zone, s.store.Now())
	ed.SaveOff = !ed.Preview.OK
	return ed
}

func previewSchedule(expr, zone string, now time.Time) routinePreview {
	sched, err := cron.Parse(expr)
	if err != nil {
		return routinePreview{Err: cron.BadSchedule, Say: cron.NoPreview}
	}
	loc, err := time.LoadLocation(strings.TrimSpace(zone))
	if err != nil {
		return routinePreview{Err: "Pick a time zone.", Say: cron.NoPreview}
	}
	if cron.TooFrequent(sched, loc) {
		return routinePreview{Err: cron.TooClose, Say: cron.NoPreview}
	}
	next := sched.Upcoming(now, loc, 3)
	var parts []string
	for _, t := range next {
		parts = append(parts, routineWhen(t, now, loc))
	}
	line := ""
	if len(parts) > 0 {
		line = "Next 3 runs: " + strings.Join(parts, " · ")
	}
	return routinePreview{OK: true, Say: cron.Describe(sched), Next: line}
}

func (s *Server) listRows(r *http.Request, user store.User, agent store.Agent, now time.Time, loc *time.Location) ([]routineListRow, int) {
	routines, err := s.store.ListRoutines(r.Context(), user.ID, agent.ID)
	if err != nil {
		return nil, 0
	}
	var rows []routineListRow
	for _, routine := range routines {
		runs, _ := s.store.ListRoutineRuns(r.Context(), user.ID, routine.ID, 5)
		rloc := loc
		if loaded, err := time.LoadLocation(routine.Timezone); err == nil {
			rloc = loaded
		}
		sched, _ := cron.Parse(routine.Cron)
		row := routineListRow{
			Name:     routine.Name,
			Schedule: cron.Describe(sched),
			Href:     routineDetailPath(agent.ID, routine.ID),
			Toggle:   "/agents/" + url.PathEscape(agent.ID) + "/routines/" + url.PathEscape(routine.ID) + "/toggle",
			On:       routine.Enabled,
			Off:      !routine.Enabled,
		}
		if !routine.Enabled {
			row.Next = "Paused · no runs until you turn it on"
			row.NextClass = "paused"
		} else if t, err := time.Parse(time.RFC3339, routine.NextRunAt); err == nil {
			row.Next = "Next: " + routineWhen(t, now, rloc)
		}
		waiting := false
		for _, run := range runs {
			if s.runWaiting(r, user.ID, run) {
				waiting = true
			}
		}
		row.Pill, row.PillClass = listPill(runs, now, rloc, waiting)
		rows = append(rows, row)
	}
	return rows, len(rows)
}

func listPill(runs []store.RoutineRun, now time.Time, loc *time.Location, waiting bool) (string, string) {
	if waiting {
		return "Needs approval now", "wait"
	}
	for _, row := range runs {
		if row.FinishedAt == "" && row.RunID != "" && (row.Status == store.RoutineRunning || row.Status == store.RoutineLate) {
			return "Running", "run"
		}
	}
	for _, row := range runs {
		if row.Status != store.RoutineDone && !(row.Status == store.RoutineLate && row.FinishedAt != "") {
			continue
		}
		stamp := row.FinishedAt
		if stamp == "" {
			stamp = row.StartedAt
		}
		t, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			continue
		}
		return donePill(t, now, loc), "ok"
	}
	return "", ""
}

func (s *Server) runWaiting(r *http.Request, userID string, row store.RoutineRun) bool {
	if row.RunID == "" {
		return false
	}
	runRow, err := s.store.GetRun(r.Context(), userID, row.RunID)
	return err == nil && runRow.Status == "waiting_approval"
}

// filterRoutineThreads opens the agent chat limited to one routine's threads.
// See all uses this instead of dumping every history row into the panel.
func (s *Server) filterRoutineThreads(r *http.Request, user store.User, agent store.Agent, routineID string, p *page) error {
	routine, err := s.store.GetRoutine(r.Context(), user.ID, routineID)
	if err != nil {
		return err
	}
	if routine.AgentID != agent.ID {
		return store.ErrNotFound
	}
	runs, err := s.store.ListRoutineRuns(r.Context(), user.ID, routine.ID, 0)
	if err != nil {
		return err
	}
	name := routine.Name
	if routine.DeletedAt != "" {
		name = "deleted routine"
	}
	var threads []threadView
	var markers []msgView
	on := false
	for _, row := range runs {
		if row.ThreadID == "" {
			continue
		}
		title := row.Detail
		if title == "" {
			title = name
		}
		href := "/agents/" + url.PathEscape(agent.ID) + "?threads=" + url.QueryEscape(routine.ID) + "&thread=" + url.QueryEscape(row.ThreadID)
		chosen := row.ThreadID == p.ThreadID
		if chosen {
			on = true
		}
		threads = append(threads, threadView{Title: title, Href: href, On: chosen})
		markers = append(markers, msgView{
			Marker: true, Body: "Routine run: " + name, Sub: title, Href: href,
			Pill: "Done", PillClass: "ok",
		})
	}
	p.Threads = threads
	if !on {
		p.Messages = markers
		p.Lives = nil
		p.ThreadID = ""
	}
	return nil
}

func (s *Server) detailView(r *http.Request, user store.User, agent store.Agent, id string, linked store.RoutineRun, now time.Time) (routineDetail, routineDelete) {
	routine, err := s.store.GetLiveRoutine(r.Context(), user.ID, id)
	if err != nil {
		return routineDetail{}, routineDelete{}
	}
	loc := userLocation(routine.Timezone)
	sched, _ := cron.Parse(routine.Cron)
	runs, _ := s.store.ListRoutineRuns(r.Context(), user.ID, routine.ID, routineHistory)
	count, _ := s.store.CountRoutineRuns(r.Context(), user.ID, routine.ID)
	d := routineDetail{
		Name:       routine.Name,
		When:       cron.Describe(sched) + " · " + cron.Friendly(routine.Timezone),
		On:         routine.Enabled,
		Prompt:     routine.Prompt,
		RunHref:    "/agents/" + url.PathEscape(agent.ID) + "/routines/" + url.PathEscape(routine.ID) + "/run",
		Toggle:     "/agents/" + url.PathEscape(agent.ID) + "/routines/" + url.PathEscape(routine.ID) + "/toggle",
		EditHref:   routineDetailPath(agent.ID, routine.ID) + "&edit=1",
		DeleteHref: routineDetailPath(agent.ID, routine.ID) + "&delete=1",
		Runs:       runsSince(runs, count, loc),
	}
	if routine.MissedPolicy == store.MissedSkip {
		d.Missed = "Skip it"
	} else {
		d.Missed = "Run once when back"
	}
	if routine.ApprovalPolicy == store.ApprovalDeny {
		d.Approval = "Deny right away"
	} else {
		d.Approval = "Wait 10 min, then deny"
	}
	if t, err := time.Parse(time.RFC3339, routine.NextRunAt); err == nil && routine.Enabled {
		d.Next = routineWhen(t, now, loc)
		d.History = append(d.History, historyRow{Pill: "Scheduled", Class: "sched", When: d.Next, Detail: "Next run"})
	}
	for _, row := range runs {
		d.History = append(d.History, s.historyItem(r, user, agent, routine, row, linked, now, loc))
	}
	if count > routineHistory {
		d.SeeAll = true
		d.AllHref = "/agents/" + url.PathEscape(agent.ID) + "?threads=" + url.QueryEscape(routine.ID)
	}
	d.Note, d.NoteClass = outageNote(runs, now, loc)
	d.Siblings = s.skipSiblings(r, user, agent, routine, runs, now)
	busy, _ := s.store.RoutineInProgress(r.Context(), user.ID, routine.ID)
	if busy {
		d.RunNowOff = true
		d.RunNowWhy = "A run of this routine is already going."
	}
	if s.replacing(r.Context(), user.ID, agent.ID) {
		d.RunNowOff = true
		d.RunNowWhy = "This machine is being replaced."
	}
	del := routineDelete{}
	if r.URL.Query().Get("delete") == "1" {
		del = routineDelete{
			On: true, Name: routine.Name, Agent: agent.Name,
			Action: "/agents/" + url.PathEscape(agent.ID) + "/routines/" + url.PathEscape(routine.ID) + "/delete",
			Back:   routineDetailPath(agent.ID, routine.ID),
		}
	}
	return d, del
}

func runsSince(shown []store.RoutineRun, count int, loc *time.Location) string {
	if count == 0 {
		return "None yet"
	}
	earliest := ""
	for _, row := range shown {
		if earliest == "" || row.ScheduledFor < earliest {
			earliest = row.ScheduledFor
		}
	}
	when := ""
	if t, err := time.Parse(time.RFC3339, earliest); err == nil {
		when = t.In(loc).Format("Jan 2")
	}
	if when == "" {
		return strconv.Itoa(count)
	}
	return strconv.Itoa(count) + " since " + when
}

func (s *Server) historyItem(r *http.Request, user store.User, agent store.Agent, routine store.Routine, row store.RoutineRun, linked store.RoutineRun, now time.Time, loc *time.Location) historyRow {
	item := historyRow{When: rowWhen(row, now, loc), Detail: row.Detail}
	if row.ThreadID != "" {
		item.Href = "/agents/" + url.PathEscape(agent.ID) + "?thread=" + url.QueryEscape(row.ThreadID)
	}
	if linked.ID != "" && linked.ID == row.ID {
		item.Current = true
	}
	if s.runWaiting(r, user.ID, row) {
		tool := "a step"
		left := "10"
		if rows, err := s.store.ListRunApprovals(r.Context(), user.ID, row.RunID); err == nil {
			for _, ap := range rows {
				if ap.Status == "pending" {
					tool = ap.Tool
					left = strconv.Itoa(minutesLeft(ap.ExpiresAt, s.store.Now()))
				}
			}
		}
		item.Pill = "Needs approval"
		item.Class = "wait"
		item.Detail = tool + " · denied in " + left + " min if not answered"
		return item
	}
	switch row.Status {
	case store.RoutineRunning:
		item.Pill, item.Class = "Running", "run"
		item.Detail = runningDetail(row)
	case store.RoutineQueued:
		item.Pill, item.Class = "Queued", "queue"
		if item.Detail == "" {
			item.Detail = "Waits for the reply in progress"
		}
	case store.RoutineHeld:
		item.Pill, item.Class = "Held", "queue"
		if item.Detail == "" {
			item.Detail = "Waits for the machine replacement"
		}
	case store.RoutineDone:
		item.Pill, item.Class = "Done", "ok"
	case store.RoutineLate:
		item.Pill, item.Class = "Ran late", "late"
	case store.RoutineMissed:
		item.Pill, item.Class = "Missed", "skip"
	case store.RoutineSkipped:
		item.Pill, item.Class = "Skipped", "skip"
	case store.RoutineExpired:
		item.Pill, item.Class = "Approval expired", "exp"
	case store.RoutineFailed:
		item.Pill, item.Class = "Failed", "err"
		if item.Detail == "" {
			item.Detail = "The machine did not wake"
		}
	default:
		item.Pill, item.Class = row.Status, "sched"
	}
	return item
}

func runningDetail(row store.RoutineRun) string {
	if row.Source == store.RoutineSourceManual {
		if row.PausedMS >= 0 {
			return "Run now by you · woke the machine"
		}
		return "Run now by you"
	}
	if row.PausedMS >= 0 {
		return "Woke the machine"
	}
	if row.Detail != "" {
		return row.Detail
	}
	return "Machine was running"
}

func (s *Server) runBar(r *http.Request, user store.User, agent store.Agent, row store.RoutineRun, now time.Time) runBar {
	routine, err := s.store.GetRoutine(r.Context(), user.ID, row.RoutineID)
	if err != nil {
		return runBar{}
	}
	loc := userLocation(routine.Timezone)
	name := routine.Name
	if routine.DeletedAt != "" {
		name = "deleted routine"
	}
	bar := runBar{
		On:      true,
		Back:    "/agents/" + url.PathEscape(agent.ID),
		Name:    name,
		Machine: "/agents/" + url.PathEscape(agent.ID) + "/machine",
		RunNow:  "/agents/" + url.PathEscape(agent.ID) + "/routines/" + url.PathEscape(routine.ID) + "/run",
	}
	when := rowWhen(row, now, loc)
	source := ""
	if row.Source == store.RoutineSourceManual {
		source = " · Run now by you"
	}
	bar.When = "Routine run · " + when + source + " · " + cron.Friendly(routine.Timezone)
	switch {
	case s.runWaiting(r, user.ID, row):
		bar.Pill, bar.Class = "Needs approval", "wait"
	case row.Status == store.RoutineRunning:
		bar.Pill, bar.Class = "Running", "run"
	case row.Status == store.RoutineDone:
		bar.Pill, bar.Class = "Done", "ok"
	case row.Status == store.RoutineLate:
		bar.Pill, bar.Class = "Ran late", "late"
	case row.Status == store.RoutineFailed:
		bar.Pill, bar.Class = "Failed", "err"
	case row.Status == store.RoutineExpired:
		bar.Pill, bar.Class = "Approval expired", "exp"
	default:
		bar.Pill, bar.Class = "Running", "run"
	}
	if row.PausedMS >= 0 {
		bar.Wake = wakeLine(agent.Name, time.Duration(row.PausedMS)*time.Millisecond, time.Duration(row.WakeMS)*time.Millisecond)
	}
	if row.Status == store.RoutineDone || (row.Status == store.RoutineLate && row.FinishedAt != "") {
		if start, err := time.Parse(time.RFC3339, row.StartedAt); err == nil {
			end := now
			if fin, err := time.Parse(time.RFC3339, row.FinishedAt); err == nil {
				end = fin
			}
			bar.Done = "Run done in " + formatSpan(end.Sub(start)) + ". The machine pauses again after 15 min idle."
		}
	}
	if row.Status == store.RoutineFailed {
		code := s.latestWakeCode(r.Context(), user.ID, agent.ID)
		bar.Fail = true
		bar.Class = "err"
		bar.Pill = "Failed"
		next := ""
		if t, err := time.Parse(time.RFC3339, routine.NextRunAt); err == nil {
			next = routineWhen(t, now, loc)
		}
		bar.FailLead = "This run did not start."
		rest := agent.Name + "'s machine did not wake"
		if code == store.WakeTimeout || code == "" {
			rest += " within 90 s"
		}
		rest += ", so the prompt was never sent."
		if next != "" && routine.DeletedAt == "" {
			rest += " The routine stays on and runs again " + next + "."
		}
		bar.FailRest = rest
		at := "the scheduled time"
		if t, err := time.Parse(time.RFC3339, row.ScheduledFor); err == nil {
			at = t.In(loc).Format("3:04 PM")
		}
		if code == "" {
			code = store.WakeTimeout
		}
		bar.Wake = "Wake requested at " + at + ". No answer"
		if code == store.WakeTimeout {
			bar.Wake += " after 90 s"
		}
		bar.Wake += " (" + code + ")."
		bar.Done = ""
	}
	return bar
}

func wakeLine(name string, paused, woke time.Duration) string {
	line := "Woke " + name + "'s machine for this run."
	if paused > 0 {
		line += " It was paused for " + formatSpan(paused) + "."
	}
	line += " Waking took " + formatSpan(woke) + "."
	return line
}

func (s *Server) mergeMarkers(r *http.Request, user store.User, agent store.Agent, msgs []msgView, now time.Time, loc *time.Location) []msgView {
	rows, err := s.store.ListStartedRoutineRuns(r.Context(), user.ID, agent.ID)
	if err != nil || len(rows) == 0 {
		return msgs
	}
	var markers []msgView
	for _, row := range rows {
		routine, err := s.store.GetRoutine(r.Context(), user.ID, row.RoutineID)
		if err != nil {
			continue
		}
		name := routine.Name
		if routine.DeletedAt != "" {
			name = "deleted routine"
		}
		rloc := loc
		if loaded, err := time.LoadLocation(routine.Timezone); err == nil {
			rloc = loaded
		}
		pill, class := "Done", "ok"
		switch row.Status {
		case store.RoutineRunning:
			if row.FinishedAt == "" {
				pill, class = "Running", "run"
			}
		case store.RoutineLate:
			if row.FinishedAt == "" {
				pill, class = "Running", "run"
			} else {
				pill, class = "Ran late", "late"
			}
		case store.RoutineFailed:
			pill, class = "Failed", "err"
		case store.RoutineExpired:
			pill, class = "Approval expired", "exp"
		case store.RoutineQueued:
			pill, class = "Queued", "queue"
		}
		if s.runWaiting(r, user.ID, row) {
			pill, class = "Needs approval", "wait"
		}
		sub := ""
		if t, err := time.Parse(time.RFC3339, row.StartedAt); err == nil {
			sub = t.In(rloc).Format("3:04 PM")
		}
		if excerpt := s.agentExcerpt(r, user.ID, row.ThreadID); excerpt != "" {
			if sub != "" {
				sub += " · "
			}
			sub += excerpt
		}
		markers = append(markers, msgView{
			Marker: true, At: row.StartedAt, Body: "Routine run: " + name, Sub: sub,
			Pill: pill, PillClass: class,
			Href: "/agents/" + url.PathEscape(agent.ID) + "?thread=" + url.QueryEscape(row.ThreadID),
		})
	}
	if len(markers) == 0 {
		return msgs
	}
	var out []msgView
	i := 0
	for _, marker := range markers {
		for i < len(msgs) && (msgs[i].At == "" || msgs[i].At <= marker.At) {
			out = append(out, msgs[i])
			i++
		}
		out = append(out, marker)
	}
	out = append(out, msgs[i:]...)
	return out
}

func (s *Server) agentExcerpt(r *http.Request, userID, threadID string) string {
	if threadID == "" {
		return ""
	}
	msgs, err := s.store.ListMessages(r.Context(), userID, threadID)
	if err != nil {
		return ""
	}
	for _, msg := range msgs {
		if msg.AuthorKind != "agent" {
			continue
		}
		body := strings.TrimSpace(msg.Body)
		if len(body) > 80 {
			body = body[:80]
		}
		return body
	}
	return ""
}

func outageNote(runs []store.RoutineRun, now time.Time, loc *time.Location) (string, string) {
	var late *store.RoutineRun
	var days []string
	var clock string
	n := 0
	for i := range runs {
		row := &runs[i]
		catch := row.Status == store.RoutineLate && (row.Catchup || row.OutageFrom != "")
		if row.Status == store.RoutineMissed || catch {
			n++
			if t, err := time.Parse(time.RFC3339, row.ScheduledFor); err == nil {
				local := t.In(loc)
				days = append(days, local.Format("Mon"))
				if clock == "" {
					clock = local.Format("3:04 PM")
				}
			}
		}
		if catch && late == nil {
			late = row
		}
	}
	for i, j := 0, len(days)-1; i < j; i, j = i+1, j-1 {
		days[i], days[j] = days[j], days[i]
	}
	if late == nil || late.OutageFrom == "" || n == 0 {
		return "", ""
	}
	from, _ := time.Parse(time.RFC3339, late.OutageFrom)
	to, _ := time.Parse(time.RFC3339, late.OutageTo)
	started := ""
	if t, err := time.Parse(time.RFC3339, late.StartedAt); err == nil {
		started = t.In(loc).Format("3:04 PM")
	}
	note := "castbot was offline from " + routineWhen(from, now, loc) + " to " + routineWhen(to, now, loc) + ". This routine missed " + strconv.Itoa(n) + " runs (" + joinDays(days) + ", " + clock + ") and ran once at " + started + ", as set."
	return note, "late"
}

func (s *Server) skipSiblings(r *http.Request, user store.User, agent store.Agent, routine store.Routine, runs []store.RoutineRun, now time.Time) []siblingView {
	var from string
	for _, row := range runs {
		if row.OutageFrom != "" {
			from = row.OutageFrom
			break
		}
	}
	if from == "" {
		return nil
	}
	others, err := s.store.ListRoutines(r.Context(), user.ID, agent.ID)
	if err != nil {
		return nil
	}
	var out []siblingView
	for _, other := range others {
		if other.ID == routine.ID || other.MissedPolicy != store.MissedSkip {
			continue
		}
		rows, err := s.store.ListRoutineRuns(r.Context(), user.ID, other.ID, 0)
		if err != nil {
			continue
		}
		loc := userLocation(other.Timezone)
		var missed []store.RoutineRun
		for _, row := range rows {
			if row.Status == store.RoutineSkipped && row.OutageFrom == from {
				missed = append(missed, row)
			}
		}
		if len(missed) == 0 {
			continue
		}
		sched, _ := cron.Parse(other.Cron)
		say := cron.Describe(sched)
		if say != "" {
			say = strings.ToLower(say[:1]) + say[1:]
		}
		when := ""
		if t, err := time.Parse(time.RFC3339, missed[len(missed)-1].ScheduledFor); err == nil {
			when = t.In(loc).Format("Mon Jan 2")
		}
		next := ""
		if t, err := time.Parse(time.RFC3339, other.NextRunAt); err == nil {
			next = t.In(loc).Format("Mon Jan 2")
		}
		sib := siblingView{
			Heading: "Same outage, a routine set to skip",
			Note:    other.Name + " (" + say + ") is set to skip missed runs. It missed " + when + " during the same outage and waits for " + next + ".",
		}
		if t, err := time.Parse(time.RFC3339, other.NextRunAt); err == nil {
			sib.History = append(sib.History, historyRow{Pill: "Scheduled", Class: "sched", When: routineWhen(t, now, loc), Detail: "Next run"})
		}
		for _, row := range rows {
			if row.Status == store.RoutineSkipped || row.Status == store.RoutineDone {
				sib.History = append(sib.History, s.historyItem(r, user, agent, other, row, store.RoutineRun{}, now, loc))
			}
		}
		out = append(out, sib)
	}
	return out
}

func (s *Server) routineCardExtra(row store.Approval, user store.User) (extra, from string) {
	ctx := context.Background()
	runRow, err := s.store.GetRun(ctx, user.ID, row.RunID)
	if err != nil || runRow.RoutineRunID == "" {
		return "", ""
	}
	rr, err := s.store.GetRoutineRun(ctx, user.ID, runRow.RoutineRunID)
	if err != nil {
		return "", ""
	}
	routine, err := s.store.GetRoutine(ctx, user.ID, rr.RoutineID)
	if err != nil {
		return "", ""
	}
	from = "Tool " + row.Tool + " is set to Ask · from the routine " + routine.Name
	if routine.DeletedAt != "" {
		from = "Tool " + row.Tool + " is set to Ask · from a deleted routine"
	}
	if row.Status != "pending" || routine.ApprovalPolicy != store.ApprovalWait {
		return "", from
	}
	mins := minutesLeft(row.ExpiresAt, s.store.Now())
	extra = "Denied automatically in " + strconv.Itoa(mins) + " min if not answered. The run finishes without it."
	return extra, from
}

func minutesLeft(expires string, now time.Time) int {
	exp, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return 10
	}
	left := exp.Sub(now)
	mins := int(left / time.Minute)
	if left%time.Minute != 0 {
		mins++
	}
	if mins < 1 {
		mins = 1
	}
	return mins
}

func routineWhen(t, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	now = now.In(loc)
	clock := t.Format("3:04 PM")
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return "Today, " + clock
	}
	return t.Format("Mon Jan 2") + ", " + clock
}

func rowWhen(row store.RoutineRun, now time.Time, loc *time.Location) string {
	stamp := row.ScheduledFor
	if row.StartedAt != "" && (row.Status == store.RoutineRunning || row.Status == store.RoutineDone || row.Status == store.RoutineLate) {
		stamp = row.StartedAt
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return stamp
	}
	return routineWhen(t, now, loc)
}

func donePill(t, now time.Time, loc *time.Location) string {
	t = t.In(loc)
	now = now.In(loc)
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return "Done today " + t.Format("3:04 PM")
	}
	return "Done " + t.Format("Jan 2") + ", " + t.Format("3:04 PM")
}

func formatSpan(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " s"
	}
	if d < time.Minute {
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + " s"
	}
	if d >= time.Hour && d%time.Hour < time.Minute {
		return strconv.Itoa(int(d.Round(time.Hour)/time.Hour)) + " h"
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	if secs == 0 {
		return strconv.Itoa(mins) + " min"
	}
	return strconv.Itoa(mins) + " min " + strconv.Itoa(secs) + " s"
}

func joinDays(days []string) string {
	if len(days) == 0 {
		return ""
	}
	if len(days) == 1 {
		return days[0]
	}
	if len(days) == 2 {
		return days[0] + " and " + days[1]
	}
	return strings.Join(days[:len(days)-1], ", ") + ", and " + days[len(days)-1]
}
