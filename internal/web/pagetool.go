package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/scott/castbot/internal/pagetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const (
	pageRejected = "That request was rejected."
	pageMiss     = "No page matches that."
	pageDoc      = "That document was not saved. Check the page format and try again."
	pageCap      = "That page was not saved. This agent or the workspace is at the page limit."
	pageStale    = "The page changed. Read it and try again."
	pageTitle    = "Enter a title up to 80 characters."
	pageVis      = "Choose only_you, workspace, invited, or link."
	pageWho      = "Say who should see it."
	pagePeople   = "No person matches that name."
	pageAgents   = "No agent matches that name."
	pageManyP    = "More than one person matches. Say which one."
	pageManyA    = "More than one agent matches. Say which one."
	pageShare    = "The page could not be shared."
	pageEmpty    = "No pages yet."
	pageRead     = "The page could not be read."
)

// machinePage runs one page tool for the machine's account and the agent
// in the path. A user_id or agent_id argument is ignored.
func (s *Server) machinePage(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	caller, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, pageRejected, true)
		return
	}
	var args pageCall
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, pageRejected, true)
			return
		}
	}
	fields, ok := pagetool.FieldsFromCall(name, args.Title, args.Page, args.Visibility, args.Document, args.Revision, args.People, args.Agents)
	if !ok && name != pagetool.ToolList {
		writeAgentResult(w, pageRejected, true)
		return
	}
	if name == pagetool.ToolList {
		fields = pagetool.Fields{Action: pagetool.ToolList}
	}
	text, failed := s.runPageTool(r.Context(), userID, caller, fields)
	writeAgentResult(w, text, failed)
}

type pageCall struct {
	Title      string          `json:"title"`
	Document   json.RawMessage `json:"document"`
	Page       string          `json:"page"`
	Revision   *int            `json:"revision"`
	Visibility string          `json:"visibility"`
	People     []string        `json:"people"`
	Agents     []string        `json:"agents"`
}

// maybePage reads one castbot-page fence from the latest reply.
func (s *Server) maybePage(job run.Job) {
	ctx := context.Background()
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, fields, ok := pagetool.Parse(reply.Body)
	if !ok {
		return
	}
	caller, err := s.store.GetAgent(ctx, job.UserID, job.AgentID)
	if err != nil {
		return
	}
	text, failed := s.runPageTool(ctx, job.UserID, caller, fields)
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed || !strings.Contains(body, text) {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func (s *Server) runPageTool(ctx context.Context, userID string, caller store.Agent, fields pagetool.Fields) (string, bool) {
	switch fields.Action {
	case pagetool.ToolCreate:
		row, err := s.store.CreatePage(ctx, userID, caller.ID, fields.Title, fields.Document)
		return pageWrite("Saved", row, err)
	case pagetool.ToolUpdate:
		row, err := s.store.UpdatePage(ctx, userID, caller.ID, fields.Page, fields.Document, fields.Revision)
		return pageWrite("Updated", row, err)
	case pagetool.ToolRename:
		row, err := s.store.RenamePage(ctx, userID, caller.ID, fields.Page, fields.Title)
		if errors.Is(err, store.ErrInvalid) {
			return pageTitle, true
		}
		return pageWrite("Renamed", row, err)
	case pagetool.ToolDelete:
		current, err := s.store.PageForAgent(ctx, userID, caller.ID, fields.Page)
		if err != nil {
			return pageErr(err), true
		}
		if current.AgentID != caller.ID || current.UserID != userID {
			return pageMiss, true
		}
		if err := s.store.DeletePage(ctx, userID, caller.ID, fields.Page); err != nil {
			return pageErr(err), true
		}
		return "Deleted " + current.Title + ".", false
	case pagetool.ToolList:
		return s.listPageText(ctx, userID, caller.ID)
	case pagetool.ToolRead:
		row, err := s.store.PageForAgent(ctx, userID, caller.ID, fields.Page)
		if err != nil {
			return pageErr(err), true
		}
		return pageReadText(row), false
	case pagetool.ToolShare:
		return s.sharePageText(ctx, userID, caller, fields)
	default:
		return pageRejected, true
	}
}

func pageWrite(verb string, row store.Page, err error) (string, bool) {
	if err != nil {
		return pageErr(err), true
	}
	return verb + " " + row.Title + ". /p/" + row.Token + ". Revision " + strconv.Itoa(row.Revision) + ". " + pageVisWord(row.Visibility) + ".", false
}

func pageErr(err error) string {
	switch {
	case errors.Is(err, store.ErrRevision):
		return pageStale
	case errors.Is(err, store.ErrPageCap):
		return pageCap
	case errors.Is(err, store.ErrInvalid):
		return pageDoc
	case errors.Is(err, store.ErrNotFound):
		return pageMiss
	default:
		return pageRejected
	}
}

func pageVisWord(v string) string {
	if v == store.PagePrivate {
		return "only_you"
	}
	return v
}

func pageVisStore(word string) (string, bool) {
	switch strings.TrimSpace(strings.ToLower(word)) {
	case "only_you", store.PagePrivate:
		return store.PagePrivate, true
	case store.PageWorkspace, store.PageInvited, store.PageLink:
		return strings.TrimSpace(strings.ToLower(word)), true
	default:
		return "", false
	}
}

func pageReadText(row store.Page) string {
	return "Title: " + row.Title + "\nVisibility: " + pageVisWord(row.Visibility) + "\nRevision: " + strconv.Itoa(row.Revision) + "\nPath: /p/" + row.Token + "\nDocument:\n" + row.Body
}

func (s *Server) listPageText(ctx context.Context, userID, agentID string) (string, bool) {
	own, err := s.store.ListAgentPages(ctx, userID, agentID)
	if err != nil {
		return pageRead, true
	}
	shared, err := s.store.ListPagesSharedWithAgent(ctx, userID, agentID)
	if err != nil {
		return pageRead, true
	}
	if len(own) == 0 && len(shared) == 0 {
		return pageEmpty, false
	}
	var b strings.Builder
	b.WriteString("Yours")
	for _, row := range own {
		writePageLine(&b, row)
	}
	if len(shared) > 0 {
		if b.Len() > 0 {
			b.WriteString("\nShared")
		}
		for _, row := range shared {
			writePageLine(&b, row)
		}
	}
	return b.String(), false
}

func writePageLine(b *strings.Builder, row store.Page) {
	line := row.Title + " | " + pageVisWord(row.Visibility) + " | /p/" + row.Token + " | " + row.ID + " | revision " + strconv.Itoa(row.Revision)
	if b.Len() > 0 && b.Len()+1+len(line) > 8000 {
		return
	}
	b.WriteByte('\n')
	b.WriteString(line)
}

func (s *Server) sharePageText(ctx context.Context, userID string, caller store.Agent, fields pagetool.Fields) (string, bool) {
	vis, ok := pageVisStore(fields.Visibility)
	if !ok {
		return pageVis, true
	}
	var userIDs, agentIDs []string
	if vis == store.PageInvited {
		var errText string
		userIDs, errText, ok = s.matchPagePeople(ctx, userID, fields.People)
		if !ok {
			return errText, true
		}
		agentIDs, errText, ok = s.matchPageAgents(ctx, userID, fields.Agents)
		if !ok {
			return errText, true
		}
		if len(userIDs)+len(agentIDs) == 0 {
			return pageWho, true
		}
	}
	row, err := s.store.SharePage(ctx, userID, caller.ID, fields.Page, vis, userIDs, agentIDs)
	if errors.Is(err, store.ErrInvalid) {
		return pageWho, true
	}
	return pageWrite("Shared", row, err)
}

func (s *Server) matchPagePeople(ctx context.Context, userID string, names []string) ([]string, string, bool) {
	if len(names) == 0 {
		return nil, "", true
	}
	members, err := s.store.ListMembers(ctx, userID)
	if err != nil {
		return nil, pageShare, false
	}
	var ids []string
	for _, name := range names {
		var hits []store.User
		for _, member := range members {
			if strings.EqualFold(member.ID, name) || strings.EqualFold(member.Email, name) || strings.EqualFold(member.DisplayName, name) {
				hits = append(hits, member)
			}
		}
		if len(hits) == 0 {
			return nil, pagePeople, false
		}
		if len(hits) > 1 {
			return nil, pageManyP, false
		}
		ids = append(ids, hits[0].ID)
	}
	return ids, "", true
}

func (s *Server) matchPageAgents(ctx context.Context, userID string, names []string) ([]string, string, bool) {
	if len(names) == 0 {
		return nil, "", true
	}
	members, err := s.store.ListMembers(ctx, userID)
	if err != nil {
		return nil, pageShare, false
	}
	var agents []store.Agent
	for _, member := range members {
		rows, err := s.store.ListAgents(ctx, member.ID)
		if err != nil {
			return nil, pageShare, false
		}
		agents = append(agents, rows...)
	}
	var ids []string
	for _, name := range names {
		var hits []store.Agent
		for _, agent := range agents {
			if strings.EqualFold(agent.ID, name) || strings.EqualFold(agent.Name, name) {
				hits = append(hits, agent)
			}
		}
		if len(hits) == 0 {
			return nil, pageAgents, false
		}
		if len(hits) > 1 {
			return nil, pageManyA, false
		}
		ids = append(ids, hits[0].ID)
	}
	return ids, "", true
}
