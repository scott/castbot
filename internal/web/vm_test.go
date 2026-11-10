package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
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
			code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(url.Values{}), ip, nil)
			if code != http.StatusBadGateway || !strings.Contains(body, "The machine did not start.") {
				t.Fatalf("fail %d %s", code, body)
			}
			if _, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID); err != store.ErrNotFound {
				t.Fatal(err)
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

func containsCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}
