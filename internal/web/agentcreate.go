package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/approvetool"
	"github.com/scott/castbot/internal/chattool"
	"github.com/scott/castbot/internal/connectiontool"
	"github.com/scott/castbot/internal/edittool"
	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/machinetool"
	"github.com/scott/castbot/internal/replacetool"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/secretstool"
	"github.com/scott/castbot/internal/selftool"
	"github.com/scott/castbot/internal/settingstool"
	"github.com/scott/castbot/internal/store"
)

const recentCreateWindow = 2 * time.Minute

func (s *Server) machineAgents(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(&call); err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	switch call.Name {
	case agentskill.ToolList:
		writeAgentResult(w, s.listAgentText(r.Context(), userID), false)
	case agentskill.ToolCreate:
		var args struct {
			Description string `json:"description"`
			Name        string `json:"name"`
		}
		if len(call.Arguments) > 0 {
			_ = json.Unmarshal(call.Arguments, &args)
		}
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
		text, failed, _ := s.createRequestedAgent(r.Context(), user, caller, args.Description, args.Name, "")
		writeAgentResult(w, text, failed)
	case routinetool.ToolList, routinetool.ToolCreate, routinetool.ToolUpdate, routinetool.ToolDelete, routinetool.ToolRun:
		s.machineRoutine(w, r, userID, call.Name, call.Arguments)
	default:
		if selftool.Known(call.Name) {
			s.machineSelf(w, r, userID, call.Name, call.Arguments)
			return
		}
		if grouptool.Known(call.Name) {
			s.machineGroup(w, r, userID, call.Name, call.Arguments)
			return
		}
		if machinetool.Known(call.Name) {
			s.machinePower(w, r, userID, call.Name, call.Arguments)
			return
		}
		if edittool.Known(call.Name) {
			s.applyEdit(w, r, userID, call.Arguments)
			return
		}
		if approvetool.Known(call.Name) {
			s.applyApproval(w, r, userID, call.Name, call.Arguments)
			return
		}
		if chattool.Known(call.Name) {
			s.machineChat(w, r, userID, call.Name, call.Arguments)
			return
		}
		if settingstool.Known(call.Name) {
			s.machineSettings(w, r, userID, call.Name, call.Arguments)
			return
		}
		if connectiontool.Known(call.Name) {
			s.machineConnection(w, r, userID, call.Name, call.Arguments)
			return
		}
		if secretstool.Known(call.Name) {
			s.machineVault(w, r, userID, call.Name, call.Arguments)
			return
		}
		if replacetool.Known(call.Name) {
			s.machineReplace(w, r, userID, call.Name, call.Arguments)
			return
		}
		writeAgentResult(w, "That request was rejected.", true)
	}
}

func writeAgentResult(w http.ResponseWriter, text string, failed bool) {
	writeJSON(w, http.StatusOK, map[string]any{"text": text, "error": failed})
}

func (s *Server) listAgentText(ctx context.Context, userID string) string {
	agents, err := s.store.ListAgents(ctx, userID)
	if err != nil {
		return "The agent list could not be read."
	}
	if len(agents) == 0 {
		return "No agents yet."
	}
	var b strings.Builder
	for _, agent := range agents {
		desc, _ := redactSecrets(agent.Description)
		title, _ := redactSecrets(agent.Title)
		line := agent.Name + " | " + title + " | " + clipRunes(desc, 160) + " | " + agent.ID
		if b.Len() > 0 && b.Len()+1+len(line) > 8000 {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
	}
	if b.Len() == 0 {
		return "No agents yet."
	}
	return b.String()
}

// maybeCreate reads a castbot-create fence from the latest reply and saves
// the agent for that same account. A fence is ignored when the tool already
// saved the same description in the last two minutes.
func (s *Server) maybeCreate(job run.Job) {
	ctx := context.Background()
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	unlock := s.lockCreate(job.RunID)
	defer unlock()
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, req, ok := agentskill.Parse(reply.Body)
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
	if _, _, jobText, good := preparedRequest(req.Description, req.Name); good && s.recentSameJob(ctx, job.UserID, jobText) {
		body := strings.TrimSpace(visible)
		if body == "" {
			body = "That agent is already in the sidebar."
		}
		s.replaceReply(ctx, job.UserID, reply.ID, body)
		return
	}
	text, failed, createdID := s.createRequestedAgent(ctx, user, caller, req.Description, req.Name, job.RunID)
	if createdID != "" {
		s.rememberSidebar(job.RunID, createdID)
	}
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func (s *Server) recentSameJob(ctx context.Context, userID, job string) bool {
	job = strings.TrimSpace(job)
	if job == "" {
		return false
	}
	agents, err := s.store.ListAgents(ctx, userID)
	if err != nil {
		return false
	}
	cutoff := s.store.Now().Add(-recentCreateWindow)
	for _, agent := range agents {
		if strings.TrimSpace(agent.Description) != job {
			continue
		}
		at, err := time.Parse(time.RFC3339, agent.CreatedAt)
		if err != nil || at.Before(cutoff) {
			continue
		}
		return true
	}
	return false
}

func (s *Server) replaceReply(ctx context.Context, userID, id, body string) {
	body = cutBytes(strings.TrimSpace(body), 8000)
	if body == "" {
		return
	}
	_ = s.store.UpdateMessage(ctx, userID, id, body)
}

// createRequestedAgent saves one agent for user. The bool is true when no
// row should be treated as created. The caller is the agent that asked.
// A user id in the tool arguments is ignored.
func (s *Server) createRequestedAgent(ctx context.Context, user store.User, caller store.Agent, description, rawName, runID string) (string, bool, string) {
	desc, name, job, ok := preparedRequest(description, rawName)
	if !ok {
		return "No agent was created. The description was not clear enough.", true, ""
	}
	model, ok := s.modelForNewAgent(ctx, user.ID, caller.Model)
	if !ok {
		return "No agent was created. Add a model key first.", true, ""
	}
	used := s.usedNames(ctx, user.ID)
	explicit := name != ""
	if explicit && used[strings.ToLower(name)] {
		return "That name is already used. No agent was created.", true, ""
	}
	chosen := name
	if !explicit {
		chosen, _ = autoName(desc, job, used)
	}
	card := setupState{
		Name:         chosen,
		Title:        titleFor(job),
		Instructions: instructionsFor(chosen, job, desc),
		Job:          job,
		Model:        model,
		Size:         createSize,
		Idle:         createIdle,
		HasKey:       true,
	}
	h, image, publicURL := s.hostConfig()
	pending := h != nil && image != "" && publicURL != ""
	agent, err := s.materialize(ctx, user, card, pending)
	if errors.Is(err, store.ErrConflict) && explicit {
		return "That name is already used. No agent was created.", true, ""
	}
	if errors.Is(err, store.ErrConflict) {
		alt := freeAlt(card.Name, s.usedNames(ctx, user.ID))
		if strings.EqualFold(alt, card.Name) || s.usedNames(ctx, user.ID)[strings.ToLower(alt)] {
			return "That name is already used. No agent was created.", true, ""
		}
		card.Name = alt
		card.Instructions = instructionsFor(alt, job, desc)
		agent, err = s.materialize(ctx, user, card, pending)
	}
	if err != nil {
		return "No agent was created.", true, ""
	}
	if err := s.launchNewAgent(ctx, user.ID, agent); err != nil {
		if errors.Is(err, store.ErrQuota) {
			_ = s.store.DeleteAgent(ctx, user.ID, agent.ID)
			return "This account has reached its machine limit.", true, ""
		}
		if errors.Is(err, errNoHost) {
			s.noteChatAgent(ctx, user.ID, caller.ID, agent)
			s.rememberSidebar(runID, agent.ID)
			return "Created " + agent.Name + ". It is in the sidebar at /agents/" + agent.ID + ". It does not have a machine yet.", false, agent.ID
		}
	}
	s.noteChatAgent(ctx, user.ID, caller.ID, agent)
	s.rememberSidebar(runID, agent.ID)
	return "Created " + agent.Name + ". It is in the sidebar at /agents/" + agent.ID + ".", false, agent.ID
}

func preparedRequest(description, rawName string) (desc, name, job string, ok bool) {
	desc, _ = redactSecrets(strings.TrimSpace(description))
	if !agentskill.UsableDescription(desc) {
		return "", "", "", false
	}
	name = cleanName(strings.TrimSpace(rawName))
	if name == "" {
		name = nameFromText(desc)
	}
	job = strings.TrimSpace(jobLine(desc, name))
	if job == "" {
		job = clipRunes(desc, 160)
	}
	return desc, name, job, true
}

func (s *Server) modelForNewAgent(ctx context.Context, userID, callerModel string) (string, bool) {
	var candidates []string
	if model := strings.TrimSpace(callerModel); model != "" {
		candidates = append(candidates, model)
	}
	if model := strings.TrimSpace(s.defaultConfiguredModel(ctx, userID)); model != "" {
		candidates = append(candidates, model)
	}
	if model := defaultModelID(); model != "" {
		candidates = append(candidates, model)
	}
	seen := map[string]bool{}
	for _, model := range candidates {
		if seen[model] {
			continue
		}
		seen[model] = true
		if !s.AcceptModel(ctx, userID, model, "") {
			continue
		}
		if _, ok := s.userModelKey(ctx, userID, model); !ok {
			continue
		}
		return model, true
	}
	return "", false
}

func cutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return strings.TrimSpace(s)
}

func joinLimit(left, right string, max int) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if right == "" {
		return cutBytes(left, max)
	}
	extra := "\n\n" + right
	if len(extra) >= max {
		return cutBytes(right, max)
	}
	room := max - len(extra)
	return strings.TrimSpace(cutBytes(left, room) + extra)
}
