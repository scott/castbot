package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/runtime"
)

func TestMachineAPI(t *testing.T) {
	const secret = "cbt_bootstrap_secret_value"
	const token = "machine-token-for-tests"
	cases := []struct {
		name    string
		op      string
		gets    []string
		reason  string
		want    string
		wantErr error
		deleted bool
	}{
		{name: "create", op: "create", gets: []string{"running"}, want: "running"},
		{name: "poll", op: "create", gets: []string{"creating", "creating", "running"}, want: "running"},
		{name: "failed-create cleanup", op: "create", gets: []string{"creating", "failed"}, reason: "image pull failed", wantErr: ErrFailed, deleted: true},
		{name: "pause", op: "pause", want: "paused"},
		{name: "resume", op: "resume", want: "running"},
		{name: "delete", op: "delete", deleted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newScriptAPI(t, secret)
			api.gets = tc.gets
			api.reason = tc.reason
			m := newTestClient(t, api, token)
			ctx := context.Background()
			switch tc.op {
			case "create":
				vm, err := m.Create(ctx, testSpec(secret))
				if tc.wantErr != nil {
					if !errors.Is(err, tc.wantErr) {
						t.Fatalf("err %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				} else if vm.State != tc.want || vm.Endpoint == "" {
					t.Fatalf("vm %+v", vm)
				}
				if err == nil || tc.deleted {
					assertCreateBody(t, api.body, secret)
				}
			case "pause":
				vm, err := m.Pause(ctx, "vm-1")
				if err != nil || vm.State != "paused" {
					t.Fatalf("%v %+v", err, vm)
				}
			case "resume":
				vm, err := m.Wake(ctx, "vm-1")
				if err != nil || vm.State != "running" {
					t.Fatalf("%v %+v", err, vm)
				}
			case "delete":
				if err := m.Delete(ctx, "vm-1"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.deleted && api.deletes != 1 {
				t.Fatalf("deletes %d", api.deletes)
			}
			if !tc.deleted && tc.op == "create" && api.deletes != 0 {
				t.Fatalf("unexpected delete")
			}
			if strings.Contains(strings.Join(api.paths, " "), "/castlet/") {
				t.Fatal("status path touched the guest")
			}
		})
	}
}

func TestStatusDoesNotTouchTheGuest(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("guest called: %s", r.URL.Path)
	}))
	t.Cleanup(edge.Close)
	api := newScriptAPI(t, "")
	m := newTestClient(t, api, "machine-token-for-tests")
	vm, err := m.Status(context.Background(), "vm-1")
	if err != nil || vm.State != "running" {
		t.Fatalf("%v %+v", err, vm)
	}
	if api.posts != 0 {
		t.Fatal("status posted")
	}
}

func TestCreateRedactsSecrets(t *testing.T) {
	const secret = "cbt_bootstrap_secret_value"
	const token = "machine-token-for-tests"
	api := newScriptAPI(t, secret)
	api.failCreate = true
	m := newTestClient(t, api, token)
	_, err := m.Create(context.Background(), testSpec(secret))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), token) {
		t.Fatal("error leaked a credential")
	}
}

func TestReloadRetriesConflict(t *testing.T) {
	var hits int
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") == "" || r.Header.Get("X-Cast-Authorization") == "" {
			t.Error("missing edge auth")
		}
		if hits < 3 {
			http.Error(w, "reload in progress", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "machine-token-for-tests")
	if err := m.Reload(context.Background(), edge.URL, "agent-token"); err != nil {
		t.Fatal(err)
	}
	if hits != 3 {
		t.Fatalf("hits %d", hits)
	}
}

func TestReloadConflictIsNotSuccess(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "reload in progress", http.StatusConflict)
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "machine-token-for-tests")
	err := m.Reload(context.Background(), edge.URL, "agent-token")
	if err == nil || !strings.Contains(err.Error(), "reload") {
		t.Fatalf("err %v", err)
	}
}

func TestReloadOtherStatusIsFatal(t *testing.T) {
	var hits int
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Error(w, "no", http.StatusBadGateway)
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "machine-token-for-tests")
	if err := m.Reload(context.Background(), edge.URL, "agent-token"); err == nil {
		t.Fatal("expected error")
	}
	if hits != 1 {
		t.Fatalf("retries %d", hits)
	}
}

func TestProbeUsesBothTokens(t *testing.T) {
	var saw bool
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/castlet/health" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer machine-token-for-tests" {
			t.Error("machine token missing")
		}
		if r.Header.Get("X-Cast-Authorization") != "Bearer agent-token" {
			t.Error("agent token missing")
		}
		saw = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "machine-token-for-tests")
	if err := m.Probe(context.Background(), edge.URL, "agent-token"); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("probe did not call the edge")
	}
}

func TestCancelDoesNotCallTheGuest(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("guest called")
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "machine-token-for-tests")
	if err := m.Cancel(context.Background(), edge.URL, "agent-token", "run-1"); !errors.Is(err, ErrCancel) {
		t.Fatal(err)
	}
}

func TestStreamSetsEdgeHeaders(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer machine-token-for-tests" {
			t.Error("machine token")
		}
		if r.Header.Get("X-Cast-Authorization") != "Bearer agent-token" {
			t.Error("agent token")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "machine-token-for-tests")
	stream, err := m.Stream(context.Background(), edge.URL, "agent-token", runtime.Send{Text: "hi", StreamID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	frame, err := stream.Next()
	if err != nil || frame.Data != "[DONE]" {
		t.Fatalf("%v %+v", err, frame)
	}
}

func TestConfirmNameAndGuard(t *testing.T) {
	if err := ConfirmName("Atlas", "Atlas"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ConfirmName("atlas", "Atlas"), ErrName) {
		t.Fatal("case")
	}
	if !errors.Is(ConfirmName("", "Atlas"), ErrName) {
		t.Fatal("empty")
	}
	if err := GuardDelete(Machine{Name: "castbot-abc", Tags: []string{"castbot"}}); err != nil {
		t.Fatal(err)
	}
	if err := GuardDelete(Machine{Name: "cast-demo-agent-1"}); err == nil {
		t.Fatal("demo")
	}
	if err := GuardDelete(Machine{Name: "other", Tags: []string{"castbot-s00"}}); err == nil {
		t.Fatal("probe tag")
	}
}

func TestQuotaDenialDoesNotCreate(t *testing.T) {
	api := newScriptAPI(t, "cbt_bootstrap_secret_value")
	m := newTestClient(t, api, "machine-token-for-tests")
	err := provisionIfAllowed(context.Background(), m, testSpec("cbt_bootstrap_secret_value"), false)
	if !errors.Is(err, errQuota) {
		t.Fatal(err)
	}
	if api.posts != 0 {
		t.Fatal("quota denial created a machine")
	}
}

func TestTypedNameRejectsDelete(t *testing.T) {
	api := newScriptAPI(t, "")
	m := newTestClient(t, api, "machine-token-for-tests")
	err := deleteIfNamed(context.Background(), m, "vm-1", "Atlas", "Ledger")
	if !errors.Is(err, ErrName) {
		t.Fatal(err)
	}
	if api.deletes != 0 {
		t.Fatal("rejected delete still called the api")
	}
	if err := deleteIfNamed(context.Background(), m, "vm-1", "Atlas", "Atlas"); err != nil {
		t.Fatal(err)
	}
	if api.deletes != 1 {
		t.Fatalf("deletes %d", api.deletes)
	}
}

var errQuota = errors.New("agent limit reached")

func provisionIfAllowed(ctx context.Context, m *MicroVM, spec CreateSpec, allowed bool) error {
	if !allowed {
		return errQuota
	}
	_, err := m.Create(ctx, spec)
	return err
}

func deleteIfNamed(ctx context.Context, m *MicroVM, id, name, typed string) error {
	if err := ConfirmName(typed, name); err != nil {
		return err
	}
	return m.Delete(ctx, id)
}

func testSpec(secret string) CreateSpec {
	return CreateSpec{
		Name: "castbot-abc123", AgentID: "abc123", Image: "example.invalid/castbot-runtime:s04",
		Region: "mkc1", CPU: 1, MemoryMiB: 2048, Idle: 5 * time.Minute, HTTPPort: 8080,
		ControlURL: "https://castbot.example", Bootstrap: secret,
	}
}

func newTestClient(t *testing.T, api *scriptAPI, token string) *MicroVM {
	t.Helper()
	m, err := New(Options{Token: token, BaseURL: api.srv.URL, Interval: 5 * time.Millisecond, RetryFor: 40 * time.Millisecond, InsecureEdge: true})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func assertCreateBody(t *testing.T, raw []byte, secret string) {
	t.Helper()
	var body createReq
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Size.CPU != 1 || body.Size.Memory != 2048 || body.HTTPPort != 8080 {
		t.Fatalf("size %+v port %d", body.Size, body.HTTPPort)
	}
	if body.Source.OCIRef != "example.invalid/castbot-runtime:s04" {
		t.Fatal(body.Source.OCIRef)
	}
	if body.AutoPause.Idle != "5m" {
		t.Fatal(body.AutoPause.Idle)
	}
	if len(body.Tags) != 2 || body.Tags[0] != "castbot" || body.Tags[1] != "agent:abc123" {
		t.Fatalf("tags %#v", body.Tags)
	}
	if len(body.Env) != 3 || body.Env["CASTBOT_URL"] != "https://castbot.example" || body.Env["CASTBOT_AGENT_ID"] != "abc123" || body.Env["CASTBOT_BOOTSTRAP_TOKEN"] != secret {
		t.Fatalf("env keys %d", len(body.Env))
	}
	if strings.Contains(string(raw), "CASTBOT_CA") || strings.Contains(string(raw), "secret extra") {
		t.Fatal("unexpected env")
	}
}

type urlStep struct {
	Hostname string
	Status   string
}

type scriptAPI struct {
	srv        *httptest.Server
	mu         sync.Mutex
	gets       []string
	urls       []urlStep
	getN       int
	reason     string
	deletes    int
	posts      int
	paths      []string
	body       []byte
	failCreate bool
	secret     string
}

func newScriptAPI(t *testing.T, secret string) *scriptAPI {
	t.Helper()
	api := &scriptAPI{secret: secret, gets: []string{"running"}}
	api.srv = httptest.NewServer(api)
	t.Cleanup(api.srv.Close)
	return api
}

func (a *scriptAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.paths = append(a.paths, r.URL.Path)
	if r.Header.Get("Authorization") == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Header.Get("X-Cast-Authorization") != "" {
		http.Error(w, "guest header on the api", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v2/microvms":
		a.posts++
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		a.body = raw
		if a.failCreate {
			http.Error(w, "nope "+a.secret+" "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), http.StatusInternalServerError)
			return
		}
		writeVM(w, http.StatusCreated, "vm-1", "creating", "", urlStep{})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v2/microvms/"):
		state := "running"
		if a.getN < len(a.gets) {
			state = a.gets[a.getN]
		}
		step := a.urlAt(a.getN)
		a.getN++
		writeVM(w, http.StatusOK, "vm-1", state, a.reason, step)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pause"):
		writeVM(w, http.StatusOK, "vm-1", "paused", "", urlStep{Hostname: "vm-1.example.test"})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/resume"):
		writeVM(w, http.StatusOK, "vm-1", "running", "", urlStep{Hostname: "vm-1.example.test"})
	case r.Method == http.MethodDelete:
		a.deletes++
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (a *scriptAPI) urlAt(i int) urlStep {
	if len(a.urls) == 0 {
		return urlStep{Hostname: "vm-1.example.test"}
	}
	if i >= len(a.urls) {
		return a.urls[len(a.urls)-1]
	}
	return a.urls[i]
}

func writeVM(w http.ResponseWriter, status int, id, state, reason string, step urlStep) {
	if step.Hostname == "" && step.Status == "" && len(step.Hostname) == 0 {
		step.Hostname = "vm-1.example.test"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"microvm": map[string]any{
			"id": id, "name": "castbot-abc123", "state": state, "region": "mkc1",
			"failure_reason": reason,
			"tags":           []string{"castbot", "agent:abc123"},
			"size":           map[string]int{"cpu": 1, "memory": 2048},
			"source":         map[string]string{"oci_ref": "example.invalid/castbot-runtime:s04"},
			"urls": []map[string]any{{
				"default": true, "hostname": step.Hostname, "status": step.Status, "port": 8080,
			}},
		},
	})
}

func TestCreateBodyIncludesInstanceTag(t *testing.T) {
	const secret = "cbt_bootstrap_secret_value"
	api := newScriptAPI(t, secret)
	m := newTestClient(t, api, "machine-token-for-tests")
	spec := testSpec(secret)
	spec.Tags = []string{"castbot-inst:0123456789abcdef0123456789abcdef"}
	if _, err := m.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(api.body), `"castbot-inst:0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("body %s", api.body)
	}
	if !strings.Contains(string(api.body), `"castbot"`) || !strings.Contains(string(api.body), `"agent:abc123"`) {
		t.Fatalf("body %s", api.body)
	}
}

func TestLiveWindowRequiresFortyMinutes(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if err := LiveWindow(time.Time{}, false, now); err == nil {
		t.Fatal("missing deadline was accepted")
	}
	if err := LiveWindow(now.Add(10*time.Minute), true, now); err == nil {
		t.Fatal("10m deadline was accepted")
	}
	if err := LiveWindow(now.Add(45*time.Minute), true, now); err != nil {
		t.Fatal(err)
	}
}

func TestLiveLeftoversSkipProtectedMachines(t *testing.T) {
	got := LiveLeftovers([]Machine{
		{ID: "a", Name: "cast-demo-agent-1", Tags: []string{"castbot-live"}},
		{ID: "b", Name: "probe", Tags: []string{"castbot-s00", "castbot-live"}},
		{ID: "c", Name: "castbot-old", Tags: []string{"castbot", "castbot-live"}},
		{ID: "d", Name: "castbot-current", Tags: []string{"castbot"}},
	})
	if len(got) != 1 || got[0].ID != "c" {
		t.Fatalf("%+v", got)
	}
}

func TestEdgeURLRejectsArbitraryHosts(t *testing.T) {
	if got := endpoint([]apiURL{{Default: true, Hostname: "http://127.0.0.1:9"}}, false); got != "" {
		t.Fatalf("http endpoint %q", got)
	}
	if got := endpoint([]apiURL{{Default: true, Hostname: "https://evil.example.net"}}, false); got != "" {
		t.Fatalf("arbitrary https %q", got)
	}
	if got := endpoint([]apiURL{{Default: true, Hostname: "vm.mkc1.microvm.ondigitalocean.com"}}, false); got != "https://vm.mkc1.microvm.ondigitalocean.com" {
		t.Fatalf("real edge %q", got)
	}
	var hits int
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(edge.Close)
	m, err := New(Options{Token: "machine-token-for-tests", BaseURL: "https://api.example", InsecureEdge: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Probe(context.Background(), edge.URL, "agent-token"); err == nil {
		t.Fatal("http edge was accepted")
	}
	if hits != 0 {
		t.Fatal("bearer was sent over http")
	}
	if err := m.Probe(context.Background(), "https://evil.example.net/", "agent-token"); err == nil {
		t.Fatal("arbitrary host was accepted")
	}
}

func TestReloadStopsWhenTheContextEnds(t *testing.T) {
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "reload in progress", http.StatusConflict)
	}))
	t.Cleanup(edge.Close)
	m := newTestClient(t, newScriptAPI(t, ""), "machine-token-for-tests")
	m.retryFor = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := m.Reload(ctx, edge.URL, "agent-token")
	if err == nil {
		t.Fatal("expected cancel")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("reload ignored the context for %s", time.Since(start))
	}
}

func TestPollWaitsForHostnameThenAcceptsIt(t *testing.T) {
	const secret = "cbt_bootstrap_secret_value"
	api := newScriptAPI(t, secret)
	api.gets = []string{"running", "running", "running"}
	api.urls = []urlStep{
		{Status: "PENDING"},
		{Status: "PENDING"},
		{Hostname: "vm-1.mkc1.microvm.ondigitalocean.com", Status: "ACTIVE"},
	}
	m := newTestClient(t, api, "machine-token-for-tests")
	m.insecure = false
	m.addressFor = time.Second
	vm, err := m.Create(context.Background(), testSpec(secret))
	if err != nil {
		t.Fatal(err)
	}
	if vm.AddressRejected || vm.Endpoint != "https://vm-1.mkc1.microvm.ondigitalocean.com" {
		t.Fatalf("vm %+v", vm)
	}
	if api.deletes != 0 {
		t.Fatalf("deletes %d", api.deletes)
	}
	if api.getN < 3 {
		t.Fatalf("polls %d", api.getN)
	}
}

func TestPublishedBadHostnameIsRejected(t *testing.T) {
	const secret = "cbt_bootstrap_secret_value"
	api := newScriptAPI(t, secret)
	api.gets = []string{"running"}
	api.urls = []urlStep{{Hostname: "evil.example.net", Status: "ACTIVE"}}
	m := newTestClient(t, api, "machine-token-for-tests")
	m.insecure = false
	m.addressFor = time.Second
	start := time.Now()
	vm, err := m.Create(context.Background(), testSpec(secret))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("waited %s for a published hostname", time.Since(start))
	}
	if !vm.AddressRejected || vm.Endpoint != "" {
		t.Fatalf("vm %+v", vm)
	}
	if api.deletes != 0 {
		t.Fatalf("create deleted the machine itself: %d", api.deletes)
	}
}

func TestUnpublishedHostnameIsNotRejected(t *testing.T) {
	const secret = "cbt_bootstrap_secret_value"
	api := newScriptAPI(t, secret)
	api.gets = []string{"running"}
	api.urls = []urlStep{{Status: "PENDING"}}
	m := newTestClient(t, api, "machine-token-for-tests")
	m.insecure = false
	m.addressFor = 40 * time.Millisecond
	start := time.Now()
	vm, err := m.Create(context.Background(), testSpec(secret))
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("poll ran for %s", time.Since(start))
	}
	if vm.AddressRejected || vm.Endpoint != "" || vm.State != "running" {
		t.Fatalf("vm %+v", vm)
	}
}

func TestStatusIsCachedBriefly(t *testing.T) {
	api := newScriptAPI(t, "")
	m := newTestClient(t, api, "machine-token-for-tests")
	if _, err := m.Status(context.Background(), "vm-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(context.Background(), "vm-1"); err != nil {
		t.Fatal(err)
	}
	if api.getN != 1 {
		t.Fatalf("status calls %d", api.getN)
	}
	paused, err := m.Pause(context.Background(), "vm-1")
	if err != nil || paused.State != "paused" {
		t.Fatal(err, paused.State)
	}
	got, err := m.Status(context.Background(), "vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "paused" {
		t.Fatalf("stale status %s", got.State)
	}
	if api.getN != 1 {
		t.Fatalf("status calls %d", api.getN)
	}
}
