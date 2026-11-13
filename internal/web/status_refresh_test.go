package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestRenderDoesNotCallStatus(t *testing.T) {
	ip := "203.0.113.70:9"
	h, fake, agent, _ := runningRig(t, ip, "status-render@example.com")
	block := make(chan struct{})
	fake.StatusBlock = block
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	before := callCount(fake, "status")
	code, body, elapsed := doLimited(t, h, http.MethodGet, "/agents/"+agent.ID, "", ip)
	if code != 200 || !strings.Contains(body, "Running") || !strings.Contains(body, `id="vm-sync"`) || elapsed > 800*time.Millisecond {
		t.Fatalf("chat %d in %s", code, elapsed)
	}
	code, body, elapsed = doLimited(t, h, http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip)
	if code != 200 || !strings.Contains(body, "The machine is running.") || !strings.Contains(body, ">Refresh<") || elapsed > 800*time.Millisecond {
		t.Fatalf("panel %d in %s", code, elapsed)
	}
	time.Sleep(40 * time.Millisecond)
	if callCount(fake, "status") != before || wakeCount(fake) != 0 {
		t.Fatalf("status %d wake %d", callCount(fake, "status"), wakeCount(fake))
	}
	_, edge := fake.Snapshot()
	if edge != 0 {
		t.Fatalf("edge %d", edge)
	}
	if !strings.Contains(body, `load delay:2s, load delay:6s`) {
		t.Fatal("sync hook missing")
	}
}

func TestWarmCachePaintsWithoutStatus(t *testing.T) {
	ip := "203.0.113.71:9"
	h, fake, agent, _ := runningRig(t, ip, "status-warm@example.com")
	id := "vm-" + agent.ID
	before := callCount(fake, "status")
	fake.Warm = map[string]host.Machine{
		id: {ID: id, State: "paused", Endpoint: "https://vm.example"},
	}
	code, body, elapsed := doLimited(t, h, http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip)
	if code != 200 || !strings.Contains(body, "The machine is paused.") || strings.Contains(body, "The machine is running.") || elapsed > 800*time.Millisecond {
		t.Fatalf("warm page %d in %s", code, elapsed)
	}
	if callCount(fake, "status") != before || wakeCount(fake) != 0 {
		t.Fatalf("status %d wake %d", callCount(fake, "status"), wakeCount(fake))
	}
	vm, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID)
	if err != nil || vm.State != "paused" {
		t.Fatalf("saved %+v %v", vm, err)
	}
}

func TestExplicitRefreshCallsStatus(t *testing.T) {
	ip := "203.0.113.72:9"
	h, fake, agent, _ := runningRig(t, ip, "status-refresh@example.com")
	fake.SetForce("paused")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "The machine is running.") || !strings.Contains(body, ">Refresh<") {
		t.Fatalf("before %d", code)
	}
	if callCount(fake, "status") != 0 || wakeCount(fake) != 0 {
		t.Fatalf("render called status %d wake %d", callCount(fake, "status"), wakeCount(fake))
	}

	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/refresh", "x=1", ip, nil)
	if code != http.StatusForbidden || callCount(fake, "status") != 0 {
		t.Fatalf("csrf %d status %d", code, callCount(fake, "status"))
	}
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/refresh", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || !strings.HasSuffix(hdr.Get("Location"), "/machine") {
		t.Fatalf("refresh %d %s", code, hdr.Get("Location"))
	}
	if callCount(fake, "status") == 0 || wakeCount(fake) != 0 {
		t.Fatalf("status %d wake %d", callCount(fake, "status"), wakeCount(fake))
	}
	_, edge := fake.Snapshot()
	if edge != 0 {
		t.Fatalf("edge %d", edge)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "The machine is paused.") {
		t.Fatalf("after %d", code)
	}
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/refresh", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || !strings.Contains(body, "The machine is paused.") || !strings.Contains(body, `id="machine-root"`) {
		t.Fatalf("hx %d", code)
	}
	_, otherSecret := h.account("status-other@example.com", "Other", "correct-horse-1")
	h.login("status-other@example.com", "correct-horse-1", otherSecret, ip, false)
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/refresh", h.form(url.Values{}), ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("other %d", code)
	}
}

func TestMachineSyncCatchesUpWithoutStatus(t *testing.T) {
	ip := "203.0.113.73:9"
	h, fake, agent, _ := runningRig(t, ip, "status-sync@example.com")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	path := syncPath(t, body)
	before := callCount(fake, "status")
	code, body, _ = h.do(http.MethodGet, path, "", ip, nil)
	if code != http.StatusNoContent || body != "" || callCount(fake, "status") != before {
		t.Fatalf("same %d %q status %d", code, body, callCount(fake, "status"))
	}
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "paused", "", ""); err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, path, "", ip, nil)
	if code != 200 || !strings.Contains(body, "Paused") || !strings.Contains(body, `id="vmchip"`) || !strings.Contains(body, `id="hd-dot"`) || !strings.Contains(body, `id="ag-sub-`) || !strings.Contains(body, `id="machine-lead"`) || !strings.Contains(body, `hx-swap-oob="outerHTML"`) {
		t.Fatalf("catch up %d %s", code, body)
	}
	if callCount(fake, "status") != before || wakeCount(fake) != 0 {
		t.Fatalf("status %d wake %d", callCount(fake, "status"), wakeCount(fake))
	}
	_, secret := h.account("status-sync-other@example.com", "Other", "correct-horse-1")
	h.login("status-sync-other@example.com", "correct-horse-1", secret, ip, false)
	code, _, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/sync", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("other %d", code)
	}
}

func TestStatusRefreshLoopUpdatesTheRow(t *testing.T) {
	ip := "203.0.113.74:9"
	h, fake, agent, _ := runningRig(t, ip, "status-loop@example.com")
	fake.SetForce("paused")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "The machine is running.") || callCount(fake, "status") != 0 {
		t.Fatalf("paint %d status %d", code, callCount(fake, "status"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.srv.RunStatusRefresh(ctx, 15*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		vm, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		state = vm.State
		if state == "paused" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if state != "paused" || callCount(fake, "status") == 0 || wakeCount(fake) != 0 {
		t.Fatalf("state %s status %d wake %d", state, callCount(fake, "status"), wakeCount(fake))
	}
	_, edge := fake.Snapshot()
	if edge != 0 {
		t.Fatalf("edge %d", edge)
	}
}

func TestBackgroundRefreshFeedsThePage(t *testing.T) {
	ip := "203.0.113.78:9"
	h, fake, agent := pausedRig(t, ip, "status-catchup@example.com")
	fake.SetForce("creating")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "The machine is paused.") || callCount(fake, "status") != 0 {
		t.Fatalf("paint %d status %d", code, callCount(fake, "status"))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.srv.RunStatusRefresh(ctx, 15*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	var state string
	for time.Now().Before(deadline) {
		vm, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		state = vm.State
		if state == "creating" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if state != "creating" || wakeCount(fake) != 0 {
		t.Fatalf("state %s wake %d", state, wakeCount(fake))
	}
	code, body, _ = h.do(http.MethodGet, syncPath(t, body), "", ip, nil)
	if code != 200 || !strings.Contains(body, "Waking") || !strings.Contains(body, "The machine is waking.") || !strings.Contains(body, `id="machine-lead"`) || !strings.Contains(body, `id="machine-chip"`) {
		t.Fatalf("sync %d %s", code, body)
	}
	if wakeCount(fake) != 0 {
		t.Fatal("status refresh woke the machine")
	}
}

func runningRig(t *testing.T, ip, email string) (*harness, *host.Fake, store.Agent, string) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s04", "https://castbot.example")
	user, secret := h.account(email, "Owner", "correct-horse-1")
	h.login(email, "correct-horse-1", secret, ip, false)
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
	readyMachine(t, h, fake, agent, "vm-"+agent.ID, "running")
	return h, fake, agent, secret
}

func syncPath(t *testing.T, body string) string {
	t.Helper()
	const key = `id="vm-sync" hidden hx-get="`
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatal("missing vm-sync")
	}
	rest := body[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatal("sync url")
	}
	return strings.ReplaceAll(rest[:j], "&amp;", "&")
}

func callCount(fake *host.Fake, name string) int {
	calls, _ := fake.Snapshot()
	n := 0
	for _, call := range calls {
		if call == name {
			n++
		}
	}
	return n
}

func doLimited(t *testing.T, h *harness, method, path, form, ip string) (int, string, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var body io.Reader
	if form != "" {
		body = strings.NewReader(form)
	}
	req := httptest.NewRequest(method, path, body).WithContext(ctx)
	req.RemoteAddr = ip
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	h.mu.Lock()
	for _, c := range h.jar {
		req.AddCookie(c)
	}
	h.mu.Unlock()
	rec := httptest.NewRecorder()
	start := time.Now()
	h.srv.Handler().ServeHTTP(rec, req)
	elapsed := time.Since(start)
	res := rec.Result()
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(raw), elapsed
}
