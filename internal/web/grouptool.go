package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const recentOrganizeWindow = 2 * time.Minute

type organizeArgs struct {
	Group   string   `json:"group"`
	Title   string   `json:"title"`
	Name    string   `json:"name"`
	Section string   `json:"section"`
	Agents  []string `json:"agents"`
	People  []string `json:"people"`
	Out     bool     `json:"out"`
}

func organizeFrom(req grouptool.Request) organizeArgs {
	return organizeArgs{
		Group: req.Group, Title: req.Title, Name: req.Name, Section: req.Section,
		Agents: append([]string(nil), req.Agents...), People: append([]string(nil), req.People...),
		Out: req.Out,
	}
}

// machineGroup runs one group or section tool for the machine's account.
// user_id and agent_id arguments are ignored.
func (s *Server) machineGroup(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	if _, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id")); err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	var args organizeArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, "That request was rejected.", true)
			return
		}
	}
	text, failed := s.organize(r.Context(), user, name, args, "")
	writeAgentResult(w, text, failed)
}

// maybeGroup reads one castbot-organize fence from the latest reply.
// A create that matches a group or section saved in the last two minutes
// is not inserted again. That covers a tool call and a fence for the same ask.
func (s *Server) maybeGroup(job run.Job) {
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
	visible, req, ok := grouptool.Parse(reply.Body)
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
	if req.Action == grouptool.ToolCreateGroup && s.recentSameGroup(ctx, user, req) {
		body := strings.TrimSpace(visible)
		if body == "" {
			body = strings.TrimSpace(req.Title) + " is already there."
		}
		s.replaceReply(ctx, job.UserID, reply.ID, body)
		return
	}
	if req.Action == grouptool.ToolCreateSection && s.recentSameSection(ctx, user.ID, req.Name) {
		body := strings.TrimSpace(visible)
		if body == "" {
			body = strings.TrimSpace(req.Name) + " is already there."
		}
		s.replaceReply(ctx, job.UserID, reply.ID, body)
		return
	}
	if req.Action == grouptool.ToolDeleteGroup {
		if _, msg, failed := s.resolveGroup(ctx, user.ID, req.Group, true); failed && organizeGone(msg, "group") {
			body := strings.TrimSpace(visible)
			if body == "" {
				body = "That group is already gone."
			}
			s.replaceReply(ctx, job.UserID, reply.ID, body)
			return
		}
	}
	if req.Action == grouptool.ToolDeleteSection {
		if _, msg, failed := s.resolveSection(ctx, user.ID, req.Section); failed && organizeGone(msg, "section") {
			body := strings.TrimSpace(visible)
			if body == "" {
				body = "That section is already gone."
			}
			s.replaceReply(ctx, job.UserID, reply.ID, body)
			return
		}
	}
	text, failed := s.organize(ctx, user, req.Action, organizeFrom(req), job.RunID)
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func organizeGone(msg, kind string) bool {
	switch kind {
	case "section":
		return strings.HasPrefix(msg, "No section is named ") || msg == "That section was not found."
	default:
		return strings.HasPrefix(msg, "No group is named ") || msg == "That group was not found."
	}
}

func (s *Server) organize(ctx context.Context, user store.User, name string, args organizeArgs, runID string) (string, bool) {
	args.Group = strings.TrimSpace(args.Group)
	args.Title = strings.TrimSpace(args.Title)
	args.Name = strings.TrimSpace(args.Name)
	args.Section = strings.TrimSpace(args.Section)
	args.Agents = cleanNames(args.Agents)
	args.People = cleanNames(args.People)
	text, failed := s.runOrganize(ctx, user, name, args)
	if !failed {
		s.noteSections(ctx, user.ID, runID)
	}
	return text, failed
}

func (s *Server) runOrganize(ctx context.Context, user store.User, name string, args organizeArgs) (string, bool) {
	switch name {
	case grouptool.ToolCreateGroup:
		return s.createGroupTool(ctx, user, args)
	case grouptool.ToolUpdateGroup:
		return s.updateGroupTool(ctx, user, args)
	case grouptool.ToolAddMembers:
		return s.addGroupMembers(ctx, user, args)
	case grouptool.ToolRemoveMembers:
		return s.removeGroupMembers(ctx, user, args)
	case grouptool.ToolDeleteGroup:
		return s.deleteGroupTool(ctx, user, args)
	case grouptool.ToolPinGroup:
		return s.pinGroupTool(ctx, user, args, true)
	case grouptool.ToolUnpinGroup:
		return s.pinGroupTool(ctx, user, args, false)
	case grouptool.ToolCreateSection:
		return s.createSectionTool(ctx, user, args)
	case grouptool.ToolRenameSection:
		return s.renameSectionTool(ctx, user, args)
	case grouptool.ToolDeleteSection:
		return s.deleteSectionTool(ctx, user, args)
	case grouptool.ToolMoveAgents:
		return s.moveAgentsTool(ctx, user, args)
	case grouptool.ToolPlaceGroup:
		return s.placeGroupTool(ctx, user, args)
	default:
		return "That request was rejected.", true
	}
}

func (s *Server) createGroupTool(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	if args.Title == "" {
		return "Give the group a title.", true
	}
	if len(args.Title) > 80 {
		return "That title is too long.", true
	}
	agents, msg, failed := s.resolveAgents(ctx, user.ID, args.Agents)
	if failed {
		return msg, true
	}
	if len(agents) < 2 {
		return "A group needs at least two of your agents.", true
	}
	people, msg, failed := s.resolvePeople(ctx, user, args.People)
	if failed {
		return msg, true
	}
	ids := agentIDs(agents)
	others := otherIDs(people, user.ID)
	th, err := s.store.CreateGroup(ctx, user.ID, args.Title, ids, others)
	if err != nil {
		return groupSaveErr(err), true
	}
	s.openSidebarGroup(ctx, user.ID, th.ID)
	return createdGroupSentence(th.Title, agents, people, user.ID, th.ID), false
}

func (s *Server) updateGroupTool(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	if args.Title == "" {
		return "Give the group a title.", true
	}
	if len(args.Title) > 80 {
		return "That title is too long.", true
	}
	card, msg, failed := s.resolveGroup(ctx, user.ID, args.Group, true)
	if failed {
		return msg, true
	}
	if card.Title == args.Title {
		return "The group is already named " + card.Title + ".", false
	}
	if err := s.store.SetGroupTitle(ctx, user.ID, card.ID, args.Title); err != nil {
		return groupSaveErr(err), true
	}
	s.openSidebarGroup(ctx, user.ID, card.ID)
	return "Renamed the group to " + args.Title + ".", false
}

func (s *Server) addGroupMembers(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	if len(args.Agents) == 0 && len(args.People) == 0 {
		return "Say who to add.", true
	}
	card, msg, failed := s.resolveGroup(ctx, user.ID, args.Group, true)
	if failed {
		return msg, true
	}
	addAgents, msg, failed := s.resolveAgents(ctx, user.ID, args.Agents)
	if failed {
		return msg, true
	}
	addPeople, msg, failed := s.resolvePeople(ctx, user, args.People)
	if failed {
		return msg, true
	}
	currentAgents, currentHumans, bad := s.groupRoster(ctx, user.ID, card.ID)
	if bad {
		return "That group could not be read.", true
	}
	haveAgent := map[string]bool{}
	ids := make([]string, 0, len(currentAgents)+len(addAgents))
	for _, agent := range currentAgents {
		haveAgent[agent.ID] = true
		ids = append(ids, agent.ID)
	}
	haveHuman := map[string]bool{user.ID: true}
	others := make([]string, 0, len(currentHumans))
	for _, human := range currentHumans {
		haveHuman[human.ID] = true
		if human.ID != user.ID {
			others = append(others, human.ID)
		}
	}
	var added []string
	askedOther := false
	for _, agent := range addAgents {
		askedOther = true
		if haveAgent[agent.ID] {
			continue
		}
		haveAgent[agent.ID] = true
		ids = append(ids, agent.ID)
		added = append(added, agent.Name)
	}
	for _, person := range addPeople {
		if person.ID == user.ID {
			continue
		}
		askedOther = true
		if haveHuman[person.ID] {
			continue
		}
		haveHuman[person.ID] = true
		others = append(others, person.ID)
		added = append(added, person.DisplayName)
	}
	if len(added) == 0 {
		if !askedOther {
			return "You are already in " + card.Title + ".", false
		}
		return "They are already in " + card.Title + ".", false
	}
	if err := s.store.UpdateGroup(ctx, user.ID, card.ID, card.Title, ids, others); err != nil {
		return groupSaveErr(err), true
	}
	s.openSidebarGroup(ctx, user.ID, card.ID)
	return "Added " + joinNames(added) + " to " + card.Title + ".", false
}

func (s *Server) removeGroupMembers(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	if len(args.Agents) == 0 && len(args.People) == 0 {
		return "Say who to remove.", true
	}
	card, msg, failed := s.resolveGroup(ctx, user.ID, args.Group, true)
	if failed {
		return msg, true
	}
	dropAgents, msg, failed := s.resolveAgents(ctx, user.ID, args.Agents)
	if failed {
		return msg, true
	}
	dropPeople, msg, failed := s.resolvePeople(ctx, user, args.People)
	if failed {
		return msg, true
	}
	currentAgents, currentHumans, bad := s.groupRoster(ctx, user.ID, card.ID)
	if bad {
		return "That group could not be read.", true
	}
	dropAgent := map[string]bool{}
	for _, agent := range dropAgents {
		dropAgent[agent.ID] = true
	}
	dropHuman := map[string]bool{}
	stay := false
	for _, person := range dropPeople {
		if person.ID == user.ID {
			stay = true
			continue
		}
		dropHuman[person.ID] = true
	}
	kept := make([]string, 0, len(currentAgents))
	var removed []string
	for _, agent := range currentAgents {
		if dropAgent[agent.ID] {
			removed = append(removed, agent.Name)
			continue
		}
		kept = append(kept, agent.ID)
	}
	others := make([]string, 0, len(currentHumans))
	for _, human := range currentHumans {
		if human.ID == user.ID {
			continue
		}
		if dropHuman[human.ID] {
			removed = append(removed, human.DisplayName)
			continue
		}
		others = append(others, human.ID)
	}
	if len(removed) == 0 {
		if stay {
			return "You stay in the group.", false
		}
		return "They are not in " + card.Title + ".", false
	}
	if len(kept) < 2 && len(currentAgents) >= 2 {
		return "A group needs at least two of your agents.", true
	}
	if err := s.store.UpdateGroup(ctx, user.ID, card.ID, card.Title, kept, others); err != nil {
		return groupSaveErr(err), true
	}
	s.openSidebarGroup(ctx, user.ID, card.ID)
	msg = "Removed " + joinNames(removed) + " from " + card.Title + "."
	if stay {
		msg += " You stay in the group."
	}
	return msg, false
}

func (s *Server) deleteGroupTool(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	card, msg, failed := s.resolveGroup(ctx, user.ID, args.Group, true)
	if failed {
		return msg, true
	}
	if err := s.store.DeleteGroup(ctx, user.ID, card.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return "Wait until the reply finishes.", true
		}
		return "That group could not be deleted.", true
	}
	return "Deleted " + card.Title + ".", false
}

func (s *Server) pinGroupTool(ctx context.Context, user store.User, args organizeArgs, pin bool) (string, bool) {
	card, msg, failed := s.resolveGroup(ctx, user.ID, args.Group, false)
	if failed {
		return msg, true
	}
	if pin && card.Pinned {
		return card.Title + " is already pinned.", false
	}
	if !pin && !card.Pinned {
		return card.Title + " is not pinned.", false
	}
	if err := s.store.SetGroupPin(ctx, user.ID, card.ID, pin); err != nil {
		return "That group could not be saved.", true
	}
	s.openSidebarGroup(ctx, user.ID, card.ID)
	if pin {
		return "Pinned " + card.Title + ".", false
	}
	return "Unpinned " + card.Title + ".", false
}

func (s *Server) createSectionTool(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	if args.Name == "" {
		return "Give the section a name.", true
	}
	if len(args.Name) > 40 {
		return "That name is too long.", true
	}
	sec, err := s.store.CreateSection(ctx, user.ID, args.Name)
	if err != nil {
		return "That section could not be saved.", true
	}
	_ = s.store.ExpandSection(ctx, user.ID, sec.ID)
	return "Added the section " + sec.Name + ". " + sec.ID, false
}

func (s *Server) renameSectionTool(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	if args.Name == "" {
		return "Give the section a name.", true
	}
	if len(args.Name) > 40 {
		return "That name is too long.", true
	}
	sec, msg, failed := s.resolveSection(ctx, user.ID, args.Section)
	if failed {
		return msg, true
	}
	if sec.Kind != "custom" {
		return "That section stays.", true
	}
	if sec.Name == args.Name {
		return "The section is already named " + sec.Name + ".", false
	}
	if err := s.store.UpdateSection(ctx, user.ID, sec.ID, args.Name); err != nil {
		if errors.Is(err, store.ErrProtected) {
			return "That section stays.", true
		}
		return "That section could not be saved.", true
	}
	return "Renamed the section to " + args.Name + ".", false
}

func (s *Server) deleteSectionTool(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	sec, msg, failed := s.resolveSection(ctx, user.ID, args.Section)
	if failed {
		return msg, true
	}
	if sec.Kind != "custom" {
		return "That section stays.", true
	}
	if err := s.store.DeleteSection(ctx, user.ID, sec.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return "Move the agents out of " + sec.Name + " before removing it.", true
		}
		if errors.Is(err, store.ErrProtected) {
			return "That section stays.", true
		}
		return "That section could not be removed.", true
	}
	return "Removed " + sec.Name + ".", false
}

func (s *Server) moveAgentsTool(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	if len(args.Agents) == 0 {
		return "Say which agents to move.", true
	}
	if args.Out == (args.Section != "") {
		return "Say one section, or say to move them out.", true
	}
	agents, msg, failed := s.resolveAgents(ctx, user.ID, args.Agents)
	if failed {
		return msg, true
	}
	if len(agents) == 0 {
		return "Say which agents to move.", true
	}
	var dest store.Section
	if args.Out {
		var err error
		dest, err = s.store.SystemSection(ctx, user.ID, "agents")
		if err != nil {
			return "That request was rejected.", true
		}
	} else {
		var resolveFailed bool
		dest, msg, resolveFailed = s.resolveSection(ctx, user.ID, args.Section)
		if resolveFailed {
			return msg, true
		}
	}
	var moved, already []string
	for _, agent := range agents {
		if agent.SectionID == dest.ID {
			already = append(already, agent.Name)
			continue
		}
		if err := s.store.MoveAgentSection(ctx, user.ID, agent.ID, dest.ID); err != nil {
			return "That agent could not be moved.", true
		}
		moved = append(moved, agent.Name)
	}
	_ = s.store.ExpandSection(ctx, user.ID, dest.ID)
	return moveSentence(moved, already, dest.Name), false
}

func (s *Server) placeGroupTool(ctx context.Context, user store.User, args organizeArgs) (string, bool) {
	if args.Group == "" {
		return "Say which group.", true
	}
	card, msg, failed := s.resolveGroup(ctx, user.ID, args.Group, false)
	if failed {
		return msg, true
	}
	section := strings.TrimSpace(args.Section)
	destID := ""
	destName := "Groups"
	if section != "" && section != "groups" {
		sec, resolveMsg, resolveFailed := s.resolveSection(ctx, user.ID, section)
		if resolveFailed {
			return resolveMsg, true
		}
		destID = sec.ID
		destName = sec.Name
	}
	if card.SectionID == destID {
		return card.Title + " is already in " + destName + ".", false
	}
	target := destID
	if destID == "" {
		target = "groups"
	}
	if err := s.store.PlaceGroup(ctx, user.ID, card.ID, target); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "That section was not found.", true
		}
		return "That group could not be moved.", true
	}
	if destID == "" {
		return "Moved " + card.Title + " back to Groups.", false
	}
	return "Moved " + card.Title + " to " + destName + ".", false
}

func moveSentence(moved, already []string, dest string) string {
	if len(moved) == 0 {
		if len(already) == 1 {
			return already[0] + " is already in " + dest + "."
		}
		return "They are already in " + dest + "."
	}
	msg := "Moved " + joinNames(moved) + " to " + dest + "."
	if len(already) == 1 {
		msg += " " + already[0] + " was already there."
	} else if len(already) > 1 {
		msg += " The others were already there."
	}
	return msg
}

func (s *Server) recentSameGroup(ctx context.Context, user store.User, req grouptool.Request) bool {
	agents, _, failed := s.resolveAgents(ctx, user.ID, req.Agents)
	if failed || len(agents) < 2 {
		return false
	}
	people, _, failed := s.resolvePeople(ctx, user, req.People)
	if failed {
		return false
	}
	rows, err := s.store.ListGroups(ctx, user.ID)
	if err != nil {
		return false
	}
	title := strings.TrimSpace(req.Title)
	wantAgents := map[string]bool{}
	for _, agent := range agents {
		wantAgents[agent.ID] = true
	}
	wantHumans := map[string]bool{user.ID: true}
	for _, person := range people {
		wantHumans[person.ID] = true
	}
	cutoff := s.store.Now().Add(-recentOrganizeWindow)
	for _, row := range rows {
		if row.UserID != user.ID || row.Title != title {
			continue
		}
		at, err := time.Parse(time.RFC3339, row.CreatedAt)
		if err != nil || at.Before(cutoff) {
			continue
		}
		gotAgents, humans, bad := s.groupRoster(ctx, user.ID, row.ID)
		if bad || len(gotAgents) != len(wantAgents) || len(humans) != len(wantHumans) {
			continue
		}
		match := true
		for _, agent := range gotAgents {
			if !wantAgents[agent.ID] {
				match = false
				break
			}
		}
		for _, human := range humans {
			if !wantHumans[human.ID] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (s *Server) recentSameSection(ctx context.Context, userID, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	sections, err := s.store.ListSections(ctx, userID)
	if err != nil {
		return false
	}
	var newest time.Time
	found := false
	for _, sec := range sections {
		if sec.Name != name {
			continue
		}
		at, err := time.Parse(time.RFC3339, sec.CreatedAt)
		if err != nil {
			continue
		}
		if !found || at.After(newest) {
			newest = at
			found = true
		}
	}
	if !found {
		return false
	}
	return !newest.Before(s.store.Now().Add(-recentOrganizeWindow))
}

func (s *Server) resolveGroup(ctx context.Context, userID, key string, creatorOnly bool) (store.GroupCard, string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return store.GroupCard{}, "Say which group.", true
	}
	cards, err := s.store.ListGroupCards(ctx, userID)
	if err != nil {
		return store.GroupCard{}, "The group list could not be read.", true
	}
	var byID *store.GroupCard
	var byTitle []store.GroupCard
	for i := range cards {
		if cards[i].ID == key {
			byID = &cards[i]
		}
		if strings.EqualFold(cards[i].Title, key) {
			byTitle = append(byTitle, cards[i])
		}
	}
	var card store.GroupCard
	switch {
	case byID != nil:
		card = *byID
	case len(byTitle) == 1:
		card = byTitle[0]
	case len(byTitle) == 0:
		return store.GroupCard{}, namedMiss("group", key), true
	default:
		return store.GroupCard{}, namedMany("group", key, cardIDs(byTitle)), true
	}
	if creatorOnly && card.UserID != userID {
		return store.GroupCard{}, "Only the person who created " + card.Title + " can change it.", true
	}
	return card, "", false
}

func (s *Server) resolveSection(ctx context.Context, userID, key string) (store.Section, string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return store.Section{}, "Say which section.", true
	}
	sections, err := s.store.ListSections(ctx, userID)
	if err != nil {
		return store.Section{}, "The section list could not be read.", true
	}
	var byID *store.Section
	var byName []store.Section
	for i := range sections {
		if sections[i].ID == key {
			byID = &sections[i]
		}
		if strings.EqualFold(sections[i].Name, key) {
			byName = append(byName, sections[i])
		}
	}
	switch {
	case byID != nil:
		return *byID, "", false
	case len(byName) == 1:
		return byName[0], "", false
	case len(byName) == 0:
		return store.Section{}, namedMiss("section", key), true
	default:
		ids := make([]string, 0, len(byName))
		for _, sec := range byName {
			ids = append(ids, sec.ID)
		}
		sort.Strings(ids)
		return store.Section{}, namedMany("section", key, ids), true
	}
}

func (s *Server) resolveAgents(ctx context.Context, userID string, keys []string) ([]store.Agent, string, bool) {
	if len(keys) == 0 {
		return nil, "", false
	}
	all, err := s.store.ListAgents(ctx, userID)
	if err != nil {
		return nil, "The agent list could not be read.", true
	}
	var out []store.Agent
	seen := map[string]bool{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		var byID *store.Agent
		var byName []store.Agent
		for i := range all {
			if all[i].ID == key {
				byID = &all[i]
			}
			if strings.EqualFold(all[i].Name, key) {
				byName = append(byName, all[i])
			}
		}
		var pick store.Agent
		switch {
		case byID != nil:
			pick = *byID
		case len(byName) == 1:
			pick = byName[0]
		case len(byName) == 0:
			return nil, namedMiss("agent", key), true
		default:
			ids := make([]string, 0, len(byName))
			for _, agent := range byName {
				ids = append(ids, agent.ID)
			}
			sort.Strings(ids)
			return nil, namedMany("agent", key, ids), true
		}
		if seen[pick.ID] {
			continue
		}
		seen[pick.ID] = true
		out = append(out, pick)
	}
	return out, "", false
}

func (s *Server) resolvePeople(ctx context.Context, user store.User, keys []string) ([]store.User, string, bool) {
	if len(keys) == 0 {
		return nil, "", false
	}
	members, err := s.store.ListMembers(ctx, user.ID)
	if err != nil {
		return nil, "The people list could not be read.", true
	}
	var out []store.User
	seen := map[string]bool{}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if strings.EqualFold(key, "me") || strings.EqualFold(key, "myself") {
			if !seen[user.ID] {
				seen[user.ID] = true
				out = append(out, user)
			}
			continue
		}
		var byID *store.User
		var byEmail []store.User
		var byName []store.User
		for i := range members {
			if members[i].ID == key {
				byID = &members[i]
			}
			if strings.EqualFold(members[i].Email, key) {
				byEmail = append(byEmail, members[i])
			}
			if strings.EqualFold(members[i].DisplayName, key) {
				byName = append(byName, members[i])
			}
		}
		var pick store.User
		switch {
		case byID != nil:
			pick = *byID
		case len(byEmail) == 1:
			pick = byEmail[0]
		case len(byName) == 1:
			pick = byName[0]
		case len(byName) == 0 && len(byEmail) == 0:
			return nil, namedMiss("person", key), true
		default:
			ids := map[string]bool{}
			for _, person := range byName {
				ids[person.ID] = true
			}
			for _, person := range byEmail {
				ids[person.ID] = true
			}
			list := make([]string, 0, len(ids))
			for id := range ids {
				list = append(list, id)
			}
			sort.Strings(list)
			return nil, namedMany("person", key, list), true
		}
		if seen[pick.ID] {
			continue
		}
		seen[pick.ID] = true
		out = append(out, pick)
	}
	return out, "", false
}

func (s *Server) groupRoster(ctx context.Context, userID, threadID string) ([]store.Agent, []store.User, bool) {
	agents, err := s.store.GroupAgents(ctx, userID, threadID)
	if err != nil {
		return nil, nil, true
	}
	humans, err := s.store.GroupHumans(ctx, userID, threadID)
	if err != nil {
		return nil, nil, true
	}
	return agents, humans, false
}

func (s *Server) openSidebarGroup(ctx context.Context, userID, threadID string) {
	cards, err := s.store.ListGroupCards(ctx, userID)
	if err != nil {
		return
	}
	for _, card := range cards {
		if card.ID != threadID {
			continue
		}
		if card.SectionID != "" {
			_ = s.store.ExpandSection(ctx, userID, card.SectionID)
			return
		}
		folds, err := s.store.FoldedSections(ctx, userID)
		if err == nil && folds["groups"] {
			_, _ = s.store.ToggleFold(ctx, userID, "groups")
		}
		return
	}
}

func groupSaveErr(err error) string {
	switch {
	case errors.Is(err, store.ErrConflict):
		return "Wait until the reply finishes."
	case errors.Is(err, store.ErrInvalid):
		return "A group needs at least two of your agents."
	default:
		return "That group could not be saved."
	}
}

func createdGroupSentence(title string, agents []store.Agent, people []store.User, ownerID, id string) string {
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		names = append(names, agent.Name)
	}
	msg := "Created " + title + " with " + joinNames(names) + "."
	var others []string
	for _, person := range people {
		if person.ID == ownerID {
			continue
		}
		others = append(others, person.DisplayName)
	}
	if len(others) > 0 {
		msg += " " + joinNames(others) + " can open it."
	}
	return msg + " " + id
}

func cardIDs(cards []store.GroupCard) []string {
	ids := make([]string, 0, len(cards))
	for _, card := range cards {
		ids = append(ids, card.ID)
	}
	sort.Strings(ids)
	return ids
}

func cleanNames(in []string) []string {
	var out []string
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func namedMiss(kind, key string) string {
	if looksLikeID(key) {
		switch kind {
		case "agent":
			return "That agent was not found."
		case "section":
			return "That section was not found."
		case "person":
			return "That person was not found."
		default:
			return "That group was not found."
		}
	}
	switch kind {
	case "agent":
		return "No agent is named " + key + "."
	case "section":
		return "No section is named " + key + "."
	case "person":
		return "No person is named " + key + "."
	default:
		return "No group is named " + key + "."
	}
}

func namedMany(kind, key string, ids []string) string {
	what := "group"
	switch kind {
	case "agent":
		what = "agent"
	case "section":
		what = "section"
	case "person":
		what = "person"
	}
	return "More than one " + what + " is named " + key + ". " + strings.Join(ids, ", ")
}

func looksLikeID(key string) bool {
	if len(key) != 32 {
		return false
	}
	for _, c := range key {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func joinNames(names []string) string {
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
