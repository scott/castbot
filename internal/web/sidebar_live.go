package web

import (
	"context"
	"io"
	"net/url"
	"sync"

	"github.com/scott/castbot/internal/store"
)

// lockCreate serializes fence application for one run so the stream and the
// after-run hook cannot insert the same agent twice.
func (s *Server) lockCreate(runID string) func() {
	s.createMu.Lock()
	if s.createAt == nil {
		s.createAt = map[string]*sync.Mutex{}
	}
	gate := s.createAt[runID]
	if gate == nil {
		gate = &sync.Mutex{}
		s.createAt[runID] = gate
	}
	s.createMu.Unlock()
	gate.Lock()
	return gate.Unlock
}

// rememberSidebar notes an agent the open page should list for this run.
func (s *Server) rememberSidebar(runID, agentID string) {
	if runID == "" || agentID == "" {
		return
	}
	s.sideMu.Lock()
	defer s.sideMu.Unlock()
	if s.sideNew == nil {
		s.sideNew = map[string][]string{}
	}
	for _, id := range s.sideNew[runID] {
		if id == agentID {
			return
		}
	}
	s.sideNew[runID] = append(s.sideNew[runID], agentID)
}

func (s *Server) takeSidebar(runID string) []string {
	s.sideMu.Lock()
	defer s.sideMu.Unlock()
	ids := s.sideNew[runID]
	delete(s.sideNew, runID)
	return ids
}

func (s *Server) restoreSidebar(runID string, ids []string) {
	for _, id := range ids {
		s.rememberSidebar(runID, id)
	}
}

// noteChatAgent records a saved agent on the caller's open run and opens
// the Agents section so the row is visible.
func (s *Server) noteChatAgent(ctx context.Context, userID, callerID string, agent store.Agent) {
	if agent.ID == "" {
		return
	}
	_ = s.store.ExpandSection(ctx, userID, agent.SectionID)
	rows, err := s.store.ListOpenRuns(ctx, userID)
	if err != nil {
		return
	}
	var found string
	for _, row := range rows {
		if row.AgentID != callerID {
			continue
		}
		found = row.ID
		if row.Status == "streaming" {
			break
		}
	}
	s.rememberSidebar(found, agent.ID)
}

// sidebarFrame is the Agents section, ready to swap into the open page.
// It is empty when this run has not saved an agent since the last frame.
func (s *Server) sidebarFrame(ctx context.Context, user store.User, runRow store.Run, csrf string) string {
	ids := s.takeSidebar(runRow.ID)
	if len(ids) == 0 {
		return ""
	}
	html, err := s.agentsSectionHTML(ctx, user, runRow.AgentID, csrf)
	if err != nil || html == "" {
		s.restoreSidebar(runRow.ID, ids)
		return ""
	}
	return html
}

func (s *Server) writeSidebar(ctx context.Context, w io.Writer, user store.User, runRow store.Run, csrf string) error {
	frame := s.sidebarFrame(ctx, user, runRow, csrf)
	if frame == "" {
		return nil
	}
	return writeSSE(w, "", "approvals", frame)
}

func (s *Server) agentsSectionHTML(ctx context.Context, user store.User, currentID, csrf string) (string, error) {
	sections, err := s.store.ListSections(ctx, user.ID)
	if err != nil {
		return "", err
	}
	agents, err := s.store.ListAgents(ctx, user.ID)
	if err != nil {
		return "", err
	}
	cards, err := s.store.ListGroupCards(ctx, user.ID)
	if err != nil {
		return "", err
	}
	var sec store.Section
	var found bool
	for _, item := range sections {
		if item.Kind == "agents" {
			sec = item
			found = true
			break
		}
	}
	if !found {
		return "", nil
	}
	loc := userLocation(user.Timezone)
	open := map[string]store.OpenRun{}
	if rows, err := s.store.ListOpenRuns(ctx, user.ID); err == nil {
		for _, row := range rows {
			open[row.AgentID] = row
		}
	}
	var views []agentView
	for _, agent := range agents {
		if agent.SectionID != sec.ID {
			continue
		}
		view := s.paintAgent(ctx, user.ID, agent, agent.ID == currentID, loc)
		row, has := open[agent.ID]
		s.decorateAgent(ctx, user.ID, &view, row, has)
		views = append(views, view)
	}
	var groups []groupLink
	for _, row := range cards {
		if row.SectionID != sec.ID {
			continue
		}
		link := groupLink{
			ID: row.ID, Title: row.Title, Href: "/groups/" + url.PathEscape(row.ID),
			Letter: letter(row.Title), Pinned: row.Pinned, Sub: "Group chat",
		}
		if row.Pinned {
			link.Sub = "Pinned"
		}
		groups = append(groups, link)
	}
	return execTemplate("sidebarSection", sectionWrap{
		Section: sectionView{
			ID: sec.ID, Name: sec.Name, Kind: sec.Kind,
			Agents: views, Groups: groups,
		},
		CSRF: csrf,
		OOB:  true,
	})
}
