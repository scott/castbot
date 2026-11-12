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
	p.Page = "home"
	p.Title = "Agents"
	p.AgentCount = len(agents)
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	s.renderChat(w, r, user, r.PathValue("id"), r.URL.Query().Get("pane") == "1", "chat", http.StatusOK, "")
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
	s.renderChat(w, r, user, r.PathValue("id"), false, tab, http.StatusOK, "")
}

func (s *Server) renderChat(w http.ResponseWriter, r *http.Request, user store.User, id string, pane bool, tab string, status int, formError string) {
	s.renderAgent(w, r, user, id, pane, tab, status, formError)
}

func (s *Server) renderAgent(w http.ResponseWriter, r *http.Request, user store.User, id string, pane bool, tab string, status int, formError string) error {
	agent, err := s.store.GetAgent(r.Context(), user.ID, id)
	if s.missing(w, err) {
		return nil
	}
	machine := s.syncMachine(r.Context(), user.ID, &agent)
	if machine.LastWake != "" {
		if t, ok := inZone(machine.LastWake, userLocation(user.Timezone)); ok {
			machine.LastWake = t.Format("Jan 2, 3:04 PM")
		}
	}
	if machine.ShowFirst {
		if machine.FirstRunning == "" {
			machine.FirstRunning = "unknown"
		} else if t, ok := inZone(machine.FirstRunning, userLocation(user.Timezone)); ok {
			machine.FirstRunning = t.Format("Jan 2, 3:04 PM")
		}
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return nil
	}
	p.Agent = s.paintAgent(r.Context(), user.ID, agent, true, userLocation(user.Timezone))
	p.HasAgent = true
	p.Machine = machine
	p.FormError = formError
	p.Pane = pane
	p.markOn(agent.ID)
	threads, err := s.store.ListThreadsByAgent(r.Context(), user.ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return nil
	}
	current, err := s.pickThread(r, user.ID, agent.ID)
	if err != nil && len(threads) > 0 {
		http.Error(w, "Not found\n", http.StatusNotFound)
		return nil
	}
	var cliThreads bool
	for _, t := range threads {
		if t.Kind == "cli" {
			cliThreads = true
		}
	}
	if cliThreads {
		for _, t := range threads {
			title := t.Title
			if title == "" {
				title = "Chat"
			}
			if t.Kind != "cli" {
				title = "Chat"
			}
			p.Threads = append(p.Threads, threadView{
				Title: title,
				Href:  "/agents/" + url.PathEscape(agent.ID) + "?thread=" + url.QueryEscape(t.ID),
				On:    t.ID == current.ID,
			})
		}
	}
	if current.ID != "" {
		p.ThreadID = current.ID
		msgs, err := s.store.ListMessages(r.Context(), user.ID, current.ID)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return nil
		}
		p.Messages = presentMessages(msgs, p.Agent, s.store.Now(), userLocation(user.Timezone))
		s.linkMentionedFiles(r.Context(), user.ID, agent.ID, p.Messages)
		p.Lives = s.loadLives(r, user, current.ID, p.Agent)
		if rows, err := s.store.ListThreadApprovals(r.Context(), user.ID, current.ID); err == nil {
			live := map[string]bool{}
			for _, lv := range p.Lives {
				if !lv.Failed {
					live[lv.RunID] = true
				}
			}
			var cards []cardView
			for _, row := range rows {
				cards = append(cards, s.presentApproval(row, user, from(r).CSRF))
			}
			p.Messages = weaveCards(p.Messages, cards, live)
		}
	}
	panelAsk := requestedPanel(r)
	if wantsConfirm(r) {
		panelAsk = "machine"
	}
	if tab == "machine" || tab == "" || panelAsk == "machine" || strings.HasSuffix(r.URL.Path, "/machine/card") {
		tokens, err := s.store.ListCLITokens(r.Context(), user.ID, agent.ID)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return nil
		}
		loc := userLocation(user.Timezone)
		for _, tok := range tokens {
			p.CLITokens = append(p.CLITokens, cliTokenView(tok, loc))
		}
		if slug, err := s.store.CLISlug(r.Context(), user.ID, agent.ID); err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return nil
		} else {
			p.CLIName = slug
		}
	}
	if flash, ok := r.Context().Value(flashKey{}).(cliFlash); ok {
		p.CLIBlob = flash.blob
		p.CLINote = flash.note
	}
	s.attachRoutines(r, user, agent, current, &p)
	if filter := strings.TrimSpace(r.URL.Query().Get("threads")); filter != "" {
		if err := s.filterRoutineThreads(r, user, agent, filter, &p); s.missing(w, err) {
			return nil
		}
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
	p.StartMachine = machine.CanStart && !machine.CanRetry
	if r.URL.Query().Get("panel") == "secrets" {
		p.Vault.Panel = true
		p.Pane = true
		if err := s.fillAgentSecrets(r, user, agent, &p); err != nil {
			if s.missing(w, err) {
				return nil
			}
			http.Error(w, "Error", http.StatusInternalServerError)
			return nil
		}
	}
	s.attachReplace(r.Context(), user, agent, machine, &p)
	if wantsConfirm(r) && p.Replace.CanReplace {
		p.Replace.Confirm = true
		if vm, err := s.store.LiveVM(r.Context(), user.ID, agent.ID); err == nil && vm.State == "paused" {
			p.Replace.PausedNote = true
		}
	}
	p.LowSpace = s.diskLow()
	filesOn := (r.URL.Query().Get("panel") == "files" || strings.HasSuffix(r.URL.Path, "/files")) && p.Page != "machine"
	if filesOn {
		p.Page = "chat"
		p.Tab = "chat"
		p.Pane = true
		p.Panel = "files"
		p.Files = s.loadFiles(r, user, agent)
	}
	if !s.applyPanel(w, r, user, agent, &p) {
		return nil
	}
	if status == 0 {
		status = http.StatusOK
	}
	name := "app"
	switch {
	case strings.HasSuffix(r.URL.Path, "/machine/card"):
		name = "machinePanel"
	case panelPartial(r):
		name = panelTemplate(p)
	default:
		if p.HasAgent {
			s.fillToasts(w, r, agent.ID, &p)
		}
	}
	return s.render(w, status, name, finish(p))
}

func tabCopy(tab string) (string, string) {
	switch tab {
	case "agent-routines":
		return "Routines", "Open the Routines tab in the agent panel. Each run wakes the machine and gets its own thread."
	case "agent-files":
		return "Files", "Files arrive in a later update."
	default:
		return "Machine", "State from the cloud API. Checking it does not wake the machine."
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
	if s.refuseReplacing(w, r, user.ID, agent.ID) {
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	extra, announced := s.pendingPathText(r, user.ID, agent.ID, agent.Name)
	if extra != "" {
		if body != "" {
			body += "\n\n" + extra
		} else {
			body = extra
		}
	}
	thread, err := s.pickThread(r, user.ID, agent.ID)
	if s.missing(w, err) {
		return
	}
	msg, err := s.store.CreateMessage(r.Context(), user.ID, thread.ID, "user", user.ID, body)
	if s.bad(w, err) {
		return
	}
	if len(announced) > 0 {
		_ = s.store.MarkAnnounced(r.Context(), user.ID, announced)
	}
	var run store.Run
	s.withAgent(agent.ID, func() {
		busy, _ := s.store.AgentBusy(r.Context(), user.ID, agent.ID)
		created, cerr := s.store.CreateRun(r.Context(), user.ID, thread.ID, agent.ID)
		if cerr != nil {
			return
		}
		if cerr = s.store.SetMessageRun(r.Context(), user.ID, msg.ID, created.ID); cerr != nil {
			return
		}
		run = created
		if busy {
			return
		}
		// Start runs before this response names the run. The row stays queued
		// until the goroutine marks it streaming, and the event handler does
		// not fail a queued run, so a reader in that gap cannot interrupt it.
		s.runs.Start(runJob(user.ID, thread.ID, agent.ID, run.ID, msg.ID, msg.Body, thread.CastSessionID, s.priorTurns(r.Context(), user.ID, thread.ID, msg.ID)))
	})
	if run.ID == "" {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		av := presentAgent(agent, true, userLocation(user.Timezone))
		loc := userLocation(user.Timezone)
		stamp := ""
		if vm, err := s.store.LiveVM(r.Context(), user.ID, agent.ID); err == nil && vm.State == "paused" {
			stamp = "Waking"
		}
		p := page{
			CSRF:     from(r).CSRF,
			Messages: presentMessages([]store.Message{msg}, av, s.store.Now(), loc),
			Lives:    []liveView{{RunID: run.ID, Color: av.Color, Letter: av.Letter, CSRF: from(r).CSRF, Status: stamp}},
		}
		for i := range p.Messages {
			p.Messages[i].ShowDay = false
		}
		s.render(w, http.StatusOK, "turn", p)
		return
	}
	target := "/agents/" + url.PathEscape(agent.ID)
	if thread.Kind == "cli" {
		target += "?thread=" + url.QueryEscape(thread.ID)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
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
	if sum, err := s.vaultSummary(r.Context(), user); err == nil {
		p.Vault = sum
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
	if _, _, ok := s.requireUser(w, r); !ok {
		return
	}
	http.Redirect(w, r, "/settings/secrets", http.StatusSeeOther)
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
	form, err := s.blankForm(r.Context(), user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Page = "new"
	p.Title = "New agent"
	p.AgentForm = form
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if r.FormValue("form") == "agent" {
		s.saveNewAgent(w, r, user)
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
		f, ferr := s.blankForm(r.Context(), user)
		if ferr != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		f.Name = r.FormValue("name")
		f.Title = r.FormValue("title")
		f.NameErr = "Choose a name that is not already used."
		if errors.Is(err, store.ErrInvalid) {
			f.NameErr = "Use a name up to 40 characters."
		}
		p.AgentForm = f
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
		p.StubBody = "When an agent needs a yes or no, it shows up here. Nothing is waiting."
		if rows, err := s.store.ListPendingApprovals(r.Context(), user.ID); err == nil {
			for _, row := range rows {
				p.Cards = append(p.Cards, s.presentApproval(row, user, from(r).CSRF))
			}
			if len(p.Cards) > 0 {
				p.StubBody = "When an agent needs a yes or no, it shows up here."
			}
		}
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
		by[a.SectionID] = append(by[a.SectionID], s.paintAgent(r.Context(), user.ID, a, false, loc))
	}
	var sv []sectionView
	for _, sec := range sections {
		sv = append(sv, sectionView{ID: sec.ID, Name: sec.Name, Kind: sec.Kind, Agents: by[sec.ID]})
	}
	p := page{
		Theme:      user.Theme,
		CSRF:       from(r).CSRF,
		User:       user,
		HasUser:    true,
		Initials:   initials(user.DisplayName),
		Sections:   sv,
		Approvals:  approvals,
		AgentCount: len(agents),
		Empty:      len(agents) == 0,
		RoleLine:   roleLine(user.Role, user.WorkspaceName),
		PasswordOn: func() string {
			d := monthDay(user.PasswordChangedAt, loc)
			if d == "" {
				return "Password is set"
			}
			return "Last changed " + d
		}(),
	}
	s.attachSearch(r, &p)
	return p, nil
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
		v := msgView{ID: m.ID, Author: m.AuthorKind, Body: m.Body, Text: m.Body, Stamp: clockLabel(m.CreatedAt, loc), Day: day, At: m.CreatedAt, Origin: m.Origin}
		if m.Origin == "routine" && m.AuthorKind == "user" {
			v.Prompt = true
		}
		if m.AuthorKind == "user" && !v.Prompt {
			text, lines, names := splitUserBody(m.Body)
			v.Text = text
			v.PathLines = lines
			for _, name := range names {
				v.Chips = append(v.Chips, chipView{Name: name})
			}
		}
		if m.AuthorKind == "system" {
			if card, ok := cliCard(m.Body, m.ID); ok {
				v.HasCard = true
				v.Card = card
			}
		}
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

func weaveCards(msgs []msgView, cards []cardView, live map[string]bool) []msgView {
	var kept []cardView
	for _, card := range cards {
		if live[card.RunID] {
			continue
		}
		kept = append(kept, card)
	}
	if len(kept) == 0 {
		return msgs
	}
	var out []msgView
	i := 0
	for _, card := range kept {
		for i < len(msgs) && (msgs[i].At == "" || msgs[i].At <= card.At) {
			out = append(out, msgs[i])
			i++
		}
		out = append(out, msgView{HasCard: true, Card: card, At: card.At})
	}
	out = append(out, msgs[i:]...)
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
