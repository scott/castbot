package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/scott/castbot/internal/cron"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const recentRoutineWindow = 2 * time.Minute

// machineRoutine runs one routine tool for the machine's account and the
// agent in the path. user_id and agent_id arguments are ignored.
func (s *Server) machineRoutine(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	caller, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	var args routineArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, "That request was rejected.", true)
			return
		}
	}
	text, failed := s.runRoutineTool(r.Context(), user, caller, name, args)
	writeAgentResult(w, text, failed)
}

// maybeRoutine reads one castbot-routine fence from the latest reply.
// A create that matches a routine saved in the last two minutes is not
// inserted again. That covers a tool call and a fence for the same ask.
func (s *Server) maybeRoutine(job run.Job) {
	ctx := context.Background()
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, req, ok := routinetool.Parse(reply.Body)
	if !ok {
		return
	}
	user, err := s.store.GetUser(ctx, job.UserID)
	if err != nil {
		return
	}
	caller, err := s.store.GetAgent(ctx, job.UserID, job.AgentID)
	if err != nil {
		return
	}
	if req.Action == "create" && s.recentSameRoutine(ctx, job.UserID, job.AgentID, req) {
		body := strings.TrimSpace(visible)
		if body == "" {
			body = "That routine is already saved."
		}
		s.replaceReply(ctx, job.UserID, reply.ID, body)
		return
	}
	text, failed := s.runRoutineTool(ctx, user, caller, routineToolName(req.Action), routineArgs{
		ID: req.ID, Name: req.Name, Cron: req.Cron, Timezone: req.Timezone,
		Prompt: req.Prompt, Missed: req.Missed, Approval: req.Approval, Enabled: req.Enabled,
	})
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func routineToolName(action string) string {
	switch action {
	case "create":
		return routinetool.ToolCreate
	case "update":
		return routinetool.ToolUpdate
	case "delete":
		return routinetool.ToolDelete
	default:
		return ""
	}
}

func (s *Server) recentSameRoutine(ctx context.Context, userID, agentID string, req routinetool.Request) bool {
	if req.Name == nil || req.Cron == nil || req.Prompt == nil {
		return false
	}
	sched, err := cron.Parse(*req.Cron)
	if err != nil {
		return false
	}
	name := strings.TrimSpace(*req.Name)
	prompt := strings.TrimSpace(*req.Prompt)
	rows, err := s.store.ListRoutines(ctx, userID, agentID)
	if err != nil {
		return false
	}
	cutoff := s.store.Now().Add(-recentRoutineWindow)
	for _, row := range rows {
		if row.Name != name || row.Cron != sched.Raw || strings.TrimSpace(row.Prompt) != prompt {
			continue
		}
		at, err := time.Parse(time.RFC3339, row.CreatedAt)
		if err != nil || at.Before(cutoff) {
			continue
		}
		return true
	}
	return false
}

type routineArgs struct {
	ID       string  `json:"id"`
	Name     *string `json:"name"`
	Cron     *string `json:"cron"`
	Timezone *string `json:"timezone"`
	Prompt   *string `json:"prompt"`
	Missed   *string `json:"missed"`
	Approval *string `json:"approval"`
	Enabled  *bool   `json:"enabled"`
}

func (s *Server) runRoutineTool(ctx context.Context, user store.User, caller store.Agent, name string, args routineArgs) (string, bool) {
	switch name {
	case routinetool.ToolList:
		return s.listRoutineText(ctx, user.ID, caller.ID), false
	case routinetool.ToolCreate:
		return s.createRoutineTool(ctx, user, caller, args)
	case routinetool.ToolUpdate:
		return s.updateRoutineTool(ctx, user, caller, args)
	case routinetool.ToolDelete:
		return s.deleteRoutineTool(ctx, user, caller, args)
	default:
		return "That request was rejected.", true
	}
}

func (s *Server) listRoutineText(ctx context.Context, userID, agentID string) string {
	rows, err := s.store.ListRoutines(ctx, userID, agentID)
	if err != nil {
		return "The routine list could not be read."
	}
	if len(rows) == 0 {
		return "No routines yet."
	}
	now := s.store.Now()
	var b strings.Builder
	for _, row := range rows {
		line := routineSentence(row, now)
		if b.Len() > 0 && b.Len()+1+len(line) > 8000 {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	if b.Len() == 0 {
		return "No routines yet."
	}
	return b.String()
}

func (s *Server) createRoutineTool(ctx context.Context, user store.User, caller store.Agent, args routineArgs) (string, bool) {
	in := store.RoutineInput{
		Name:           deref(args.Name),
		Cron:           deref(args.Cron),
		Timezone:       user.Timezone,
		Prompt:         deref(args.Prompt),
		MissedPolicy:   store.MissedOnce,
		ApprovalPolicy: store.ApprovalWait,
		Enabled:        true,
	}
	if tz := strings.TrimSpace(deref(args.Timezone)); tz != "" {
		in.Timezone = tz
	}
	if missed := strings.TrimSpace(deref(args.Missed)); missed != "" {
		in.MissedPolicy = missed
	}
	if approval := strings.TrimSpace(deref(args.Approval)); approval != "" {
		in.ApprovalPolicy = approval
	}
	if msg := routineInputError(in); msg != "" {
		return msg, true
	}
	saved, err := s.store.CreateRoutine(ctx, user.ID, caller.ID, in)
	if err != nil {
		return "That routine could not be saved.", true
	}
	return routineSentence(saved, s.store.Now()), false
}

func (s *Server) updateRoutineTool(ctx context.Context, user store.User, caller store.Agent, args routineArgs) (string, bool) {
	cur, ok := s.ownRoutine(ctx, user.ID, caller.ID, args.ID)
	if !ok {
		return "That routine was not found.", true
	}
	in := store.RoutineInput{
		Name:           cur.Name,
		Cron:           cur.Cron,
		Timezone:       cur.Timezone,
		Prompt:         cur.Prompt,
		MissedPolicy:   cur.MissedPolicy,
		ApprovalPolicy: cur.ApprovalPolicy,
		Enabled:        cur.Enabled,
	}
	if args.Name != nil {
		in.Name = *args.Name
	}
	if args.Cron != nil {
		in.Cron = *args.Cron
	}
	if args.Timezone != nil {
		in.Timezone = *args.Timezone
	}
	if args.Prompt != nil {
		in.Prompt = *args.Prompt
	}
	if args.Missed != nil {
		in.MissedPolicy = *args.Missed
	}
	if args.Approval != nil {
		in.ApprovalPolicy = *args.Approval
	}
	if args.Enabled != nil {
		in.Enabled = *args.Enabled
	}
	if msg := routineInputError(in); msg != "" {
		return msg, true
	}
	onlyEnabled := args.Name == nil && args.Cron == nil && args.Timezone == nil && args.Prompt == nil && args.Missed == nil && args.Approval == nil && args.Enabled != nil
	var saved store.Routine
	var err error
	if onlyEnabled {
		saved, err = s.store.SetRoutineEnabled(ctx, user.ID, cur.ID, *args.Enabled)
	} else {
		saved, err = s.store.UpdateRoutine(ctx, user.ID, cur.ID, in)
	}
	if err != nil {
		return "That routine could not be saved.", true
	}
	return routineSentence(saved, s.store.Now()), false
}

func (s *Server) deleteRoutineTool(ctx context.Context, user store.User, caller store.Agent, args routineArgs) (string, bool) {
	cur, ok := s.ownRoutine(ctx, user.ID, caller.ID, args.ID)
	if !ok {
		return "That routine was not found.", true
	}
	if err := s.store.DeleteRoutine(ctx, user.ID, cur.ID); err != nil {
		return "That routine was not found.", true
	}
	return "Deleted " + cur.Name + ".", false
}

func (s *Server) ownRoutine(ctx context.Context, userID, agentID, id string) (store.Routine, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return store.Routine{}, false
	}
	row, err := s.store.GetLiveRoutine(ctx, userID, id)
	if err != nil || row.AgentID != agentID {
		return store.Routine{}, false
	}
	return row, true
}

func routineSentence(row store.Routine, now time.Time) string {
	say := row.Cron
	if sched, err := cron.Parse(row.Cron); err == nil {
		say = cron.Describe(sched)
	}
	if !row.Enabled {
		return row.Name + ". " + say + ". " + row.Timezone + ". Off. Paused · no runs until you turn it on. " + row.ID
	}
	next := "Next run is not scheduled"
	if t, err := time.Parse(time.RFC3339, row.NextRunAt); err == nil {
		loc, lerr := time.LoadLocation(row.Timezone)
		if lerr != nil {
			loc = time.UTC
		}
		next = "Next: " + routineWhen(t, now, loc)
	}
	return row.Name + ". " + say + ". " + row.Timezone + ". On. " + next + ". " + row.ID
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
