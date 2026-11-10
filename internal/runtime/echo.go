package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// KeepaliveInterval is how often a quiet stream emits an SSE comment.
	KeepaliveInterval = 5 * time.Second
	// ResumeRetain is how long a finished run stays resumable.
	ResumeRetain = 120 * time.Second
)

// KeepaliveFrame matches the Cast runtime comment: a colon, a space, and
// the word keepalive repeated so a quiet proxy still sees bytes.
var KeepaliveFrame = ": " + strings.Repeat("keepalive ", 400) + "\n\n"

var streamIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// EchoOptions configures the local echo server.
// WordDelay zero sends the whole reply with no pause.
// Keepalive and Retain use the Cast defaults when zero.
// FailWith records the user turn and then returns that JSON-RPC error,
// so a retry can be checked against the session transcript.
type EchoOptions struct {
	Name      string
	WordDelay time.Duration
	Keepalive time.Duration
	Retain    time.Duration
	FailWith  string
	// Quiet is a silence before the first token. Keepalives still flow.
	Quiet time.Duration
}

// Echo is a local stand-in for the Cast A2A server in echo mode.
// It speaks JSON-RPC message/send over SSE, resumes with streamId and
// streamOffset, keeps a finished run for Retain, and emits keepalive
// comments while a follower is quiet.
type Echo struct {
	name      string
	wordDelay time.Duration
	keepalive time.Duration
	retain    time.Duration
	failWith  string
	quiet     time.Duration
	base      context.Context
	cancel    context.CancelFunc

	mu       sync.Mutex
	runs     map[string]*echoRun
	sessions map[string]*echoSession
	starts   int
}

// echoSession is the stand-in for a Cast session keyed by session id.
// A message id is recorded once, so a retry of the same user turn does
// not append it again.
type echoSession struct {
	mu    sync.Mutex
	turns []Turn
	seen  map[string]struct{}
}

type echoRun struct {
	mu       sync.Mutex
	events   []string
	done     bool
	finished time.Time
	notify   chan struct{}
}

// NewEcho builds an echo server. Close stops producers.
func NewEcho(opt EchoOptions) *Echo {
	if opt.Name == "" {
		opt.Name = "echo"
	}
	if opt.Keepalive <= 0 {
		opt.Keepalive = KeepaliveInterval
	}
	if opt.Retain <= 0 {
		opt.Retain = ResumeRetain
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Echo{
		name:      opt.Name,
		wordDelay: opt.WordDelay,
		keepalive: opt.Keepalive,
		retain:    opt.Retain,
		failWith:  opt.FailWith,
		quiet:     opt.Quiet,
		base:      ctx,
		cancel:    cancel,
		runs:      map[string]*echoRun{},
		sessions:  map[string]*echoSession{},
	}
}

// Transcript returns a copy of the session turns recorded so far.
func (e *Echo) Transcript(sessionID string) []Turn {
	e.mu.Lock()
	sess := e.sessions[sessionID]
	e.mu.Unlock()
	if sess == nil {
		return nil
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return append([]Turn(nil), sess.turns...)
}

// Close stops background producers.
func (e *Echo) Close() { e.cancel() }

// Starts reports how many producers have been created.
func (e *Echo) Starts() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.starts
}

// EchoText is the Cast echo-mode reply for a user message.
func EchoText(name, message string) string {
	lower := strings.ToLower(message)
	switch lower {
	case "hi", "hello", "hey":
		return fmt.Sprintf("Hello! I'm %s. How can I help you today?", name)
	}
	if strings.Contains(lower, "tool") {
		return "I have these tools available: bash"
	}
	if strings.Contains(lower, "help") {
		return fmt.Sprintf("I'm %s. I can:\n- Run bash commands\n- Read files\n- Search for files\n\nWhat would you like me to do?", name)
	}
	return fmt.Sprintf("You said: %s\n\n(I'm a demo agent - set OPENROUTER_API_KEY for full LLM capabilities)", message)
}

func (e *Echo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/.well-known/agent.json" && r.Method == http.MethodGet {
		e.writeCard(w)
		return
	}
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
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"jsonrpc": "2.0",
			"error":   map[string]any{"code": -32700, "message": "Parse error"},
		})
		return
	}
	e.noteSession(req)
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		e.writeOnce(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	method, _ := req["method"].(string)
	text, reqID := messageText(req)
	streamID, offset := streamCursor(req["params"])
	if streamID == "" {
		e.writeOneShot(r.Context(), w, flusher, method, reqID, text)
		return
	}
	frames := e.follow(r.Context(), streamID, offset, func(ctx context.Context) <-chan string {
		return e.generate(ctx, method, reqID, text)
	})
	for frame := range frames {
		if _, err := io.WriteString(w, frame); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (e *Echo) writeOneShot(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, method, reqID, text string) {
	events := e.generate(ctx, method, reqID, text)
	for {
		timer := time.NewTimer(e.keepalive)
		select {
		case ev, ok := <-events:
			timer.Stop()
			if !ok {
				return
			}
			if _, err := io.WriteString(w, ev); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-timer.C:
			if _, err := io.WriteString(w, KeepaliveFrame); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

func (e *Echo) writeOnce(w http.ResponseWriter, req map[string]any) {
	method, _ := req["method"].(string)
	text, reqID := messageText(req)
	if e.failWith != "" && method == "message/send" {
		writeJSON(w, http.StatusOK, rpcError(reqID, -32000, e.failWith))
		return
	}
	if method != "message/send" {
		writeJSON(w, http.StatusOK, rpcError(reqID, -32601, fmt.Sprintf("Method '%s' not found", method)))
		return
	}
	if strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusOK, rpcError(reqID, -32602, "No text content found in message"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      reqID,
		"result": map[string]any{
			"parts": []map[string]string{{"kind": "text", "text": EchoText(e.name, text)}},
		},
	})
}

func (e *Echo) writeCard(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"protocolVersion": "0.2.9",
		"name":            e.name,
		"description":     "Local echo runtime",
		"url":             "http://127.0.0.1/",
		"version":         "0",
		"capabilities": map[string]any{
			"streaming":              true,
			"pushNotifications":      false,
			"stateTransitionHistory": true,
			"chatAgent":              true,
		},
		"defaultInputModes":  []string{"text/plain"},
		"defaultOutputModes": []string{"text/plain"},
		"skills":             []any{},
	})
}

func (e *Echo) follow(ctx context.Context, id string, offset int, start func(context.Context) <-chan string) <-chan string {
	out := make(chan string)
	run := e.prepare(id, start)
	go func() {
		defer close(out)
		send := func(s string) bool {
			select {
			case out <- s:
				return true
			case <-ctx.Done():
				return false
			}
		}
		if !send(fmt.Sprintf(": cast-stream %s\n\n", id)) {
			return
		}
		index := offset
		if index < 0 {
			index = 0
		}
		for {
			run.mu.Lock()
			for index >= len(run.events) && !run.done {
				ch := run.notify
				run.mu.Unlock()
				timer := time.NewTimer(e.keepalive)
				select {
				case <-ch:
					timer.Stop()
				case <-timer.C:
					if !send(KeepaliveFrame) {
						return
					}
				case <-ctx.Done():
					timer.Stop()
					return
				}
				run.mu.Lock()
			}
			if index < len(run.events) {
				ev := run.events[index]
				index++
				run.mu.Unlock()
				if !send(ev) {
					return
				}
				continue
			}
			run.mu.Unlock()
			return
		}
	}()
	return out
}

func (e *Echo) prepare(id string, start func(context.Context) <-chan string) *echoRun {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.purgeLocked()
	if run := e.runs[id]; run != nil {
		return run
	}
	run := &echoRun{notify: make(chan struct{})}
	e.runs[id] = run
	e.starts++
	go e.produce(run, start(e.base))
	return run
}

func (e *Echo) produce(run *echoRun, events <-chan string) {
	for ev := range events {
		run.mu.Lock()
		run.events = append(run.events, ev)
		close(run.notify)
		run.notify = make(chan struct{})
		run.mu.Unlock()
	}
	run.mu.Lock()
	run.done = true
	run.finished = time.Now()
	close(run.notify)
	run.notify = make(chan struct{})
	run.mu.Unlock()
}

func (e *Echo) purgeLocked() {
	now := time.Now()
	for id, run := range e.runs {
		run.mu.Lock()
		expired := run.done && !run.finished.IsZero() && now.Sub(run.finished) >= e.retain
		run.mu.Unlock()
		if expired {
			delete(e.runs, id)
		}
	}
}

func (e *Echo) generate(ctx context.Context, method, reqID, text string) <-chan string {
	ch := make(chan string)
	go func() {
		defer close(ch)
		if method != "message/send" {
			_ = sendFrame(ctx, ch, dataFrame(rpcErrorRaw(reqID, -32601, fmt.Sprintf("Method '%s' not supported for streaming", method))))
			_ = sendFrame(ctx, ch, dataFrame("[DONE]"))
			return
		}
		if strings.TrimSpace(text) == "" {
			_ = sendFrame(ctx, ch, dataFrame(rpcErrorRaw(reqID, -32602, "No text content found in message")))
			_ = sendFrame(ctx, ch, dataFrame("[DONE]"))
			return
		}
		if e.failWith != "" {
			_ = sendFrame(ctx, ch, dataFrame(rpcErrorRaw(reqID, -32000, e.failWith)))
			return
		}
		if e.quiet > 0 {
			timer := time.NewTimer(e.quiet)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
		words := strings.Split(EchoText(e.name, text), " ")
		for i, word := range words {
			if i > 0 {
				if !sendFrame(ctx, ch, textFrame(reqID, " ")) {
					return
				}
			}
			if !sendFrame(ctx, ch, textFrame(reqID, word)) {
				return
			}
			if e.wordDelay > 0 {
				timer := time.NewTimer(e.wordDelay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}
		}
		_ = sendFrame(ctx, ch, dataFrame("[DONE]"))
	}()
	return ch
}

func sendFrame(ctx context.Context, ch chan<- string, frame string) bool {
	select {
	case ch <- frame:
		return true
	case <-ctx.Done():
		return false
	}
}

func textFrame(id, text string) string {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result": map[string]any{
			"parts": []map[string]string{{"kind": "text", "text": text}},
		},
	})
	return dataFrame(string(raw))
}

func dataFrame(payload string) string { return "data: " + payload + "\n\n" }

func rpcError(id string, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	}
}

func rpcErrorRaw(id string, code int, message string) string {
	raw, _ := json.Marshal(rpcError(id, code, message))
	return string(raw)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (e *Echo) noteSession(req map[string]any) {
	if method, _ := req["method"].(string); method != "message/send" {
		return
	}
	params, _ := req["params"].(map[string]any)
	meta, _ := params["metadata"].(map[string]any)
	sessionID, _ := meta["sessionId"].(string)
	if sessionID == "" {
		return
	}
	msg, _ := params["message"].(map[string]any)
	messageID, _ := msg["messageId"].(string)
	text, _ := messageText(req)
	var history []Turn
	if raw, ok := meta["history"].([]any); ok {
		for _, item := range raw {
			row, _ := item.(map[string]any)
			role, _ := row["role"].(string)
			body, _ := row["text"].(string)
			if role == "" {
				continue
			}
			history = append(history, Turn{Role: role, Text: body})
		}
	}
	e.mu.Lock()
	sess := e.sessions[sessionID]
	if sess == nil {
		sess = &echoSession{seen: map[string]struct{}{}}
		e.sessions[sessionID] = sess
	}
	e.mu.Unlock()
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.turns) == 0 {
		sess.turns = append(sess.turns, history...)
	}
	if messageID != "" {
		if _, ok := sess.seen[messageID]; ok {
			return
		}
		sess.seen[messageID] = struct{}{}
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	sess.turns = append(sess.turns, Turn{Role: "user", Text: text})
}

func streamCursor(params any) (string, int) {
	m, ok := params.(map[string]any)
	if !ok {
		return "", 0
	}
	meta, ok := m["metadata"].(map[string]any)
	if !ok {
		return "", 0
	}
	id, _ := meta["streamId"].(string)
	if !streamIDRe.MatchString(id) {
		return "", 0
	}
	offset := 0
	switch v := meta["streamOffset"].(type) {
	case float64:
		if v > 0 {
			offset = int(v)
		}
	case bool:
		offset = 0
	}
	return id, offset
}

func messageText(req map[string]any) (string, string) {
	id := "1"
	switch v := req["id"].(type) {
	case string:
		if v != "" {
			id = v
		}
	case float64:
		id = strconv.FormatInt(int64(v), 10)
	}
	params, _ := req["params"].(map[string]any)
	msg, _ := params["message"].(map[string]any)
	parts, _ := msg["parts"].([]any)
	var b strings.Builder
	for _, part := range parts {
		pm, _ := part.(map[string]any)
		if pm["kind"] != "text" {
			continue
		}
		if text, ok := pm["text"].(string); ok {
			b.WriteString(text)
		}
	}
	return b.String(), id
}
