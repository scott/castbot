package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
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
	if code != http.StatusOK || !mcpSentence(body, "The connection request was rejected.") || got != 0 || strings.Contains(body, connectCanary) {
		t.Fatalf("retired %d dials %d %s", code, got, body)
	}

	write := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/create"}}}}`
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", write)
	mu.Lock()
	got = len(paths)
	mu.Unlock()
	if code != http.StatusOK || !mcpSentence(body, "This action needs approval.") || got != 0 || strings.Contains(body, connectCanary) {
		t.Fatalf("write %d dials %d %s", code, got, body)
	}
	if waitConnectCardText(t, h, user.ID, agent.ID, "invoke_tool") == "" {
		t.Fatal("write card")
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

func TestPromptGrantForwardsInvokeTool(t *testing.T) {
	ip := "203.0.113.92:9"
	h := newHarness(t)
	user, secret := h.account("conn-do-grant@example.com", "Ada", "correct-horse-1")
	h.login("conn-do-grant@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.DigitalOcean, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.DigitalOcean); err != nil {
		t.Fatal(err)
	}
	openPromptGrant(t, h.st, user.ID, agent)
	var mu sync.Mutex
	var paths []string
	ts, dial := connectUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		if msg.Method == "initialize" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "sess-grant")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`))
			return
		}
		if msg.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"` + connectCanary + `"}]}}`))
	})
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	token := guestToken(t, h, agent)
	write := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/create"}}}}`
	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/mcp", write)
	mu.Lock()
	got := len(paths)
	mu.Unlock()
	if code != http.StatusOK || got < 3 || strings.Contains(body, connectCanary) || !strings.Contains(body, "[redacted]") {
		t.Fatalf("grant %d dials %d %s", code, got, body)
	}
	rows, err := h.st.ListConnectApprovals(context.Background(), user.ID, agent.ID, h.st.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	consumed := 0
	for _, row := range rows {
		if !strings.Contains(row.Action, "invoke_tool") {
			continue
		}
		if row.Status == "pending" || row.Status == "approved" {
			t.Fatalf("card still open %s", row.Status)
		}
		if row.Status == "consumed" {
			consumed++
		}
	}
	if consumed != 1 {
		t.Fatalf("consumed %d", consumed)
	}
}

func openPromptGrant(t *testing.T, st *store.Store, userID string, agent store.Agent) {
	t.Helper()
	threads, err := st.ListThreadsByAgent(context.Background(), userID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatalf("threads %v %d", err, len(threads))
	}
	run, err := st.CreateRun(context.Background(), userID, threads[0].ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkStreaming(context.Background(), userID, run.ID); err != nil {
		t.Fatal(err)
	}
	msg, err := st.CreateMessage(context.Background(), userID, threads[0].ID, "user", userID, "Create a droplet")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageRun(context.Background(), userID, msg.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.OpenPromptGrant(context.Background(), userID, msg.ID); err != nil {
		t.Fatal(err)
	}
}

func mcpSentence(body, sentence string) bool {
	var wrapped struct {
		Message struct {
			JSONRPC string `json:"jsonrpc"`
			Error   struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(body), &wrapped) != nil {
		return false
	}
	return wrapped.Message.JSONRPC == "2.0" && wrapped.Message.Error.Code == -32000 && wrapped.Message.Error.Message == sentence
}
