package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/connectiontool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const (
	connMiss     = "That connection was not found."
	connMany     = "More than one connection matches. Say which one."
	connAsk      = "Say which connection."
	connToolMiss = "Unknown tool."
	connToolMany = "More than one tool matches. Say which one."
	connToolAsk  = "Say which tool."
	connLevelBad = "Choose ask, always allow, or deny."
	connSaved    = "Saved. Every agent you own uses these choices."
	connOff      = "Disconnected. Tool choices stay. Every agent you own is off until you connect again."
	connAlready  = "Already disconnected. Tool choices stay."
	connCouldNot = "That connection could not be saved."
	connRejected = "That request was rejected."
	connReadFail = "Connections could not be read."
)

// machineConnection runs one connection tool for the machine's account.
// A user_id, agent_id, or token argument is ignored. The token stays in the vault.
func (s *Server) machineConnection(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, connRejected, true)
		return
	}
	if _, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id")); err != nil {
		writeAgentResult(w, connRejected, true)
		return
	}
	var args connectionArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, connRejected, true)
			return
		}
	}
	text, failed := s.runConnectionTool(r.Context(), user, name, args)
	writeAgentResult(w, text, failed)
}

type connectionArgs struct {
	Connection string `json:"connection"`
	Tool       string `json:"tool"`
	Level      string `json:"level"`
}

// maybeConnection reads one castbot-connection fence from the latest reply.
func (s *Server) maybeConnection(job run.Job) {
	ctx := context.Background()
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, fields, ok := connectiontool.Parse(reply.Body)
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
	text, failed := s.runConnectionTool(ctx, user, fields.Action, connectionArgs{
		Connection: fields.Connection, Tool: fields.Tool, Level: fields.Level,
	})
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func (s *Server) runConnectionTool(ctx context.Context, user store.User, name string, args connectionArgs) (string, bool) {
	switch name {
	case connectiontool.ToolDisconnect:
		return s.disconnectConnectionNow(ctx, user, args.Connection)
	case connectiontool.ToolLevel:
		return s.setToolLevelNow(ctx, user, args)
	default:
		return connRejected, true
	}
}

func (s *Server) disconnectConnectionNow(ctx context.Context, user store.User, query string) (string, bool) {
	panel, text, failed := onePanel(query)
	if failed {
		return text, true
	}
	on, err := s.store.ConnectionSwitchedOn(ctx, user.ID, panel.ID)
	if err != nil {
		return connReadFail, true
	}
	if !on {
		return connAlready, true
	}
	if err := s.store.SetConnectionSwitch(ctx, user.ID, panel.ID, false); err != nil {
		return connCouldNot, true
	}
	if err := s.projectConnection(ctx, user, panel.ID, false); err != nil {
		return connCouldNot, true
	}
	return connOff, false
}

func (s *Server) setToolLevelNow(ctx context.Context, user store.User, args connectionArgs) (string, bool) {
	panel, text, failed := onePanel(args.Connection)
	if failed {
		return text, true
	}
	tool, text, failed := onePanelTool(panel, args.Tool)
	if failed {
		return text, true
	}
	level, ok := canonicalLevel(args.Level)
	if !ok {
		return connLevelBad, true
	}
	on, err := s.store.ConnectionSwitchedOn(ctx, user.ID, panel.ID)
	if err != nil {
		return connReadFail, true
	}
	if panel.NeedsSecret && !on {
		if _, err := s.store.ConnectionSecret(ctx, user.ID, panel.ID); err != nil {
			return panel.Title + " needs a token. The token stays in your vault.", true
		}
	}
	stored, err := s.store.ConnectionLevels(ctx, user.ID, panel.ID)
	if err != nil {
		return connReadFail, true
	}
	if err := s.store.SaveConnectionLevels(ctx, user.ID, panel.ID, mergedLevels(panel, stored, tool.Name, level)); err != nil {
		return connCouldNot, true
	}
	if !on {
		return "Saved. " + panel.Title + " stays disconnected.", false
	}
	if err := s.projectConnection(ctx, user, panel.ID, true); err != nil {
		return connCouldNot, true
	}
	return connSaved, false
}

func onePanel(query string) (connect.Panel, string, bool) {
	if strings.TrimSpace(query) == "" {
		return connect.Panel{}, connAsk, true
	}
	chosen := matchPanels(query)
	if len(chosen) == 0 {
		return connect.Panel{}, connMiss, true
	}
	if len(chosen) > 1 {
		return connect.Panel{}, panelChoice(connMany, chosen), true
	}
	return chosen[0], "", false
}

func onePanelTool(panel connect.Panel, query string) (connect.PanelTool, string, bool) {
	if strings.TrimSpace(query) == "" {
		return connect.PanelTool{}, connToolAsk, true
	}
	chosen := matchPanelTools(panel, query)
	if len(chosen) == 0 {
		return connect.PanelTool{}, connToolMiss, true
	}
	if len(chosen) > 1 {
		return connect.PanelTool{}, toolChoice(connToolMany, chosen), true
	}
	return chosen[0], "", false
}

func mergedLevels(panel connect.Panel, stored map[string]string, tool, level string) map[string]string {
	out := map[string]string{}
	for _, item := range panel.Tools {
		choice := item.DefaultLevel()
		if lv := stored[item.Name]; lv != "" {
			if norm, ok := castcfg.NormalizeLevel(lv); ok {
				choice = norm
			}
		}
		out[item.Name] = choice
	}
	out[tool] = level
	return out
}

func canonicalLevel(raw string) (string, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	raw = strings.ReplaceAll(raw, " ", "-")
	return castcfg.NormalizeLevel(raw)
}

func matchPanels(query string) []connect.Panel {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	var exactID, exactTitle, loose []connect.Panel
	q := strings.ToLower(query)
	for _, panel := range connect.Panels() {
		if strings.EqualFold(panel.ID, query) {
			exactID = append(exactID, panel)
		}
		if strings.EqualFold(panel.Title, query) {
			exactTitle = append(exactTitle, panel)
		}
		if strings.Contains(strings.ToLower(panel.Title+" "+panel.ID), q) {
			loose = append(loose, panel)
		}
	}
	if len(exactID) > 0 {
		return exactID
	}
	if len(exactTitle) > 0 {
		return exactTitle
	}
	return loose
}

func matchPanelTools(panel connect.Panel, query string) []connect.PanelTool {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	var exactName, exactTitle, loose []connect.PanelTool
	q := strings.ToLower(query)
	for _, tool := range panel.Tools {
		if strings.EqualFold(tool.Name, query) {
			exactName = append(exactName, tool)
		}
		if strings.EqualFold(tool.Title, query) {
			exactTitle = append(exactTitle, tool)
		}
		if strings.Contains(strings.ToLower(tool.Title+" "+tool.Name), q) {
			loose = append(loose, tool)
		}
	}
	if len(exactName) > 0 {
		return exactName
	}
	if len(exactTitle) > 0 {
		return exactTitle
	}
	return loose
}

func panelChoice(head string, panels []connect.Panel) string {
	var b strings.Builder
	b.WriteString(head)
	for _, panel := range panels {
		line := panel.Title + ". " + panel.ID
		if b.Len()+1+len(line) > 8000 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}

func toolChoice(head string, tools []connect.PanelTool) string {
	var b strings.Builder
	b.WriteString(head)
	for _, tool := range tools {
		line := tool.Title + ". " + tool.Name
		if b.Len()+1+len(line) > 8000 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}
