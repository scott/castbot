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
