package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestMachineHandlers(t *testing.T) {
	ip := "203.0.113.120:9"
	cases := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{"create", func(t *testing.T) {
			h, fake, agent := machineUser(t, ip)
			code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil)
			if code != http.StatusSeeOther || !strings.HasSuffix(hdr.Get("Location"), "/machine") {
				t.Fatalf("create %d %s", code, hdr.Get("Location"))
			}
			waitMachine(t, h, agent, ip, "Pause")
			if len(fake.Creates) != 1 {
				t.Fatalf("creates %d", len(fake.Creates))
			}
			spec := fake.Creates[0]
			if spec.Image != "example.invalid/castbot-runtime:s04" || spec.ControlURL != "https://castbot.example" {
				t.Fatalf("spec %+v", spec)
			}
			if spec.CPU != 1 || spec.MemoryMiB != 2048 || spec.HTTPPort != 8080 || spec.Region != "mkc1" {
				t.Fatalf("size %+v", spec)
			}
			if spec.Idle != 5*time.Minute || spec.Name != host.MachineName(agent.ID) || spec.Bootstrap == "" {
				t.Fatalf("idle %s name %s", spec.Idle, spec.Name)
			}
			inst, err := h.st.InstanceID(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !containsTag(spec.Tags, host.InstanceTag(inst)) {
				t.Fatalf("create tags %#v", spec.Tags)
			}
			code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
			if code != 200 || !strings.Contains(body, "Pause") || strings.Contains(body, spec.Bootstrap) || strings.Contains(body, "vm.example") {
				t.Fatalf("page leaked or missed pause: %d", code)
			}
			calls, edge := fake.Snapshot()
			if edge != 0 {
				t.Fatalf("status touched the edge: %v", calls)
			}
		}},
		{"pause and resume", func(t *testing.T) {
			h, fake, agent := machineUser(t, ip)
			if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
				t.Fatal(code)
			}
			waitMachine(t, h, agent, ip, "Pause")
			if code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/pause", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
				t.Fatalf("pause %d %s", code, hdr.Get("Location"))
			}
			got, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID)
			if err != nil || got.Status != "paused" {
				t.Fatalf("status %s %v", got.Status, err)
			}
			if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/wake", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
				t.Fatal(code)
			}
			got, err = h.st.GetAgent(context.Background(), agent.UserID, agent.ID)
			if err != nil || got.Status != "ready" {
				t.Fatalf("wake %s %v", got.Status, err)
			}
			calls, edge := fake.Snapshot()
			if edge != 0 || !containsCall(calls, "pause") || !containsCall(calls, "wake") {
				t.Fatalf("calls %v edge %d", calls, edge)
			}
		}},
		{"failed create releases the slot", func(t *testing.T) {
			h, fake, agent := machineUser(t, ip)
			fake.CreateErr = &host.Failed{Reason: "image pull failed"}
			code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil)
			if code != http.StatusSeeOther {
				t.Fatalf("fail %d", code)
			}
			waitMachine(t, h, agent, ip, "The machine did not start.")
			_, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
			if !strings.Contains(body, "Try again") {
				t.Fatal("page has no retry")
			}
			if _, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID); err != store.ErrNotFound {
				t.Fatal(err)
			}
			used, _, err := h.st.Quota(context.Background(), agent.UserID)
			if err != nil || used != 0 {
				t.Fatalf("quota %d %v", used, err)
			}
			fake.CreateErr = nil
			if code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
				t.Fatalf("retry %d %s", code, hdr.Get("Location"))
			}
		}},
		{"quota denial", func(t *testing.T) {
			h, fake, agent := machineUser(t, ip)
			if err := h.st.SetQuota(context.Background(), agent.UserID, 0); err != nil {
				t.Fatal(err)
			}
			code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil)
			if code != http.StatusConflict || !strings.Contains(body, "machine limit") {
				t.Fatalf("quota %d %s", code, body)
			}
			if len(fake.Creates) != 0 {
				t.Fatal("quota denial created a machine")
			}
		}},
		{"typed name rejection", func(t *testing.T) {
			h, fake, agent := machineUser(t, ip)
			if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
				t.Fatal(code)
			}
			waitMachine(t, h, agent, ip, "Pause")
			code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/delete", h.form(url.Values{"confirm": {"not-the-name"}}), ip, nil)
			if code != http.StatusBadRequest || !strings.Contains(body, "Type the agent name") {
				t.Fatalf("reject %d %s", code, body)
			}
			if _, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID); err != nil {
				t.Fatal(err)
			}
			if len(fake.Deletes) != 0 {
				t.Fatal("rejected delete called the host")
			}
			code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/delete", h.form(url.Values{"confirm": {agent.Name}}), ip, nil)
			if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
				t.Fatalf("delete %d %s", code, hdr.Get("Location"))
			}
			if len(fake.Deletes) != 1 {
				t.Fatalf("deletes %#v", fake.Deletes)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t) })
	}
}

func TestVMAuthz(t *testing.T) {
	h, fake, agent := machineUser(t, "203.0.113.130:9")
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), "203.0.113.130:9", nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, "203.0.113.130:9", "Pause")
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	fake.Frames = []runtime.Frame{{
		Data:      `{"jsonrpc":"2.0","result":{"parts":[{"kind":"text","text":"pong"}]}}`,
		DataEvent: true,
	}}
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello"}}), "203.0.113.130:9", map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatalf("send %d %s", code, body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		calls, _ := fake.Snapshot()
		if containsCall(calls, "stream") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	calls, _ := fake.Snapshot()
	if !containsCall(calls, "stream") {
		t.Fatalf("owner send did not stream: %v", calls)
	}
	before := len(calls)

	_, bSecret := h.account("b-vm@example.com", "User B", "correct-horse-1")
	h.do(http.MethodPost, "/logout", h.form(url.Values{}), "203.0.113.130:9", nil)
	h.login("b-vm@example.com", "correct-horse-1", bSecret, "203.0.113.131:9", false)
	paths := []struct {
		method, path string
		form         url.Values
	}{
		{http.MethodPost, "/agents/" + agent.ID + "/machine", nil},
		{http.MethodPost, "/agents/" + agent.ID + "/machine/pause", nil},
		{http.MethodPost, "/agents/" + agent.ID + "/machine/wake", nil},
		{http.MethodPost, "/agents/" + agent.ID + "/machine/delete", url.Values{"confirm": {agent.Name}}},
		{http.MethodPost, "/agents/" + agent.ID + "/messages", url.Values{"body": {"stolen"}}},
		{http.MethodPost, "/agents/" + agent.ID + "/delete", url.Values{"confirm": {agent.Name}}},
	}
	for _, p := range paths {
		form := p.form
		if form == nil {
			form = url.Values{}
		}
		code, body, _ := h.do(p.method, p.path, h.form(form), "203.0.113.131:9", nil)
		if code != http.StatusNotFound {
			t.Fatalf("B %s %s -> %d %s", p.method, p.path, code, body)
		}
	}
	calls, _ = fake.Snapshot()
	if len(calls) != before {
		t.Fatalf("B reached the machine: before %d after %v", before, calls)
	}
	if _, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedProxyLockout(t *testing.T) {
	h := newHarness(t)
	nets, err := ParseTrustedProxies("10.0.0.8/32")
	if err != nil {
		t.Fatal(err)
	}
	h.srv.SetTrustedProxies(nets)
	h.account("alex-proxy@example.com", "Alex Rivera", "correct-horse-1")
	h.account("other-proxy@example.com", "Other Person", "correct-horse-1")
	proxy := "10.0.0.8:443"
	var body string
	for i := 0; i < 5; i++ {
		_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex-proxy@example.com"}, "password": {"wrong-password-1"}}), proxy, map[string]string{"X-Forwarded-For": "198.51.100.10"})
	}
	if !strings.Contains(body, "Sign-in is paused") {
		t.Fatalf("client was not locked: %s", body)
	}
	code, _, hdr := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"other-proxy@example.com"}, "password": {"correct-horse-1"}}), proxy, map[string]string{"X-Forwarded-For": "198.51.100.20"})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("second client shared the lock: %d %s", code, hdr.Get("Location"))
	}
	_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"other-proxy@example.com"}, "password": {"correct-horse-1"}}), proxy, map[string]string{"X-Forwarded-For": "198.51.100.10"})
	if !strings.Contains(body, "Sign-in is paused") {
		t.Fatal("locked client was treated as a new address")
	}
}

func TestUntrustedForwardedForSharesTheConnection(t *testing.T) {
	h := newHarness(t)
	h.account("other-plain@example.com", "Other Person", "correct-horse-1")
	remote := "203.0.113.140:9"
	for i := 0; i < 5; i++ {
		h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"ghost-plain@example.com"}, "password": {"wrong-password-1"}}), remote, map[string]string{"X-Forwarded-For": "198.51.100.40"})
	}
	_, body, _ := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"other-plain@example.com"}, "password": {"correct-horse-1"}}), remote, map[string]string{"X-Forwarded-For": "198.51.100.41"})
	if !strings.Contains(body, "Sign-in is paused") {
		t.Fatal("untrusted forwarded header split the lock")
	}
	code, _, hdr := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"other-plain@example.com"}, "password": {"correct-horse-1"}}), "203.0.113.141:9", map[string]string{"X-Forwarded-For": "203.0.113.140"})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("spoofed header was trusted: %d %s", code, hdr.Get("Location"))
	}
}

func TestClientAddr(t *testing.T) {
	nets, err := ParseTrustedProxies("10.0.0.0/8, 192.0.2.5")
	if err != nil {
		t.Fatal(err)
	}
	if got := clientAddr("10.0.0.1:9", "198.51.100.9, 10.1.1.1", nets); got != "198.51.100.9" {
		t.Fatalf("chain %s", got)
	}
	if got := clientAddr("203.0.113.5:9", "198.51.100.9", nets); got != "203.0.113.5" {
		t.Fatalf("untrusted %s", got)
	}
	if got := clientAddr("10.0.0.1:9", "", nets); got != "10.0.0.1" {
		t.Fatalf("empty %s", got)
	}
	if got := clientAddr("192.0.2.5:9", "198.51.100.8", nets); got != "198.51.100.8" {
		t.Fatalf("single %s", got)
	}
	if got := clientAddr("10.0.0.1:9", "198.51.100.7, unknown", nets); got != "10.0.0.1" {
		t.Fatalf("unknown %s", got)
	}
	if got := clientAddr("10.0.0.1:9", "198.51.100.6, 198.51.100.5:80", nets); got != "10.0.0.1" {
		t.Fatalf("host:port %s", got)
	}
	if got := clientAddr("10.0.0.1:9", "198.51.100.4,", nets); got != "10.0.0.1" {
		t.Fatalf("trailing comma %s", got)
	}
	if got := clientAddr("10.0.0.1:9", "198.51.100.3, unknown, 10.1.1.1", nets); got != "10.1.1.1" {
		t.Fatalf("trusted then junk %s", got)
	}
	if _, err := ParseTrustedProxies("0.0.0.0/0"); err == nil {
		t.Fatal("accepted every address")
	}
	if _, err := ParseTrustedProxies("::/0"); err == nil {
		t.Fatal("accepted every ipv6 address")
	}
}

func TestCancelledRequestStillFinishes(t *testing.T) {
	ip := "203.0.113.160:9"
	h, fake, agent := machineUser(t, ip)
	hold := make(chan struct{})
	fake.WaitCreate = hold
	fake.CreateStarted = make(chan struct{}, 1)
	body := h.form(url.Values{})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/agents/"+agent.ID+"/machine", strings.NewReader(body))
	req = req.WithContext(ctx)
	req.RemoteAddr = ip
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.mu.Lock()
	for _, c := range h.jar {
		req.AddCookie(c)
	}
	h.mu.Unlock()
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.srv.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-fake.CreateStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("create did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler stayed on the cancelled request")
	}
	if rec.Code != http.StatusSeeOther {
		raw, _ := io.ReadAll(rec.Body)
		t.Fatalf("create %d %s", rec.Code, raw)
	}
	close(hold)
	waitMachine(t, h, agent, ip, "Pause")
	vm, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID)
	if err != nil || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		t.Fatalf("row %+v %v", vm, err)
	}
	used, _, err := h.st.Quota(context.Background(), agent.UserID)
	if err != nil || used != 1 {
		t.Fatalf("quota %d %v", used, err)
	}
	if _, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentProvisionIsConflict(t *testing.T) {
	ip := "203.0.113.161:9"
	h, _, agent := machineUser(t, ip)
	var wg sync.WaitGroup
	codes := make([]int, 6)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil)
		}(i)
	}
	wg.Wait()
	sawOK := 0
	for _, code := range codes {
		switch code {
		case http.StatusSeeOther:
			sawOK++
		case http.StatusConflict:
		default:
			t.Fatalf("codes %v", codes)
		}
	}
	if sawOK != 1 {
		t.Fatalf("codes %v", codes)
	}
}

func TestChatDoesNotEchoWhileTheMachineStarts(t *testing.T) {
	ip := "203.0.113.162:9"
	h, fake, agent := machineUser(t, ip)
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatalf("send %d", code)
	}
	deadline := time.Now().Add(3 * time.Second)
	var got store.Run
	for time.Now().Before(deadline) {
		threads, err := h.st.ListThreadsByAgent(context.Background(), agent.UserID, agent.ID)
		if err != nil || len(threads) == 0 {
			t.Fatal(err)
		}
		runs, err := h.st.ListThreadRuns(context.Background(), agent.UserID, threads[0].ID)
		if err != nil || len(runs) == 0 {
			t.Fatal(err)
		}
		got = runs[len(runs)-1]
		if got.Status == "failed" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got.Status != "failed" || got.Error != "The machine is starting." {
		t.Fatalf("run %+v", got)
	}
	calls, edge := fake.Snapshot()
	if edge != 0 || containsCall(calls, "stream") {
		t.Fatalf("fell through to a runtime: %v edge %d", calls, edge)
	}
}

func TestSweepReleasesStaleAndOrphans(t *testing.T) {
	ip := "203.0.113.163:9"
	h, fake, agent := machineUser(t, ip)
	past := time.Now().Add(-20 * time.Minute)
	h.st.SetNow(func() time.Time { return past })
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s04"); err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(nil)
	inst, err := h.st.InstanceID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tag := host.InstanceTag(inst)
	fake.Machines["demo"] = host.Machine{ID: "demo", Name: "cast-demo-agent-1", State: "paused", Tags: []string{"castbot", tag}}
	fake.Machines["probe"] = host.Machine{ID: "probe", Name: "leftover-probe", State: "paused", Tags: []string{"castbot", "castbot-s00", tag}}
	fake.Machines["live"] = host.Machine{ID: "live", Name: "castbot-harness", State: "running", Tags: []string{"castbot", "castbot-live", tag}}
	fake.Machines["foreign"] = host.Machine{ID: "foreign", Name: "castbot-other", State: "running", Tags: []string{"castbot", "agent:someone"}}
	fake.Machines["other"] = host.Machine{ID: "other", Name: "castbot-other-inst", State: "running", Tags: []string{"castbot", "castbot-inst:ffffffffffffffffffffffffffffffff"}}
	fake.Machines["orphan"] = host.Machine{ID: "orphan", Name: "castbot-leftover", State: "running", Tags: []string{"castbot", tag, "agent:old"}}
	h.srv.SweepMachines(context.Background())
	if _, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID); err != store.ErrNotFound {
		t.Fatal(err)
	}
	used, _, err := h.st.Quota(context.Background(), agent.UserID)
	if err != nil || used != 0 {
		t.Fatalf("quota %d %v", used, err)
	}
	if _, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.Machines["orphan"]; ok {
		t.Fatal("orphan remained")
	}
	for _, id := range []string{"demo", "probe", "live", "foreign", "other"} {
		if _, ok := fake.Machines[id]; !ok {
			t.Fatalf("%s was deleted", id)
		}
	}
	_, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if !strings.Contains(body, "Try again") {
		t.Fatal(body)
	}
}

func TestSweepFreshDatabaseDeletesNothing(t *testing.T) {
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s04", "https://castbot.example")
	fake.Machines["a"] = host.Machine{ID: "a", Name: "castbot-aaa", State: "running", Tags: []string{"castbot", "agent:aaa"}}
	fake.Machines["b"] = host.Machine{ID: "b", Name: "castbot-bbb", State: "paused", Tags: []string{"castbot", "agent:bbb"}}
	fake.Machines["c"] = host.Machine{ID: "c", Name: "castbot-ccc", State: "running", Tags: []string{"castbot", "agent:ccc", "castbot-inst:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	h.srv.SweepMachines(context.Background())
	if len(fake.Deletes) != 0 {
		t.Fatalf("fresh database deleted %#v", fake.Deletes)
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, ok := fake.Machines[id]; !ok {
			t.Fatalf("%s was deleted", id)
		}
	}
}

func TestStartingMachineIsNotReady(t *testing.T) {
	ip := "203.0.113.164:9"
	h, _, agent := machineUser(t, ip)
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	_, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if strings.Contains(body, "dot run") || strings.Contains(body, "chip run") {
		t.Fatal("starting machine used the ready dot")
	}
	if !strings.Contains(body, "dot start") || !strings.Contains(body, "Starting") {
		t.Fatal("starting state was not shown")
	}
}

func TestRejectedAddressIsAnError(t *testing.T) {
	ip := "203.0.113.165:9"
	h, fake, agent := machineUser(t, ip)
	fake.BlankEndpoint = true
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "The machine address was not accepted.")
	if _, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID); err != store.ErrNotFound {
		t.Fatal(err)
	}
	if len(fake.Deletes) != 1 {
		t.Fatalf("deletes %#v", fake.Deletes)
	}
	if _, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID); err != nil {
		t.Fatal(err)
	}
}

func machineUser(t *testing.T, ip string) (*harness, *host.Fake, store.Agent) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s04", "https://castbot.example")
	user, secret := h.account("owner-vm@example.com", "Owner", "correct-horse-1")
	h.login("owner-vm@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper",
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, fake, agent
}

func waitMachine(t *testing.T, h *harness, agent store.Agent, ip, needle string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
		last = body
		if code == 200 && strings.Contains(body, needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("machine page never showed %q (%d bytes)", needle, len(last))
}

func containsTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}
