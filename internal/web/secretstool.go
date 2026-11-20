package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/secretstool"
	"github.com/scott/castbot/internal/store"
)

const (
	secMiss         = "That secret was not found."
	secMany         = "More than one secret matches. Say which one."
	secAsk          = "Say which secret."
	secAgentMiss    = "That agent was not found."
	secAgentMany    = "More than one agent matches. Say which one."
	secAgentAsk     = "Say which agent."
	secRejected     = "That request was rejected."
	secRead         = "Secrets could not be read."
	secCouldNot     = "That secret could not be saved."
	secNoGrant      = "That secret cannot be given to an agent. The value stays in the vault."
	secNotHeld      = "That agent does not have this secret."
	secNoMove       = "That secret cannot move into the connection vault."
	secMovedAlready = "That secret is already in the connection vault."
	secAgents       = "Agents could not be read."
)

// machineVault runs one secret tool for the machine's account.
// A user_id, agent_id, or value argument is ignored. The value stays in the vault.
func (s *Server) machineVault(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, secRejected, true)
		return
	}
	if _, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id")); err != nil {
		writeAgentResult(w, secRejected, true)
		return
	}
	var args vaultArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, secRejected, true)
			return
		}
	}
	text, failed := s.runVaultTool(r.Context(), user, name, args)
	writeAgentResult(w, text, failed)
}

type vaultArgs struct {
	Secret string `json:"secret"`
	Agent  string `json:"agent"`
}

// maybeVault reads one castbot-secrets fence from the latest reply.
func (s *Server) maybeVault(job run.Job) {
	ctx := context.Background()
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, fields, ok := secretstool.Parse(reply.Body)
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
	text, failed := s.runVaultTool(ctx, user, fields.Action, vaultArgs{Secret: fields.Secret, Agent: fields.Agent})
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func (s *Server) runVaultTool(ctx context.Context, user store.User, name string, args vaultArgs) (string, bool) {
	switch name {
	case secretstool.ToolGrant:
		return s.grantSecretNow(ctx, user, args)
	case secretstool.ToolRevoke:
		return s.revokeSecretNow(ctx, user, args)
	case secretstool.ToolMove:
		return s.moveConnectionNow(ctx, user, args.Secret)
	case secretstool.ToolDelete:
		return s.deleteVaultNow(ctx, user, args.Secret)
	default:
		return secRejected, true
	}
}

func (s *Server) grantSecretNow(ctx context.Context, user store.User, args vaultArgs) (string, bool) {
	sec, text, failed := s.oneSecret(ctx, user.ID, args.Secret)
	if failed {
		return text, true
	}
	agent, text, failed := s.oneOwnedAgent(ctx, user.ID, args.Agent)
	if failed {
		return text, true
	}
	if s.reloadBlocked(ctx, user.ID, []string{agent.ID}) {
		return replaceSentence, true
	}
	if err := s.store.GrantSecret(ctx, user.ID, agent.ID, sec.ID); err != nil {
		if errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrNotFound) {
			return secNoGrant, true
		}
		return secCouldNot, true
	}
	s.audit(ctx, user, "secret.granted", sec.ID, sec.Name, agent.ID, agent.Name)
	s.reloadOne(ctx, user.ID, sec.ID, agent.ID)
	return "Granted " + sec.Name + " to " + agent.Name + ". No new machine.", false
}

func (s *Server) revokeSecretNow(ctx context.Context, user store.User, args vaultArgs) (string, bool) {
	sec, text, failed := s.oneSecret(ctx, user.ID, args.Secret)
	if failed {
		return text, true
	}
	agent, text, failed := s.oneOwnedAgent(ctx, user.ID, args.Agent)
	if failed {
		return text, true
	}
	if s.reloadBlocked(ctx, user.ID, []string{agent.ID}) {
		return replaceSentence, true
	}
	if err := s.store.RevokeSecret(ctx, user.ID, agent.ID, sec.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return secNotHeld, true
		}
		return secCouldNot, true
	}
	s.audit(ctx, user, "secret.revoked", sec.ID, sec.Name, agent.ID, agent.Name)
	s.reloadOne(ctx, user.ID, sec.ID, agent.ID)
	return "Revoked " + sec.Name + " from " + agent.Name + ". The secret stays in the vault. No new machine.", false
}

func (s *Server) moveConnectionNow(ctx context.Context, user store.User, query string) (string, bool) {
	sec, text, failed := s.oneSecret(ctx, user.ID, query)
	if failed {
		return text, true
	}
	kind, err := s.store.SecretKind(ctx, user.ID, sec.ID)
	if err != nil {
		return secRead, true
	}
	if kind == "connection" {
		return secMovedAlready, false
	}
	connection, known := connect.ProviderEnv(sec.Name)
	if !known || sec.Provider != "" {
		return secNoMove, true
	}
	value, err := s.store.DecryptSecret(ctx, user.ID, sec.ID)
	if err != nil {
		return secCouldNot, true
	}
	granted, err := s.store.GrantAgentIDs(ctx, user.ID, sec.ID)
	if err != nil {
		return secRead, true
	}
	agents := s.connectionTargets(ctx, user.ID, connection, granted)
	vaultName := connect.SecretName(connection)
	if sec.Name == vaultName {
		if err := s.store.DeleteSecret(ctx, user.ID, sec.ID); err != nil {
			return secCouldNot, true
		}
		if _, err := s.store.SaveConnectionSecret(ctx, user.ID, connection, value); err != nil {
			return secCouldNot, true
		}
	} else {
		extra, err := s.moveAliasToConnection(ctx, user, sec.ID, connection, vaultName, value)
		if err != nil {
			return secCouldNot, true
		}
		agents = append(agents, extra...)
	}
	if connection == connect.DigitalOcean {
		_, _ = s.ensureDigitalOceanSSH(ctx, user.ID)
	}
	if err := s.enableConnectionAgents(ctx, user, connection, agents); err != nil {
		return secCouldNot, true
	}
	return "Moved " + sec.Name + " into the connection vault. The value stays in the vault. The agent does not see it.", false
}

func (s *Server) deleteVaultNow(ctx context.Context, user store.User, query string) (string, bool) {
	sec, text, failed := s.oneSecret(ctx, user.ID, query)
	if failed {
		return text, true
	}
	if sec.Provider != "" {
		if slot, ok := slotByID(sec.Provider); ok {
			blocked, err := s.keyInUse(ctx, user.ID, sec, slot)
			if err != nil {
				return secRead, true
			}
			if blocked != "" {
				return blocked, true
			}
		}
	}
	ids, err := s.store.GrantAgentIDs(ctx, user.ID, sec.ID)
	if err != nil {
		return secRead, true
	}
	if s.reloadBlocked(ctx, user.ID, ids) {
		return replaceSentence, true
	}
	names, err := s.agentNames(ctx, user.ID, ids)
	if err != nil {
		return secAgents, true
	}
	if err := s.store.DeleteSecret(ctx, user.ID, sec.ID); err != nil {
		return secCouldNot, true
	}
	s.audit(ctx, user, "secret.deleted", sec.ID, sec.Name, "", "")
	s.reloadAgents(ctx, user.ID, sec.ID, ids)
	who := "No agent had it."
	if len(names) > 0 {
		who = englishAnd(names) + " lose it."
	}
	return "Deleted " + sec.Name + ". " + who + " No new machine.", false
}

func (s *Server) oneSecret(ctx context.Context, userID, query string) (store.Secret, string, bool) {
	if strings.TrimSpace(query) == "" {
		return store.Secret{}, secAsk, true
	}
	rows, err := s.store.ListSecrets(ctx, userID, "")
	if err != nil {
		return store.Secret{}, secRead, true
	}
	chosen := matchSecrets(query, rows)
	if len(chosen) == 0 {
		return store.Secret{}, secMiss, true
	}
	if len(chosen) > 1 {
		return store.Secret{}, secretChoice(secMany, chosen), true
	}
	return chosen[0], "", false
}

func (s *Server) oneOwnedAgent(ctx context.Context, userID, query string) (store.Agent, string, bool) {
	if strings.TrimSpace(query) == "" {
		return store.Agent{}, secAgentAsk, true
	}
	rows, err := s.store.ListAgents(ctx, userID)
	if err != nil {
		return store.Agent{}, secAgents, true
	}
	chosen := matchOwnedAgents(query, rows)
	if len(chosen) == 0 {
		return store.Agent{}, secAgentMiss, true
	}
	if len(chosen) > 1 {
		return store.Agent{}, agentChoice(secAgentMany, chosen), true
	}
	return chosen[0], "", false
}

func (s *Server) agentNames(ctx context.Context, userID string, ids []string) ([]string, error) {
	var names []string
	for _, id := range ids {
		agent, err := s.store.GetAgent(ctx, userID, id)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		names = append(names, agent.Name)
	}
	sort.Strings(names)
	return names, nil
}

func matchSecrets(query string, rows []store.Secret) []store.Secret {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	var exactID, exactName, loose []store.Secret
	q := strings.ToLower(query)
	for _, row := range rows {
		if strings.EqualFold(row.ID, query) {
			exactID = append(exactID, row)
		}
		if strings.EqualFold(row.Name, query) {
			exactName = append(exactName, row)
		}
		if strings.Contains(strings.ToLower(row.Name+" "+row.ID), q) {
			loose = append(loose, row)
		}
	}
	if len(exactID) > 0 {
		return exactID
	}
	if len(exactName) > 0 {
		return exactName
	}
	return loose
}

func matchOwnedAgents(query string, rows []store.Agent) []store.Agent {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	var exactID, exactName, loose []store.Agent
	q := strings.ToLower(query)
	for _, row := range rows {
		if strings.EqualFold(row.ID, query) {
			exactID = append(exactID, row)
		}
		if strings.EqualFold(row.Name, query) {
			exactName = append(exactName, row)
		}
		if strings.Contains(strings.ToLower(row.Name+" "+row.ID), q) {
			loose = append(loose, row)
		}
	}
	if len(exactID) > 0 {
		return exactID
	}
	if len(exactName) > 0 {
		return exactName
	}
	return loose
}

func secretChoice(head string, rows []store.Secret) string {
	var b strings.Builder
	b.WriteString(head)
	for _, row := range rows {
		line := row.Name + ". " + row.ID
		if b.Len()+1+len(line) > 8000 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}

func agentChoice(head string, rows []store.Agent) string {
	var b strings.Builder
	b.WriteString(head)
	for _, row := range rows {
		line := row.Name + ". " + row.ID
		if b.Len()+1+len(line) > 8000 {
			break
		}
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}
