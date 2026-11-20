package castlet

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/approvetool"
	"github.com/scott/castbot/internal/chattool"
	"github.com/scott/castbot/internal/connectiontool"
	"github.com/scott/castbot/internal/deletetool"
	"github.com/scott/castbot/internal/edittool"
	"github.com/scott/castbot/internal/egresscontract"
	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/importtool"
	"github.com/scott/castbot/internal/machinetool"
	"github.com/scott/castbot/internal/replacetool"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/secretstool"
	"github.com/scott/castbot/internal/selftool"
	"github.com/scott/castbot/internal/settingstool"
)

const fixtureValue = "fixture-should-not-appear"

type egressHit struct {
	mu    sync.Mutex
	paths []string
	auth  []string
	body  []string
}

func (h *egressHit) record(r *http.Request, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.paths = append(h.paths, r.Method+" "+r.URL.Path)
	h.auth = append(h.auth, r.Header.Get("Authorization"))
	h.body = append(h.body, body)
}

func (h *egressHit) blob() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.paths, "\n") + "\n" + strings.Join(h.auth, "\n") + "\n" + strings.Join(h.body, "\n")
}

func (h *egressHit) count(suffix string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, path := range h.paths {
		if strings.HasSuffix(path, suffix) || strings.Contains(path, suffix) {
			n++
		}
	}
	return n
}

func newEgress(t *testing.T, fn http.HandlerFunc) (*Supervisor, *egressHit) {
	t.Helper()
	hit := &egressHit{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		hit.record(r, string(raw))
		if strings.Contains(string(raw), fixtureValue) || strings.Contains(r.Header.Get("Authorization"), fixtureValue) {
			t.Errorf("host saw the env value")
		}
		fn(w, r)
	}))
	t.Cleanup(srv.Close)
	sup, err := New(Config{
		Token:      "machine-capability",
		ControlURL: srv.URL,
		AgentID:    "agent-1",
		AllowHTTP:  true,
		AgentDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sup, hit
}

func localReq(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+fixtureValue)
	req.Header.Set("X-Castbot-Approval", "approve-1")
	return req
}

func TestLoopbackAddr(t *testing.T) {
	got, err := LoopbackAddr("")
	if err != nil || got != "127.0.0.1:8090" {
		t.Fatalf("%s %v", got, err)
	}
	for _, raw := range []string{"0.0.0.0:8090", ":8090", "127.0.0.1", "localhost:8090", "10.0.0.1:8090"} {
		if _, err := LoopbackAddr(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestEgressIgnoresOutsideAndThePublicPort(t *testing.T) {
	sup, _ := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	req := localReq(http.MethodGet, "/status", "")
	req.RemoteAddr = "192.0.2.1:9"
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("peer %d", rec.Code)
	}
	pub := httptest.NewRequest(http.MethodGet, "/status", nil)
	pub.Header.Set("X-Cast-Authorization", "Bearer machine-capability")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, pub)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "enabled") {
		t.Fatalf("public %d %s", rec.Code, rec.Body.String())
	}
	git := httptest.NewRequest(http.MethodPost, "/git/github/acme/repo", strings.NewReader(fixtureValue))
	git.Header.Set("X-Cast-Authorization", "Bearer machine-capability")
	rec = httptest.NewRecorder()
	sup.ServeHTTP(rec, git)
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), fixtureValue) {
		t.Fatal("public git route")
	}
}

func TestEgressFailClosed(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", fixtureValue)
	t.Setenv("DIGITALOCEAN_ACCESS_TOKEN", fixtureValue)
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/secret-request") || strings.HasSuffix(r.URL.Path, "/egress/http") || strings.HasSuffix(r.URL.Path, "/egress/git") {
			t.Errorf("forwarded while disconnected: %s", r.URL.Path)
		}
		http.NotFound(w, r)
	})
	start := time.Now()
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodGet, "/status", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/http", `{"provider":"github","method":"GET","path":"/user"}`))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), egresscontract.NotConnected) {
		t.Fatalf("http %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/git/github/acme/repo.git/git-receive-pack", "pack"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), egresscontract.NotConnected) {
		t.Fatalf("git %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodGet, "/upstream/digitalocean/v2/account", ""))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), egresscontract.NotConnected) {
		t.Fatalf("upstream %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"connection":"github","reason":"need a repository"}`))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), egresscontract.NotConnected) {
		t.Fatalf("secret %d %s", rec.Code, rec.Body.String())
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("fail closed hung")
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"connection":"aws","reason":"phish"}`))
	if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), fixtureValue) {
		t.Fatalf("aws %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(hit.blob(), fixtureValue) {
		t.Fatal("host saw the env value")
	}
	for _, auth := range hit.auth {
		if auth != "" && auth != "Bearer machine-capability" {
			t.Fatal("unexpected authorization on the host call")
		}
	}
}

func TestEgressForwardsWithoutTheGuestToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", fixtureValue)
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/egress"):
			_, _ = io.WriteString(w, `{"enabled":true,"providers":["github","digitalocean"]}`)
		case strings.HasSuffix(r.URL.Path, "/egress/http"):
			_, _ = io.WriteString(w, `{"status":200,"headers":{"content-type":"application/json","location":"https://evil.example/`+fixtureValue+`"},"body_b64":"`+base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))+`"}`)
		case strings.HasSuffix(r.URL.Path, "/egress/git"):
			_, _ = io.WriteString(w, `{"status":302,"headers":{"location":"https://evil.example/`+fixtureValue+`"},"body_b64":"`+base64.StdEncoding.EncodeToString([]byte(fixtureValue))+`"}`)
		case strings.HasSuffix(r.URL.Path, "/egress/secret-request"):
			_, _ = io.WriteString(w, `{"result":"saved","leak":"`+fixtureValue+`"}`)
		default:
			http.NotFound(w, r)
		}
	})
	rec := httptest.NewRecorder()
	req := localReq(http.MethodGet, "/upstream/digitalocean/v2/account", "")
	req.Header.Set("Accept", "application/json")
	sup.ServeEgress(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), fixtureValue) || strings.Contains(strings.ToLower(rec.Body.String()), "evil.example") {
		t.Fatalf("relay %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `{"ok":true}`) {
		t.Fatal(rec.Body.String())
	}
	var forwarded bool
	hit.mu.Lock()
	for _, body := range hit.body {
		if !strings.Contains(body, `"/v2/account"`) {
			continue
		}
		forwarded = true
		var fwd egresscontract.Forward
		if err := json.Unmarshal([]byte(body), &fwd); err != nil {
			t.Fatal(err)
		}
		if fwd.Headers["authorization"] != "" || fwd.Provider != "digitalocean" || fwd.ApprovalID != "approve-1" {
			t.Fatal("forward kept a guest authorization header or dropped the approval id")
		}
	}
	hit.mu.Unlock()
	if !forwarded {
		t.Fatal("account read was not forwarded")
	}
	sup.mu.Lock()
	sup.egressAt = time.Time{}
	sup.mu.Unlock()
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/git/github/acme/repo.git/info/refs?service=git-upload-pack", "pack"))
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), fixtureValue) || !strings.Contains(rec.Body.String(), egresscontract.RedirectRefused) {
		t.Fatalf("redirect %d %s", rec.Code, rec.Body.String())
	}
	sup.mu.Lock()
	sup.egressAt = time.Time{}
	sup.mu.Unlock()
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"connection":"github","reason":"need ghp_abcdefghijklmnop","name":"not a name"}`)) // gitleaks:allow
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), fixtureValue) || strings.Contains(rec.Body.String(), "ghp_") {
		t.Fatalf("secret %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"result":"saved"`) {
		t.Fatal(rec.Body.String())
	}
	var sawReason bool
	hit.mu.Lock()
	for _, body := range hit.body {
		if strings.Contains(body, "ghp_") {
			t.Error("host received a token-shaped reason")
		}
		if strings.Contains(body, "[redacted]") {
			sawReason = true
		}
		if strings.Contains(body, fixtureValue) {
			t.Error("host body contained the env value")
		}
	}
	hit.mu.Unlock()
	if !sawReason {
		t.Fatal("reason was not scrubbed")
	}
	for _, auth := range hit.auth {
		if auth != "" && auth != "Bearer machine-capability" {
			t.Fatal("host authorization was not the machine capability")
		}
	}
}

func TestSecretRequestWhenTheProviderIsNotConnected(t *testing.T) {
	for _, status := range []string{
		`{"enabled":true,"providers":[]}`,
		`{"enabled":true,"providers":["digitalocean"]}`,
	} {
		sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/egress"):
				_, _ = io.WriteString(w, status)
			case strings.HasSuffix(r.URL.Path, "/egress/secret-request"):
				_, _ = io.WriteString(w, `{"result":"saved","leak":"`+fixtureValue+`"}`)
			default:
				t.Errorf("forwarded %s", r.URL.Path)
				http.NotFound(w, r)
			}
		})
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"connection":"github","reason":"need a repository"}`))
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), fixtureValue) || strings.Contains(rec.Body.String(), "leak") {
			t.Fatalf("secret %s %d %s", status, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"result":"saved"`) {
			t.Fatal(rec.Body.String())
		}
		if hit.count("/egress/secret-request") != 1 {
			t.Fatalf("secret forwards %d for %s", hit.count("/egress/secret-request"), status)
		}
		rec = httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/http", `{"provider":"github","method":"GET","path":"/user"}`))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), egresscontract.NotConnected) {
			t.Fatalf("http %d %s", rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/git/github/acme/repo.git/info/refs?service=git-upload-pack", "pack"))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), egresscontract.NotConnected) {
			t.Fatalf("git %d %s", rec.Code, rec.Body.String())
		}
		rec = httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/mcp", `{"server":"github","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}`))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), egresscontract.NotConnected) {
			t.Fatalf("mcp %d %s", rec.Code, rec.Body.String())
		}
		if hit.count("/egress/http") != 0 || hit.count("/egress/git") != 0 || hit.count("/egress/mcp") != 0 {
			t.Fatalf("unlisted provider was forwarded: %s", hit.blob())
		}
		rec = httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"connection":"aws","reason":"phish"}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("aws %d %s", rec.Code, rec.Body.String())
		}
		if hit.count("/egress/secret-request") != 1 {
			t.Fatal("aws was forwarded")
		}
	}
}

func TestSecretRequest404IsImmediate(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	start := time.Now()
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"connection":"github","reason":"need a repository"}`))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), egresscontract.NotConnected) {
		t.Fatalf("secret %d %s", rec.Code, rec.Body.String())
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("disabled secret request waited")
	}
	if hit.count("/egress/secret-request") != 0 {
		t.Fatal("404 host was asked for a card")
	}
}

func TestAgentsForwardIgnoresProviderStatus(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/egress") {
			t.Errorf("agents forward checked egress %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer machine-capability" {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Created Quill.","error":false,"user_id":"hidden"}`)
	})
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+agentskill.ToolCreate+`","arguments":{"description":"Sort the support inbox and draft replies."}}`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Created Quill.") || strings.Contains(rec.Body.String(), "hidden") || strings.Contains(rec.Body.String(), fixtureValue) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if hit.count("/agents") != 1 {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"sign_out","arguments":{}}`))
	if hit.count("/agents") != 1 || !strings.Contains(rec.Body.String(), egresscontract.Rejected) {
		t.Fatalf("unknown tool %d %s", hit.count("/agents"), rec.Body.String())
	}
	off := httptest.NewRequest(http.MethodPost, "/agents", strings.NewReader(`{"name":"`+agentskill.ToolList+`","arguments":{}}`))
	off.RemoteAddr = "203.0.113.5:9"
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, off)
	if rec.Code != http.StatusNotFound || hit.count("/agents") != 1 {
		t.Fatalf("off loopback %d forwards %d", rec.Code, hit.count("/agents"))
	}
}

func TestAgentsForwardRoutines(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Morning digest. Every weekday at 8:00 AM.","error":false}`)
	})
	for _, name := range []string{routinetool.ToolList, routinetool.ToolCreate, routinetool.ToolUpdate, routinetool.ToolDelete, routinetool.ToolRun} {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Morning digest") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != 5 {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"sign_out","arguments":{}}`))
	if hit.count("/agents") != 5 || !strings.Contains(rec.Body.String(), egresscontract.Rejected) {
		t.Fatalf("unknown tool %d %s", hit.count("/agents"), rec.Body.String())
	}
}

func TestAgentsForwardChat(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Stopped the reply.","error":false}`)
	})
	for _, spec := range chattool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Stopped the reply.") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(chattool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardSettings(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Name saved.","error":false}`)
	})
	for _, spec := range settingstool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Name saved.") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(settingstool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardConnections(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Saved. Every agent you own uses these choices.","error":false}`)
	})
	for _, spec := range connectiontool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Saved. Every agent you own uses these choices.") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(connectiontool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardVault(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"The secret stays in the vault.","error":false}`)
	})
	for _, spec := range secretstool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "The secret stays in the vault.") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(secretstool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardReplace(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Chat is paused until it is done.","error":false}`)
	})
	for _, spec := range replacetool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Chat is paused until it is done.") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(replacetool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardImport(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Saved. Name is Imported.","error":false}`)
	})
	for _, spec := range importtool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Saved. Name is Imported.") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(importtool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardDelete(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Type Ada to delete this machine.","error":false}`)
	})
	for _, spec := range deletetool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Type Ada to delete this machine.") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(deletetool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardSelf(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"This month for Ada: $0.00","error":false}`)
	})
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+selftool.ToolUsage+`","arguments":{}}`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "$0.00") || strings.Contains(rec.Body.String(), fixtureValue) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if hit.count("/agents") != 1 {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"sign_out","arguments":{}}`))
	if hit.count("/agents") != 1 || !strings.Contains(rec.Body.String(), egresscontract.Rejected) {
		t.Fatalf("unknown tool %d %s", hit.count("/agents"), rec.Body.String())
	}
}

func TestAgentsForwardGroups(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"Created Standup.","error":false}`)
	})
	for _, spec := range grouptool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Standup") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(grouptool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardMachine(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"text":"The machine is paused. A chat message wakes it.","error":false}`)
	})
	for _, spec := range machinetool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "paused") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(machinetool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"sign_out","arguments":{}}`))
	if hit.count("/agents") != len(machinetool.Specs()) || !strings.Contains(rec.Body.String(), egresscontract.Rejected) {
		t.Fatalf("unknown tool %d %s", hit.count("/agents"), rec.Body.String())
	}
}

func TestAgentsForwardEdit(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if strings.Contains(string(raw), fixtureValue) {
			t.Errorf("forwarded the env value")
		}
		_, _ = io.WriteString(w, `{"text":"Saved. Name is Quill.","error":false}`)
	})
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+edittool.ToolUpdate+`","arguments":{"name":"Quill"}}`))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Quill") || strings.Contains(rec.Body.String(), fixtureValue) {
		t.Fatalf("edit %d %s", rec.Code, rec.Body.String())
	}
	if hit.count("/agents") != 1 {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
}

func TestAgentsForwardApproval(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agents") {
			t.Errorf("path %s", r.URL.Path)
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if strings.Contains(string(raw), fixtureValue) {
			t.Errorf("forwarded the env value")
		}
		_, _ = io.WriteString(w, `{"text":"Approved. bash.","error":false}`)
	})
	for _, spec := range approvetool.Specs() {
		rec := httptest.NewRecorder()
		sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"`+spec.Name+`","arguments":{"card":"bash"}}`))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Approved") || strings.Contains(rec.Body.String(), fixtureValue) {
			t.Fatalf("%s %d %s", spec.Name, rec.Code, rec.Body.String())
		}
	}
	if hit.count("/agents") != len(approvetool.Specs()) {
		t.Fatalf("forwards %d", hit.count("/agents"))
	}
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/agents", `{"name":"sign_out","arguments":{}}`))
	if hit.count("/agents") != len(approvetool.Specs()) || !strings.Contains(rec.Body.String(), egresscontract.Rejected) {
		t.Fatalf("unknown tool %d %s", hit.count("/agents"), rec.Body.String())
	}
}

func TestEgressRejectsBadPaths(t *testing.T) {
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/egress") {
			_, _ = io.WriteString(w, `{"enabled":true,"providers":["github"]}`)
			return
		}
		t.Errorf("forwarded %s", r.URL.Path)
		http.NotFound(w, r)
	})
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/http", `{"provider":"github","method":"GET","path":"/../../etc/passwd"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path %d %s", rec.Code, rec.Body.String())
	}
	if hit.count("/egress/http") != 0 {
		t.Fatal("bad path was forwarded")
	}
}

func TestNamedSecretForwardsWhenProxyEnabled(t *testing.T) {
	var cards int
	sup, hit := newEgress(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/egress"):
			_, _ = io.WriteString(w, `{"enabled":true,"providers":[]}`)
		case strings.HasSuffix(r.URL.Path, "/egress/secret-request"):
			cards++
			if cards == 1 {
				_, _ = io.WriteString(w, `{"result":"rejected","leak":"`+fixtureValue+`"}`)
				return
			}
			_, _ = io.WriteString(w, `{"result":"not_found","leak":"`+fixtureValue+`"}`)
		default:
			t.Errorf("forwarded %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	rec := httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"action":"save","name":"x_api_key","label":"Monitor","reason":"Watch posts","value":"`+fixtureValue+`"}`))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), fixtureValue) || !strings.Contains(rec.Body.String(), `"result":"rejected"`) {
		t.Fatalf("save %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"action":"delete","name":"x_api_key","reason":"Stop","value":"`+fixtureValue+`"}`))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), fixtureValue) || !strings.Contains(rec.Body.String(), `"result":"not_found"`) {
		t.Fatalf("delete %d %s", rec.Code, rec.Body.String())
	}
	hit.mu.Lock()
	posted := strings.Join(hit.body, "\n")
	hit.mu.Unlock()
	if !strings.Contains(posted, `"name":"X_API_KEY"`) || !strings.Contains(posted, `"action":"save"`) || !strings.Contains(posted, `"action":"delete"`) || strings.Contains(posted, fixtureValue) || strings.Contains(posted, `"connection":"github"`) {
		t.Fatalf("posted %s", posted)
	}
	rec = httptest.NewRecorder()
	sup.ServeEgress(rec, localReq(http.MethodPost, "/secret-request", `{"connection":"aws","reason":"phish"}`))
	if rec.Code != http.StatusBadRequest || hit.count("/egress/secret-request") != 2 {
		t.Fatalf("aws %d forwards %d", rec.Code, hit.count("/egress/secret-request"))
	}
}
