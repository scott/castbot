package runtime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

// AskFinished is the text the ask stand-in sends after Approve.
const AskFinished = "bash finished."

// AskOptions configures the local ask stand-in.
// Args may include a secret-shaped key. The stand-in echoes it so the
// control plane can prove it is not stored or shown.
type AskOptions struct {
	Tool string
	Args map[string]any
}

// Ask is a local Cast stand-in with one bash tool set to ask.
// Approve continues the reply. Deny ends it with Cast's denial text.
type Ask struct {
	tool string
	args map[string]any

	mu    sync.Mutex
	wait  map[string]chan bool
	calls []bool
}

// NewAsk builds the stand-in. Close is not required.
func NewAsk(opt AskOptions) *Ask {
	if opt.Tool == "" {
		opt.Tool = "bash"
	}
	if opt.Args == nil {
		opt.Args = map[string]any{"command": "ls"}
	}
	return &Ask{tool: opt.Tool, args: opt.Args, wait: map[string]chan bool{}}
}

// Calls lists permission/respond decisions in arrival order.
// true is Approve.
func (a *Ask) Calls() []bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]bool(nil), a.calls...)
}

// Forget drops waiting prompts. The next respond is treated as unknown.
func (a *Ask) Forget() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.wait = map[string]chan bool{}
}

func (a *Ask) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || (r.URL.Path != "/" && r.URL.Path != "") {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req map[string]any
	if json.Unmarshal(body, &req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"jsonrpc": "2.0",
			"error":   map[string]any{"code": -32700, "message": "Parse error"},
		})
		return
	}
	method, _ := req["method"].(string)
	if method == "permission/respond" {
		a.respond(w, req)
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		writeJSON(w, http.StatusOK, rpcError(strID(req["id"]), -32601, "Method not supported"))
		return
	}
	a.stream(w, r, req)
}

func (a *Ask) stream(w http.ResponseWriter, r *http.Request, req map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	id := newAskID()
	ch := make(chan bool, 1)
	a.mu.Lock()
	a.wait[id] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.wait, id)
		a.mu.Unlock()
	}()
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      strID(req["id"]),
		"result": map[string]any{
			"parts": []any{},
			"metadata": map[string]any{
				"permissionRequest": map[string]any{
					"id":        id,
					"tool":      a.tool,
					"arguments": a.args,
				},
			},
		},
	})
	if _, err := io.WriteString(w, "data: "+string(payload)+"\n\n"); err != nil {
		return
	}
	flush()
	var allow bool
	select {
	case allow = <-ch:
	case <-r.Context().Done():
		return
	}
	text := DenialMessage(a.tool)
	if allow {
		text = AskFinished
	}
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      strID(req["id"]),
		"result": map[string]any{
			"parts": []map[string]string{{"kind": "text", "text": text}},
		},
	})
	_, _ = io.WriteString(w, "data: "+string(frame)+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flush()
}

func (a *Ask) respond(w http.ResponseWriter, req map[string]any) {
	params, _ := req["params"].(map[string]any)
	id, _ := params["id"].(string)
	approved, ok := params["approved"].(bool)
	id = strings.TrimSpace(id)
	a.mu.Lock()
	ch := a.wait[id]
	if ch != nil && ok {
		delete(a.wait, id)
		a.calls = append(a.calls, approved)
	}
	a.mu.Unlock()
	if ch == nil || !ok {
		writeJSON(w, http.StatusOK, rpcError(strID(req["id"]), -32602, "No pending permission request"))
		return
	}
	ch <- approved
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      strID(req["id"]),
		"result":  map[string]any{"ok": true},
	})
}

func strID(v any) string {
	s, _ := v.(string)
	if s == "" {
		return "1"
	}
	return s
}

func newAskID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "ask"
	}
	return "ask-" + hex.EncodeToString(buf)
}
