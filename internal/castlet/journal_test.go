package castlet

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCLITokenAcceptsThenRevokes(t *testing.T) {
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"result\":{\"parts\":[{\"kind\":\"text\",\"text\":\"pong\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	sup, err := New(Config{Token: "agent-token", RuntimeURL: up.URL, AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sup.MarkReady()
	hash := sha256Hex("cli-token-for-tests")
	post := httptest.NewRequest(http.MethodPost, "/castlet/cli-tokens", strings.NewReader(`{"tokens":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hash":"`+hash+`","revoked":false}]}`))
	post.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec := httptest.NewRecorder()
	sup.ServeHTTP(rec, post)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("push %d %s", rec.Code, rec.Body.String())
	}
	body := `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"hi"}]},"metadata":{"sessionId":"sess-1"}}}`
	send := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	send.Header.Set("X-Cast-Authorization", "Bearer cli-token-for-tests")
	send.Header.Set("Accept", "text/event-stream")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, send)
	if rec.Code != http.StatusOK || hits != 1 {
		t.Fatalf("cli %d hits %d", rec.Code, hits)
	}
	admin := httptest.NewRequest(http.MethodGet, "/castlet/health", nil)
	admin.Header.Set("X-Cast-Authorization", "Bearer cli-token-for-tests")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, admin)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("admin %d", rec.Code)
	}
	revoke := httptest.NewRequest(http.MethodPost, "/castlet/cli-tokens", strings.NewReader(`{"tokens":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hash":"`+hash+`","revoked":true}]}`))
	revoke.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, revoke)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke %d", rec.Code)
	}
	again := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	again.Header.Set("X-Cast-Authorization", "Bearer cli-token-for-tests")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, again)
	if rec.Code != http.StatusUnauthorized || hits != 1 {
		t.Fatalf("after revoke %d hits %d", rec.Code, hits)
	}
	raw := string(mustRead(t, sup.stateDir()+"/journal.jsonl"))
	if strings.Contains(raw, "cli-token-for-tests") || strings.Contains(raw, "agent-token") {
		t.Fatal("journal kept a token")
	}
	if !strings.Contains(raw, `"kind":"request"`) || !strings.Contains(raw, `"kind":"reply"`) || !strings.Contains(raw, "pong") {
		t.Fatal("journal missing the turn")
	}
}

func TestIncompleteTurnIsMarked(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"result\":{\"parts\":[{\"kind\":\"text\",\"text\":\"partial\"}]}}\n\n")
	}))
	t.Cleanup(up.Close)
	sup, err := New(Config{Token: "agent-token", RuntimeURL: up.URL, AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sup.MarkReady()
	body := `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"hi"}]},"metadata":{"sessionId":"sess-1"}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	sup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	got := sup.journalAfter(0)
	var reply journalEntry
	for _, e := range got {
		if e.Kind == "reply" {
			reply = e
		}
	}
	if !reply.Incomplete || reply.Text != "partial" {
		t.Fatalf("%+v", reply)
	}
}

func TestRevokedTokenFailsClosed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"result\":{\"parts\":[{\"kind\":\"text\",\"text\":\"pong\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	hash := sha256Hex("cli-token-for-tests")
	open := true
	var used, gets int
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cli-tokens") {
			gets++
		}
		if !open {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cli-used") {
			used++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, `{"tokens":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hash":"`+hash+`","revoked":false}]}`)
	}))
	t.Cleanup(cp.Close)
	sup, err := New(Config{
		Token: "agent-token", RuntimeURL: up.URL, AgentDir: t.TempDir(),
		ControlURL: cp.URL, AgentID: "agent", AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sup.MarkReady()
	body := `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"hi"}]},"metadata":{"sessionId":"sess-1"}}}`
	send := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	send.Header.Set("X-Cast-Authorization", "Bearer cli-token-for-tests")
	rec := httptest.NewRecorder()
	sup.ServeHTTP(rec, send)
	if rec.Code != http.StatusOK || used != 1 || gets != 1 {
		t.Fatalf("first %d used %d gets %d", rec.Code, used, gets)
	}
	open = false
	cached := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	cached.Header.Set("X-Cast-Authorization", "Bearer cli-token-for-tests")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, cached)
	if rec.Code != http.StatusOK || gets != 1 {
		t.Fatalf("fresh cache %d gets %d", rec.Code, gets)
	}
	sup.cliMu.Lock()
	sup.cliFresh = time.Now().Add(-time.Minute)
	sup.cliMu.Unlock()
	again := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	again.Header.Set("X-Cast-Authorization", "Bearer cli-token-for-tests")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, again)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired cache %d", rec.Code)
	}
}

func TestPushedRevokeReplacesTheTokenCache(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(up.Close)
	hash := sha256Hex("cli-token-for-tests")
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, `{"tokens":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hash":"`+hash+`","revoked":false}]}`)
	}))
	t.Cleanup(cp.Close)
	sup, err := New(Config{
		Token: "agent-token", RuntimeURL: up.URL, AgentDir: t.TempDir(),
		ControlURL: cp.URL, AgentID: "agent", AllowHTTP: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sup.MarkReady()
	body := `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"hi"}]},"metadata":{"sessionId":"sess-1"}}}`
	send := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	send.Header.Set("X-Cast-Authorization", "Bearer cli-token-for-tests")
	rec := httptest.NewRecorder()
	sup.ServeHTTP(rec, send)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	push := httptest.NewRequest(http.MethodPost, "/castlet/cli-tokens", strings.NewReader(`{"tokens":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","hash":"`+hash+`","revoked":true}]}`))
	push.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, push)
	if rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code)
	}
	again := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	again.Header.Set("X-Cast-Authorization", "Bearer cli-token-for-tests")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, again)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("pushed revoke %d", rec.Code)
	}
}

func TestJournalSeqIsReadAndDoesNotReuseEntries(t *testing.T) {
	dir := t.TempDir()
	sup, err := New(Config{Token: "agent-token", AgentDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sup.stateDir()+"/journal.seq", []byte("4"), 0o600); err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(sup.stateDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sup.stateDir()+"/journal.seq", []byte("4"), 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Fatal(err)
		}
	}
	first, err := sup.appendJournal(journalEntry{Caller: "control_plane", Kind: "request", Text: "one"})
	if err != nil || first.Seq != 4 {
		t.Fatalf("seq %d %v", first.Seq, err)
	}
	next, err := New(Config{Token: "agent-token", AgentDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next.stateDir()+"/journal.seq", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := next.appendJournal(journalEntry{Caller: "control_plane", Kind: "request", Text: "two"})
	if err != nil || second.Seq != 5 {
		t.Fatalf("kept %d %v", second.Seq, err)
	}
	raw := string(mustRead(t, next.stateDir()+"/journal.seq"))
	if strings.TrimSpace(raw) != "6" {
		t.Fatalf("seq file %s", raw)
	}
	if _, err := os.Stat(next.stateDir() + "/journal.seq.tmp"); err == nil {
		t.Fatal("seq temp file remained")
	}
}

func TestDecisionKeepsThePermissionSession(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"result\":{\"metadata\":{\"permissionRequest\":{\"id\":\"perm-1\",\"tool\":\"bash\"}}}}\n\n")
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"result\":{\"parts\":[{\"kind\":\"text\",\"text\":\"wait\"}]}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	sup, err := New(Config{Token: "agent-token", RuntimeURL: up.URL, AgentDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sup.MarkReady()
	body := `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"role":"user","messageId":"msg-1","parts":[{"kind":"text","text":"hi"}]},"metadata":{"sessionId":"sess-9","streamId":"run-9"}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	sup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	respond := `{"jsonrpc":"2.0","id":"2","method":"permission/respond","params":{"id":"perm-1","approved":true}}`
	dec := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(respond))
	dec.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, dec)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	entries := sup.journalAfter(0)
	var request, decision journalEntry
	for _, e := range entries {
		if e.Kind == "request" {
			request = e
		}
		if e.Kind == "decision" {
			decision = e
		}
	}
	if request.SessionID != "sess-9" || request.MessageID != "msg-1" || request.StreamID != "run-9" {
		t.Fatalf("request %+v", request)
	}
	if decision.SessionID != "sess-9" || decision.RequestID != "perm-1" || decision.Decision != "approved" {
		t.Fatalf("decision %+v", decision)
	}
	later := sup.journalAfter(request.Seq)
	if len(later) == 0 || later[0].Seq <= request.Seq {
		t.Fatalf("tail %+v", later)
	}
	raw := string(mustRead(t, sup.stateDir()+"/journal.seq"))
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "1" {
		t.Fatalf("seq file %s", raw)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
