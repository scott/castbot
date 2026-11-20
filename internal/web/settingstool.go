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
	settingsNameBad    = "Enter a name up to 80 characters."
	settingsNameOK     = "Name saved."
	settingsThemeBad   = "Choose dark, light, or system."
	settingsInviteBad  = "Enter a name and an email address."
	settingsInviteHas  = "That email already has an account."
	settingsInviteOwn  = "The owner invites people to this workspace."
	settingsInviteNo   = "That invite could not be created."
	settingsInviteOnce = "Copy this invite link now. It is shown once."
	settingsRevokeAsk  = "Say which invite to revoke."
	settingsRevokeMiss = "That invite was not found."
	settingsRevokeMany = "More than one invite matches. Say which one."
	settingsInvites    = "Invites could not be read."
	settingsRejected   = "That request was rejected."
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
	text, failed := s.runSettingsTool(r.Context(), r, user, name, args)
	writeAgentResult(w, text, failed)
}

type settingsArgs struct {
	Name   string `json:"name"`
	Theme  string `json:"theme"`
	Email  string `json:"email"`
	Invite string `json:"invite"`
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
	text, failed := s.runSettingsTool(ctx, nil, user, fields.Action, settingsArgs{
		Name: fields.Name, Theme: fields.Theme, Email: fields.Email, Invite: fields.Invite,
	})
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed || fields.Action == settingstool.ToolInvite {
		// The invite link is shown once in this reply. Prose alone does not carry it.
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func (s *Server) runSettingsTool(ctx context.Context, r *http.Request, user store.User, name string, args settingsArgs) (string, bool) {
	switch name {
	case settingstool.ToolName:
		return s.updateDisplayName(ctx, user.ID, args.Name)
	case settingstool.ToolTheme:
		return s.updateThemeNow(ctx, user.ID, args.Theme)
	case settingstool.ToolInvite:
		return s.createInviteNow(ctx, r, user, args.Name, args.Email)
	case settingstool.ToolRevoke:
		return s.revokeInviteNow(ctx, user.ID, args.Invite)
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

func (s *Server) createInviteNow(ctx context.Context, r *http.Request, user store.User, name, email string) (string, bool) {
	_, token, err := s.store.CreateInvite(ctx, user.ID, email, name)
	if errors.Is(err, store.ErrNotFound) {
		return settingsInviteOwn, true
	}
	if errors.Is(err, store.ErrConflict) {
		return settingsInviteHas, true
	}
	if errors.Is(err, store.ErrInvalid) {
		return settingsInviteBad, true
	}
	if err != nil {
		return settingsInviteNo, true
	}
	return settingsInviteOnce + "\n" + s.settingsInviteLink(r, token), false
}

func (s *Server) settingsInviteLink(r *http.Request, token string) string {
	if r != nil && r.Host != "" {
		return inviteURL(r, token)
	}
	_, _, publicURL := s.hostConfig()
	if publicURL != "" {
		return publicURL + "/invite/" + token
	}
	return "/invite/" + token
}

func (s *Server) revokeInviteNow(ctx context.Context, userID, query string) (string, bool) {
	if strings.TrimSpace(query) == "" {
		return settingsRevokeAsk, true
	}
	rows, err := s.store.ListInvites(ctx, userID)
	if err != nil {
		return settingsInvites, true
	}
	chosen := matchInvites(rows, query)
	if len(chosen) == 0 {
		return settingsRevokeMiss, true
	}
	if len(chosen) > 1 {
		return inviteChoice(settingsRevokeMany, chosen), true
	}
	row := chosen[0]
	if err := s.store.RevokeInvite(ctx, userID, row.ID); err != nil {
		return settingsRevokeMiss, true
	}
	return "Revoked " + row.DisplayName + ".", false
}

func inviteChoice(head string, rows []store.Invite) string {
	var b strings.Builder
	b.WriteString(head)
	for _, row := range rows {
		line := row.DisplayName + ". " + row.Email + ". " + row.ID
		if b.Len()+1+len(line) > 8000 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}

func matchInvites(rows []store.Invite, query string) []store.Invite {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	var exactID []store.Invite
	for _, row := range rows {
		if strings.EqualFold(row.ID, query) {
			exactID = append(exactID, row)
		}
	}
	if len(exactID) > 0 {
		return exactID
	}
	var exact []store.Invite
	for _, row := range rows {
		if strings.EqualFold(row.Email, query) || strings.EqualFold(row.DisplayName, query) {
			exact = append(exact, row)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	q := strings.ToLower(query)
	var loose []store.Invite
	for _, row := range rows {
		blob := strings.ToLower(row.DisplayName + " " + row.Email + " " + row.ID)
		if strings.Contains(blob, q) {
			loose = append(loose, row)
		}
	}
	return loose
}
