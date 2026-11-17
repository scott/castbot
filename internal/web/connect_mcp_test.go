package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/scott/castbot/internal/connect"
)

func TestDigitalOceanEgressUsesOnlyTheMCPServer(t *testing.T) {
	ip := "203.0.113.91:9"
	h := newHarness(t)
	user, secret := h.account("conn-do-mcp@example.com", "Ada", "correct-horse-1")
	h.login("conn-do-mcp@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.DigitalOcean, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.DigitalOcean); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var paths []string
	var hosts []string
	var inits int
	ts, dial := connectUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		hosts = append(hosts, r.Host)
		mu.Unlock()
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.Method == "initialize" {
			mu.Lock()
			inits++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "sess-1")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`))
			return
		}
		if msg.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"` + connectCanary + `"}]}}`))
	})
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	token := guestToken(t, h, agent)

	retired := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"do_droplets_list"}}}`
	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/mcp", retired)
	mu.Lock()
	got := len(paths)
	mu.Unlock()
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"bad_request"`) || got != 0 || strings.Contains(body, connectCanary) {
		t.Fatalf("retired %d dials %d %s", code, got, body)
	}

	write := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/create"}}}}`
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", write)
	mu.Lock()
	got = len(paths)
	mu.Unlock()
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || got != 0 || strings.Contains(body, connectCanary) {
		t.Fatalf("write %d dials %d %s", code, got, body)
	}

	read := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"find_tools","arguments":{"query":"droplets"}}}}`
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", read)
	if code != 200 || strings.Contains(body, connectCanary) || !strings.Contains(body, "[redacted]") {
		t.Fatalf("read %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", read)
	if code != 200 || strings.Contains(body, connectCanary) {
		t.Fatalf("reuse %d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if inits != 1 {
		t.Fatalf("inits %d", inits)
	}
	for i, host := range hosts {
		if host != "mcp.digitalocean.com" || paths[i] != "/mcp" {
			t.Fatalf("upstream %s %s", host, paths[i])
		}
	}
}
