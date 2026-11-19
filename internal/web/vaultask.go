package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/secretask"
	"github.com/scott/castbot/internal/store"
)

const secretDeleteFor = 10 * time.Minute

// maybeSecret reads one castbot-secret fence from the latest reply and
// opens a save card or a delete approval. A fence is removed from the
// reply. The typed key never enters the reply or the tool result.
func (s *Server) maybeSecret(job run.Job) {
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	unlock := s.lockCreate(job.RunID)
	defer unlock()
	ctx := context.Background()
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, req, ok := secretask.Parse(reply.Body)
	if !ok {
		return
	}
	note := ""
	if !req.Skip {
		note = s.openSecretAsk(ctx, job, req)
	}
	body := strings.TrimSpace(visible)
	if body == "" {
		body = secretAskFallback(req, note)
	}
	s.replaceReply(ctx, job.UserID, reply.ID, body)
}

func secretAskFallback(req secretask.Request, note string) string {
	if req.Skip {
		return "Say which API to connect and I will ask for the key."
	}
	switch note {
	case "card":
		if req.Action == "delete" {
			return "Approve the card to delete the key."
		}
		return "Paste the key on the card."
	case "already_configured":
		return "That key is already saved for this agent."
	case "not_found":
		return "There is no saved key with that name."
	case "rejected":
		return "That name cannot be saved from chat."
	case "declined":
		return "That request was declined."
	default:
		if req.Action == "delete" {
			return "Approve the card to delete the key."
		}
		return "Paste the key on the card."
	}
}

func (s *Server) openSecretAsk(ctx context.Context, job run.Job, req secretask.Request) string {
	name := strings.ToUpper(strings.TrimSpace(req.Name))
	reason := connect.ScrubReason(req.Reason)
	label := cleanSecretLabel(req.Label)
	if !store.SecretNameOK(name) {
		return "rejected"
	}
	switch req.Action {
	case "save":
		_, result := s.beginEnvSecret(ctx, job.UserID, job.AgentID, job.ThreadID, job.RunID, name, label, reason)
		return result
	case "delete":
		_, result := s.beginSecretDelete(ctx, job.UserID, job.AgentID, job.ThreadID, job.RunID, name, reason)
		return result
	default:
		return "rejected"
	}
}

// beginEnvSecret opens a masked card, or returns a terminal result when no
// card should be shown. result is card, already_configured, rejected, or declined.
func (s *Server) beginEnvSecret(ctx context.Context, userID, agentID, threadID, runID, name, label, reason string) (store.SecretRequest, string) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if !store.SecretNameOK(name) || connect.BlocksGuest(name) {
		return store.SecretRequest{}, "rejected"
	}
	state, err := s.envSecretState(ctx, userID, agentID, name)
	if err != nil {
		return store.SecretRequest{}, "rejected"
	}
	if state == "blocked" {
		return store.SecretRequest{}, "rejected"
	}
	if state == "granted" {
		return store.SecretRequest{}, "already_configured"
	}
	row, _, err := s.store.CreateNamedSecretRequest(ctx, userID, agentID, threadID, runID, name, cleanSecretLabel(label), connect.ScrubReason(reason), s.store.Now().Add(connect.RequestTTL*time.Minute))
	if errors.Is(err, store.ErrConflict) {
		return store.SecretRequest{}, "declined"
	}
	if err != nil {
		return store.SecretRequest{}, "rejected"
	}
	s.notifyAgent(ctx, userID, agentID)
	return row, "card"
}

// beginSecretDelete opens an approval card, or returns a terminal result.
// result is card, not_found, rejected, or declined.
func (s *Server) beginSecretDelete(ctx context.Context, userID, agentID, threadID, runID, name, reason string) (store.SecretDelete, string) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if !store.SecretNameOK(name) || connect.BlocksGuest(name) {
		return store.SecretDelete{}, "rejected"
	}
	state, err := s.envSecretState(ctx, userID, agentID, name)
	if err != nil {
		return store.SecretDelete{}, "rejected"
	}
	if state == "missing" {
		return store.SecretDelete{}, "not_found"
	}
	if state == "blocked" {
		return store.SecretDelete{}, "rejected"
	}
	row, _, err := s.store.CreateSecretDelete(ctx, userID, agentID, threadID, runID, name, connect.ScrubReason(reason), s.store.Now().Add(secretDeleteFor))
	if errors.Is(err, store.ErrConflict) {
		return store.SecretDelete{}, "declined"
	}
	if err != nil {
		return store.SecretDelete{}, "rejected"
	}
	s.notifyAgent(ctx, userID, agentID)
	return row, "card"
}

// envSecretState is missing, granted, saved, or blocked.
// saved means the vault has the key and this agent does not.
// blocked means the name is a model key or not an environment secret.
func (s *Server) envSecretState(ctx context.Context, userID, agentID, name string) (string, error) {
	sec, err := s.store.SecretByName(ctx, userID, name)
	if errors.Is(err, store.ErrNotFound) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	kind, err := s.store.SecretKind(ctx, userID, sec.ID)
	if err != nil {
		return "", err
	}
	if sec.Provider != "" || kind != "env" || connect.BlocksGuest(sec.Name) {
		return "blocked", nil
	}
	granted, err := s.store.GrantedMeta(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	for _, row := range granted {
		if row.Name == name {
			return "granted", nil
		}
	}
	return "saved", nil
}

func (s *Server) machineEnvSecret(w http.ResponseWriter, r *http.Request, userID, name, label, reason string) {
	agentID := r.PathValue("id")
	runID, threadID, _ := s.store.OpenRun(r.Context(), userID, agentID)
	row, result := s.beginEnvSecret(r.Context(), userID, agentID, threadID, runID, name, label, reason)
	if result != "card" {
		writeSecretResult(w, result)
		return
	}
	writeSecretResult(w, s.waitSecret(r.Context(), userID, row.ID))
}

func (s *Server) machineDeleteSecret(w http.ResponseWriter, r *http.Request, userID, name, reason string) {
	agentID := r.PathValue("id")
	runID, threadID, _ := s.store.OpenRun(r.Context(), userID, agentID)
	row, result := s.beginSecretDelete(r.Context(), userID, agentID, threadID, runID, name, reason)
	if result != "card" {
		writeSecretResult(w, result)
		return
	}
	writeSecretResult(w, s.waitSecretDelete(r.Context(), userID, row.ID))
}

func (s *Server) waitSecretDelete(ctx context.Context, userID, id string) string {
	for {
		if err := ctx.Err(); err != nil {
			return "expired"
		}
		row, err := s.store.ExpireSecretDelete(ctx, userID, id)
		if err != nil {
			return "expired"
		}
		if row.Status != "pending" {
			switch row.Status {
			case "deleted", "declined", "expired":
				return row.Status
			default:
				return "expired"
			}
		}
		timer := time.NewTimer(connectPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "expired"
		case <-timer.C:
		}
	}
}

func writeSecretResult(w http.ResponseWriter, result string) {
	switch result {
	case "saved", "declined", "expired", "already_configured", "rejected", "deleted", "not_found":
		writeJSON(w, http.StatusOK, map[string]string{"result": result})
	default:
		writeEgressFail(w, "bad_request")
	}
}

func (s *Server) submitEnvSecret(w http.ResponseWriter, r *http.Request, user store.User, row store.SecretRequest) {
	name := row.Connection
	if !store.SecretNameOK(name) || connect.BlocksGuest(name) {
		view := s.presentSecret(user, row, from(r).CSRF)
		view.Line = "That key cannot be saved."
		view.Pending = true
		s.renderConnectCard(w, r, row.AgentID, "secretCard", view)
		return
	}
	value := trimSecretInput(r.FormValue("value"))
	sec, err := s.store.SecretByName(r.Context(), user.ID, name)
	missing := errors.Is(err, store.ErrNotFound)
	if err != nil && !missing {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if !missing {
		kind, kindErr := s.store.SecretKind(r.Context(), user.ID, sec.ID)
		if kindErr != nil || sec.Provider != "" || kind != "env" {
			view := s.presentSecret(user, row, from(r).CSRF)
			view.Line = "That key cannot be saved."
			view.Pending = true
			s.renderConnectCard(w, r, row.AgentID, "secretCard", view)
			return
		}
	}
	if value == "" && missing {
		view := s.presentSecret(user, row, from(r).CSRF)
		view.Line = "A key is required."
		view.Pending = true
		s.renderConnectCard(w, r, row.AgentID, "secretCard", view)
		return
	}
	var holders []string
	if !missing && value != "" {
		holders, err = s.store.GrantAgentIDs(r.Context(), user.ID, sec.ID)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		sec, err = s.store.ReplaceSecret(r.Context(), user.ID, sec.ID, value)
	} else if missing {
		sec, err = s.store.CreateSecret(r.Context(), user.ID, store.SecretInput{Name: name, Label: row.Label, Value: value})
	}
	if err != nil {
		view := s.presentSecret(user, row, from(r).CSRF)
		view.Line = "That key cannot be saved."
		view.Pending = true
		s.renderConnectCard(w, r, row.AgentID, "secretCard", view)
		return
	}
	agentName := ""
	if agent, agentErr := s.store.GetAgent(r.Context(), user.ID, row.AgentID); agentErr == nil {
		agentName = agent.Name
	}
	if err := s.store.GrantSecret(r.Context(), user.ID, row.AgentID, sec.ID); err != nil {
		view := s.presentSecret(user, row, from(r).CSRF)
		view.Line = "That key cannot be saved."
		view.Pending = true
		s.renderConnectCard(w, r, row.AgentID, "secretCard", view)
		return
	}
	if missing {
		s.audit(r.Context(), user, "secret.added", sec.ID, sec.Name, row.AgentID, agentName)
	} else if value != "" {
		s.audit(r.Context(), user, "secret.replaced", sec.ID, sec.Name, row.AgentID, agentName)
	}
	s.audit(r.Context(), user, "secret.granted", sec.ID, sec.Name, row.AgentID, agentName)
	row, err = s.store.ResolveSecretRequest(r.Context(), user.ID, row.ID, "saved")
	if s.missing(w, err) {
		return
	}
	ids := uniqueIDs(append(holders, row.AgentID))
	s.reloadAgents(r.Context(), user.ID, sec.ID, ids)
	s.notifyAgent(r.Context(), user.ID, row.AgentID)
	s.writeSecretCard(w, r, user, row)
}

func (s *Server) approveSecretDelete(w http.ResponseWriter, r *http.Request) {
	s.finishSecretDelete(w, r, "deleted")
}

func (s *Server) denySecretDelete(w http.ResponseWriter, r *http.Request) {
	s.finishSecretDelete(w, r, "declined")
}

func (s *Server) finishSecretDelete(w http.ResponseWriter, r *http.Request, status string) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	if _, err := s.store.GetAgent(r.Context(), user.ID, agentID); s.missing(w, err) {
		return
	}
	row, err := s.store.GetSecretDelete(r.Context(), user.ID, r.PathValue("req"))
	if s.missing(w, err) {
		return
	}
	if row.AgentID != agentID {
		s.missing(w, store.ErrNotFound)
		return
	}
	row, err = s.store.ExpireSecretDelete(r.Context(), user.ID, row.ID)
	if s.missing(w, err) {
		return
	}
	if row.Status != "pending" {
		s.writeSecretDelete(w, r, user, row)
		return
	}
	if status == "deleted" {
		if !s.deleteNamedSecret(r.Context(), user, row.Name) {
			row, err = s.store.ResolveSecretDelete(r.Context(), user.ID, row.ID, "declined")
			if s.missing(w, err) {
				return
			}
			s.notifyAgent(r.Context(), user.ID, agentID)
			s.writeSecretDelete(w, r, user, row)
			return
		}
	}
	row, err = s.store.ResolveSecretDelete(r.Context(), user.ID, row.ID, status)
	if s.missing(w, err) {
		return
	}
	s.notifyAgent(r.Context(), user.ID, agentID)
	s.writeSecretDelete(w, r, user, row)
}

// deleteNamedSecret removes an environment secret and reloads agents that had it.
// A missing row is already gone. A model key or connection token is left in place.
func (s *Server) deleteNamedSecret(ctx context.Context, user store.User, name string) bool {
	sec, err := s.store.SecretByName(ctx, user.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	if err != nil {
		return false
	}
	kind, err := s.store.SecretKind(ctx, user.ID, sec.ID)
	if err != nil || sec.Provider != "" || kind != "env" || !store.SecretNameOK(sec.Name) || connect.BlocksGuest(sec.Name) {
		return false
	}
	holders, err := s.store.GrantAgentIDs(ctx, user.ID, sec.ID)
	if err != nil {
		return false
	}
	if err := s.store.DeleteSecret(ctx, user.ID, sec.ID); err != nil {
		return false
	}
	s.audit(ctx, user, "secret.deleted", sec.ID, sec.Name, "", "")
	s.reloadAgents(ctx, user.ID, sec.ID, holders)
	return true
}

func (s *Server) writeSecretDelete(w http.ResponseWriter, r *http.Request, user store.User, row store.SecretDelete) {
	s.renderConnectCard(w, r, row.AgentID, "approvalCard", s.presentSecretDelete(user, row, from(r).CSRF))
}

func (s *Server) secretDeleteViews(ctx context.Context, user store.User, agentID, csrf string) []cardView {
	rows, err := s.store.ListSecretDeletes(ctx, user.ID, agentID, s.store.Now().Add(-2*time.Hour))
	if err != nil {
		return nil
	}
	var out []cardView
	for _, row := range rows {
		if row.Status == "pending" {
			row, _ = s.store.ExpireSecretDelete(ctx, user.ID, row.ID)
		}
		if row.Status == "pending" || row.ResolvedAt != "" && row.Status != "expired" {
			out = append(out, s.presentSecretDelete(user, row, csrf))
		}
	}
	return out
}

func (s *Server) presentEnvSecret(user store.User, row store.SecretRequest, csrf string) cardView {
	name := row.Connection
	title := "Save " + name
	if label := strings.TrimSpace(row.Label); label != "" && label != name {
		title = label
	}
	_, err := s.store.SecretByName(context.Background(), user.ID, name)
	missing := err != nil
	view := cardView{
		ID: row.ID, Tool: title, CSRF: csrf, Secret: true,
		Reason: connect.ScrubReason(row.Reason), FieldLabel: name,
		SubmitURL:   "/agents/" + url.PathEscape(row.AgentID) + "/secret-requests/" + url.PathEscape(row.ID) + "/submit",
		DeclineURL:  "/agents/" + url.PathEscape(row.AgentID) + "/secret-requests/" + url.PathEscape(row.ID) + "/decline",
		SubmitLabel: "Save key",
		NeedsValue:  missing,
		At:          row.CreatedAt,
		When:        clockLabel(row.CreatedAt, userLocation(user.Timezone)),
	}
	if !view.NeedsValue {
		view.SubmitLabel = "Use saved key"
	}
	switch row.Status {
	case "pending":
		view.Pending = true
		view.Extra = "Saved as " + name + ". The key is stored in your vault and given to this agent. The agent does not see what you type. This request expires in 60 minutes."
		if !missing {
			view.Extra = "A saved key named " + name + " is already in your vault. Leave the field blank to give it to this agent, or paste a new key to replace it. The agent does not see what you type. This request expires in 60 minutes."
		}
	case "saved":
		view.Line = "Saved. This agent can use it on the next turn."
	case "declined":
		view.Line = "Declined."
	case "expired":
		view.Line = "Expired."
	default:
		view.Line = "This request is no longer active."
	}
	return view
}

func (s *Server) presentSecretDelete(user store.User, row store.SecretDelete, csrf string) cardView {
	view := cardView{
		ID: row.ID, Tool: "Delete " + row.Name, CSRF: csrf,
		When:       clockLabel(row.CreatedAt, userLocation(user.Timezone)),
		At:         row.CreatedAt,
		Args:       []argView{{Key: "Name", Value: row.Name}},
		ApproveURL: "/agents/" + url.PathEscape(row.AgentID) + "/secret-deletes/" + url.PathEscape(row.ID) + "/approve",
		DenyURL:    "/agents/" + url.PathEscape(row.AgentID) + "/secret-deletes/" + url.PathEscape(row.ID) + "/deny",
	}
	if reason := connect.ScrubReason(row.Reason); reason != "" {
		view.Args = append(view.Args, argText("Reason", reason))
	}
	switch row.Status {
	case "pending":
		view.Pending = true
		view.Extra = "Approving removes this key from your vault. Every agent that has it loses it on the next turn. Denied automatically in 10 minutes if not answered."
	case "deleted":
		view.Fold = "Deleted"
		view.Line = "Deleted."
	case "declined":
		view.Fold = "Denied"
		view.Line = "Denied"
	case "expired":
		view.Fold = "Expired"
		view.Line = "Expired"
	default:
		view.Fold = "Closed"
		view.Line = "This request is no longer active."
	}
	return view
}

func cleanSecretLabel(label string) string {
	label = connect.ScrubReason(label)
	runes := []rune(label)
	if len(runes) > 80 {
		label = strings.TrimSpace(string(runes[:80]))
	}
	return label
}

func uniqueIDs(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
