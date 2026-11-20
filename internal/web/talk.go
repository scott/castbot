package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott/castbot/internal/handoff"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const askedFallback = "I asked the other agents."

func (s *Server) settleTalk(job run.Job, status string) {
	if status == "done" {
		s.maybeDelegate(job)
		s.maybeCreate(job)
		s.maybeRoutine(job)
		s.maybeSecret(job)
		s.maybeSelf(job)
		s.maybeGroup(job)
		s.maybeMachine(job)
		s.maybeEdit(job)
		s.appendDeliveryLinks(job)
	}
	s.finishDispatch(job, status)
}

func (s *Server) maybeDelegate(job run.Job) {
	ctx := context.Background()
	th, err := s.store.GetThread(ctx, job.UserID, job.ThreadID)
	if err != nil || th.Kind != "dm" || th.AgentID != job.AgentID {
		return
	}
	if job.MessageID != "" {
		msg, err := s.store.GetMessage(ctx, job.UserID, job.MessageID)
		if err != nil || (msg.Origin != "" && msg.Origin != "web") {
			return
		}
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, plan, ok := handoff.Parse(reply.Body)
	if !ok {
		return
	}
	asks, err := s.asksFromPlan(ctx, job.UserID, job.AgentID, plan)
	if err != nil {
		return
	}
	if strings.TrimSpace(visible) == "" {
		visible = askedFallback
	}
	if err := s.store.UpdateMessage(ctx, job.UserID, reply.ID, visible); err != nil {
		return
	}
	if _, err := s.dispatchAsks(ctx, job.UserID, job.AgentID, job.MessageID, asks); err != nil {
		if errors.Is(err, store.ErrRejected) {
			_, _ = s.store.CreateOriginMessage(ctx, job.UserID, th.ID, "system", job.AgentID, "This prompt has used its specialist turns.", "notice")
		}
	}
}

func (s *Server) appendDeliveryLinks(job run.Job) {
	ctx := context.Background()
	batch, err := s.store.BatchByDelivery(ctx, job.UserID, job.MessageID)
	if err != nil || strings.TrimSpace(batch.LinksMD) == "" {
		return
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil || strings.Contains(reply.Body, batch.LinksMD) {
		return
	}
	body := strings.TrimSpace(reply.Body + "\n\n" + batch.LinksMD)
	if body == "" || len(body) > 8000 {
		return
	}
	_ = s.store.UpdateMessage(ctx, job.UserID, reply.ID, body)
}

func (s *Server) finishDispatch(job run.Job, status string) {
	ctx := context.Background()
	row, err := s.store.DispatchByRun(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	next := "done"
	note := ""
	if status != "done" {
		next = "blocked"
		why := "The machine is not ready."
		if runRow, rerr := s.store.GetRun(ctx, job.UserID, job.RunID); rerr == nil && strings.TrimSpace(runRow.Error) != "" {
			why = runRow.Error
		}
		name := "An agent"
		if ag, aerr := s.store.GetAgent(ctx, job.UserID, row.AgentID); aerr == nil && ag.Name != "" {
			name = ag.Name
		}
		note = didNotRun(name, why)
		batch, berr := s.store.GetBatch(ctx, job.UserID, row.BatchID)
		if berr == nil {
			_, _ = s.store.CreateOriginMessage(ctx, job.UserID, batch.OwnerThreadID, "system", row.AgentID, note, "notice")
		}
	}
	updated, err := s.store.MarkDispatch(ctx, job.UserID, job.RunID, next, note)
	if err != nil || !updated {
		return
	}
	n, err := s.store.CountRunning(ctx, job.UserID, row.BatchID)
	if err != nil || n > 0 {
		return
	}
	s.deliverBatch(ctx, job.UserID, row.BatchID)
}

func (s *Server) deliverBatch(ctx context.Context, userID, batchID string) {
	batch, ok, err := s.store.ClaimBatchDelivery(ctx, userID, batchID)
	if err != nil || !ok {
		return
	}
	rows, err := s.store.ListDispatches(ctx, userID, batchID)
	if err != nil {
		return
	}
	var lines []string
	for _, row := range rows {
		name := "An agent"
		if ag, aerr := s.store.GetAgent(ctx, userID, row.AgentID); aerr == nil && ag.Name != "" {
			name = ag.Name
		}
		if row.Status == "done" && row.RunID != "" {
			if reply, rerr := s.store.LatestAgentReply(ctx, userID, row.RunID); rerr == nil {
				lines = append(lines, name+": "+clipTalk(reply.Body, 1500))
				continue
			}
		}
		if strings.TrimSpace(row.Note) != "" {
			lines = append(lines, row.Note)
			continue
		}
		lines = append(lines, didNotRun(name, ""))
	}
	prompt := handoff.DeliveryPrompt(lines)
	if err := s.store.SetBatchPrompt(ctx, userID, batch.ID, prompt); err != nil {
		return
	}
	note, err := s.store.CreateOriginMessage(ctx, userID, batch.OwnerThreadID, "system", batch.ChiefAgentID, "The other agents finished.", "handoff")
	if err != nil {
		return
	}
	if err := s.store.SetBatchDeliveryMessage(ctx, userID, batch.ID, note.ID); err != nil {
		return
	}
	s.startChief(ctx, userID, batch.ChiefAgentID, batch.OwnerThreadID, note.ID, prompt)
}

func (s *Server) startChief(ctx context.Context, userID, chiefID, threadID, messageID, text string) {
	var created store.Run
	start := false
	var err error
	s.withAgent(chiefID, func() {
		busy, _ := s.store.AgentBusy(ctx, userID, chiefID)
		created, err = s.store.CreateRun(ctx, userID, threadID, chiefID)
		if err != nil {
			return
		}
		if err = s.store.SetMessageRun(ctx, userID, messageID, created.ID); err != nil {
			return
		}
		start = !busy
	})
	if err != nil || created.ID == "" || !start {
		return
	}
	s.runs.Start(runJob(userID, threadID, chiefID, created.ID, messageID, text, "", nil))
}

type talkAsk struct {
	kind   string
	task   string
	agents []store.Agent
}

func (s *Server) asksFromPlan(ctx context.Context, userID, chiefID string, plan handoff.Plan) ([]talkAsk, error) {
	var asks []talkAsk
	if plan.DM != nil {
		ask, err := s.oneAsk(ctx, userID, chiefID, "dm", plan.DM.Task, []string{plan.DM.Agent})
		if err != nil {
			return nil, err
		}
		asks = append(asks, ask)
	}
	if plan.Chat != nil {
		ask, err := s.oneAsk(ctx, userID, chiefID, "chat", plan.Chat.Task, plan.Chat.Agents)
		if err != nil {
			return nil, err
		}
		asks = append(asks, ask)
	}
	if len(asks) == 0 {
		return nil, store.ErrInvalid
	}
	return asks, nil
}

func (s *Server) oneAsk(ctx context.Context, userID, chiefID, kind, task string, ids []string) (talkAsk, error) {
	kind = normalizeTalkKind(kind)
	task = strings.TrimSpace(task)
	if kind == "" {
		return talkAsk{}, store.ErrInvalid
	}
	if task == "" || len(task) > 8000 {
		return talkAsk{}, store.ErrInvalid
	}
	seen := map[string]bool{}
	var agents []store.Agent
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || id == chiefID || seen[id] {
			return talkAsk{}, store.ErrRejected
		}
		seen[id] = true
		ag, err := s.store.GetAgent(ctx, userID, id)
		if err != nil {
			return talkAsk{}, err
		}
		agents = append(agents, ag)
	}
	switch kind {
	case "dm":
		if len(agents) != 1 {
			return talkAsk{}, store.ErrRejected
		}
	case "chat":
		if len(agents) < 2 {
			return talkAsk{}, store.ErrRejected
		}
	}
	return talkAsk{kind: kind, task: task, agents: agents}, nil
}

func (s *Server) dispatchAsks(ctx context.Context, userID, chiefID, ownerMessageID string, asks []talkAsk) ([]string, error) {
	msg, err := s.store.GetMessage(ctx, userID, ownerMessageID)
	if err != nil {
		return nil, err
	}
	th, err := s.store.GetThread(ctx, userID, msg.ThreadID)
	if err != nil {
		return nil, err
	}
	if th.Kind != "dm" || th.AgentID != chiefID {
		return nil, store.ErrRejected
	}
	chief, err := s.store.GetAgent(ctx, userID, chiefID)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, ask := range asks {
		n += len(ask.agents)
	}
	job, err := s.store.EnsureAgentJob(ctx, userID, th.ID, msg.ID, chiefID)
	if err != nil {
		return nil, err
	}
	if err := s.store.ClaimSpecialistTurns(ctx, userID, job.ID, n); err != nil {
		return nil, err
	}
	batch, err := s.store.CreateBatch(ctx, userID, job.ID)
	if err != nil {
		return nil, err
	}
	var ids []string
	var links []string
	for _, ask := range asks {
		thread, err := s.talkThread(ctx, userID, job.ID, chief, ask)
		if err != nil {
			return nil, err
		}
		ids = append(ids, thread.ID)
		links = append(links, threadLink(chief.ID, thread.ID, thread.Title))
		for _, ag := range ask.agents {
			if err := s.startSpecialist(ctx, userID, batch, chief, ag, thread, ask.task); err != nil {
				return nil, err
			}
		}
	}
	if err := s.store.SetBatchLinks(ctx, userID, batch.ID, strings.Join(links, "\n")); err != nil {
		return nil, err
	}
	running, err := s.store.CountRunning(ctx, userID, batch.ID)
	if err != nil {
		return nil, err
	}
	if running == 0 {
		s.deliverBatch(ctx, userID, batch.ID)
	}
	return ids, nil
}

func (s *Server) talkThread(ctx context.Context, userID, jobID string, chief store.Agent, ask talkAsk) (store.Thread, error) {
	if ask.kind == "dm" {
		spec := ask.agents[0]
		thread, err := s.store.FindAgentDM(ctx, userID, jobID, chief.ID, spec.ID)
		if err == nil {
			return thread, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return store.Thread{}, err
		}
		return s.store.CreateAgentThread(ctx, userID, "agent_dm", chief.ID, "DM with "+spec.Name, jobID, []string{chief.ID, spec.ID})
	}
	ids := []string{chief.ID}
	for _, ag := range ask.agents {
		ids = append(ids, ag.ID)
	}
	return s.store.CreateAgentThread(ctx, userID, "agent_chat", chief.ID, "Chat", jobID, ids)
}

func (s *Server) startSpecialist(ctx context.Context, userID string, batch store.AgentBatch, chief, spec store.Agent, thread store.Thread, task string) error {
	msg, err := s.store.CreateOriginMessage(ctx, userID, thread.ID, "agent", chief.ID, task, "handoff")
	if err != nil {
		return err
	}
	if why, block := s.blockSpecialist(ctx, userID, spec.ID); block {
		note := didNotRun(spec.Name, why)
		_, _ = s.store.CreateOriginMessage(ctx, userID, batch.OwnerThreadID, "system", spec.ID, note, "notice")
		_, err = s.store.AddDispatch(ctx, userID, batch.ID, thread.ID, spec.ID, "", "blocked", task, note)
		return err
	}
	var created store.Run
	start := false
	s.withAgent(spec.ID, func() {
		busy, _ := s.store.AgentBusy(ctx, userID, spec.ID)
		created, err = s.store.CreateRun(ctx, userID, thread.ID, spec.ID)
		if err != nil {
			return
		}
		if err = s.store.SetMessageRun(ctx, userID, msg.ID, created.ID); err != nil {
			return
		}
		start = !busy
	})
	if err != nil {
		return err
	}
	if _, err = s.store.AddDispatch(ctx, userID, batch.ID, thread.ID, spec.ID, created.ID, "running", task, ""); err != nil {
		return err
	}
	if !start {
		return nil
	}
	text := handoff.SpecialistPrompt(chief.Name, task)
	s.runs.Start(runJob(userID, thread.ID, spec.ID, created.ID, msg.ID, text, "", nil))
	return nil
}

// blockSpecialist reports why a specialist with no machine cannot start
// when the account is already at its machine cap. A live machine uses the
// same wake path as a human send. A missing host is the dev stand-in.
func (s *Server) blockSpecialist(ctx context.Context, userID, agentID string) (string, bool) {
	s.mu.Lock()
	h := s.machine
	s.mu.Unlock()
	if h == nil {
		return "", false
	}
	_, err := s.store.LiveVM(ctx, userID, agentID)
	if err == nil {
		return "", false
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "The machine is not ready.", true
	}
	used, max, qerr := s.store.Quota(ctx, userID)
	if qerr != nil {
		return "The machine is not ready.", true
	}
	if used >= max {
		return "This account has reached its machine limit.", true
	}
	return "", false
}

func (s *Server) agentRoster(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id")); err != nil {
		if s.missing(w, err) {
			return
		}
		return
	}
	agents, err := s.store.ListAgents(r.Context(), user.ID)
	if err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	type row struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	out := struct {
		Agents []row `json:"agents"`
	}{Agents: []row{}}
	for _, ag := range agents {
		out.Agents = append(out.Agents, row{ID: ag.ID, Name: ag.Name, Title: ag.Title, Description: ag.Description})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) postHandoff(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		writeText(w, http.StatusBadRequest, "Bad request\n")
		return
	}
	s.writeHandoff(w, r, user.ID, r.PathValue("id"), r.FormValue("message"), r.FormValue("kind"), r.FormValue("task"), r.Form["agent"])
}

func (s *Server) machineHandoff(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	var in struct {
		Message string   `json:"message"`
		Kind    string   `json:"kind"`
		Agents  []string `json:"agents"`
		Task    string   `json:"task"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&in); err != nil {
		writeText(w, http.StatusBadRequest, "Bad request\n")
		return
	}
	s.writeHandoff(w, r, userID, r.PathValue("id"), in.Message, in.Kind, in.Task, in.Agents)
}

func (s *Server) writeHandoff(w http.ResponseWriter, r *http.Request, userID, senderID, messageID, kind, task string, agents []string) {
	if _, err := s.store.GetAgent(r.Context(), userID, senderID); err != nil {
		if s.missing(w, err) {
			return
		}
		return
	}
	ids, err := s.executeHandoff(r.Context(), userID, senderID, messageID, kind, task, agents)
	if writeTalkErr(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "threads": ids})
}

func (s *Server) executeHandoff(ctx context.Context, userID, senderID, messageID, kind, task string, agents []string) ([]string, error) {
	msg, err := s.store.GetMessage(ctx, userID, messageID)
	if err != nil {
		return nil, err
	}
	th, err := s.store.GetThread(ctx, userID, msg.ThreadID)
	if err != nil {
		return nil, err
	}
	chiefID := ""
	ownerMessageID := ""
	switch th.Kind {
	case "dm":
		chiefID = th.AgentID
		ownerMessageID = msg.ID
	case "agent_dm", "agent_chat":
		job, jerr := s.store.JobForThread(ctx, userID, th.ID)
		if jerr != nil {
			return nil, jerr
		}
		chiefID = job.ChiefAgentID
		ownerMessageID = job.OwnerMessageID
	default:
		return nil, store.ErrRejected
	}
	if senderID == chiefID {
		if th.Kind != "dm" {
			return nil, store.ErrRejected
		}
		ask, err := s.oneAsk(ctx, userID, chiefID, kind, task, agents)
		if err != nil {
			return nil, err
		}
		return s.dispatchAsks(ctx, userID, chiefID, ownerMessageID, []talkAsk{ask})
	}
	return s.specialistHandoff(ctx, userID, senderID, chiefID, ownerMessageID, kind, task, agents)
}

func (s *Server) specialistHandoff(ctx context.Context, userID, senderID, chiefID, ownerMessageID, kind, task string, agents []string) ([]string, error) {
	kind = normalizeTalkKind(kind)
	task = strings.TrimSpace(task)
	if task == "" || len(task) > 8000 {
		return nil, store.ErrInvalid
	}
	if kind != "dm" || len(agents) != 1 || strings.TrimSpace(agents[0]) != chiefID {
		return nil, store.ErrRejected
	}
	job, err := s.store.JobByMessage(ctx, userID, ownerMessageID)
	if err != nil {
		return nil, err
	}
	if job.ChiefAgentID != chiefID {
		return nil, store.ErrRejected
	}
	on, err := s.store.AgentOnJob(ctx, userID, job.ID, senderID)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, store.ErrRejected
	}
	spec, err := s.store.GetAgent(ctx, userID, senderID)
	if err != nil {
		return nil, err
	}
	chief, err := s.store.GetAgent(ctx, userID, chiefID)
	if err != nil {
		return nil, err
	}
	thread, err := s.store.FindAgentDM(ctx, userID, job.ID, chief.ID, spec.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		thread, err = s.store.CreateAgentThread(ctx, userID, "agent_dm", chief.ID, "DM with "+spec.Name, job.ID, []string{chief.ID, spec.ID})
		if err != nil {
			return nil, err
		}
	}
	if _, err := s.store.CreateOriginMessage(ctx, userID, thread.ID, "agent", spec.ID, task, "handoff"); err != nil {
		return nil, err
	}
	prompt := handoff.RelayPrompt(spec.Name, task)
	if len(prompt) > 8000 {
		prompt = clipTalk(prompt, 8000)
	}
	note, err := s.store.CreateOriginMessage(ctx, userID, job.OwnerThreadID, "system", spec.ID, prompt, "relay")
	if err != nil {
		return nil, err
	}
	s.startChief(ctx, userID, chief.ID, job.OwnerThreadID, note.ID, prompt)
	return []string{thread.ID}, nil
}

func (s *Server) withRoster(ctx context.Context, job run.Job, req runtime.Send) runtime.Send {
	if job.UserID == "" || job.ThreadID == "" || job.AgentID == "" {
		return req
	}
	th, err := s.store.GetThread(ctx, job.UserID, job.ThreadID)
	if err != nil || th.Kind != "dm" {
		return req
	}
	if job.MessageID != "" {
		msg, err := s.store.GetMessage(ctx, job.UserID, job.MessageID)
		if err != nil {
			return req
		}
		if msg.Origin == "handoff" || msg.Origin == "relay" {
			return req
		}
	}
	agents, err := s.store.ListAgents(ctx, job.UserID)
	if err != nil || len(agents) < 2 {
		return req
	}
	var self store.Agent
	found := false
	for _, ag := range agents {
		if ag.ID == job.AgentID {
			self = ag
			found = true
			break
		}
	}
	if !found || !self.CanDelegate {
		return req
	}
	roster := make([]handoff.Agent, 0, len(agents))
	for _, ag := range agents {
		roster = append(roster, handoff.Agent{ID: ag.ID, Name: ag.Name, Title: ag.Title, Description: ag.Description})
	}
	req.Text = handoff.Preface(job.AgentID, roster) + req.Text
	return req
}

// startHandoffQueued starts a queued handoff run with the prompt that
// belongs to that turn. A normal chat run returns false.
func (s *Server) startHandoffQueued(ctx context.Context, runRow store.Run) bool {
	msg, err := s.store.LinkedMessage(ctx, runRow.UserID, runRow.ID)
	if err != nil || (msg.Origin != "handoff" && msg.Origin != "relay") {
		return false
	}
	th, err := s.store.GetThread(ctx, runRow.UserID, runRow.ThreadID)
	if err != nil {
		return true
	}
	text := ""
	switch {
	case msg.Origin == "relay":
		text = msg.Body
	case msg.Origin == "handoff" && msg.AuthorKind == "system":
		batch, berr := s.store.BatchByDelivery(ctx, runRow.UserID, msg.ID)
		if berr != nil {
			return true
		}
		text = batch.Prompt
	case msg.Origin == "handoff" && (th.Kind == "agent_dm" || th.Kind == "agent_chat"):
		chief, cerr := s.store.GetAgent(ctx, runRow.UserID, msg.AuthorID)
		if cerr != nil {
			return true
		}
		text = handoff.SpecialistPrompt(chief.Name, msg.Body)
	default:
		return true
	}
	if strings.TrimSpace(text) == "" {
		return true
	}
	s.runs.Start(runJob(runRow.UserID, runRow.ThreadID, runRow.AgentID, runRow.ID, msg.ID, text, th.CastSessionID, nil))
	return true
}

func (s *Server) labelSpeakers(ctx context.Context, userID string, msgs []store.Message, views []msgView) {
	if len(msgs) != len(views) {
		return
	}
	known := map[string]store.Agent{}
	for i, msg := range msgs {
		if msg.AuthorKind != "agent" || msg.AuthorID == "" {
			continue
		}
		ag, ok := known[msg.AuthorID]
		if !ok {
			var err error
			ag, err = s.store.GetAgent(ctx, userID, msg.AuthorID)
			if err != nil {
				continue
			}
			known[msg.AuthorID] = ag
		}
		views[i].Name = ag.Name
		views[i].Letter = letter(ag.Name)
		if ag.Color != "" {
			views[i].Color = ag.Color
		}
	}
}

func (s *Server) specialistCardName(user store.User, runID string) string {
	ctx := context.Background()
	runRow, err := s.store.GetRun(ctx, user.ID, runID)
	if err != nil {
		return ""
	}
	th, err := s.store.GetThread(ctx, user.ID, runRow.ThreadID)
	if err != nil {
		return ""
	}
	if th.Kind != "agent_dm" && th.Kind != "agent_chat" {
		return ""
	}
	ag, err := s.store.GetAgent(ctx, user.ID, runRow.AgentID)
	if err != nil {
		return ""
	}
	return ag.Name
}

func normalizeTalkKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case "dm", "agent_dm":
		return "dm"
	case "chat", "agent_chat":
		return "chat"
	default:
		return ""
	}
}

func didNotRun(name, why string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "An agent"
	}
	why = strings.TrimSpace(why)
	if why == "" {
		return name + " did not run."
	}
	return name + " did not run. " + why
}

func threadLink(chiefID, threadID, title string) string {
	if strings.TrimSpace(title) == "" {
		title = "Chat"
	}
	href := "/agents/" + url.PathEscape(chiefID) + "?thread=" + url.QueryEscape(threadID)
	return "[" + title + "](" + href + ")"
}

func clipTalk(s string, n int) string {
	if n < 1 || len(s) <= n {
		return s
	}
	return s[:n]
}

func writeTalkErr(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrRejected) {
		writeText(w, http.StatusConflict, "Rejected\n")
		return true
	}
	return mapStoreErr(w, r, err)
}
