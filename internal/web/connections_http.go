package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

type connPage struct {
	Phone, Notice, Error string
	Back                 bool
	Cards                []connCard
	Detail               connDetail
}

type connCard struct {
	ID, Title, Blurb, Status, Action, Href string
	On                                     bool
}

type connDetail struct {
	ID, Title, Body, TokenLabel, TokenMask string
	NeedsSecret, On, HasSecret, EditToken  bool
	Tools                                  []connTool
}

type connTool struct {
	Name, Title, Level string
}

func (s *Server) connectionsList(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	cards, err := s.connectionCards(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.renderConnections(w, r, user, connPage{
		Phone: "Connections", Cards: cards, Notice: connectionNotice(r.URL.Query().Get("notice")),
	}, http.StatusOK)
}

func (s *Server) connectionShow(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	panel, found := connect.PanelLookup(r.PathValue("id"))
	if !found {
		s.missing(w, store.ErrNotFound)
		return
	}
	detail, err := s.connectionDetail(r.Context(), user.ID, panel, nil)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	view := connPage{Phone: panel.Title, Back: true, Detail: detail, Notice: connectionNotice(r.URL.Query().Get("notice"))}
	s.renderConnections(w, r, user, view, http.StatusOK)
}

func (s *Server) connectionSave(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	panel, found := connect.PanelLookup(r.PathValue("id"))
	if !found {
		s.missing(w, store.ErrNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Error", http.StatusBadRequest)
		return
	}
	levels, levelErr := postedLevels(r, panel)
	token := trimSecretInput(r.FormValue("token"))
	tokenErr := ""
	if panel.NeedsSecret && token != "" && !secretValueOK(token) {
		tokenErr = "That token cannot be saved."
	}
	hasSecret := false
	if panel.NeedsSecret {
		_, err := s.store.ConnectionSecret(r.Context(), user.ID, panel.ID)
		hasSecret = err == nil
	}
	if panel.NeedsSecret && token == "" && !hasSecret && tokenErr == "" {
		tokenErr = panel.Title + " needs a token."
	}
	if levelErr != "" || tokenErr != "" {
		detail, err := s.connectionDetail(r.Context(), user.ID, panel, levels)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		msg := levelErr
		if tokenErr != "" {
			msg = tokenErr
		}
		detail.EditToken = token != ""
		s.renderConnections(w, r, user, connPage{Phone: panel.Title, Back: true, Detail: detail, Error: msg}, http.StatusBadRequest)
		return
	}
	wasOn, err := s.store.ConnectionSwitchedOn(r.Context(), user.ID, panel.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if panel.NeedsSecret && token != "" {
		if _, err := s.store.SaveConnectionSecret(r.Context(), user.ID, panel.ID, token); err != nil {
			detail, derr := s.connectionDetail(r.Context(), user.ID, panel, levels)
			if derr != nil {
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			detail.EditToken = true
			s.renderConnections(w, r, user, connPage{Phone: panel.Title, Back: true, Detail: detail, Error: "That token cannot be saved."}, http.StatusBadRequest)
			return
		}
	}
	if err := s.store.SaveConnectionLevels(r.Context(), user.ID, panel.ID, levels); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetConnectionSwitch(r.Context(), user.ID, panel.ID, true); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if err := s.projectConnection(r.Context(), user, panel.ID, true); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if wasOn {
		http.Redirect(w, r, "/settings/connections?notice=saved", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings/connections/"+url.PathEscape(panel.ID)+"?notice=connected", http.StatusSeeOther)
}

func (s *Server) connectionDisconnect(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	panel, found := connect.PanelLookup(r.PathValue("id"))
	if !found {
		s.missing(w, store.ErrNotFound)
		return
	}
	if err := s.store.SetConnectionSwitch(r.Context(), user.ID, panel.ID, false); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if err := s.projectConnection(r.Context(), user, panel.ID, false); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/connections/"+url.PathEscape(panel.ID)+"?notice=disconnected", http.StatusSeeOther)
}

func (s *Server) renderConnections(w http.ResponseWriter, r *http.Request, user store.User, view connPage, status int) {
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Page = "connections"
	p.Title = "Connections"
	if view.Detail.Title != "" {
		p.Title = view.Detail.Title
	}
	p.Connections = view
	s.render(w, status, "app", finish(p))
}

func (s *Server) connectionCards(ctx context.Context, userID string) ([]connCard, error) {
	var cards []connCard
	for _, panel := range connect.Panels() {
		on, err := s.store.ConnectionSwitchedOn(ctx, userID, panel.ID)
		if err != nil {
			return nil, err
		}
		status, action := "Not connected", "Connect"
		if on {
			status, action = "Connected", "Configure"
		}
		cards = append(cards, connCard{
			ID: panel.ID, Title: panel.Title, Blurb: panel.Blurb,
			Status: status, Action: action, On: on,
			Href: "/settings/connections/" + panel.ID,
		})
	}
	return cards, nil
}

func (s *Server) connectionDetail(ctx context.Context, userID string, panel connect.Panel, levels map[string]string) (connDetail, error) {
	on, err := s.store.ConnectionSwitchedOn(ctx, userID, panel.ID)
	if err != nil {
		return connDetail{}, err
	}
	has := false
	mask := ""
	if panel.NeedsSecret {
		_, secErr := s.store.ConnectionSecret(ctx, userID, panel.ID)
		has = secErr == nil
		if has {
			value, verr := s.store.ConnectionValue(ctx, userID, panel.ID)
			if verr != nil {
				return connDetail{}, verr
			}
			mask = tokenMask(value)
		}
	}
	if levels == nil {
		levels, err = s.store.ConnectionLevels(ctx, userID, panel.ID)
		if err != nil {
			return connDetail{}, err
		}
	}
	var tools []connTool
	for _, tool := range panel.Tools {
		level := tool.DefaultLevel()
		if lv := levels[tool.Name]; lv != "" {
			if norm, good := castcfg.NormalizeLevel(lv); good {
				level = norm
			}
		}
		tools = append(tools, connTool{Name: tool.Name, Title: tool.Title, Level: level})
	}
	return connDetail{
		ID: panel.ID, Title: panel.Title, Body: panel.Body, TokenLabel: panel.TokenLabel,
		TokenMask: mask, NeedsSecret: panel.NeedsSecret, On: on, HasSecret: has, Tools: tools,
	}, nil
}

// tokenMask shows the first characters of a saved token. The rest stays
// in the vault. A short value contributes one character so the mask is
// not the secret itself.
func tokenMask(value string) string {
	rs := []rune(strings.TrimSpace(value))
	if len(rs) == 0 {
		return ""
	}
	n := 4
	if len(rs) <= n {
		n = 1
	}
	if len(rs) == 1 {
		return "xxxxxxx"
	}
	return string(rs[:n]) + "-xxxxxxx"
}

func postedLevels(r *http.Request, panel connect.Panel) (map[string]string, string) {
	allowed := map[string]bool{}
	levels := map[string]string{}
	for _, tool := range panel.Tools {
		allowed[tool.Name] = true
		raw := strings.TrimSpace(r.FormValue("level_" + tool.Name))
		if raw == "" {
			levels[tool.Name] = tool.DefaultLevel()
			continue
		}
		norm, good := castcfg.NormalizeLevel(raw)
		if !good {
			return levels, "Choose ask, always allow, or deny."
		}
		levels[tool.Name] = norm
	}
	for key := range r.Form {
		if !strings.HasPrefix(key, "level_") {
			continue
		}
		if !allowed[strings.TrimPrefix(key, "level_")] {
			return levels, "Unknown tool."
		}
	}
	return levels, ""
}

func secretValueOK(value string) bool {
	if value == "" || len(value) > store.SecretValueMax {
		return false
	}
	return !strings.ContainsAny(value, "\x00\n\r")
}

func connectionNotice(kind string) string {
	switch kind {
	case "connected":
		return "Connected. Every agent you own can use this."
	case "saved":
		return "Saved. Every agent you own uses these choices."
	case "disconnected":
		return "Disconnected. Tool choices stay. Every agent you own is off until you connect again."
	default:
		return ""
	}
}

// turnOnAccountConnection is the chat card path. The token is already in
// the vault. The switch turns on for every agent this account owns.
func (s *Server) turnOnAccountConnection(ctx context.Context, user store.User, connection string) error {
	if _, ok := connect.Lookup(connection); !ok {
		return store.ErrInvalid
	}
	if err := s.store.SetConnectionSwitch(ctx, user.ID, connection, true); err != nil {
		return err
	}
	return s.projectConnection(ctx, user, connection, true)
}

// projectConnection pushes the account switch onto every owned agent.
// A proxy that is on gets a grant when the vault has a token. A proxy that
// is off loses the grant. Shell Actions has no grant. The switch itself is
// stored by the caller before this runs, so a refresh cannot turn a
// disconnected proxy back on.
func (s *Server) projectConnection(ctx context.Context, user store.User, id string, on bool) error {
	if _, ok := connect.PanelLookup(id); !ok {
		return store.ErrInvalid
	}
	_, proxy := connect.Lookup(id)
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if proxy {
			if on {
				err = s.store.EnableConnection(ctx, user.ID, agent.ID, id)
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				if err := s.resolvePendingSecrets(ctx, user.ID, agent.ID, id); err != nil {
					return err
				}
			} else {
				err = s.store.RevokeConnection(ctx, user.ID, agent.ID, id)
				if err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
			}
		}
		if err := s.refreshConnectionBundle(ctx, user.ID, agent.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		s.notifyAgent(ctx, user.ID, agent.ID)
	}
	return nil
}

func (s *Server) resolvePendingSecrets(ctx context.Context, userID, agentID, connection string) error {
	rows, err := s.store.ListSecretRequests(ctx, userID, agentID, s.store.Now().Add(-2*time.Hour))
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Status != "pending" || row.Connection != connection {
			continue
		}
		if _, err := s.store.ResolveSecretRequest(ctx, userID, row.ID, "saved"); err != nil {
			return err
		}
	}
	return nil
}

// syncAccountGrants gives an agent a proxy grant when the account switch
// is already on. It does not revoke, so a secrets-page grant can stay
// while the switch is off. A missing token is skipped.
func (s *Server) syncAccountGrants(ctx context.Context, userID, agentID string) error {
	enabled, err := s.store.EnabledConnections(ctx, userID, agentID)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, name := range enabled {
		have[name] = true
	}
	for _, entry := range connect.Catalog() {
		on, err := s.store.ConnectionSwitchedOn(ctx, userID, entry.ID)
		if err != nil {
			return err
		}
		if !on || have[entry.ID] {
			continue
		}
		err = s.store.EnableConnection(ctx, userID, agentID, entry.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}
