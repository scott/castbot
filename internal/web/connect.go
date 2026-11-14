package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

const (
	connectApprovalFor = 10 * time.Minute
	connectPoll        = 200 * time.Millisecond
)

// SetConnectDial installs a test dialer. The allowlist still runs first.
func (s *Server) SetConnectDial(d *connect.Dial) {
	s.mu.Lock()
	s.connectDial = d
	s.mu.Unlock()
}

func (s *Server) dial() *connect.Dial {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connectDial
}

func (s *Server) mcpPool() *connect.Pool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mcp == nil {
		s.mcp = connect.NewPool(connect.FromEnv())
	}
	return s.mcp
}

func scopesJSON(perms map[string]string) string {
	extra := map[string]string{}
	for key, level := range perms {
		if castcfg.KnownTool(key) || len(key) > 80 || strings.ContainsAny(key, "\r\n ") {
			continue
		}
		norm, ok := castcfg.NormalizeLevel(level)
		if !ok {
			continue
		}
		extra[key] = norm
	}
	if len(extra) == 0 {
		return ""
	}
	raw, err := json.Marshal(extra)
	if err != nil || len(raw) > 8000 {
		return ""
	}
	return string(raw)
}

func mergeScopes(spec *castcfg.Spec, raw string, posted bool, previous map[string]string) {
	if spec.Permissions == nil {
		spec.Permissions = map[string]string{}
	}
	if posted {
		if raw == "" || len(raw) > 8000 {
			return
		}
		var extra map[string]string
		if json.Unmarshal([]byte(raw), &extra) != nil {
			return
		}
		for key, level := range extra {
			if castcfg.KnownTool(key) || len(key) > 80 || strings.ContainsAny(key, "\r\n ") {
				continue
			}
			norm, ok := castcfg.NormalizeLevel(level)
			if !ok {
				continue
			}
			spec.Permissions[key] = norm
		}
		return
	}
	for key, level := range previous {
		if castcfg.KnownTool(key) {
			continue
		}
		if _, ok := spec.Permissions[key]; ok {
			continue
		}
		norm, ok := castcfg.NormalizeLevel(level)
		if !ok {
			continue
		}
		spec.Permissions[key] = norm
	}
}

func applyConnectionPerms(spec *castcfg.Spec, enabled []string, levels map[string]map[string]string) {
	if spec.Permissions == nil {
		spec.Permissions = map[string]string{}
	}
	want := map[string]string{connect.RequestTool: "always-allow"}
	if len(enabled) > 0 {
		want["mcp"] = "ask"
	}
	for _, id := range enabled {
		for _, tool := range connect.Tools(id) {
			level := "always-allow"
			if tool.Write {
				level = "ask"
			}
			if lv := levels[id][tool.Name]; lv != "" {
				if norm, ok := castcfg.NormalizeLevel(lv); ok {
					level = norm
				}
			}
			want[tool.Name] = level
		}
	}
	managed := map[string]bool{}
	for _, name := range connect.PermNames() {
		managed[name] = true
	}
	for name := range managed {
		if level, ok := want[name]; ok {
			spec.Permissions[name] = level
			continue
		}
		if name == "mcp" {
			continue
		}
		delete(spec.Permissions, name)
	}
}

func applyShellTools(spec *castcfg.Spec, on bool, levels map[string]string) {
	if spec.Permissions == nil {
		spec.Permissions = map[string]string{}
	}
	var kept []string
	for _, name := range spec.Tools {
		if castcfg.KnownTool(name) {
			continue
		}
		kept = append(kept, name)
	}
	for _, tool := range castcfg.Tools() {
		delete(spec.Permissions, tool.Name)
	}
	if !on {
		spec.Tools = kept
		return
	}
	for _, tool := range castcfg.Tools() {
		level := castcfg.DefaultLevel(tool.Name)
		if lv := levels[tool.Name]; lv != "" {
			if norm, ok := castcfg.NormalizeLevel(lv); ok {
				level = norm
			}
		}
		kept = append(kept, tool.Name)
		spec.Permissions[tool.Name] = level
	}
	spec.Tools = kept
}

func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, name := range a {
		seen[name] = true
	}
	for _, name := range b {
		if !seen[name] {
			return false
		}
	}
	return true
}

func (s *Server) scheduleBundleReload(ctx context.Context, userID, agentID, version string) error {
	if err := s.store.SetPendingBundle(ctx, userID, agentID, version); err != nil {
		return err
	}
	wait, err := s.guestRestart(ctx, userID, agentID, "")
	if err != nil || wait {
		return err
	}
	err = s.applyPendingExcept(ctx, userID, agentID, "")
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

func (s *Server) refreshConnectionBundle(ctx context.Context, userID, agentID string) error {
	bundle, err := s.store.LatestBundle(ctx, userID, agentID)
	if err != nil {
		return err
	}
	spec, err := castcfg.ParseYAML(bundle.YAML)
	if err != nil {
		return err
	}
	spec.Instructions = bundle.Instructions
	for name, level := range spec.Permissions {
		if _, ok := castcfg.NormalizeLevel(level); !ok {
			delete(spec.Permissions, name)
		}
	}
	version, err := s.storeBundle(ctx, userID, agentID, spec, true)
	if err != nil {
		return err
	}
	return s.scheduleBundleReload(ctx, userID, agentID, version)
}

func (s *Server) machineEgressStatus(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	names, err := s.store.EnabledConnections(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	live := map[string]bool{}
	for _, name := range names {
		live[name] = true
	}
	providers := []string{}
	for _, entry := range connect.Catalog() {
		if live[entry.ID] {
			providers = append(providers, entry.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "providers": providers})
}

func (s *Server) machineEgressHTTP(w http.ResponseWriter, r *http.Request) {
	s.machineEgress(w, r, false)
}

func (s *Server) machineEgressGit(w http.ResponseWriter, r *http.Request) {
	s.machineEgress(w, r, true)
}

func (s *Server) machineEgress(w http.ResponseWriter, r *http.Request, git bool) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	var body struct {
		Provider   string            `json:"provider"`
		Method     string            `json:"method"`
		Path       string            `json:"path"`
		Query      string            `json:"query"`
		Headers    map[string]string `json:"headers"`
		BodyB64    string            `json:"body_b64"`
		ApprovalID string            `json:"approval_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 48<<20))
	if err := dec.Decode(&body); err != nil {
		s.finishProxy(w, userID, agentID, connect.Call{}, "", connect.Result{Outcome: connect.OutcomeBad}, connect.ErrBad)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(body.Provider))
	rawURL, err := connect.APIURL(provider, body.Path, body.Query)
	if git {
		rawURL, err = connect.GitURL(provider, body.Path, body.Query)
	}
	if err != nil {
		s.finishProxy(w, userID, agentID, connect.Call{Connection: provider, Method: body.Method}, "", connect.Result{Outcome: connect.OutcomeBad}, connect.ErrBad)
		return
	}
	var rawBody []byte
	if body.BodyB64 != "" {
		decoded, decErr := base64.StdEncoding.DecodeString(body.BodyB64)
		if decErr != nil || len(decoded) > connect.MaxBody {
			s.finishProxy(w, userID, agentID, connect.Call{Connection: provider, Method: body.Method, URL: rawURL}, "", connect.Result{Outcome: connect.OutcomeBad}, connect.ErrBad)
			return
		}
		rawBody = decoded
	}
	call := connect.Call{
		Connection: provider,
		Method:     body.Method,
		URL:        rawURL,
		Headers:    body.Headers,
		Body:       rawBody,
		ApprovalID: cleanApprovalID(body.ApprovalID),
		Hash:       connect.RequestHash(body.Method, rawURL, rawBody),
		Summary:    connect.RequestSummary(body.Method, rawURL, rawBody),
	}
	if _, known := connect.Lookup(call.Connection); !known || !connect.MethodOK(call.Method) {
		s.finishProxy(w, userID, agentID, call, "", connect.Result{Outcome: connect.OutcomeBad}, connect.ErrBad)
		return
	}
	on, err := s.store.ConnectionEnabled(r.Context(), userID, agentID, call.Connection)
	if err != nil || !on {
		s.finishProxy(w, userID, agentID, call, "", connect.Result{Outcome: connect.OutcomeNoGrant}, connect.ErrNoGrant)
		return
	}
	if connect.Mutating(call.Method, call.URL) {
		if err := s.gateApproval(r.Context(), userID, agentID, call); err != nil {
			s.finishProxy(w, userID, agentID, call, "", connect.Result{Outcome: connect.Code(err)}, err)
			return
		}
	}
	token, err := s.store.ConnectionValue(r.Context(), userID, call.Connection)
	if err != nil {
		s.finishProxy(w, userID, agentID, call, "", connect.Result{Outcome: connect.OutcomeNoGrant}, connect.ErrNoGrant)
		return
	}
	res, err := connect.Do(r.Context(), token, call, s.dial())
	s.finishProxy(w, userID, agentID, call, token, res, err)
}

func (s *Server) finishProxy(w http.ResponseWriter, userID, agentID string, call connect.Call, token string, res connect.Result, err error) {
	outcome := res.Outcome
	if outcome == "" {
		outcome = connect.Code(err)
	}
	path := auditPath(call.URL)
	if token != "" && (strings.Contains(call.URL, token) || strings.Contains(path, token) || strings.Contains(string(res.Body), token)) {
		path = ""
		res.Body = []byte(connect.Redact(token, string(res.Body)))
	}
	_ = s.store.RecordConnectAudit(context.Background(), userID, "connection.proxy", agentID, map[string]string{
		"connection": call.Connection,
		"method":     strings.ToUpper(call.Method),
		"path":       path,
		"outcome":    outcome,
		"tool":       clipAudit(call.Tool),
	})
	if outcome != connect.OutcomeOK {
		writeEgressFail(w, egressCode(outcome))
		return
	}
	headers := res.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	payload := map[string]any{
		"status":   res.Status,
		"headers":  headers,
		"body_b64": base64.StdEncoding.EncodeToString(res.Body),
	}
	encoded, encErr := json.Marshal(payload)
	if encErr != nil || len(encoded) > connect.MaxBody {
		writeEgressFail(w, "bad_request")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) machineEgressMCP(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	var body struct {
		Server  string          `json:"server"`
		Message json.RawMessage `json:"message"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
		writeEgressFail(w, "bad_request")
		return
	}
	server := strings.ToLower(strings.TrimSpace(body.Server))
	if _, known := connect.Lookup(server); !known || len(body.Message) == 0 || body.Message[0] != '{' {
		writeEgressFail(w, "bad_request")
		return
	}
	on, err := s.store.ConnectionEnabled(r.Context(), userID, agentID, server)
	if err != nil || !on {
		writeEgressFail(w, "not_connected")
		return
	}
	var msg connect.RPC
	if json.Unmarshal(body.Message, &msg) != nil {
		writeEgressFail(w, "bad_request")
		return
	}
	switch s.namedToolGate(r.Context(), userID, server, msg) {
	case "deny":
		_ = s.store.RecordConnectAudit(r.Context(), userID, "connection.mcp", agentID, map[string]string{
			"connection": server, "outcome": "denied", "tool": clipAudit(rpcTool(msg)),
		})
		writeEgressFail(w, "bad_request")
		return
	case "allow":
	default:
		if connectNeedsApproval(server, msg) {
			entry, _ := connect.Lookup(server)
			hostName := "api.github.com"
			if len(entry.Hosts) > 0 {
				hostName = entry.Hosts[0]
			}
			tool := rpcTool(msg)
			args := rpcArgs(msg)
			hash, hashErr := connect.MCPHash(tool, args)
			if hashErr != nil || tool == "" {
				writeEgressFail(w, "bad_request")
				return
			}
			call := connect.Call{
				Connection: server,
				Method:     "POST",
				URL:        "https://" + hostName + "/mcp",
				Tool:       tool,
				Hash:       hash,
				Summary:    connect.MCPSummary(tool, args),
			}
			if err := s.gateApproval(r.Context(), userID, agentID, call); err != nil {
				outcome := connect.Code(err)
				_ = s.store.RecordConnectAudit(r.Context(), userID, "connection.mcp", agentID, map[string]string{
					"connection": server, "outcome": outcome, "tool": clipAudit(call.Tool),
				})
				writeEgressFail(w, egressCode(outcome))
				return
			}
		}
	}
	token, err := s.store.ConnectionValue(r.Context(), userID, server)
	if err != nil {
		writeEgressFail(w, "not_connected")
		return
	}
	var out json.RawMessage
	if s.mcpPool().Has(server) {
		out, err = s.mcpPool().RoundTrip(r.Context(), server, token, body.Message)
		if err != nil {
			writeEgressFail(w, "bad_request")
			return
		}
	} else {
		out, err = connect.HandleRPC(r.Context(), server, msg, true, func(ctx context.Context, call connect.Call) (connect.Result, error) {
			return connect.Do(ctx, token, call, s.dial())
		})
		if err != nil && !connect.IsRequest(err) {
			_ = s.store.RecordConnectAudit(r.Context(), userID, "connection.mcp", agentID, map[string]string{
				"connection": server, "outcome": connect.Code(err), "tool": clipAudit(rpcTool(msg)),
			})
			writeEgressFail(w, egressCode(connect.Code(err)))
			return
		}
	}
	if len(out) == 0 || out[0] != '{' {
		out = json.RawMessage(`{"jsonrpc":"2.0"}`)
	}
	if bytesContain(out, token) {
		out = json.RawMessage(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"redacted"}}`)
	}
	_ = s.store.RecordConnectAudit(r.Context(), userID, "connection.mcp", agentID, map[string]string{
		"connection": server, "outcome": connect.OutcomeOK, "tool": clipAudit(rpcTool(msg)),
	})
	writeJSON(w, http.StatusOK, map[string]any{"message": out})
}

func (s *Server) machineSecretRequest(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	var body struct {
		Connection string `json:"connection"`
		Reason     string `json:"reason"`
		Name       string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		writeEgressFail(w, "bad_request")
		return
	}
	connection := strings.ToLower(strings.TrimSpace(body.Connection))
	if _, known := connect.Lookup(connection); !known {
		writeEgressFail(w, "bad_request")
		return
	}
	if name := strings.TrimSpace(body.Name); name != "" && name != connect.SecretName(connection) {
		name = ""
	}
	on, err := s.store.ConnectionEnabled(r.Context(), userID, agentID, connection)
	if err != nil {
		writeEgressFail(w, "bad_request")
		return
	}
	if on {
		writeJSON(w, http.StatusOK, map[string]string{"result": "already_configured"})
		return
	}
	reason := connect.ScrubReason(body.Reason)
	runID, threadID, _ := s.store.OpenRun(r.Context(), userID, agentID)
	row, _, err := s.store.CreateSecretRequest(r.Context(), userID, agentID, threadID, runID, connection, reason, s.store.Now().Add(connect.RequestTTL*time.Minute))
	if errors.Is(err, store.ErrConflict) {
		writeJSON(w, http.StatusOK, map[string]string{"result": "declined"})
		return
	}
	if err != nil {
		writeEgressFail(w, "bad_request")
		return
	}
	s.notifyAgent(r.Context(), userID, agentID)
	status := s.waitSecret(r.Context(), userID, row.ID)
	switch status {
	case "saved", "declined", "expired", "already_configured":
		writeJSON(w, http.StatusOK, map[string]string{"result": status})
	default:
		writeEgressFail(w, "bad_request")
	}
}

func (s *Server) waitSecret(ctx context.Context, userID, id string) string {
	for {
		if err := ctx.Err(); err != nil {
			return "expired"
		}
		row, err := s.store.ExpireSecretRequest(ctx, userID, id)
		if err != nil {
			return "expired"
		}
		if row.Status != "pending" {
			if row.Status == "saved" || row.Status == "declined" || row.Status == "expired" {
				return row.Status
			}
			return "expired"
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

// gateApproval lets a mutating call through only when a card is already
// approved. A missing card is created and the call returns at once. The
// guest forward budget is 60 seconds, so this handler does not hold the
// request open while a person decides.
func (s *Server) gateApproval(ctx context.Context, userID, agentID string, call connect.Call) error {
	method := strings.ToUpper(strings.TrimSpace(call.Method))
	path := auditPath(call.URL)
	if len(call.Hash) != 64 {
		return connect.ErrBad
	}
	if call.ApprovalID != "" {
		row, err := s.store.GetConnectApproval(ctx, userID, call.ApprovalID)
		if err != nil {
			return connect.ErrDenied
		}
		row, err = s.store.ExpireConnectApproval(ctx, userID, row.ID)
		if err != nil {
			return err
		}
		if !approvalMatches(row, agentID, call, method) {
			return connect.ErrDenied
		}
		return consumeOrWait(s, ctx, userID, row)
	}
	since := s.store.Now().Add(-connectApprovalFor)
	rows, err := s.store.ListConnectApprovals(ctx, userID, agentID, since)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Status != "pending" && row.Status != "approved" {
			continue
		}
		if !approvalMatches(row, agentID, call, method) {
			continue
		}
		row, err = s.store.ExpireConnectApproval(ctx, userID, row.ID)
		if err != nil {
			return err
		}
		if row.Status == "expired" {
			continue
		}
		if row.Status == "pending" {
			return connect.ErrApproval
		}
		if row.Status == "approved" {
			ok, err := s.store.ConsumeConnectApproval(ctx, userID, row.ID)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
		}
	}
	_, threadID, _ := s.store.OpenRun(ctx, userID, agentID)
	action := call.Summary
	if action == "" {
		action = method + " " + path
	}
	if _, err := s.store.CreateConnectApproval(ctx, userID, agentID, threadID, call.Connection, action, method, path, call.Hash, s.store.Now().Add(connectApprovalFor)); err != nil {
		return err
	}
	s.notifyAgent(ctx, userID, agentID)
	return connect.ErrApproval
}

func consumeOrWait(s *Server, ctx context.Context, userID string, row store.ConnectApproval) error {
	switch row.Status {
	case "approved":
		ok, err := s.store.ConsumeConnectApproval(ctx, userID, row.ID)
		if err != nil {
			return err
		}
		if !ok {
			return connect.ErrDenied
		}
		return nil
	case "pending":
		return connect.ErrApproval
	case "denied":
		return connect.ErrDenied
	default:
		return connect.ErrExpired
	}
}

func approvalMatches(row store.ConnectApproval, agentID string, call connect.Call, method string) bool {
	if row.AgentID != agentID || row.Connection != call.Connection || row.Method != method {
		return false
	}
	return connect.SameHash(row.RequestHash, call.Hash)
}

func (s *Server) submitSecret(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	if _, err := s.store.GetAgent(r.Context(), user.ID, agentID); s.missing(w, err) {
		return
	}
	row, err := s.store.GetSecretRequest(r.Context(), user.ID, r.PathValue("req"))
	if s.missing(w, err) {
		return
	}
	if row.AgentID != agentID {
		s.missing(w, store.ErrNotFound)
		return
	}
	row, err = s.store.ExpireSecretRequest(r.Context(), user.ID, row.ID)
	if s.missing(w, err) {
		return
	}
	if row.Status != "pending" {
		s.writeSecretCard(w, r, user, row)
		return
	}
	value := trimSecretInput(r.FormValue("value"))
	_, secretErr := s.store.ConnectionSecret(r.Context(), user.ID, row.Connection)
	has := secretErr == nil
	if value == "" && !has {
		view := s.presentSecret(user, row, from(r).CSRF)
		view.Line = "A token is required."
		s.renderConnectCard(w, r, agentID, "secretCard", view)
		return
	}
	if value != "" {
		if _, err := s.store.SaveConnectionSecret(r.Context(), user.ID, row.Connection, value); err != nil {
			view := s.presentSecret(user, row, from(r).CSRF)
			view.Line = "That token cannot be saved."
			s.renderConnectCard(w, r, agentID, "secretCard", view)
			return
		}
	}
	if err := s.turnOnAccountConnection(r.Context(), user, row.Connection); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	row, err = s.store.ResolveSecretRequest(r.Context(), user.ID, row.ID, "saved")
	if s.missing(w, err) {
		return
	}
	s.notifyAgent(r.Context(), user.ID, agentID)
	s.writeSecretCard(w, r, user, row)
}

func (s *Server) declineSecret(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	if _, err := s.store.GetAgent(r.Context(), user.ID, agentID); s.missing(w, err) {
		return
	}
	row, err := s.store.GetSecretRequest(r.Context(), user.ID, r.PathValue("req"))
	if s.missing(w, err) {
		return
	}
	if row.AgentID != agentID {
		s.missing(w, store.ErrNotFound)
		return
	}
	if row.Status == "pending" {
		row, err = s.store.ResolveSecretRequest(r.Context(), user.ID, row.ID, "declined")
		if s.missing(w, err) {
			return
		}
	}
	s.notifyAgent(r.Context(), user.ID, agentID)
	s.writeSecretCard(w, r, user, row)
}

func (s *Server) approveConnectCard(w http.ResponseWriter, r *http.Request) {
	s.decideConnectCard(w, r, "approved")
}

func (s *Server) denyConnectCard(w http.ResponseWriter, r *http.Request) {
	s.decideConnectCard(w, r, "denied")
}

func (s *Server) decideConnectCard(w http.ResponseWriter, r *http.Request, status string) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	if _, err := s.store.GetAgent(r.Context(), user.ID, agentID); s.missing(w, err) {
		return
	}
	row, _, err := s.store.DecideConnectApproval(r.Context(), user.ID, r.PathValue("approval"), status)
	if s.missing(w, err) {
		return
	}
	if row.AgentID != agentID {
		s.missing(w, store.ErrNotFound)
		return
	}
	s.notifyAgent(r.Context(), user.ID, agentID)
	view := s.presentConnect(user, row, from(r).CSRF)
	s.renderConnectCard(w, r, agentID, "approvalCard", view)
}

func (s *Server) revokeConnection(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	connection := strings.ToLower(strings.TrimSpace(r.PathValue("connection")))
	if _, known := connect.Lookup(connection); !known {
		s.missing(w, store.ErrNotFound)
		return
	}
	if _, err := s.store.GetAgent(r.Context(), user.ID, agentID); s.missing(w, err) {
		return
	}
	if err := s.store.RevokeConnection(r.Context(), user.ID, agentID, connection); s.missing(w, err) {
		return
	}
	if err := s.refreshConnectionBundle(r.Context(), user.ID, agentID); err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.notifyAgent(r.Context(), user.ID, agentID)
	http.Redirect(w, r, "/agents/"+url.PathEscape(agentID)+"/edit", http.StatusSeeOther)
}

func (s *Server) writeSecretCard(w http.ResponseWriter, r *http.Request, user store.User, row store.SecretRequest) {
	s.renderConnectCard(w, r, row.AgentID, "secretCard", s.presentSecret(user, row, from(r).CSRF))
}

func (s *Server) renderConnectCard(w http.ResponseWriter, r *http.Request, agentID, name string, view cardView) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/agents/"+url.PathEscape(agentID), http.StatusSeeOther)
		return
	}
	body, err := execTemplate(name, view)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}

func (s *Server) secretCardViews(ctx context.Context, user store.User, agentID, csrf string) []cardView {
	rows, err := s.store.ListSecretRequests(ctx, user.ID, agentID, s.store.Now().Add(-2*time.Hour))
	if err != nil {
		return nil
	}
	var out []cardView
	for _, row := range rows {
		if row.Status == "pending" {
			row, _ = s.store.ExpireSecretRequest(ctx, user.ID, row.ID)
		}
		if row.Status == "pending" || row.ResolvedAt != "" && row.Status != "expired" {
			out = append(out, s.presentSecret(user, row, csrf))
		}
	}
	return out
}

func (s *Server) connectCardViews(ctx context.Context, user store.User, agentID, csrf string) []cardView {
	rows, err := s.store.ListConnectApprovals(ctx, user.ID, agentID, s.store.Now().Add(-2*time.Hour))
	if err != nil {
		return nil
	}
	var out []cardView
	for _, row := range rows {
		if row.Status == "pending" {
			row, _ = s.store.ExpireConnectApproval(ctx, user.ID, row.ID)
		}
		if row.Status == "consumed" {
			continue
		}
		out = append(out, s.presentConnect(user, row, csrf))
	}
	return out
}

func (s *Server) presentSecret(user store.User, row store.SecretRequest, csrf string) cardView {
	entry, _ := connect.Lookup(row.Connection)
	title := "Connection"
	label := "Token"
	if entry.Title != "" {
		title = "Connect " + entry.Title
		label = entry.TokenLabel
	}
	_, err := s.store.ConnectionSecret(context.Background(), user.ID, row.Connection)
	view := cardView{
		ID: row.ID, Tool: title, CSRF: csrf, Secret: true,
		Reason: connect.ScrubReason(row.Reason), FieldLabel: label,
		SubmitURL:   "/agents/" + url.PathEscape(row.AgentID) + "/secret-requests/" + url.PathEscape(row.ID) + "/submit",
		DeclineURL:  "/agents/" + url.PathEscape(row.AgentID) + "/secret-requests/" + url.PathEscape(row.ID) + "/decline",
		SubmitLabel: "Save token",
		NeedsValue:  err != nil,
		At:          row.CreatedAt,
		When:        clockLabel(row.CreatedAt, userLocation(user.Timezone)),
	}
	if !view.NeedsValue {
		view.SubmitLabel = "Use saved token"
	}
	switch row.Status {
	case "pending":
		view.Pending = true
		view.Extra = "The token is saved in your vault. The agent does not see it. Connecting turns this on for every agent you own. This request expires in 60 minutes."
	case "saved":
		view.Line = "Saved. Every agent you own can use this on the next turn."
	case "declined":
		view.Line = "Declined."
	case "expired":
		view.Line = "Expired."
	default:
		view.Line = "This request is no longer active."
	}
	return view
}

func (s *Server) presentConnect(user store.User, row store.ConnectApproval, csrf string) cardView {
	title := row.Action
	if title == "" {
		title = row.Method + " " + row.Path
	}
	view := cardView{
		ID: row.ID, Tool: title, CSRF: csrf,
		When: clockLabel(row.CreatedAt, userLocation(user.Timezone)),
		At:   row.CreatedAt,
		Args: []argView{
			{Key: "Method", Value: row.Method},
			argText("Path", row.Path),
		},
		ApproveURL: "/agents/" + url.PathEscape(row.AgentID) + "/connect-approvals/" + url.PathEscape(row.ID) + "/approve",
		DenyURL:    "/agents/" + url.PathEscape(row.AgentID) + "/connect-approvals/" + url.PathEscape(row.ID) + "/deny",
	}
	if entry, ok := connect.Lookup(row.Connection); ok {
		view.From = entry.Title
	}
	switch row.Status {
	case "pending":
		view.Pending = true
		view.Extra = "This changes data on the connection. The token stays in your vault."
	case "approved":
		view.Approved = true
		view.Fold = "Approved"
		view.Line = "Approved by you"
	case "denied":
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

func (s *Server) notifyAgent(ctx context.Context, userID, agentID string) {
	if s.runs == nil {
		return
	}
	id, _, err := s.store.OpenRun(ctx, userID, agentID)
	if err == nil && id != "" {
		s.runs.Hub.Notify(id)
	}
}

// namedToolGate applies the account policy to a named adapter tool.
// deny refuses the call. allow skips the host card. check keeps the
// write-tool card. A read tool set to ask stays allow, so it does not
// gain a second card on top of the runtime permission.
func (s *Server) namedToolGate(ctx context.Context, userID, connection string, msg connect.RPC) string {
	if msg.Method != "tools/call" {
		return "check"
	}
	tool, ok := connect.FindTool(connection, rpcTool(msg))
	if !ok {
		return "check"
	}
	level := "always-allow"
	if tool.Write {
		level = "ask"
	}
	levels, err := s.store.ConnectionLevels(ctx, userID, connection)
	if err != nil {
		return "check"
	}
	if lv := levels[tool.Name]; lv != "" {
		if norm, good := castcfg.NormalizeLevel(lv); good {
			level = norm
		}
	}
	switch level {
	case "deny":
		return "deny"
	case "always-allow":
		return "allow"
	default:
		if tool.Write {
			return "check"
		}
		return "allow"
	}
}

func connectNeedsApproval(connection string, msg connect.RPC) bool {
	if msg.Method != "tools/call" {
		return false
	}
	tool, ok := connect.FindTool(connection, rpcTool(msg))
	if !ok {
		return true
	}
	return tool.Write
}

func rpcTool(msg connect.RPC) string {
	var params struct {
		Name string `json:"name"`
	}
	if len(msg.Params) > 0 {
		_ = json.Unmarshal(msg.Params, &params)
	}
	return strings.TrimSpace(params.Name)
}

func rpcArgs(msg connect.RPC) json.RawMessage {
	var params struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(msg.Params) > 0 {
		_ = json.Unmarshal(msg.Params, &params)
	}
	if len(bytes.TrimSpace(params.Arguments)) == 0 {
		return json.RawMessage(`{}`)
	}
	return params.Arguments
}

func egressCode(outcome string) string {
	switch outcome {
	case connect.OutcomeNoGrant:
		return "not_connected"
	case connect.OutcomeApproval:
		return "approval_required"
	case connect.OutcomeRedirect:
		return "redirect_refused"
	default:
		return "bad_request"
	}
}

func egressMessage(code string) string {
	switch code {
	case "not_connected":
		return "Connections are not available on this machine."
	case "approval_required":
		return "This action needs approval."
	case "redirect_refused":
		return "The connection proxy refused a redirect."
	default:
		return "The connection request was rejected."
	}
}

func writeEgressFail(w http.ResponseWriter, code string) {
	switch code {
	case "not_connected", "approval_required", "redirect_refused", "bad_request":
	default:
		code = "bad_request"
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": code, "message": egressMessage(code)})
}

func cleanApprovalID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 80 {
		return ""
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return ""
	}
	return id
}

func auditPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return ""
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	if service := u.Query().Get("service"); service != "" && len(service) < 40 && !strings.ContainsAny(service, "\r\n") {
		path += "?service=" + service
	}
	if len(path) > 180 {
		path = path[:180]
	}
	return path
}

func clipAudit(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", ""), "\r", "")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func bytesContain(raw []byte, token string) bool {
	if token == "" || len(raw) == 0 {
		return false
	}
	return strings.Contains(string(raw), token)
}
