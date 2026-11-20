package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/deletetool"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const (
	deleteRejected  = "That request was rejected."
	deleteNoMachine = "This agent has no machine."
	deleteCouldNot  = "The machine could not be deleted."
	deleteTypeName  = "Type the agent name to delete this machine."
)

// machineDelete runs the delete tool for the agent in the path.
// A user_id, agent_id, token, or value argument is ignored.
// Nothing is deleted until the warning is already in the thread and the
// person's latest message is this agent's name.
func (s *Server) machineDelete(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, deleteRejected, true)
		return
	}
	agent, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, deleteRejected, true)
		return
	}
	if name != deletetool.ToolDelete {
		writeAgentResult(w, deleteRejected, true)
		return
	}
	var args deleteArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, deleteRejected, true)
			return
		}
	}
	text, failed := s.decideDelete(r.Context(), user, agent, "", "", args.Confirm)
	writeAgentResult(w, text, failed)
}

type deleteArgs struct {
	Confirm string `json:"confirm"`
}

// maybeDelete reads one castbot-delete fence from the latest reply.
func (s *Server) maybeDelete(job run.Job) {
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	ctx := context.Background()
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, fields, ok := deletetool.Parse(reply.Body)
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
	text, _ := s.decideDelete(ctx, user, agent, job.ThreadID, reply.ID, fields.Confirm)
	s.writeDeleteReply(ctx, job.UserID, reply.ID, visible, text)
}

func (s *Server) writeDeleteReply(ctx context.Context, userID, replyID, visible, text string) {
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if !strings.Contains(body, text) {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, userID, replyID, body)
}

func (s *Server) decideDelete(ctx context.Context, user store.User, agent store.Agent, threadID, skipID, confirm string) (string, bool) {
	if s.replacing(ctx, user.ID, agent.ID) {
		return replaceSentence, true
	}
	if _, err := s.store.LiveVM(ctx, user.ID, agent.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return deleteNoMachine, true
		}
		return "The machine record could not be read.", true
	}
	confirm = strings.TrimSpace(confirm)
	if confirm == "" {
		return deleteExplain(agent.Name), false
	}
	if host.ConfirmName(confirm, agent.Name) != nil {
		return deleteTypeName, true
	}
	threadID = s.moveThread(ctx, user.ID, agent.ID, threadID)
	if !s.deleteAllowed(ctx, user.ID, threadID, skipID, agent.Name) {
		return deleteExplain(agent.Name), false
	}
	return s.deleteAgentNow(ctx, user, agent)
}

func (s *Server) deleteAgentNow(ctx context.Context, user store.User, agent store.Agent) (string, bool) {
	if err := s.removeMachine(ctx, user.ID, agent); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return deleteNoMachine, true
		}
		return deleteCouldNot, true
	}
	if err := s.store.DeleteAgent(ctx, user.ID, agent.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		return deleteCouldNot, true
	}
	return "Deleted " + agent.Name + ". " + deletetool.Warning, false
}

func deleteExplain(name string) string {
	return "Type " + name + " to delete this machine. " + deletetool.Warning
}

func (s *Server) deleteAllowed(ctx context.Context, userID, threadID, skipID, name string) bool {
	if threadID == "" || strings.TrimSpace(name) == "" {
		return false
	}
	msgs, err := s.store.ListMessages(ctx, userID, threadID)
	if err != nil {
		return false
	}
	told := false
	yes := false
	for _, msg := range msgs {
		if msg.ID == skipID {
			continue
		}
		if msg.AuthorKind == "agent" && deletetool.HasMarker(msg.Body) {
			told = true
			continue
		}
		if msg.AuthorKind != "user" {
			continue
		}
		if told && host.ConfirmName(msg.Body, name) == nil {
			yes = true
			continue
		}
		yes = false
	}
	return yes
}
