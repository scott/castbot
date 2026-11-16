package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

func (s *Server) fillWorkspace(ctx context.Context, user store.User, p *page) {
	p.CanInvite = user.Role == "owner"
	if people, err := s.store.ListMembers(ctx, user.ID); err == nil {
		for _, person := range people {
			p.Members = append(p.Members, memberView{
				ID: person.ID, Name: person.DisplayName, Email: person.Email,
				Role: memberRole(person.Role), You: person.ID == user.ID,
			})
		}
	}
	if rows, err := s.store.ListInvites(ctx, user.ID); err == nil {
		for _, row := range rows {
			p.Invites = append(p.Invites, inviteView{ID: row.ID, Email: row.Email, Name: row.DisplayName, When: "Expires in 7 days"})
		}
	}
	if used, max, err := s.store.Quota(ctx, user.ID); err == nil {
		p.QuotaMachines = fmt.Sprintf("%d of %d", used, max)
	}
	if n, err := s.store.UploadUsage(ctx, user.ID, user.WorkspaceID); err == nil {
		p.QuotaUploads = formatBytes(n) + " of 2 GB"
	}
}

func memberRole(role string) string {
	if role == "member" {
		return "Member"
	}
	return "Owner"
}

func inviteURL(r *http.Request, token string) string {
	scheme := "https"
	if r.TLS == nil && !strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	return scheme + "://" + r.Host + "/invite/" + token
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	_, token, err := s.store.CreateInvite(r.Context(), user.ID, r.FormValue("email"), r.FormValue("name"))
	p, perr := s.settingsView(r, user, sess)
	if perr != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		p.FormError = "That email already has an account."
		p.InviteEmail = strings.TrimSpace(r.FormValue("email"))
		s.render(w, http.StatusConflict, "app", finish(p))
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		p.FormError = "Enter a name and an email address."
		s.render(w, http.StatusBadRequest, "app", finish(p))
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.InviteLink = inviteURL(r, token)
	p.Notice = "Copy this invite link now. It is shown once."
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) settingsView(r *http.Request, user store.User, sess store.Session) (page, error) {
	p, err := s.shell(r, user)
	if err != nil {
		return page{}, err
	}
	sv, err := s.sessionSummary(r, user, sess.ID)
	if err != nil {
		return page{}, err
	}
	p.Sessions = sv
	p.Page = "settings"
	p.Title = "Settings"
	if sum, err := s.vaultSummary(r.Context(), user); err == nil {
		p.Vault = sum
	}
	s.fillAccountUsage(r.Context(), user, &p)
	s.fillWorkspace(r.Context(), user, &p)
	return p, nil
}

func (s *Server) revokeInvite(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if s.missing(w, s.store.RevokeInvite(r.Context(), user.ID, r.PathValue("id"))) {
		return
	}
	http.Redirect(w, r, "/settings#people", http.StatusSeeOther)
}

func inviteForm(r *http.Request, owner store.User, row store.Invite) page {
	p := page{
		Title: "Join " + owner.WorkspaceName, Email: row.Email,
		Text: owner.DisplayName, StubTitle: owner.WorkspaceName, StubBody: row.DisplayName,
		PathToken: r.PathValue("token"),
	}
	// A signed-in person keeps their theme. A logged-out invitee uses the sign-in default.
	if user := from(r).User; user != nil {
		switch user.Theme {
		case "light", "dark", "system":
			p.Theme = user.Theme
		}
	}
	return p
}

func (s *Server) inviteGet(w http.ResponseWriter, r *http.Request) {
	row, owner, ok := s.pendingInvite(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "invite", authPage(from(r), inviteForm(r, owner, row)))
}

func (s *Server) invitePost(w http.ResponseWriter, r *http.Request) {
	row, owner, ok := s.pendingInvite(w, r)
	if !ok {
		return
	}
	password := r.FormValue("password")
	again := r.FormValue("again")
	base := inviteForm(r, owner, row)
	if len(password) < 12 || len(password) > 1024 || password != again {
		base.FormError = "Use 12 or more characters, and enter the password twice."
		s.render(w, http.StatusBadRequest, "invite", authPage(from(r), base))
		return
	}
	hash, err := auth.Hash(password, s.store.Argon())
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	user, err := s.store.AcceptInvite(r.Context(), r.PathValue("token"), hash, secret)
	if errors.Is(err, store.ErrNotFound) {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		base.FormError = "That email already has an account."
		s.render(w, http.StatusConflict, "invite", authPage(from(r), base))
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	sess, err := s.store.CreateSession(r.Context(), user.ID, userAgent(r), false)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, cookieSession, seal("session", sess.ID, s.key), "/", 0)
	done := page{
		Title: "Save your authenticator secret", Email: user.Email,
		InviteSecret: secret, InviteURL: auth.OTPAuthURL(user.Email, secret),
	}
	s.render(w, http.StatusOK, "inviteSecret", authPage(from(r), done))
}

func (s *Server) pendingInvite(w http.ResponseWriter, r *http.Request) (store.Invite, store.User, bool) {
	row, err := s.store.PeekInvite(r.Context(), r.PathValue("token"))
	if err != nil {
		writeText(w, http.StatusNotFound, "Not found\n")
		return store.Invite{}, store.User{}, false
	}
	owner, err := s.store.GetUser(r.Context(), row.UserID)
	if err != nil || owner.Disabled {
		writeText(w, http.StatusNotFound, "Not found\n")
		return store.Invite{}, store.User{}, false
	}
	return row, owner, true
}

func (s *Server) groupNew(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	n, err := s.groupAgentCount(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if n < 2 {
		s.renderGroupShort(w, r, user)
		return
	}
	http.Redirect(w, r, "/?list=1", http.StatusSeeOther)
}

func (s *Server) groupForm(r *http.Request, user store.User) (page, error) {
	p, err := s.shell(r, user)
	if err != nil {
		return page{}, err
	}
	agents, err := s.store.ListAgents(r.Context(), user.ID)
	if err != nil {
		return page{}, err
	}
	loc := userLocation(user.Timezone)
	for _, agent := range agents {
		if agent.Status == "deleted" {
			continue
		}
		p.Group.Agents = append(p.Group.Agents, presentAgent(agent, false, loc))
	}
	people, err := s.store.ListMembers(r.Context(), user.ID)
	if err != nil {
		return page{}, err
	}
	for _, person := range people {
		if person.ID == user.ID {
			continue
		}
		p.Members = append(p.Members, memberView{ID: person.ID, Name: person.DisplayName, Email: person.Email, Role: memberRole(person.Role)})
	}
	p.Page = "group-new"
	p.Title = "New group"
	return p, nil
}

func (s *Server) groupCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	n, err := s.groupAgentCount(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if n < 2 {
		s.renderGroupShort(w, r, user)
		return
	}
	th, err := s.store.StartGroup(r.Context(), user.ID)
	if s.missing(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, groupPanelPath(th.ID), http.StatusSeeOther)
}

func (s *Server) groupAgentCount(r *http.Request, user store.User) (int, error) {
	agents, err := s.store.ListAgents(r.Context(), user.ID)
	if err != nil {
		return 0, err
	}
	return len(agents), nil
}

func (s *Server) renderGroupShort(w http.ResponseWriter, r *http.Request, user store.User) {
	p, err := s.groupForm(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) groupShow(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.groupPage(r, user, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("panel") == "group" || panelPartial(r) {
		if err := s.attachGroupPanel(r, user, &p); err != nil {
			if s.missing(w, err) {
				return
			}
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
	}
	if panelPartial(r) {
		s.render(w, http.StatusOK, "groupPane", p)
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) groupFeed(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.groupPage(r, user, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "groupFeed", p)
}

func (s *Server) groupPost(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	threadID := r.PathValue("id")
	th, err := s.store.GroupAccess(r.Context(), user.ID, threadID)
	if s.missing(w, err) {
		return
	}
	msg, err := s.store.CreateGroupMessage(r.Context(), user.ID, threadID, r.FormValue("body"))
	if errors.Is(err, store.ErrInvalid) {
		p, perr := s.groupPage(r, user, threadID)
		if perr != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		p.FormError = "Enter a message."
		if r.Header.Get("HX-Request") == "true" {
			s.render(w, http.StatusBadRequest, "groupFeed", p)
			return
		}
		s.render(w, http.StatusBadRequest, "app", finish(p))
		return
	}
	if s.missing(w, err) {
		return
	}
	agents, err := s.store.GroupAgents(r.Context(), user.ID, threadID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if ask, isAsk := store.ParseGroupAsk(msg.Body); isAsk && len(store.MentionedAgents(agents, msg.Body)) == 0 {
		note, path := s.applyGroupAsk(r, user, th, ask)
		if path == "" {
			path = "/groups/" + url.PathEscape(threadID)
			if note != "" {
				path += "?ask=" + url.QueryEscape(note)
			}
		}
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, path, http.StatusSeeOther)
		return
	}
	s.startGroupReplies(r.Context(), th, msg, agents)
	if r.Header.Get("HX-Request") == "true" {
		p, err := s.groupPage(r, user, threadID)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		s.render(w, http.StatusOK, "groupFeed", p)
		return
	}
	http.Redirect(w, r, "/groups/"+url.PathEscape(threadID), http.StatusSeeOther)
}

func (s *Server) groupPage(r *http.Request, user store.User, threadID string) (page, error) {
	th, err := s.store.GroupAccess(r.Context(), user.ID, threadID)
	if err != nil {
		return page{}, err
	}
	p, err := s.shell(r, user)
	if err != nil {
		return page{}, err
	}
	markGroup(&p, th.ID)
	cards, err := s.store.ListGroupCards(r.Context(), user.ID)
	if err != nil {
		return page{}, err
	}
	var card store.GroupCard
	for _, row := range cards {
		if row.ID == th.ID {
			card = row
			break
		}
	}
	agents, err := s.store.GroupAgents(r.Context(), user.ID, th.ID)
	if err != nil {
		return page{}, err
	}
	humans, err := s.store.GroupHumans(r.Context(), user.ID, th.ID)
	if err != nil {
		return page{}, err
	}
	msgs, err := s.store.ListMessages(r.Context(), th.UserID, th.ID)
	if err != nil {
		return page{}, err
	}
	open, err := s.store.GroupOpen(r.Context(), user.ID, th.ID)
	if err != nil {
		return page{}, err
	}
	loc := userLocation(user.Timezone)
	p.Page = "group"
	p.Title = th.Title
	p.Messages = presentGroup(msgs, agents, humans, s.store.Now(), loc)
	p.Group = groupPage{
		ID: th.ID, Title: th.Title, Who: groupWho(agents, humans), Poll: open,
		Owner: th.UserID == user.ID, Pinned: card.Pinned, SectionID: card.SectionID,
		Letter: letter(th.Title),
		Place:  groupPlace(card.Pinned, sectionName(p, card.SectionID)),
		Note:   askSentence(r.URL.Query().Get("ask"), sectionName(p, card.SectionID)),
	}
	return p, nil
}

func markGroup(p *page, id string) {
	for i := range p.Groups {
		p.Groups[i].On = p.Groups[i].ID == id
	}
	for i := range p.Sections {
		for j := range p.Sections[i].Groups {
			p.Sections[i].Groups[j].On = p.Sections[i].Groups[j].ID == id
		}
	}
}

func sectionName(p page, id string) string {
	if id == "" {
		return ""
	}
	for _, sec := range p.Sections {
		if sec.ID == id {
			return sec.Name
		}
	}
	return ""
}

func (s *Server) startGroupReplies(ctx context.Context, th store.Thread, visible store.Message, agents []store.Agent) {
	s.fanGroup(ctx, th, visible, agents, true, "")
}

func groupPlace(pinned bool, section string) string {
	label := "Group chat"
	if pinned {
		label = "Pinned"
	}
	if section == "" {
		return label
	}
	return label + " · " + section
}

func groupWho(agents []store.Agent, humans []store.User) string {
	var names []string
	for _, agent := range agents {
		names = append(names, agent.Name)
	}
	if len(humans) > 1 {
		for _, person := range humans {
			names = append(names, person.DisplayName)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "With " + englishList(names) + "."
}

func englishList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return strings.Join(names[:len(names)-1], ", ") + ", and " + names[len(names)-1]
	}
}

func presentGroup(msgs []store.Message, agents []store.Agent, humans []store.User, now time.Time, loc *time.Location) []msgView {
	if loc == nil {
		loc = userLocation("")
	}
	now = now.In(loc)
	byAgent := map[string]store.Agent{}
	for _, agent := range agents {
		byAgent[agent.ID] = agent
	}
	byHuman := map[string]store.User{}
	for _, person := range humans {
		byHuman[person.ID] = person
	}
	var out []msgView
	prev := ""
	for _, m := range msgs {
		if m.Origin == "mention" || m.Origin == "look" || m.Origin == "pass" || m.Origin == "setup" || m.Origin == "relay" {
			continue
		}
		day := dayLabel(m.CreatedAt, now, loc)
		v := msgView{ID: m.ID, Author: m.AuthorKind, Body: m.Body, Text: m.Body, Stamp: clockLabel(m.CreatedAt, loc), Day: day, At: m.CreatedAt, Origin: m.Origin}
		if m.AuthorKind == "user" {
			if person, ok := byHuman[m.AuthorID]; ok {
				v.Name = person.DisplayName
			}
		}
		if m.AuthorKind == "agent" {
			agent, ok := byAgent[m.AuthorID]
			if !ok {
				agent = store.Agent{Name: "Agent", Color: "c0"}
			}
			v.Name = agent.Name
			v.Color = agent.Color
			v.Letter = letter(agent.Name)
			if v.Color == "" {
				v.Color = "c0"
			}
		}
		if m.AuthorKind == "system" || m.Origin == "notice" {
			v.Sys = true
		}
		if day != "" && day != prev {
			v.ShowDay = true
			prev = day
		}
		fillMessage(&v)
		out = append(out, v)
	}
	return out
}
