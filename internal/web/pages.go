package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if p.Empty {
		p.Page = "first"
		p.Title = "Welcome"
		s.render(w, http.StatusOK, "app", finish(p))
		return
	}
	agents, err := s.store.ListAgents(r.Context(), user.ID)
	if err != nil || len(agents) == 0 {
		p.Page = "first"
		p.Title = "Welcome"
		s.render(w, http.StatusOK, "app", finish(p))
		return
	}
	s.renderChat(w, r, user, agents[0].ID, false, "home")
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	s.renderChat(w, r, user, r.PathValue("id"), r.URL.Query().Get("pane") == "1", "chat")
}

func (s *Server) agentTab(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	tab := "machine"
	switch {
	case strings.HasSuffix(r.URL.Path, "/routines"):
		tab = "agent-routines"
	case strings.HasSuffix(r.URL.Path, "/files"):
		tab = "agent-files"
	}
	s.renderChat(w, r, user, r.PathValue("id"), false, tab)
}

func (s *Server) renderChat(w http.ResponseWriter, r *http.Request, user store.User, id string, pane bool, tab string) {
	agent, err := s.store.GetAgent(r.Context(), user.ID, id)
	if s.missing(w, err) {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Agent = presentAgent(agent, true, userLocation(user.Timezone))
	p.HasAgent = true
	p.Pane = pane
	p.markOn(agent.ID)
	threads, err := s.store.ListThreadsByAgent(r.Context(), user.ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if len(threads) > 0 {
		msgs, err := s.store.ListMessages(r.Context(), user.ID, threads[0].ID)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		p.Messages = presentMessages(msgs, p.Agent, s.store.Now(), userLocation(user.Timezone))
		p.Lives = s.loadLives(r, user, threads[0].ID, p.Agent)
	}
	switch tab {
	case "home":
		p.Page = "home"
		p.Tab = "chat"
	case "chat":
		p.Page = "chat"
		p.Tab = "chat"
	default:
		p.Page = tab
		p.Tab = tab
		p.StubTitle, p.StubBody = tabCopy(tab)
	}
	p.Title = agent.Name
	s.render(w, http.StatusOK, "app", finish(p))
}

func tabCopy(tab string) (string, string) {
	switch tab {
	case "agent-routines":
		return "Routines", "Routines arrive in a later update. Nothing here runs on a schedule yet."
	case "agent-files":
		return "Files", "Files arrive in a later update."
	default:
		return "Machine", "This version does not connect a machine. Status in the sidebar is saved on your account."
	}
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	threads, err := s.store.ListThreadsByAgent(r.Context(), user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	msg, err := s.store.CreateMessage(r.Context(), user.ID, threads[0].ID, "user", user.ID, body)
	if s.bad(w, err) {
		return
	}
	run, err := s.store.CreateRun(r.Context(), user.ID, threads[0].ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if err := s.store.SetMessageRun(r.Context(), user.ID, msg.ID, run.ID); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	// Start runs before this response names the run. The row stays queued
	// until the goroutine marks it streaming, and the event handler does
	// not fail a queued run, so a reader in that gap cannot interrupt it.
	s.runs.Start(runJob(user.ID, threads[0].ID, agent.ID, run.ID, msg.ID, msg.Body, s.priorTurns(r.Context(), user.ID, threads[0].ID, msg.ID)))
	if r.Header.Get("HX-Request") == "true" {
		av := presentAgent(agent, true, userLocation(user.Timezone))
		loc := userLocation(user.Timezone)
		p := page{
			CSRF:     from(r).CSRF,
			Messages: presentMessages([]store.Message{msg}, av, s.store.Now(), loc),
			Lives:    []liveView{{RunID: run.ID, Color: av.Color, Letter: av.Letter, CSRF: from(r).CSRF}},
		}
		for i := range p.Messages {
			p.Messages[i].ShowDay = false
		}
		s.render(w, http.StatusOK, "turn", p)
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID), http.StatusSeeOther)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	sv, err := s.sessionSummary(r, user, sess.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Sessions = sv
	p.Page = "settings"
	p.Title = "Settings"
	p.EditName = r.URL.Query().Get("edit") == "name"
	if n, ok := notices[r.URL.Query().Get("notice")]; ok {
		p.Notice = n
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) saveName(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if err := s.store.UpdateName(r.Context(), user.ID, r.FormValue("name")); err != nil {
		p, _ := s.shell(r, user)
		p.Page = "settings"
		p.Title = "Settings"
		p.EditName = true
		p.FormError = "Enter a name up to 80 characters."
		s.render(w, http.StatusBadRequest, "app", finish(p))
		return
	}
	http.Redirect(w, r, "/settings?notice=name", http.StatusSeeOther)
}

func (s *Server) saveTheme(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if err := s.store.UpdateTheme(r.Context(), user.ID, r.FormValue("theme")); s.bad(w, err) {
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (s *Server) passwordForm(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Page = "password"
	p.Title = "Change password"
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) savePassword(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	cur := r.FormValue("current")
	next := r.FormValue("next")
	again := r.FormValue("again")
	fail := func(msg string) {
		p, _ := s.shell(r, user)
		p.Page = "password"
		p.Title = "Change password"
		p.FormError = msg
		s.render(w, http.StatusBadRequest, "app", finish(p))
	}
	hash, err := s.store.PasswordHash(r.Context(), user.ID)
	if err != nil || !auth.Verify(cur, hash) {
		fail("That password does not match.")
		return
	}
	if len(next) < 12 || len(next) > 1024 {
		fail("Use at least 12 characters.")
		return
	}
	if next != again {
		fail("The new passwords do not match.")
		return
	}
	encoded, err := auth.Hash(next, s.store.Argon())
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	nextSess, err := s.store.ChangePassword(r.Context(), user.ID, sess.ID, encoded)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	maxAge := 0
	if nextSess.Remember {
		maxAge = 30 * 24 * 3600
	}
	s.setCookie(w, cookieSession, seal("session", nextSess.ID, s.key), "/", maxAge)
	clearCookie(w, cookieDevice, "/")
	http.Redirect(w, r, "/settings?notice=password", http.StatusSeeOther)
}

func (s *Server) stubEmail(w http.ResponseWriter, r *http.Request) {
	s.stub(w, r, "Change email", "Email changes are not available in this version. A later update will send a link to confirm a new address.")
}

func (s *Server) stubTwoFactor(w http.ResponseWriter, r *http.Request) {
	s.stub(w, r, "Two-factor", "Managing recovery codes and the authenticator app arrives in a later update. Two-factor stays on.")
}

func (s *Server) stubKeys(w http.ResponseWriter, r *http.Request) {
	s.stub(w, r, "Model keys", "Adding and replacing model keys arrives in a later update. The list on Settings is a placeholder, and nothing here is saved.")
}

func (s *Server) stub(w http.ResponseWriter, r *http.Request, title, body string) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Page = "stub"
	p.Title = title
	p.StubTitle = title
	p.StubBody = body
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) sessionsConfirm(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	sv, err := s.sessionSummary(r, user, sess.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Sessions = sv
	p.Page = "stub"
	p.Title = "Sign out other sessions"
	if sv.OtherCount == 0 {
		p.StubTitle = "No other sessions"
		p.StubBody = "This browser is the only place you are signed in."
	} else {
		p.StubTitle = sv.Confirm
		p.StubBody = sv.OtherLine
		p.FormError = "confirm-sessions"
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) sessionsEnd(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.store.SignOutOthers(r.Context(), user.ID, sess.ID, deviceCookie(r)); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?notice=sessions", http.StatusSeeOther)
}

func (s *Server) newAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Page = "new"
	p.Title = "New agent"
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	sec, err := s.store.SystemSection(r.Context(), user.ID, "agents")
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	agent, _, err := s.store.CreateAgent(r.Context(), user.ID, store.AgentInput{
		SectionID: sec.ID,
		Name:      r.FormValue("name"),
		Title:     r.FormValue("title"),
		Status:    "draft",
		Color:     "c0",
	})
	if err != nil {
		p, _ := s.shell(r, user)
		p.Page = "new"
		p.Title = "New agent"
		p.FormError = "Choose a name that is not already used."
		if errors.Is(err, store.ErrInvalid) {
			p.FormError = "Use a name up to 40 characters."
		}
		s.render(w, http.StatusBadRequest, "app", finish(p))
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID), http.StatusSeeOther)
}

func (s *Server) createSection(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.store.CreateSection(r.Context(), user.ID, r.FormValue("name")); err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back(r), http.StatusSeeOther)
}

func (s *Server) workspacePage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case "/approvals":
		p.Page = "approvals"
		p.Title = "Approvals"
		p.StubTitle = "Approvals"
		p.StubBody = "When an agent needs a yes or no, it shows up here. Nothing is waiting in this version."
	case "/routines":
		p.Page = "routines"
		p.Title = "Routines"
		p.StubTitle = "Routines"
		p.StubBody = "Prompts that run on a schedule arrive in a later update."
	case "/files":
		p.Page = "files"
		p.Title = "Files"
		p.StubBody = "A library of files arrives in a later update."
		p.StubTitle = "Files"
	default:
		p.Page = "usage"
		p.Title = "Usage"
		p.StubTitle = "Usage"
		p.StubBody = "Machine time is not metered in this version. The amount in the sidebar is a placeholder."
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) shell(r *http.Request, user store.User) (page, error) {
	sections, err := s.store.ListSections(r.Context(), user.ID)
	if err != nil {
		return page{}, err
	}
	agents, err := s.store.ListAgents(r.Context(), user.ID)
	if err != nil {
		return page{}, err
	}
	loc := userLocation(user.Timezone)
	by := map[string][]agentView{}
	approvals := 0
	for _, a := range agents {
		approvals += a.Approvals
		by[a.SectionID] = append(by[a.SectionID], presentAgent(a, false, loc))
	}
	var sv []sectionView
	for _, sec := range sections {
		sv = append(sv, sectionView{ID: sec.ID, Name: sec.Name, Kind: sec.Kind, Agents: by[sec.ID]})
	}
	usage := "$0.00"
	if len(agents) > 0 {
		usage = "$7.42"
	}
	return page{
		Theme:     user.Theme,
		CSRF:      from(r).CSRF,
		User:      user,
		HasUser:   true,
		Initials:  initials(user.DisplayName),
		Sections:  sv,
		Approvals: approvals,
		Usage:     usage,
		Empty:     len(agents) == 0,
		RoleLine:  roleLine(user.Role, user.WorkspaceName),
		PasswordOn: func() string {
			d := monthDay(user.PasswordChangedAt, loc)
			if d == "" {
				return "Password is set"
			}
			return "Last changed " + d
		}(),
	}, nil
}

func (p *page) markOn(id string) {
	for i := range p.Sections {
		for j := range p.Sections[i].Agents {
			p.Sections[i].Agents[j].On = p.Sections[i].Agents[j].ID == id
		}
	}
}

func (s *Server) sessionSummary(r *http.Request, user store.User, currentID string) (sessView, error) {
	loc := userLocation(user.Timezone)
	list, err := s.store.ListSessions(r.Context(), user.ID)
	if err != nil {
		return sessView{}, err
	}
	var current store.Session
	var labels []string
	for _, sess := range list {
		if sess.ID == currentID {
			current = sess
			continue
		}
		labels = append(labels, deviceLabel(sess.UserAgent))
	}
	line := "Signed in since " + current.CreatedAt.In(loc).Format("Jan 2") + " · " + deviceLabel(current.UserAgent)
	other := "No other sessions"
	n := len(labels)
	switch n {
	case 0:
	case 1:
		other = "1 other session: " + labels[0]
	default:
		other = strings.TrimSpace(strings.Join([]string{
			itoa(n) + " other sessions:",
			strings.Join(labels, ", "),
		}, " "))
	}
	confirm := "Sign out other sessions?"
	if n == 1 {
		confirm = "Sign out 1 other session?"
	} else if n > 1 {
		confirm = "Sign out " + itoa(n) + " other sessions?"
	}
	return sessView{CurrentLine: line, OtherLine: other, OtherCount: n, Confirm: confirm}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func presentMessages(msgs []store.Message, agent agentView, now time.Time, loc *time.Location) []msgView {
	if loc == nil {
		loc = userLocation("")
	}
	now = now.In(loc)
	var out []msgView
	prev := ""
	for _, m := range msgs {
		day := dayLabel(m.CreatedAt, now, loc)
		v := msgView{ID: m.ID, Author: m.AuthorKind, Body: m.Body, Stamp: clockLabel(m.CreatedAt, loc), Day: day}
		if day != "" && day != prev {
			v.ShowDay = true
			prev = day
		}
		if m.AuthorKind != "user" {
			v.Color = agent.Color
			v.Letter = agent.Letter
		}
		out = append(out, v)
	}
	return out
}

func (s *Server) missing(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if err == store.ErrNotFound {
		writeText(w, http.StatusNotFound, "Not found\n")
		return true
	}
	writeText(w, http.StatusInternalServerError, "Error\n")
	return true
}

func (s *Server) bad(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if err == store.ErrInvalid {
		writeText(w, http.StatusBadRequest, "Bad request\n")
		return true
	}
	if err == store.ErrNotFound {
		writeText(w, http.StatusNotFound, "Not found\n")
		return true
	}
	writeText(w, http.StatusInternalServerError, "Error\n")
	return true
}

var notices = map[string]string{
	"name":     "Name saved.",
	"password": "Password changed.",
	"sessions": "Other sessions were signed out.",
}

func back(r *http.Request) string {
	ref := r.Referer()
	u, err := url.Parse(ref)
	if err != nil || u.Path == "" || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	if u.Host != "" && !strings.EqualFold(u.Host, r.Host) {
		return "/"
	}
	return u.RequestURI()
}
