package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/edittool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const editReload = "The agent could not be reloaded. The saved copy is updated. No new machine was created."

// applyEdit runs update_agent for the agent in the path.
// user_id and agent_id arguments are ignored. The call cannot name another agent.
func (s *Server) applyEdit(w http.ResponseWriter, r *http.Request, userID string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	agent, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	fields, ok := editFields(raw)
	if !ok {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	text, failed := s.editThisAgent(r.Context(), user, agent, fields, "")
	writeAgentResult(w, text, failed)
}

// maybeEdit reads one castbot-edit fence from the latest reply.
func (s *Server) maybeEdit(job run.Job) {
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
	visible, fields, ok := edittool.Parse(reply.Body)
	if !ok {
		return
	}
	user, err := s.store.GetUser(ctx, job.UserID)
	if err != nil {
		return
	}
	agent, err := s.store.GetAgent(ctx, job.UserID, job.AgentID)
	if err != nil {
		return
	}
	text, failed := s.editThisAgent(ctx, user, agent, fields, job.RunID)
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func editFields(raw json.RawMessage) (edittool.Fields, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return edittool.Fields{}, true
	}
	var fields edittool.Fields
	if err := json.Unmarshal(raw, &fields); err != nil {
		return edittool.Fields{}, false
	}
	return fields, true
}

func (s *Server) editThisAgent(ctx context.Context, user store.User, agent store.Agent, fields edittool.Fields, runID string) (string, bool) {
	if fields.Empty() {
		return "Say what should change.", true
	}
	name := agent.Name
	if fields.Name != nil {
		name = strings.TrimSpace(*fields.Name)
		if name == "" {
			return "A name is required.", true
		}
		if len(name) > 40 {
			return "Use a name up to 40 characters.", true
		}
	}
	title := agent.Title
	if fields.Title != nil {
		title = strings.TrimSpace(*fields.Title)
		if len(title) > 80 {
			return "Use a title up to 80 characters.", true
		}
	}
	job := agent.Description
	if fields.Job != nil {
		job, _ = redactSecrets(strings.TrimSpace(*fields.Job))
		if len(job) > 160 {
			return "Use a job up to 160 characters.", true
		}
	}
	model := agent.Model
	if fields.Model != nil {
		model = strings.TrimSpace(*fields.Model)
		if model == "" || len(model) > 200 {
			return "Choose a model from the list.", true
		}
		if !s.AcceptModel(ctx, user.ID, model, agent.Model) {
			return "That model is not in the provider manifest.", true
		}
	}
	color := ""
	if fields.Color != nil {
		key, ok := paletteKey(*fields.Color)
		if !ok {
			return colorRejectedCopy, true
		}
		color = key
	}
	delegate := agent.CanDelegate
	if fields.Delegate != nil {
		delegate = *fields.Delegate
	}
	spec := castcfg.Spec{Permissions: map[string]string{}}
	notes := ""
	bundle, err := s.store.LatestBundle(ctx, user.ID, agent.ID)
	if err == nil {
		parsed, perr := castcfg.ParseYAML(bundle.YAML)
		if perr != nil {
			return "The agent file could not be read.", true
		}
		spec = parsed
		notes = bundle.Instructions
	} else if !errors.Is(err, store.ErrNotFound) {
		return "The agent file could not be read.", true
	}
	if fields.Instructions != nil {
		notes = *fields.Instructions
	}
	if strings.TrimSpace(notes) == "" {
		return "Write the instructions.", true
	}
	if len(notes) > 200000 {
		return "Instructions are too long.", true
	}
	input := store.AgentInput{
		SectionID: agent.SectionID, Name: name, Title: title, Description: job,
		CanDelegate: delegate, Model: model,
	}
	if color != "" {
		input.Color = color
	}
	if err := s.store.SaveAgentFields(ctx, user.ID, agent.ID, input); err != nil {
		switch {
		case errors.Is(err, store.ErrConflict):
			return "Choose a name that is not already used.", true
		case errors.Is(err, store.ErrInvalid):
			return colorRejectedCopy, true
		default:
			return "The agent could not be saved.", true
		}
	}
	s.noteSections(ctx, user.ID, runID)
	spec.Name = name
	spec.Title = title
	spec.Model = model
	spec.Instructions = notes
	if strings.TrimSpace(spec.Version) == "" {
		spec.Version = "1"
	}
	sameModel := model == agent.Model && agent.Model != ""
	version, err := s.storeBundle(ctx, user.ID, agent.ID, spec, sameModel)
	if err != nil {
		if errors.Is(err, errFormMismatch) {
			return "The agent file does not match this form.", true
		}
		return "The agent file could not be saved.", true
	}
	fresh, err := s.store.GetAgent(ctx, user.ID, agent.ID)
	if err != nil {
		return "The agent could not be saved.", true
	}
	if _, err := s.applyBundle(ctx, user.ID, fresh, version); err != nil {
		return joinLimit("Saved. "+editSummary(fields, fresh), editReload, 8000), true
	}
	return "Saved. " + editSummary(fields, fresh), false
}

func editSummary(fields edittool.Fields, agent store.Agent) string {
	var parts []string
	if fields.Name != nil {
		parts = append(parts, "Name is "+agent.Name+".")
	}
	if fields.Title != nil {
		if agent.Title == "" {
			parts = append(parts, "Title is cleared.")
		} else {
			parts = append(parts, "Title is "+agent.Title+".")
		}
	}
	if fields.Job != nil {
		if agent.Description == "" {
			parts = append(parts, "Job is cleared.")
		} else {
			parts = append(parts, "Job is "+agent.Description+".")
		}
	}
	if fields.Instructions != nil {
		parts = append(parts, "Instructions are updated.")
	}
	if fields.Color != nil {
		parts = append(parts, "Color is "+colorName(agent.Color)+".")
	}
	if fields.Model != nil {
		label := modelLabel(agent.Model)
		if label == "" {
			label = agent.Model
		}
		parts = append(parts, "Model is "+label+".")
	}
	if fields.Delegate != nil {
		hand := "No"
		if agent.CanDelegate {
			hand = "Yes"
		}
		parts = append(parts, "Can hand work to other agents: "+hand+".")
	}
	return strings.Join(parts, " ")
}

func paletteKey(in string) (string, bool) {
	in = strings.TrimSpace(in)
	if store.ValidAgentColor(in) {
		return in, true
	}
	for _, item := range agentPalette() {
		if strings.EqualFold(item.Name, in) {
			return item.Key, true
		}
	}
	return "", false
}
