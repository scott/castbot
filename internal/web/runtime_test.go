package web

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestRuntimeImageAndSmoke(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := host.BuildRuntime(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "image", "inspect", host.RuntimeImage, "--format", "{{.Config.User}} {{.Size}}").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect: %s", out)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "nobody" {
		t.Fatalf("user %q", strings.TrimSpace(string(out)))
	}
	t.Logf("runtime image bytes %s", fields[1])
	check := exec.Command("docker", "run", "--rm", "--entrypoint", "/opt/cast/bin/python", host.RuntimeImage, "-c",
		"import os,cast.runtime; raise SystemExit(0 if not os.path.exists('/usr/local/bin/cast-standin') and not os.path.exists('/app/agent/agent.yaml') else 1)")
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("image contents: %s", out)
	}
	envOut, err := exec.Command("docker", "image", "inspect", host.RuntimeImage, "--format", "{{range .Config.Env}}{{println .}}{{end}}").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(envOut), "\n") {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "CASTBOT_PROVIDER") || strings.Contains(upper, "MODEL_ACCESS_KEY") || strings.Contains(upper, "API_KEY") || strings.Contains(upper, "BOOTSTRAP") || strings.Contains(upper, "TOKEN=") || strings.Contains(upper, "SECRET") {
			t.Fatal("image env has a secret name")
		}
	}

	const reply = "Streamed from the fixture provider."
	const key = "fixture-s07-key"
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	var hitMu sync.Mutex
	var hits []string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		hitMu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		hitMu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v3.2"}]}`))
			return
		}
		if !strings.Contains(string(raw), `"stream":true`) && !strings.Contains(string(raw), `"stream": true`) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + reply + `"}}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + reply + "\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	port := ln.Addr().(*net.TCPAddr).Port
	base := "http://host.docker.internal:" + portString(port) + "/v1"

	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", key)
	h := newHarnessWith(t, runtime.NewHTTP("http://127.0.0.1:1"))
	h.srv.SetDevEcho(false)
	h.srv.SetExplicitRuntime(true)
	ctx := context.Background()
	owner, secret := h.account("runtime-s07@example.com", "Owner", "correct-horse-1")
	sec, err := h.st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready", Model: "digitalocean/deepseek-v3.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	yaml := "name: Atlas\nversion: \"1\"\nbrain:\n  model: digitalocean/deepseek-v3.2\n  instructions_file: instructions.md\ntools:\n  builtin: []\n  mcp: []\n  custom: []\n"
	if _, err := h.st.PutBundle(ctx, owner.ID, agent.ID, "1", yaml, "Reply briefly.\n"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetAgentModel(ctx, owner.ID, agent.ID, "digitalocean/deepseek-v3.2"); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := h.st.IssueBootstrap(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	tap := &tokenTap{next: h.srv.Handler()}
	seen := &statusTap{next: tap}
	controlURL, ca := openControl(t, seen)
	gctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	guest, err := host.Run(gctx, host.RunOpt{
		ControlURL: controlURL,
		AgentID:    agent.ID,
		Bootstrap:  bootstrap,
		CAFile:     ca,
		Image:      host.RuntimeImage,
		Env:        []string{"DIGITALOCEAN_INFERENCE_BASE_URL=" + base},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { guest.Stop(context.Background()) })
	deadline := time.Now().Add(2 * time.Minute)
	var token string
	for time.Now().Before(deadline) {
		token = tap.Token()
		if token != "" {
			req, err := http.NewRequest(http.MethodGet, guest.URL()+"/castlet/health", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Cast-Authorization", "Bearer "+token)
			res, err := http.DefaultClient.Do(req)
			if err == nil {
				ok := res.StatusCode == http.StatusOK
				res.Body.Close()
				if ok {
					break
				}
			}
			token = ""
		}
		time.Sleep(200 * time.Millisecond)
	}
	if token == "" {
		hitMu.Lock()
		paths := strings.Join(hits, ",")
		hitMu.Unlock()
		failGuest(t, guest, []string{key, bootstrap}, "castlet did not become ready; provider "+paths+" control "+seen.String())
	}
	dir := t.TempDir()
	if err := guest.CopyFrom(context.Background(), "/app/agent", dir); err != nil {
		t.Fatal(err)
	}
	if treeHas(t, dir, key) {
		t.Fatal("provider key was written into the agent directory")
	}
	client := runtime.NewHTTP(guest.URL())
	client.SetToken(token)
	h.srv.SetClient(client)
	ip := "203.0.113.236:9"
	h.login("runtime-s07@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(map[string][]string{"body": {"hello"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatalf("send %d", code)
	}
	runID := runIDFrom(t, body)
	row := waitRun(t, h, owner.ID, runID, "done", 90*time.Second)
	if row.Error != "" || strings.Contains(row.Error, "Hello! I'm") {
		t.Fatalf("run %s", row.Error)
	}
	assertAgentBody(t, h, owner.ID, agent.ID, reply)
	raw := guest.Logs(context.Background())
	if strings.Contains(raw, key) || strings.Contains(raw, token) || strings.Contains(raw, bootstrap) {
		t.Fatal("secret remained in logs")
	}
}

type statusTap struct {
	next http.Handler
	mu   sync.Mutex
	line []string
}

func (s *statusTap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	s.next.ServeHTTP(rec, r)
	s.mu.Lock()
	s.line = append(s.line, r.Method+" "+r.URL.Path+" "+strconv.Itoa(rec.Code))
	s.mu.Unlock()
	for k, vs := range rec.Header() {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

func (s *statusTap) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.line, ",")
}

func failGuest(t *testing.T, guest *host.Guest, secrets []string, msg string) {
	t.Helper()
	raw := guest.Logs(context.Background())
	for _, secret := range secrets {
		if secret != "" {
			raw = strings.ReplaceAll(raw, secret, "[redacted]")
		}
	}
	if len(raw) > 6000 {
		raw = raw[len(raw)-6000:]
	}
	t.Fatalf("%s\n%s", msg, raw)
}

func treeHas(t *testing.T, root, needle string) bool {
	t.Helper()
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Size() > 1<<20 {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(raw), needle) {
			found = true
		}
		return nil
	})
	return found
}

func portString(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
