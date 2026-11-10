package web

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

var (
	imageOnce sync.Once
	imageErr  error
)

func ensureImage(t *testing.T) {
	t.Helper()
	imageOnce.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			imageErr = err
			return
		}
		imageErr = host.Build(context.Background(), root)
	})
	if imageErr != nil {
		t.Fatal(imageErr)
	}
}

type tokenTap struct {
	next  http.Handler
	mu    sync.Mutex
	token string
}

func (t *tokenTap) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	t.next.ServeHTTP(rec, r)
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/bootstrap") && rec.Code == http.StatusOK {
		var body struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body.Token != "" {
			t.mu.Lock()
			t.token = body.Token
			t.mu.Unlock()
		}
	}
	for k, vs := range rec.Header() {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = rec.Body.WriteTo(w)
}

func (t *tokenTap) Token() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.token
}

func writeCA(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	cert := ts.Certificate()
	if cert == nil {
		t.Fatal("test server has no certificate")
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func startGuest(t *testing.T, h *harness, agentID, bootstrap, delay string) (*host.Guest, string) {
	t.Helper()
	ensureImage(t)
	tap := &tokenTap{next: h.srv.Handler()}
	ts := httptest.NewTLSServer(tap)
	t.Cleanup(ts.Close)
	ca := writeCA(t, ts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	guest, err := host.Run(ctx, host.RunOpt{
		ControlURL: ts.URL,
		AgentID:    agentID,
		Bootstrap:  bootstrap,
		CAFile:     ca,
		WordDelay:  delay,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { guest.Stop(context.Background()) })
	deadline := time.Now().Add(45 * time.Second)
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
					return guest, token
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("castlet did not become ready: %s", guest.Logs(context.Background()))
	return nil, ""
}

func TestImageHasNoAgent(t *testing.T) {
	ensureImage(t)
	cmd := exec.Command("docker", "run", "--rm", "--entrypoint", "/bin/sh", host.Image, "-c",
		"test ! -e /app/agent/agent.yaml && test ! -e /app/agent/instructions.md && test -x /usr/local/bin/castlet && test -x /usr/local/bin/cast-standin")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("image check: %s", out)
	}
}

func TestContainerBootsFromBundle(t *testing.T) {
	h := newHarnessWith(t, runtime.NewHTTP("http://127.0.0.1:1"))
	ctx := context.Background()
	owner, secret := h.account("guest@example.com", "Guest", "correct-horse-1")
	other, otherSecret := h.account("guest-b@example.com", "Other", "correct-horse-1")
	sec, err := h.st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	const instructions = "Reply briefly.\n"
	if _, err := h.st.PutBundle(ctx, owner.ID, agent.ID, "1", "name: Atlas\n", instructions); err != nil {
		t.Fatal(err)
	}
	const secretValue = "fixture-value-s03"
	secRow, err := h.st.PutSecret(ctx, owner.ID, "FIXTURE_TOKEN", secretValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(ctx, owner.ID, agent.ID, secRow.ID); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := h.st.IssueBootstrap(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	guest, token := startGuest(t, h, agent.ID, bootstrap, "")
	card := castletText(t, http.MethodGet, guest.URL()+"/.well-known/agent.json", token, nil)
	if !strings.Contains(card, `"name":"Atlas"`) && !strings.Contains(card, `"name": "Atlas"`) {
		t.Fatal("agent card was not proxied")
	}
	client := runtime.NewHTTP(guest.URL())
	client.SetToken(token)
	h.srv.SetClient(client)
	h.srv.SetGuest(agent.ID, guest.URL(), token)

	ip := "203.0.113.81:9"
	h.login("guest@example.com", "correct-horse-1", secret, ip, false)
	msg := "hello"
	want := runtime.EchoText("Atlas", msg)
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(urlValues(msg)), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	runID := runIDFrom(t, body)
	row := waitRun(t, h, owner.ID, runID, "done", 20*time.Second)
	if row.Error != "" {
		t.Fatal(row.Error)
	}
	assertAgentBody(t, h, owner.ID, agent.ID, want)

	put := castletReq(t, http.MethodPut, guest.URL()+"/castlet/files/uploads/hello.txt", token, strings.NewReader("hello file"))
	if put.StatusCode != http.StatusNoContent {
		t.Fatalf("put %d", put.StatusCode)
	}
	put.Body.Close()
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/workspace/uploads/hello.txt", "", ip, nil)
	if code != 200 || body != "hello file" {
		t.Fatalf("owner file %d %q", code, body)
	}

	var mem bytes.Buffer
	tw := tar.NewWriter(&mem)
	note := []byte("remember this")
	if err := tw.WriteHeader(&tar.Header{Name: "note.txt", Mode: 0o644, Size: int64(len(note))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(note); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	imp := castletReq(t, http.MethodPut, guest.URL()+"/castlet/memory", token, bytes.NewReader(mem.Bytes()))
	if imp.StatusCode != http.StatusNoContent {
		t.Fatalf("memory import %d", imp.StatusCode)
	}
	imp.Body.Close()
	exp := castletReq(t, http.MethodGet, guest.URL()+"/castlet/memory", token, nil)
	exported, _ := io.ReadAll(exp.Body)
	exp.Body.Close()
	tr := tar.NewReader(bytes.NewReader(exported))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(tr)
	if hdr.Name != "note.txt" || string(got) != "remember this" {
		t.Fatal("memory round trip mismatch")
	}

	logs := castletText(t, http.MethodGet, guest.URL()+"/castlet/logs", token, nil)
	if !strings.Contains(logs, "stand-in ready name=Atlas") || !strings.Contains(logs, "secrets=1") || !strings.Contains(logs, "turn") {
		t.Fatal("log tail missing runtime output")
	}
	if strings.Contains(logs, secretValue) {
		t.Fatal("log tail contains a secret")
	}
	assertSecretInEnvOnly(t, guest, secretValue)

	turns := strings.Count(logs, "turn\n")
	denied := castletReq(t, http.MethodPost, guest.URL()+"/", "", strings.NewReader(`{"jsonrpc":"2.0"}`))
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token %d", denied.StatusCode)
	}
	denied.Body.Close()
	logs = castletText(t, http.MethodGet, guest.URL()+"/castlet/logs", token, nil)
	if strings.Count(logs, "turn\n") != turns {
		t.Fatal("rejected request reached the runtime")
	}

	if _, err := h.st.PutBundle(ctx, owner.ID, agent.ID, "2", "name: Birch\n", instructions); err != nil {
		t.Fatal(err)
	}
	reloaded := castletReq(t, http.MethodPost, guest.URL()+"/castlet/reload", token, nil)
	if reloaded.StatusCode != http.StatusOK {
		t.Fatalf("reload %d", reloaded.StatusCode)
	}
	reloaded.Body.Close()
	msg2 := "hello"
	want2 := runtime.EchoText("Birch", msg2)
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(urlValues(msg2)), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	run2 := runIDFrom(t, body)
	row = waitRun(t, h, owner.ID, run2, "done", 20*time.Second)
	if row.Error != "" {
		t.Fatal(row.Error)
	}
	assertAgentBody(t, h, owner.ID, agent.ID, want2)

	saved := map[string]*http.Cookie{}
	for k, v := range h.jar {
		saved[k] = v
	}
	h.login("guest-b@example.com", "correct-horse-1", otherSecret, "203.0.113.82:9", false)
	for _, path := range []string{"/bundle", "/secrets", "/files", "/workspace", "/workspace/uploads/hello.txt"} {
		code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+path, "", "203.0.113.82:9", nil)
		if code != http.StatusNotFound || strings.Contains(body, secretValue) || strings.Contains(body, "hello file") || strings.Contains(body, instructions) {
			t.Fatalf("user B %s -> %d", path, code)
		}
	}
	_ = other
	h.jar = saved
}

func urlValues(body string) url.Values {
	return url.Values{"body": {body}}
}

func castletReq(t *testing.T, method, rawURL, token string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Cast-Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func castletText(t *testing.T, method, rawURL, token string, body io.Reader) string {
	t.Helper()
	res := castletReq(t, method, rawURL, token, body)
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s %d", rawURL, res.StatusCode)
	}
	return string(raw)
}

func assertSecretInEnvOnly(t *testing.T, guest *host.Guest, value string) {
	t.Helper()
	sum := sha256.Sum256([]byte(value))
	want := hex.EncodeToString(sum[:])
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := guest.Output(ctx, "/bin/sh", "-c", `for e in /proc/[0-9]*/environ; do tr '\0' '\n' < "$e"; done | sed -n 's/^FIXTURE_TOKEN=//p' | tr -d '\n' | sha256sum`)
	if err != nil || !strings.Contains(out, want) {
		t.Fatal("runtime environment is missing the granted secret")
	}
	dest := t.TempDir()
	if err := guest.CopyFrom(ctx, "/app", filepath.Join(dest, "app")); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Walk(filepath.Join(dest, "app"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if bytes.Contains(raw, []byte(value)) {
			t.Fatal("secret was written under /app")
		}
		return nil
	})
}
