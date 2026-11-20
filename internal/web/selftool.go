package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/selftool"
	"github.com/scott/castbot/internal/store"
)

// machineSelf runs one account read for the machine's account and the agent
// in the path. user_id and agent_id arguments are ignored.
func (s *Server) machineSelf(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	caller, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, "That request was rejected.", true)
		return
	}
	var args selfArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, "That request was rejected.", true)
			return
		}
	}
	text, failed := s.runSelfTool(r.Context(), user, caller, name, args)
	writeAgentResult(w, text, failed)
}

// maybeSelf reads one castbot-account fence from the latest reply and
// appends the report. A second call finds no fence. The same lock as
// maybeCreate keeps the stream and the after-run hook from writing the
// reply at the same time.
func (s *Server) maybeSelf(job run.Job) {
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
	visible, req, ok := selftool.Parse(reply.Body)
	if !ok {
		return
	}
	user, err := s.store.GetUser(ctx, job.UserID)
	if err != nil {
		return
	}
	caller, err := s.store.GetAgent(ctx, job.UserID, job.AgentID)
	if err != nil {
		return
	}
	text, failed := s.runSelfTool(ctx, user, caller, req.Tool, selfArgs{Query: req.Query, ID: req.ID})
	body := strings.TrimSpace(visible)
	report := strings.TrimSpace(text)
	switch {
	case body == "":
		body = report
	case failed || !reportSeen(body, report):
		body = joinLimit(body, report, 8000)
	}
	body, _ = redactSecrets(body)
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func reportSeen(visible, report string) bool {
	report = strings.TrimSpace(report)
	if report == "" {
		return true
	}
	if strings.Contains(visible, report) {
		return true
	}
	line, _, _ := strings.Cut(report, "\n")
	line = strings.TrimSpace(line)
	return line != "" && strings.Contains(visible, line)
}

type selfArgs struct {
	Query string `json:"query"`
	ID    string `json:"id"`
}

func (s *Server) runSelfTool(ctx context.Context, user store.User, caller store.Agent, name string, args selfArgs) (string, bool) {
	var text string
	var failed bool
	switch name {
	case selftool.ToolDescribe:
		text, failed = s.describeMe(ctx, user, caller)
	case selftool.ToolUsage:
		text, failed = s.accountUsageText(ctx, user, caller)
	case selftool.ToolMachine:
		text, failed = s.machineStatusText(ctx, user, caller)
	case selftool.ToolApprovals:
		text, failed = s.approvalHistoryText(ctx, user, caller, args.Query)
	case selftool.ToolRuns:
		text, failed = s.routineRunsText(ctx, user, caller, args.ID)
	case selftool.ToolSecrets:
		text, failed = s.secretNamesText(ctx, user, caller)
	case selftool.ToolFiles:
		text, failed = s.filesText(ctx, user, caller)
	case selftool.ToolConnections:
		text, failed = s.connectionsText(ctx, user)
	case selftool.ToolCLI:
		text, failed = s.cliTokensText(ctx, user, caller)
	case selftool.ToolAccount:
		text, failed = s.accountOverviewText(ctx, user, caller)
	case selftool.ToolSearch:
		text, failed = s.searchAccountText(ctx, user, args.Query)
	default:
		text, failed = "That request was rejected.", true
	}
	text, _ = redactSecrets(text)
	if strings.TrimSpace(text) == "" {
		return "That could not be read.", true
	}
	return text, failed
}
