package castlet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/runtime"
)

func TestModelKeyStaysOffTheRuntime(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	t.Cleanup(echo.Close)
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	raw, sum, err := bundle.Archive("name: Atlas\n", "Reply briefly.\n")
	if err != nil {
		t.Fatal(err)
	}
	var sawHeader bool
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/bundle"):
			w.Header().Set("X-Castbot-Bundle-SHA256", sum)
			w.Header().Set("X-Castbot-Bundle-Version", "1")
			w.Write(raw)
		case strings.HasSuffix(r.URL.Path, "/secrets"):
			sawHeader = r.Header.Get("X-Castbot-Model-Proxy") == "1"
			w.Write([]byte(`{"secrets":[{"name":"NOTES_TOKEN","value":"fixture-iso-notes"},{"name":"DIGITALOCEAN_MODEL_ACCESS_KEY","value":"fixture-proxy-value"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cp.Close)
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	baseFile := filepath.Join(dir, "base")
	notes := filepath.Join(dir, "notes")
	script := "printf '%s' \"$DIGITALOCEAN_MODEL_ACCESS_KEY\" > " + quote(keyFile) +
		"; printf '%s' \"$DIGITALOCEAN_INFERENCE_BASE_URL\" > " + quote(baseFile) +
		"; printf '%s' \"$NOTES_TOKEN\" > " + quote(notes) +
		"; exec sleep 30"
	agentDir := filepath.Join(dir, "agent")
	sup, err := New(Config{
		Token:      "agent-token",
		ControlURL: cp.URL,
		AllowHTTP:  true,
		AgentID:    "agt1",
		AgentDir:   agentDir,
		RuntimeURL: up.URL,
		RuntimeCmd: []string{"/bin/sh", "-c", script},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Close)
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawHeader {
		t.Fatal("secrets pull did not ask for the proxy")
	}
	if sup.modelGate == "" || sup.modelGate == "castbot-proxy" || !waitHash(t, keyFile, sup.modelGate) {
		raw, _ := os.ReadFile(keyFile)
		t.Fatalf("key env %q", raw)
	}
	if !waitHash(t, baseFile, "http://127.0.0.1:8090/model/v1") {
		raw, _ := os.ReadFile(baseFile)
		t.Fatalf("base %q", raw)
	}
	if !waitHash(t, notes, "fixture-iso-notes") {
		t.Fatal("granted secret was dropped")
	}
	cfg, err := os.ReadFile(filepath.Join(dir, ".cast", "config.yaml"))
	if err != nil || !bytesContains(cfg, "base_url: \"http://127.0.0.1:8090/model/v1\"") || bytesContains(cfg, "fixture-proxy-value") {
		t.Fatalf("config %s %v", cfg, err)
	}
}

func TestEgressModelStreamsAndDropsTheGuestToken(t *testing.T) {
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer agent-token" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.Header.Get("Authorization"), "castbot-proxy") {
			http.Error(w, "placeholder", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: one\n\n"))
		w.(http.Flusher).Flush()
		w.Write([]byte("data: two\n\n"))
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
	req := httptest.NewRequest(http.MethodPost, "/model/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.RemoteAddr = "127.0.0.1:9"
	req.Header.Set("Authorization", "Bearer "+sup.modelGate)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unchecked caller %d", rec.Code)
	}
}

func quote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}

func bytesContains(raw []byte, needle string) bool {
	return strings.Contains(string(raw), needle)
}
