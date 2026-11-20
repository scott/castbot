package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/settingstool"
	"github.com/scott/castbot/internal/store"
)

const (
	settingsNameBad  = "Enter a name up to 80 characters."
	settingsNameOK   = "Name saved."
	settingsThemeBad = "Choose dark, light, or system."
	settingsRejected = "That request was rejected."
)

// machineSettings runs one settings tool for the machine's account.
// A user_id or agent_id argument is ignored. The agent in the path only
// identifies which machine called.
func (s *Server) machineSettings(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, settingsRejected, true)
		return
	}
	if _, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id")); err != nil {
		writeAgentResult(w, settingsRejected, true)
		return
	}
	var args settingsArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, settingsRejected, true)
			return
		}
	}
	text, failed := s.runSettingsTool(r.Context(), user, name, args)
	writeAgentResult(w, text, failed)
}

type settingsArgs struct {
	Name  string `json:"name"`
	Theme string `json:"theme"`
}

// maybeSettings reads one castbot-settings fence from the latest reply.
func (s *Server) maybeSettings(job run.Job) {
	ctx := context.Background()
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, fields, ok := settingstool.Parse(reply.Body)
	if !ok {
		return
	}
	user, err := s.store.GetUser(ctx, job.UserID)
	if err != nil {
		return
	}
	if _, err := s.store.GetAgent(ctx, job.UserID, job.AgentID); err != nil {
		return
	}
	text, failed := s.runSettingsTool(ctx, user, fields.Action, settingsArgs{
		Name: fields.Name, Theme: fields.Theme,
	})
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func (s *Server) runSettingsTool(ctx context.Context, user store.User, name string, args settingsArgs) (string, bool) {
	switch name {
	case settingstool.ToolName:
		return s.updateDisplayName(ctx, user.ID, args.Name)
	case settingstool.ToolTheme:
		return s.updateThemeNow(ctx, user.ID, args.Theme)
	default:
		return settingsRejected, true
	}
}

func (s *Server) updateDisplayName(ctx context.Context, userID, name string) (string, bool) {
	if err := s.store.UpdateName(ctx, userID, name); err != nil {
		if errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrNotFound) {
			return settingsNameBad, true
		}
		return settingsNameBad, true
	}
	return settingsNameOK, false
}

func (s *Server) updateThemeNow(ctx context.Context, userID, theme string) (string, bool) {
	canon, ok := canonicalTheme(theme)
	if !ok {
		return settingsThemeBad, true
	}
	if err := s.store.UpdateTheme(ctx, userID, canon); err != nil {
		return settingsThemeBad, true
	}
	return "Theme set to " + themeWord(canon) + ".", false
}

func canonicalTheme(theme string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(theme)) {
	case "dark", "light", "system":
		return strings.ToLower(strings.TrimSpace(theme)), true
	default:
		return "", false
	}
}
