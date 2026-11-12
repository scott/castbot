package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/scott/castbot/internal/cron"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func (s *Server) schedulerLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var minute string
	for {
		select {
		case <-s.base.Done():
			return
		case <-tick.C:
			if s.sweepPause.Load() {
				continue
			}
			now := s.store.Now().UTC().Format("2006-01-02T15:04")
			if now == minute {
				continue
			}
			minute = now
			s.Tick(context.Background())
		}
	}
}

// Tick claims due routines and starts the ones the agent can run now.
// Tests advance the store clock and call Tick. The loop does not use the wall clock.
func (s *Server) Tick(ctx context.Context) {
	rows, err := s.store.ClaimDue(ctx)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.Launch {
			s.launchRoutine(ctx, row)
		}
	}
	extra, err := s.store.UnstartedRoutineRuns(ctx)
	if err != nil {
		return
	}
	for _, row := range extra {
		s.launchRoutine(ctx, row)
	}
}

func (s *Server) launchRoutine(ctx context.Context, row store.RoutineRun) {
	s.withAgent(row.AgentID, func() {
		s.launchLocked(ctx, row)
	})
}

func (s *Server) launchLocked(ctx context.Context, row store.RoutineRun) {
	if s.replacing(ctx, row.UserID, row.AgentID) {
		return
	}
	rr, th, runRow, err := s.store.BeginRoutineExecution(ctx, row.UserID, row.ID)
	if err != nil {
		return
	}
	routine, err := s.store.GetRoutine(ctx, row.UserID, rr.RoutineID)
	if err != nil {
		return
	}
	msg, err := s.store.CreateOriginMessage(ctx, row.UserID, th.ID, "user", row.UserID, routine.Prompt, "routine")
	if err != nil {
		return
	}
	if err := s.store.SetMessageRun(ctx, row.UserID, msg.ID, runRow.ID); err != nil {
		return
	}
	s.runs.Start(runJob(row.UserID, th.ID, row.AgentID, runRow.ID, msg.ID, routine.Prompt, th.CastSessionID, nil))
}

func (s *Server) promoteAgent(ctx context.Context, userID, agentID string) {
	s.withAgent(agentID, func() {
		if s.replacing(ctx, userID, agentID) {
			return
		}
		if runRow, ok, err := s.store.NextQueuedRun(ctx, userID, agentID); err == nil && ok && !s.runs.Watching(runRow.ID) {
			s.startQueued(ctx, runRow)
			return
		}
		if rr, ok, err := s.store.PromoteRoutine(ctx, userID, agentID); err == nil && ok {
			s.launchLocked(ctx, rr)
		}
	})
}

func (s *Server) startQueued(ctx context.Context, runRow store.Run) {
	msg, err := s.store.UserMessageForRun(ctx, runRow.UserID, runRow.ID)
	if err != nil {
		return
	}
	th, err := s.store.GetThread(ctx, runRow.UserID, runRow.ThreadID)
	if err != nil {
		return
	}
	s.runs.Start(runJob(runRow.UserID, runRow.ThreadID, runRow.AgentID, runRow.ID, msg.ID, msg.Body, th.CastSessionID, s.priorTurns(ctx, runRow.UserID, runRow.ThreadID, msg.ID)))
}

func (s *Server) settleRoutine(job run.Job, status string) {
	ctx := context.Background()
	runRow, err := s.store.GetRun(ctx, job.UserID, job.RunID)
	if err != nil || runRow.RoutineRunID == "" {
		return
	}
	rr, err := s.store.GetRoutineRun(ctx, job.UserID, runRow.RoutineRunID)
	if err != nil || rr.FinishedAt != "" {
		return
	}
	if status == "failed" {
		_ = s.store.FinishRoutineRun(ctx, job.UserID, job.RunID, store.RoutineFailed, "The machine did not wake")
		return
	}
	if rows, err := s.store.ListRunApprovals(ctx, job.UserID, job.RunID); err == nil {
		for _, row := range rows {
			if row.Status != "expired" {
				continue
			}
			detail := row.Tool + " denied after " + strconv.Itoa(waitedMinutes(row)) + " min · finished without it"
			_ = s.store.FinishRoutineRun(ctx, job.UserID, job.RunID, store.RoutineExpired, detail)
			return
		}
	}
	dur := "0 s"
	if start, err := time.Parse(time.RFC3339, rr.StartedAt); err == nil {
		dur = formatSpan(s.store.Now().Sub(start))
	}
	detail := "Machine was running · " + dur
	if rr.PausedMS >= 0 {
		detail = "Woke the machine · " + dur
	}
	st := store.RoutineDone
	if rr.Status == store.RoutineLate || rr.Catchup {
		st = store.RoutineLate
		when := ""
		loc := time.UTC
		if routine, err := s.store.GetRoutine(ctx, job.UserID, rr.RoutineID); err == nil {
			if loaded, err := time.LoadLocation(routine.Timezone); err == nil {
				loc = loaded
			}
		}
		if start, err := time.Parse(time.RFC3339, rr.StartedAt); err == nil {
			when = start.In(loc).Format("3:04 PM")
		}
		detail = "castbot was offline · ran once at " + when
	}
	_ = s.store.FinishRoutineRun(ctx, job.UserID, job.RunID, st, detail)
}

func waitedMinutes(row store.Approval) int {
	created, err1 := time.Parse(time.RFC3339, row.CreatedAt)
	exp, err2 := time.Parse(time.RFC3339, row.ExpiresAt)
	if err1 != nil || err2 != nil {
		return 10
	}
	mins := int(exp.Sub(created).Round(time.Minute) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	return mins
}

func (s *Server) denyRoutinePermission(job run.Job, appr store.Approval) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runRow, err := s.store.GetRun(ctx, job.UserID, job.RunID)
	if err != nil || runRow.RoutineRunID == "" {
		return
	}
	rr, err := s.store.GetRoutineRun(ctx, job.UserID, runRow.RoutineRunID)
	if err != nil {
		return
	}
	routine, err := s.store.GetRoutine(ctx, job.UserID, rr.RoutineID)
	if err != nil || routine.ApprovalPolicy != store.ApprovalDeny {
		return
	}
	decided, ok, err := s.store.ClaimDecision(ctx, job.UserID, appr.ID, job.RunID, "denied", "routine")
	if err != nil || !ok {
		return
	}
	if user, err := s.store.GetUser(ctx, job.UserID); err == nil {
		_ = s.postPermission(ctx, user, runRow, decided.RequestID, false, false)
	}
	_ = s.store.MarkSent(ctx, job.UserID, appr.ID)
	_ = s.store.ResumeAfterApproval(ctx, job.UserID, job.RunID)
	s.runs.Hub.Notify(job.RunID)
}

func (s *Server) routinesHome(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agents, err := s.store.ListAgents(r.Context(), user.ID)
	if err != nil || len(agents) == 0 {
		s.workspacePage(w, r)
		return
	}
	last := agents[len(agents)-1]
	http.Redirect(w, r, "/agents/"+url.PathEscape(last.ID)+"?panel=routines", http.StatusSeeOther)
}

func (s *Server) routinesRedirect(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"?panel=routines", http.StatusSeeOther)
}

func (s *Server) routinePreview(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id")); s.missing(w, err) {
		return
	}
	view := previewSchedule(r.URL.Query().Get("cron"), r.URL.Query().Get("tz"), s.store.Now())
	body, err := execTemplate("routinePreview", view)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (s *Server) saveRoutine(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	in := store.RoutineInput{
		Name:           r.FormValue("name"),
		Cron:           r.FormValue("cron"),
		Timezone:       r.FormValue("tz"),
		Prompt:         r.FormValue("prompt"),
		MissedPolicy:   r.FormValue("missed"),
		ApprovalPolicy: r.FormValue("approval"),
		Enabled:        true,
	}
	if in.MissedPolicy == "" {
		in.MissedPolicy = store.MissedOnce
	}
	if in.ApprovalPolicy == "" {
		in.ApprovalPolicy = store.ApprovalWait
	}
	id := strings.TrimSpace(r.FormValue("id"))
	if msg := routineInputError(in); msg != "" {
		s.renderRoutineEditor(w, r, user, agent, id, in, msg, http.StatusBadRequest)
		return
	}
	var saved store.Routine
	if id == "" {
		saved, err = s.store.CreateRoutine(r.Context(), user.ID, agent.ID, in)
	} else {
		if cur, gerr := s.store.GetLiveRoutine(r.Context(), user.ID, id); gerr == nil {
			in.Enabled = cur.Enabled
		}
		saved, err = s.store.UpdateRoutine(r.Context(), user.ID, id, in)
	}
	if s.missing(w, err) {
		return
	}
	if err == store.ErrInvalid {
		s.renderRoutineEditor(w, r, user, agent, id, in, cron.BadSchedule, http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, routineDetailPath(agent.ID, saved.ID), http.StatusSeeOther)
}

func (s *Server) renderRoutineEditor(w http.ResponseWriter, r *http.Request, user store.User, agent store.Agent, id string, in store.RoutineInput, msg string, status int) {
	q := r.URL.Query()
	if id == "" {
		q.Set("routine", "new")
	} else {
		q.Set("routine", id)
		q.Set("edit", "1")
	}
	q.Set("panel", "routines")
	r.URL.RawQuery = q.Encode()
	r.Form.Set("routine_error", msg)
	r.Form.Set("draft_name", in.Name)
	r.Form.Set("draft_cron", in.Cron)
	r.Form.Set("draft_tz", in.Timezone)
	r.Form.Set("draft_prompt", in.Prompt)
	r.Form.Set("draft_missed", in.MissedPolicy)
	r.Form.Set("draft_approval", in.ApprovalPolicy)
	s.renderAgent(w, r, user, agent.ID, false, "chat", status, "")
}

func routineInputError(in store.RoutineInput) string {
	if strings.TrimSpace(in.Name) == "" || len(strings.TrimSpace(in.Name)) > 80 {
		return "Name is required."
	}
	sched, err := cron.Parse(in.Cron)
	if err != nil {
		return cron.BadSchedule
	}
	loc, err := time.LoadLocation(strings.TrimSpace(in.Timezone))
	if err != nil {
		return "Pick a time zone."
	}
	if cron.TooFrequent(sched, loc) {
		return cron.TooClose
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" || len(prompt) > 8000 {
		return "Say what the agent should do."
	}
	switch in.MissedPolicy {
	case store.MissedOnce, store.MissedSkip:
	default:
		return "Pick what happens when a run is missed."
	}
	switch in.ApprovalPolicy {
	case store.ApprovalWait, store.ApprovalDeny:
	default:
		return "Pick what happens when a step needs approval."
	}
	return ""
}

func (s *Server) toggleRoutine(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	on := r.FormValue("on") == "1"
	if _, err := s.store.SetRoutineEnabled(r.Context(), user.ID, r.PathValue("routine"), on); s.missing(w, err) {
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"?panel=routines", http.StatusSeeOther)
}

func (s *Server) runRoutine(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	row, err := s.store.ClaimManual(r.Context(), user.ID, r.PathValue("routine"))
	if err == store.ErrRoutineBusy || err == store.ErrProtected {
		http.Redirect(w, r, routineDetailPath(agent.ID, r.PathValue("routine")), http.StatusSeeOther)
		return
	}
	if s.missing(w, err) {
		return
	}
	if row.Launch {
		s.launchRoutine(r.Context(), row)
		fresh, ferr := s.store.GetRoutineRun(r.Context(), user.ID, row.ID)
		if ferr == nil && fresh.ThreadID != "" {
			http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"?thread="+url.QueryEscape(fresh.ThreadID), http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, routineDetailPath(agent.ID, row.RoutineID), http.StatusSeeOther)
}

func (s *Server) deleteRoutine(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if err := s.store.DeleteRoutine(r.Context(), user.ID, r.PathValue("routine")); s.missing(w, err) {
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"?panel=routines", http.StatusSeeOther)
}

func routineDetailPath(agentID, routineID string) string {
	return "/agents/" + url.PathEscape(agentID) + "?panel=routines&routine=" + url.QueryEscape(routineID)
}
