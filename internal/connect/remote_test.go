package connect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestDigitalOceanMCPIsTheOnlyUpstream(t *testing.T) {
	const token = "test-connection-value"
	var mu sync.Mutex
	var paths []string
	var hosts []string
	var sessions []string
	var inits int
	ts, dial := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		hosts = append(hosts, r.Host)
		mu.Unlock()
		if r.Host != "mcp.digitalocean.com" || r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("auth")
		}
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
		if got := r.Header.Get("Mcp-Session-Id"); got != "sess-1" {
			t.Errorf("session %q", got)
		}
		mu.Lock()
		sessions = append(sessions, r.Header.Get("Mcp-Session-Id"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"echo ` + token + `"}]}}`))
	})
	defer ts.Close()
	remote := NewRemote()
	call := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"find_tools","arguments":{"query":"droplets"}}}`)
	out, err := remote.RoundTrip(context.Background(), token, call, dial)
	if err != nil || strings.Contains(string(out), token) || !strings.Contains(string(out), "[redacted]") {
		t.Fatalf("%s %v", out, err)
	}
	out, err = remote.RoundTrip(context.Background(), token, call, dial)
	if err != nil || strings.Contains(string(out), token) {
		t.Fatalf("reuse %s %v", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if inits != 1 {
		t.Fatalf("inits %d paths %v", inits, paths)
	}
	for _, host := range hosts {
		if host != "mcp.digitalocean.com" {
			t.Fatalf("host %s", host)
		}
	}
	for _, path := range paths {
		if path != "/mcp" {
			t.Fatalf("path %s", path)
		}
	}
	if len(sessions) != 2 {
		t.Fatalf("forwarded %v", sessions)
	}
}

func TestDigitalOceanMCPParsesSSEAndRefreshesALostSession(t *testing.T) {
	const token = "test-connection-value"
	var mu sync.Mutex
	var inits int
	ts, dial := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var msg struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &msg)
		switch msg.Method {
		case "initialize":
			mu.Lock()
			inits++
			n := inits
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "sess-"+string(rune('0'+n)))
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		default:
			if r.Header.Get("Mcp-Session-Id") == "sess-1" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"ok\":true}}\n\n"))
		}
	})
	defer ts.Close()
	remote := NewRemote()
	call := []byte(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`)
	out, err := remote.RoundTrip(context.Background(), token, call, dial)
	if err != nil || !strings.Contains(string(out), `"ok":true`) || strings.Contains(string(out), token) {
		t.Fatalf("%s %v", out, err)
	}
	mu.Lock()
	got := inits
	mu.Unlock()
	if got != 2 {
		t.Fatalf("inits %d", got)
	}
}

func TestDigitalOceanAdapterDoesNotCallTheAPI(t *testing.T) {
	for _, tool := range Tools(DigitalOcean) {
		if tool.Method != "" || tool.Path != "" {
			t.Fatalf("%+v", tool)
		}
	}
	if !RetiredTool(DigitalOcean, "do_droplets_list") || RetiredTool(GitHub, "do_droplets_list") {
		t.Fatal("retired")
	}
	out, err := HandleRPC(context.Background(), DigitalOcean, RPC{
		JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call",
		Params: json.RawMessage(`{"name":"do_droplets_list"}`),
	}, true, func(context.Context, Call) (Result, error) {
		t.Fatal("adapter dialed")
		return Result{}, nil
	})
	if err != nil || !strings.Contains(string(out), "unavailable") {
		t.Fatalf("%s %v", out, err)
	}
	perms := PermissionsFor([]string{DigitalOcean})
	if perms["find_tools"] != "always-allow" || perms["retrieve_schema"] != "always-allow" || perms["invoke_tool"] != "ask" {
		t.Fatalf("%#v", perms)
	}
}

func TestFromEnvIgnoresDigitalOceanProcess(t *testing.T) {
	t.Setenv("CASTBOT_MCP_DIGITALOCEAN", "/usr/bin/false serve")
	t.Setenv("CASTBOT_MCP_GITHUB", "")
	cmds := FromEnv()
	if len(cmds.Argv(DigitalOcean)) != 0 || len(cmds.Argv(GitHub)) != 0 {
		t.Fatalf("%#v", cmds)
	}
}
