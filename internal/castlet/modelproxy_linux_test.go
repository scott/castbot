//go:build linux

package castlet

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEgressModelAllowsTheRuntimeAndRefusesOthers(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token" || r.Header.Get("X-Api-Key") != "" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.Header.Get("Authorization"), "castbot-proxy") {
			http.Error(w, "placeholder", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: one\n\ndata: two\n\n"))
	}))
	t.Cleanup(cp.Close)
	sup, err := New(Config{
		Token:      "agent-token",
		ControlURL: cp.URL,
		AllowHTTP:  true,
		AgentID:    "agt1",
		AgentDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(sup.ServeEgress), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	client := &http.Client{Timeout: 5 * time.Second}
	call := func(token, path string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+path, strings.NewReader(`{"model":"m"}`))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	code, body := call(sup.modelGate, "/model/v1/chat/completions")
	if code != http.StatusOK || !strings.Contains(body, "data: one") || !strings.Contains(body, "data: two") {
		t.Fatalf("allow %d %s", code, body)
	}
	code, body = callKey(client, ln.Addr().String(), sup.modelGate, "/model/v1/messages")
	if code != http.StatusOK || !strings.Contains(body, "data: one") {
		t.Fatalf("provider header %d %s", code, body)
	}
	code, _ = callKey(client, ln.Addr().String(), "not-the-gate", "/model/v1/messages")
	if code != http.StatusNotFound {
		t.Fatalf("wrong provider header %d", code)
	}
	code, _ = call("not-the-gate", "/model/v1/chat/completions")
	if code != http.StatusNotFound {
		t.Fatalf("wrong placeholder %d", code)
	}
	code, _ = call("", "/model/v1/chat/completions")
	if code != http.StatusNotFound {
		t.Fatalf("missing placeholder %d", code)
	}
	code, _ = call(sup.modelGate, "/model/v1/elsewhere")
	if code != http.StatusBadRequest {
		t.Fatalf("path %d", code)
	}
}

func callKey(client *http.Client, addr, token, path string) (int, string) {
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+path, strings.NewReader(`{"model":"m"}`))
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("X-Api-Key", token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	res, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw)
}
