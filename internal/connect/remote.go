package connect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// DigitalOceanMCPURL is the only DigitalOcean tool endpoint.
	// The guest does not choose this host.
	DigitalOceanMCPURL = "https://mcp.digitalocean.com/mcp"
	mcpProtocol        = "2025-03-26"
)

// Remote forwards DigitalOcean JSON-RPC to the hosted MCP server.
// The in-process adapter is not used for that connection.
type Remote struct {
	mu       sync.Mutex
	sessions map[string]*mcpSession
	gates    map[string]*sync.Mutex
	idle     time.Duration
	now      func() time.Time
}

type mcpSession struct {
	id    string
	proto string
	used  time.Time
}

// NewRemote builds a client. Sessions stop being reused after ProcIdle.
func NewRemote() *Remote {
	return &Remote{
		sessions: map[string]*mcpSession{},
		gates:    map[string]*sync.Mutex{},
		idle:     ProcIdle,
		now:      time.Now,
	}
}

// RoundTrip posts one guest JSON-RPC message. The token is sent only as the
// bearer on the allowlisted MCP host, and it is removed from the reply.
func (r *Remote) RoundTrip(ctx context.Context, token string, msg []byte, dial *Dial) ([]byte, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(msg) == 0 || msg[0] != '{' || !json.Valid(msg) {
		return nil, errBad
	}
	key := hex.EncodeToString(sumToken(token))
	gate := r.gate(key)
	gate.Lock()
	defer gate.Unlock()
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		sess, err := r.ensure(ctx, key, token, dial)
		if err != nil {
			return nil, err
		}
		res, err := r.post(ctx, token, sess, msg, dial)
		if err != nil {
			return nil, err
		}
		if res.Status == http.StatusNotFound && sess.id != "" {
			r.drop(key)
			last = errUpstream
			continue
		}
		out, err := mcpPayload(res)
		if err != nil {
			return nil, err
		}
		if bytes.Contains(out, []byte(token)) {
			return nil, errUpstream
		}
		return out, nil
	}
	if last == nil {
		last = errUpstream
	}
	return nil, last
}

func (r *Remote) ensure(ctx context.Context, key, token string, dial *Dial) (*mcpSession, error) {
	now := r.now()
	r.mu.Lock()
	sess := r.sessions[key]
	if sess != nil && now.Sub(sess.used) < r.idle {
		sess.used = now
		r.mu.Unlock()
		return sess, nil
	}
	r.mu.Unlock()
	res, err := r.post(ctx, token, &mcpSession{proto: mcpProtocol}, initializeMessage(), dial)
	if err != nil {
		return nil, err
	}
	body, err := mcpPayload(res)
	if err != nil || rpcFailed(body) {
		return nil, errUpstream
	}
	proto := negotiatedProtocol(body)
	sid := res.Headers["mcp-session-id"]
	if sid != "" && !sessionOK(sid, token) {
		return nil, errUpstream
	}
	opened := &mcpSession{id: sid, proto: proto, used: r.now()}
	note, err := r.post(ctx, token, opened, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), dial)
	if err != nil {
		return nil, err
	}
	if note.Status != http.StatusOK && note.Status != http.StatusAccepted {
		return nil, errUpstream
	}
	r.mu.Lock()
	r.sessions[key] = opened
	r.mu.Unlock()
	return opened, nil
}

func (r *Remote) post(ctx context.Context, token string, sess *mcpSession, msg []byte, dial *Dial) (Result, error) {
	proto := mcpProtocol
	headers := map[string]string{
		"Accept":               "application/json, text/event-stream",
		"Content-Type":         "application/json",
		"MCP-Protocol-Version": proto,
	}
	if sess != nil && protocolOK(sess.proto) {
		headers["MCP-Protocol-Version"] = sess.proto
	}
	if sess != nil && sess.id != "" {
		headers["Mcp-Session-Id"] = sess.id
	}
	return Do(ctx, token, Call{
		Connection: DigitalOcean,
		Method:     http.MethodPost,
		URL:        DigitalOceanMCPURL,
		Headers:    headers,
		Body:       msg,
	}, dial)
}

func (r *Remote) gate(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	g := r.gates[key]
	if g == nil {
		g = &sync.Mutex{}
		r.gates[key] = g
	}
	return g
}

func (r *Remote) drop(key string) {
	r.mu.Lock()
	delete(r.sessions, key)
	r.mu.Unlock()
}

func initializeMessage() []byte {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": mcpProtocol,
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "castbot", "version": "1"},
		},
	})
	return raw
}

func sumToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func negotiatedProtocol(msg []byte) string {
	var env struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(msg, &env) != nil || !protocolOK(env.Result.ProtocolVersion) {
		return mcpProtocol
	}
	return env.Result.ProtocolVersion
}

func rpcFailed(msg []byte) bool {
	var env struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(msg, &env) != nil {
		return true
	}
	return len(bytes.TrimSpace(env.Error)) > 0
}

func protocolOK(v string) bool {
	if len(v) != len(mcpProtocol) || !strings.HasPrefix(v, "202") || v[4] != '-' || v[7] != '-' {
		return false
	}
	for i := 0; i < len(v); i++ {
		if i == 4 || i == 7 {
			continue
		}
		if v[i] < '0' || v[i] > '9' {
			return false
		}
	}
	return true
}

func sessionOK(id, token string) bool {
	if id == "" || len(id) > 128 || (token != "" && strings.Contains(id, token)) {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func mcpPayload(res Result) ([]byte, error) {
	if res.Status != http.StatusOK && res.Status != http.StatusAccepted {
		return nil, errUpstream
	}
	body := bytes.TrimSpace(res.Body)
	if len(body) == 0 {
		if res.Status == http.StatusAccepted {
			return []byte(`{"jsonrpc":"2.0"}`), nil
		}
		return nil, errUpstream
	}
	ctype := strings.ToLower(res.Type)
	if strings.Contains(ctype, "text/event-stream") || bytes.HasPrefix(body, []byte("data:")) || bytes.HasPrefix(body, []byte("event:")) {
		msg, ok := lastSSEJSON(body)
		if !ok {
			return nil, errUpstream
		}
		return msg, nil
	}
	if body[0] != '{' || !json.Valid(body) {
		return nil, errUpstream
	}
	return body, nil
}

func lastSSEJSON(body []byte) ([]byte, bool) {
	var data []byte
	var found []byte
	flush := func() {
		raw := bytes.TrimSpace(data)
		data = nil
		if len(raw) > 0 && raw[0] == '{' && json.Valid(raw) {
			found = append([]byte(nil), raw...)
		}
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			flush()
			continue
		}
		if line[0] == ':' {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			piece := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, piece...)
		}
	}
	flush()
	return found, len(found) > 0
}
