//go:build linux

package web

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castlet"
	"github.com/scott/castbot/internal/store"
)

func TestAnthropicForwarderReachesTheHost(t *testing.T) {
	const key = "fixture-proxy-value"
	const gateMark = "castbot-proxy"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != key || r.Header.Get("Authorization") != "" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.Header.Get("X-Api-Key"), gateMark) {
			http.Error(w, "placeholder", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			http.Error(w, "version", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: anthropic-ok\n\n"))
	}))
	t.Cleanup(up.Close)

	h := newHarness(t)
	h.srv.modelProxyUpstream = up.URL + "/v1"
	t.Setenv("CASTBOT_PROVIDER_KEY_ANTHROPIC_API", key)
	ctx := context.Background()
	user, _ := h.account("ada-forward@example.com", "Ada", "correct-horse-1")
	sec, err := h.st.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Nova", Title: "Helper", Status: "ready",
		Model: "anthropic-api/claude-sonnet-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	token := proxyGuestToken(t, h, user.ID, agent.ID)
	host := httptest.NewServer(h.srv.Handler())
	t.Cleanup(host.Close)

	sup, err := castlet.New(castlet.Config{
		Token:      token,
		ControlURL: host.URL,
		AllowHTTP:  true,
		AgentID:    agent.ID,
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

	call := func(header, value string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/model/v1/messages", strings.NewReader(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"fixture-prompt-body"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(header, value)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("anthropic-version", "2023-06-01")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	code, body := call("X-Api-Key", sup.GuestGate())
	if code != http.StatusOK || !strings.Contains(body, "anthropic-ok") || strings.Contains(body, key) {
		t.Fatalf("forward %d %s", code, body)
	}
	code, _ = call("X-Api-Key", "not-the-gate")
	if code != http.StatusNotFound {
		t.Fatalf("wrong header %d", code)
	}
}
