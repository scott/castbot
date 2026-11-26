package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const agentPagePreview = 5

type pageLinkView struct {
	Title, Token, Sub string
}

// attachPageCards writes one card per page this run saved.
// The tool call finishes before the reply message exists, so the card is
// attached here, after the reply is stored. A page whose updated_at is older
// than the run is left on its earlier card.
func (s *Server) attachPageCards(job run.Job) {
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	ctx := context.Background()
	runRow, err := s.store.GetRun(ctx, job.UserID, job.RunID)
	if err != nil || runRow.StartedAt == "" {
		return
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil || reply.ID == "" {
		return
	}
	rows, err := s.store.AgentPagesTouched(ctx, job.UserID, job.AgentID, runRow.StartedAt)
	if err != nil {
		return
	}
	for _, row := range rows {
		_ = s.store.AddPageCard(ctx, job.UserID, reply.ID, row.ID, row.Revision)
	}
}

func (s *Server) fillPageCards(ctx context.Context, workspaceID string, views []msgView) {
	if workspaceID == "" || len(views) == 0 {
		return
	}
	var ids []string
	for _, view := range views {
		if view.ID != "" {
			ids = append(ids, view.ID)
		}
	}
	rows, err := s.store.PageCardsForMessages(ctx, workspaceID, ids)
	if err != nil {
		return
	}
	by := map[string][]pageLinkView{}
	for _, row := range rows {
		by[row.MessageID] = append(by[row.MessageID], pageLinkView{
			Title: row.Title,
			Token: row.Token,
			Sub:   pageSub(row.Visibility, row.WorkspaceName, row.Revision < row.CurrentRevision),
		})
	}
	for i := range views {
		views[i].PageCards = by[views[i].ID]
	}
}

func (s *Server) fillAgentPages(r *http.Request, user store.User, agent store.Agent, p *page) {
	rows, err := s.store.ListAgentPages(r.Context(), user.ID, agent.ID)
	if err != nil {
		return
	}
	if len(rows) > agentPagePreview {
		rows = rows[:agentPagePreview]
	}
	p.AgentPages = pageLinks(rows, user.WorkspaceName)
}

func (s *Server) pageLibrary(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	owned, err := s.store.ListOwnedPages(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	shared, err := s.store.ListPagesSharedWithUser(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Page = "pages"
	p.Title = "Pages"
	p.OwnedPages = pageLinks(owned, user.WorkspaceName)
	p.SharedPages = pageLinks(shared, user.WorkspaceName)
	s.render(w, http.StatusOK, "app", finish(p))
}

func pageLinks(rows []store.Page, workspaceName string) []pageLinkView {
	out := make([]pageLinkView, 0, len(rows))
	for _, row := range rows {
		out = append(out, pageLinkView{
			Title: row.Title,
			Token: row.Token,
			Sub:   pageSub(row.Visibility, workspaceName, false),
		})
	}
	return out
}

func pageSub(visibility, workspaceName string, behind bool) string {
	var sub string
	switch visibility {
	case store.PageWorkspace:
		sub = strings.TrimSpace(workspaceName)
		if sub == "" {
			sub = "Workspace"
		}
	case store.PageInvited:
		sub = "People you invited"
	case store.PageLink:
		sub = "Anyone with the link"
	default:
		sub = "Only you"
	}
	if behind {
		sub += " · Updated below"
	}
	return sub
}
