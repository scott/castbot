package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func (s *Server) withGroup(id string, fn func()) {
	s.groupMu.Lock()
	if s.groupGates == nil {
		s.groupGates = map[string]*sync.Mutex{}
	}
	mu := s.groupGates[id]
	if mu == nil {
		mu = &sync.Mutex{}
		s.groupGates[id] = mu
	}
	s.groupMu.Unlock()
	mu.Lock()
	defer mu.Unlock()
	fn()
}

func lookPrompt(body string) string {
	return runtime.GroupLookMark + "\n" + strings.TrimSpace(body) + "\nIf you have nothing to add, reply with exactly " + runtime.GroupLookPass + "."
}

func (s *Server) settleGroupLook(job run.Job, status string) {
	if status != "done" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	th, err := s.store.GetThread(ctx, job.UserID, job.ThreadID)
	if err != nil || th.Kind != "group" {
		return
	}
	hidden, promptID, err := s.store.ConcealGroupPass(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	if hidden {
		s.relookPasser(ctx, th, job.AgentID, promptID)
		return
	}
	prompt, err := s.store.GroupPrompt(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	if prompt.Origin != "look" && prompt.Origin != "mention" {
		return
	}
	turn, err := s.store.GroupTurnBefore(ctx, job.UserID, th.ID, prompt)
	if err != nil {
		return
	}
	agents, err := s.store.GroupAgents(ctx, th.UserID, th.ID)
	if err != nil {
		return
	}
	s.fanGroup(ctx, th, turn, agents, false, job.AgentID)
}

func (s *Server) relookPasser(ctx context.Context, th store.Thread, agentID, promptID string) {
	newer, err := s.store.GroupHasVisibleAfter(ctx, th.UserID, th.ID, promptID)
	if err != nil || !newer {
		return
	}
	prompt, err := s.store.GetMessage(ctx, th.UserID, promptID)
	if err != nil {
		return
	}
	turn, err := s.store.GroupTurnBefore(ctx, th.UserID, th.ID, prompt)
	if err != nil {
		return
	}
	agents, err := s.store.GroupAgents(ctx, th.UserID, th.ID)
	if err != nil {
		return
	}
	var agent store.Agent
	found := false
	for _, row := range agents {
		if row.ID == agentID {
			agent = row
			found = true
			break
		}
	}
	if !found {
		return
	}
	s.withGroup(th.ID, func() {
		posted, open, err := s.store.GroupTurnAgents(ctx, th.UserID, th.ID, turn.ID)
		if err != nil || posted[agentID] || open[agentID] {
			return
		}
		s.startGroupRun(ctx, th, agent, turn, "look")
	})
}

func (s *Server) fanGroup(ctx context.Context, th store.Thread, visible store.Message, agents []store.Agent, mentions bool, skipID string) {
	s.withGroup(th.ID, func() {
		mentioned := map[string]bool{}
		if mentions {
			for _, agent := range store.MentionedAgents(agents, visible.Body) {
				mentioned[agent.ID] = true
				s.startGroupRun(ctx, th, agent, visible, "mention")
			}
		}
		posted, open, err := s.store.GroupTurnAgents(ctx, th.UserID, th.ID, visible.ID)
		if err != nil {
			return
		}
		for _, agent := range agents {
			if agent.ID == skipID || mentioned[agent.ID] || posted[agent.ID] || open[agent.ID] {
				continue
			}
			s.startGroupRun(ctx, th, agent, visible, "look")
		}
	})
}

func (s *Server) startGroupRun(ctx context.Context, th store.Thread, agent store.Agent, visible store.Message, origin string) {
	text := visible.Body
	if origin == "look" {
		text = lookPrompt(visible.Body)
	}
	s.withAgent(agent.ID, func() {
		link, err := s.store.CreateOriginMessage(ctx, th.UserID, th.ID, "user", visible.AuthorID, visible.Body, origin)
		if err != nil {
			return
		}
		busy, _ := s.store.AgentBusy(ctx, th.UserID, agent.ID)
		created, err := s.store.CreateRun(ctx, th.UserID, th.ID, agent.ID)
		if err != nil {
			return
		}
		if err := s.store.SetMessageRun(ctx, th.UserID, link.ID, created.ID); err != nil {
			return
		}
		if busy {
			return
		}
		s.runs.Start(runJob(th.UserID, th.ID, agent.ID, created.ID, visible.ID, text, th.CastSessionID, s.priorTurns(ctx, th.UserID, th.ID, visible.ID)))
	})
}

func (s *Server) groupEdit(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.groupEditor(r, user, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) groupEditSave(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	err := s.store.UpdateGroup(r.Context(), user.ID, id, r.FormValue("title"), r.Form["agent"], r.Form["member"])
	if errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrConflict) {
		p, perr := s.groupEditor(r, user, id)
		if s.missing(w, perr) {
			return
		}
		if perr != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		if errors.Is(err, store.ErrConflict) {
			p.Group.Note = "Wait until the reply finishes."
		} else {
			p.FormError = "Enter a title and choose at least two of your agents."
		}
		s.render(w, http.StatusBadRequest, "app", finish(p))
		return
	}
	if s.missing(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, groupPanelPath(id), http.StatusSeeOther)
}

func (s *Server) groupEditor(r *http.Request, user store.User, threadID string) (page, error) {
	th, err := s.store.GroupAccess(r.Context(), user.ID, threadID)
	if err != nil {
		return page{}, err
	}
	if th.UserID != user.ID {
		return page{}, store.ErrNotFound
	}
	p, err := s.groupPage(r, user, threadID)
	if err != nil {
		return page{}, err
	}
	if err := s.fillOwnerGroup(r, user, &p); err != nil {
		return page{}, err
	}
	p.Panel = "group"
	p.Pane = true
	return p, nil
}

func (s *Server) attachGroupPanel(r *http.Request, user store.User, p *page) error {
	p.Panel = "group"
	p.Pane = true
	p.Group.Letter = letter(p.Group.Title)
	if p.Group.Owner {
		return s.fillOwnerGroup(r, user, p)
	}
	return s.fillGroupRoster(r, user, p)
}

func (s *Server) fillOwnerGroup(r *http.Request, user store.User, p *page) error {
	ctx := r.Context()
	inGroup, err := s.store.GroupAgents(ctx, user.ID, p.Group.ID)
	if err != nil {
		return err
	}
	inAgent := map[string]bool{}
	for _, agent := range inGroup {
		inAgent[agent.ID] = true
	}
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return err
	}
	loc := userLocation(user.Timezone)
	var views []agentView
	for _, agent := range agents {
		if agent.Status == "deleted" {
			continue
		}
		view := presentAgent(agent, false, loc)
		view.Checked = inAgent[agent.ID]
		views = append(views, view)
	}
	p.Group.Agents = views
	humans, err := s.store.GroupHumans(ctx, user.ID, p.Group.ID)
	if err != nil {
		return err
	}
	inHuman := map[string]bool{}
	for _, person := range humans {
		inHuman[person.ID] = true
	}
	people, err := s.store.ListMembers(ctx, user.ID)
	if err != nil {
		return err
	}
	var members []memberView
	for _, person := range people {
		if person.ID == user.ID {
			continue
		}
		members = append(members, memberView{
			ID: person.ID, Name: person.DisplayName, Email: person.Email,
			Role: memberRole(person.Role), Checked: inHuman[person.ID],
		})
	}
	p.Members = members
	return nil
}

func (s *Server) fillGroupRoster(r *http.Request, user store.User, p *page) error {
	ctx := r.Context()
	agents, err := s.store.GroupAgents(ctx, user.ID, p.Group.ID)
	if err != nil {
		return err
	}
	loc := userLocation(user.Timezone)
	var views []agentView
	for _, agent := range agents {
		views = append(views, presentAgent(agent, false, loc))
	}
	p.Group.Agents = views
	humans, err := s.store.GroupHumans(ctx, user.ID, p.Group.ID)
	if err != nil {
		return err
	}
	var members []memberView
	for _, person := range humans {
		members = append(members, memberView{ID: person.ID, Name: person.DisplayName})
	}
	p.Members = members
	return nil
}

func groupPanelPath(id string) string {
	return "/groups/" + url.PathEscape(id) + "?panel=group"
}

func (s *Server) groupDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.groupDeletePage(r, user, r.PathValue("id"), true)
	if s.missing(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) groupDeleteSave(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	open, err := s.store.GroupOpen(r.Context(), user.ID, id)
	if s.missing(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if open {
		p, perr := s.groupDeletePage(r, user, id, false)
		if s.missing(w, perr) {
			return
		}
		if perr != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		p.Group.Note = "Wait until the reply finishes."
		p.Group.Busy = true
		s.render(w, http.StatusConflict, "app", finish(p))
		return
	}
	okTake, err := s.store.TakeGroupConfirm(r.Context(), user.ID, id, r.FormValue("confirm"))
	if s.missing(w, err) {
		return
	}
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if !okTake {
		http.Redirect(w, r, "/groups/"+url.PathEscape(id), http.StatusSeeOther)
		return
	}
	if err := s.store.DeleteGroup(r.Context(), user.ID, id); err != nil {
		if errors.Is(err, store.ErrConflict) {
			p, perr := s.groupDeletePage(r, user, id, false)
			if perr != nil {
				if s.missing(w, perr) {
					return
				}
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			p.Group.Note = "Wait until the reply finishes."
			p.Group.Busy = true
			s.render(w, http.StatusConflict, "app", finish(p))
			return
		}
		if s.missing(w, err) {
			return
		}
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) groupDeletePage(r *http.Request, user store.User, threadID string, fresh bool) (page, error) {
	th, err := s.store.GroupAccess(r.Context(), user.ID, threadID)
	if err != nil {
		return page{}, err
	}
	if th.UserID != user.ID {
		return page{}, store.ErrNotFound
	}
	p, err := s.groupPage(r, user, threadID)
	if err != nil {
		return page{}, err
	}
	open, err := s.store.GroupOpen(r.Context(), user.ID, th.ID)
	if err != nil {
		return page{}, err
	}
	p.Panel = "group"
	p.Pane = true
	p.Group.Confirming = true
	p.Group.Busy = open
	p.Group.Owner = true
	if open {
		p.Group.Note = "Wait until the reply finishes."
		return p, nil
	}
	if fresh {
		confirm, err := s.store.BeginGroupConfirm(r.Context(), user.ID, th.ID)
		if err != nil {
			return page{}, err
		}
		p.Group.ConfirmID = confirm
	}
	return p, nil
}

func (s *Server) groupPin(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	cards, err := s.store.ListGroupCards(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	pinned := false
	found := false
	for _, card := range cards {
		if card.ID == id {
			found = true
			pinned = card.Pinned
			break
		}
	}
	if !found {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	if err := s.store.SetGroupPin(r.Context(), user.ID, id, !pinned); s.missing(w, err) {
		return
	}
	s.backGroup(w, r, id)
}

func (s *Server) groupSection(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := s.store.PlaceGroup(r.Context(), user.ID, id, r.FormValue("section")); s.missing(w, err) {
		return
	}
	s.backGroup(w, r, id)
}

func (s *Server) sectionFold(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.store.ToggleFold(r.Context(), user.ID, r.PathValue("id")); s.missing(w, err) {
		return
	}
	http.Redirect(w, r, back(r), http.StatusSeeOther)
}

func (s *Server) backGroup(w http.ResponseWriter, r *http.Request, id string) {
	target := groupPanelPath(id)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) applyGroupAsk(r *http.Request, user store.User, th store.Thread, ask store.GroupAsk) (string, string) {
	ctx := r.Context()
	if err := s.store.ClearGroupConfirm(ctx, user.ID, th.ID); err != nil {
		return "unclear", ""
	}
	if ask.Kind == "many" {
		return "unclear", ""
	}
	if ask.Kind == "place" {
		return s.applyPlace(ctx, user, th, ask.Section)
	}
	if th.UserID != user.ID {
		return "creator", ""
	}
	open, err := s.store.GroupOpen(ctx, user.ID, th.ID)
	if err != nil {
		return "unclear", ""
	}
	if open {
		return "wait", ""
	}
	if ask.Kind == "delete" {
		return "", "/groups/" + url.PathEscape(th.ID) + "/delete"
	}
	switch ask.Kind {
	case "rename":
		agents, err := s.store.GroupAgents(ctx, user.ID, th.ID)
		if err != nil {
			return "unclear", ""
		}
		humans, err := s.store.GroupHumans(ctx, user.ID, th.ID)
		if err != nil {
			return "unclear", ""
		}
		ids := agentIDs(agents)
		members := otherIDs(humans, user.ID)
		if err := s.store.UpdateGroup(ctx, user.ID, th.ID, ask.Title, ids, members); err != nil {
			if errors.Is(err, store.ErrInvalid) {
				return "unclear", ""
			}
			if errors.Is(err, store.ErrConflict) {
				return "wait", ""
			}
			return "unclear", ""
		}
		_ = s.groupNotice(ctx, th, "Renamed to "+ask.Title+".")
		return "", ""
	case "add", "remove":
		return s.applyMembership(ctx, user, th, ask)
	default:
		return "unclear", ""
	}
}

func (s *Server) applyPlace(ctx context.Context, user store.User, th store.Thread, name string) (string, string) {
	sections, err := s.store.ListSections(ctx, user.ID)
	if err != nil {
		return "unclear", ""
	}
	sectionID, list, ok := resolvePlace(sections, name)
	if !ok {
		return "unclear", ""
	}
	target := sectionID
	if list {
		target = "groups"
	}
	if err := s.store.PlaceGroup(ctx, user.ID, th.ID, target); err != nil {
		return "unclear", ""
	}
	return "placed", ""
}

func (s *Server) applyMembership(ctx context.Context, user store.User, th store.Thread, ask store.GroupAsk) (string, string) {
	agents, err := s.store.GroupAgents(ctx, user.ID, th.ID)
	if err != nil {
		return "unclear", ""
	}
	humans, err := s.store.GroupHumans(ctx, user.ID, th.ID)
	if err != nil {
		return "unclear", ""
	}
	allAgents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return "unclear", ""
	}
	people, err := s.store.ListMembers(ctx, user.ID)
	if err != nil {
		return "unclear", ""
	}
	kind, id, name, ok := oneName(allAgents, people, ask.Name)
	if !ok {
		return "unclear", ""
	}
	ids := agentIDs(agents)
	members := otherIDs(humans, user.ID)
	inAgent := map[string]bool{}
	for _, agent := range agents {
		inAgent[agent.ID] = true
	}
	inHuman := map[string]bool{}
	for _, person := range humans {
		inHuman[person.ID] = true
	}
	line := ""
	switch {
	case ask.Kind == "add" && kind == "agent":
		if inAgent[id] {
			return "unclear", ""
		}
		ids = append(ids, id)
		line = name + " is in this group."
	case ask.Kind == "remove" && kind == "agent":
		if !inAgent[id] {
			return "unclear", ""
		}
		next := make([]string, 0, len(ids))
		for _, agentID := range ids {
			if agentID != id {
				next = append(next, agentID)
			}
		}
		if len(next) < 2 {
			return "agents", ""
		}
		ids = next
		line = name + " is no longer in this group."
	case ask.Kind == "add" && kind == "person":
		if id == user.ID || inHuman[id] {
			return "unclear", ""
		}
		members = append(members, id)
		line = name + " can read and post here."
	case ask.Kind == "remove" && kind == "person":
		if id == user.ID || !inHuman[id] {
			return "unclear", ""
		}
		next := make([]string, 0, len(members))
		for _, memberID := range members {
			if memberID != id {
				next = append(next, memberID)
			}
		}
		members = next
		line = name + " can no longer read or post here."
	default:
		return "unclear", ""
	}
	if err := s.store.UpdateGroup(ctx, user.ID, th.ID, th.Title, ids, members); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return "wait", ""
		}
		if errors.Is(err, store.ErrInvalid) && ask.Kind == "remove" && kind == "agent" {
			return "agents", ""
		}
		return "unclear", ""
	}
	_ = s.groupNotice(ctx, th, line)
	return "", ""
}

func (s *Server) groupNotice(ctx context.Context, th store.Thread, body string) error {
	_, err := s.store.CreateOriginMessage(ctx, th.UserID, th.ID, "system", th.UserID, body, "notice")
	return err
}

func agentIDs(agents []store.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, agent := range agents {
		out = append(out, agent.ID)
	}
	return out
}

func otherIDs(humans []store.User, self string) []string {
	var out []string
	for _, person := range humans {
		if person.ID != self {
			out = append(out, person.ID)
		}
	}
	return out
}

func oneName(agents []store.Agent, people []store.User, name string) (kind, id, label string, ok bool) {
	name = strings.TrimSpace(name)
	type hit struct{ kind, id, label string }
	var hits []hit
	for _, agent := range agents {
		if agent.Status == "deleted" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			hits = append(hits, hit{"agent", agent.ID, agent.Name})
		}
	}
	for _, person := range people {
		if person.Disabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(person.DisplayName), name) {
			hits = append(hits, hit{"person", person.ID, person.DisplayName})
		}
	}
	if len(hits) != 1 {
		return "", "", "", false
	}
	return hits[0].kind, hits[0].id, hits[0].label, true
}

func resolvePlace(sections []store.Section, name string) (id string, list, ok bool) {
	name = strings.TrimSpace(name)
	listName := strings.EqualFold(name, "Groups")
	var hits []store.Section
	for _, sec := range sections {
		if strings.EqualFold(strings.TrimSpace(sec.Name), name) {
			hits = append(hits, sec)
		}
	}
	if listName && len(hits) > 0 {
		return "", false, false
	}
	if listName {
		return "", true, true
	}
	if len(hits) != 1 {
		return "", false, false
	}
	return hits[0].ID, false, true
}

func askSentence(code, sectionName string) string {
	switch code {
	case "creator":
		return "Only the person who created this group can change that."
	case "agents":
		return "A group needs at least two agents."
	case "wait":
		return "Wait until the reply finishes."
	case "unclear":
		return "That did not change the group."
	case "placed":
		if sectionName == "" {
			return "This group is under Groups."
		}
		return "This group is in " + sectionName + "."
	default:
		return ""
	}
}
