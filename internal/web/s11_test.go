package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestFormatUSD(t *testing.T) {
	if got := formatUSD(0); got != "$0.00" {
		t.Fatal(got)
	}
	if got := formatUSD(39000); got != "$0.04" {
		t.Fatal(got)
	}
	if got := formatUSD(390000); got != "$0.39" {
		t.Fatal(got)
	}
	if got := formatUSD(10000); got != "$0.01" {
		t.Fatal(got)
	}
	if got := formatUSD(312000); got != "$0.31" {
		t.Fatal(got)
	}
}

func TestMachinePanelRunning(t *testing.T) {
	ip := "203.0.113.15:9"
	h, fake, agent, _ := runningRig(t, ip, "s11-run@example.com")
	anchor := h.st.Now()
	h.st.SetNow(func() time.Time { return anchor.Add(time.Hour) })
	token := machineToken(t, h, agent)
	secret := "fixture-secret-value"
	sec, err := h.st.PutSecret(context.Background(), agent.UserID, "NOTES_TOKEN", secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), agent.UserID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	shaped := "sk-" + "live-fixture-token-value"
	fake.LogTail = "boot ok\n" + secret + "\n" + shaped + "\n" + token + "\n"
	fake.SeedDisk("https://vm.example", nil, map[string][]byte{"notes.md": []byte("hi")})

	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || callCount(fake, "logs") != 0 || callCount(fake, "list-files") != 0 {
		t.Fatalf("chat %d logs %d list %d", code, callCount(fake, "logs"), callCount(fake, "list-files"))
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	for _, line := range []string{
		`id="month-est"`, "$0.04", "An estimate, not a bill.",
		"boot ok", "[redacted]", "********", "notes.md", "Open files",
		"Latest lines from the machine.",
		"Workspace files on this machine.",
		`/machine/logs`, `id="machine-file-filter"`,
	} {
		if !strings.Contains(body, line) {
			t.Fatalf("missing %q", line)
		}
	}
	if strings.Contains(body, secret) || strings.Contains(body, shaped) || strings.Contains(body, token) {
		t.Fatal("secret or token leaked into the machine panel")
	}
	if callCount(fake, "logs") != 1 || callCount(fake, "list-files") != 1 {
		t.Fatalf("logs %d list %d", callCount(fake, "logs"), callCount(fake, "list-files"))
	}
	if _, edge := fake.Snapshot(); edge != 0 {
		t.Fatalf("edge %d", edge)
	}
	code, frag, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/logs", "", ip, nil)
	if code != 200 || !strings.Contains(frag, "boot ok") || !strings.Contains(frag, `id="machine-logs"`) || strings.Contains(frag, "<!doctype") {
		t.Fatalf("fragment %d", code)
	}
	if strings.Contains(frag, secret) || strings.Contains(frag, token) {
		t.Fatal("fragment leaked a secret")
	}
}

func TestMachinePanelDoesNotWake(t *testing.T) {
	ip := "203.0.113.16:9"
	h, fake, agent, _ := runningRig(t, ip, "s11-pause@example.com")
	id := "vm-" + agent.ID
	fake.SetMachineState(id, "paused")
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "paused", "", ""); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	for _, line := range []string{
		"The machine is paused.",
		"Logs stay on the machine. Wake it to read the tail. This check does not wake it.",
		"Opening this panel does not wake the machine.",
	} {
		if !strings.Contains(body, line) {
			t.Fatalf("missing %q", line)
		}
	}
	if strings.Contains(body, "/machine/logs") {
		t.Fatal("paused panel polls logs")
	}
	if callCount(fake, "logs") != 0 || callCount(fake, "list-files") != 0 || wakeCount(fake) != 0 {
		t.Fatalf("logs %d list %d wake %d", callCount(fake, "logs"), callCount(fake, "list-files"), wakeCount(fake))
	}
	if fake.Machines[id].State != "paused" {
		t.Fatalf("state %s", fake.Machines[id].State)
	}
	vm, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID)
	if err != nil || vm.State != "paused" {
		t.Fatalf("saved %+v %v", vm, err)
	}
	code, frag, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/logs", "", ip, nil)
	if code != 200 || !strings.Contains(frag, "This check does not wake it.") || strings.Contains(frag, "hx-get=") {
		t.Fatalf("paused fragment %d", code)
	}
	if callCount(fake, "logs") != 0 || fake.Machines[id].State != "paused" {
		t.Fatal("log fragment woke the machine")
	}
}

func TestMachinePanelQuietStates(t *testing.T) {
	ip := "203.0.113.17:9"
	h, fake, agent, _ := runningRig(t, ip, "s11-quiet@example.com")
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "failed", "", ""); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "No log tail yet.") || callCount(fake, "logs") != 0 {
		t.Fatalf("failed %d logs %d", code, callCount(fake, "logs"))
	}
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "creating", "", ""); err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "No log tail yet.") || callCount(fake, "logs") != 0 || callCount(fake, "list-files") != 0 {
		t.Fatalf("creating %d", code)
	}
	fake.SetMachineState("vm-"+agent.ID, "running")
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "running", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	fake.FailClass = "logs"
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "The log tail could not be read. The machine was not woken.") {
		t.Fatalf("read error %d", code)
	}
	if _, edge := fake.Snapshot(); edge != 0 || wakeCount(fake) != 0 {
		t.Fatal("log failure woke the machine")
	}
}

func TestMachinePanelAuthz(t *testing.T) {
	ip := "203.0.113.18:9"
	h, _, agent, _ := runningRig(t, ip, "s11-own@example.com")
	_, secret := h.account("s11-other@example.com", "Other", "correct-horse-1")
	h.login("s11-other@example.com", "correct-horse-1", secret, ip, false)
	code, _, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("panel %d", code)
	}
	code, _, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/logs", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("logs %d", code)
	}
}

func TestSettingsMachineEstimate(t *testing.T) {
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s04", "https://castbot.example")
	when := time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return when })
	ip := "203.0.113.19:9"
	user, secret := h.account("s11-bill@example.com", "Ada Lovelace", "correct-horse-1")
	h.login("s11-bill@example.com", "correct-horse-1", secret, ip, false)
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
	readyMachine(t, h, fake, agent, "vm-bill", "running")
	h.st.SetNow(func() time.Time { return when.Add(10 * time.Hour) })
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	for _, line := range []string{"$0.39", "This month", "An estimate, not a bill.", ">Atlas<", "?panel=machine"} {
		if !strings.Contains(body, line) {
			t.Fatalf("missing %q", line)
		}
	}
	if strings.Contains(body, "Machine time is not metered") {
		t.Fatal("unmetered banner is still shown")
	}
	_, otherSecret := h.account("s11-bill-other@example.com", "Bea", "correct-horse-1")
	h.login("s11-bill-other@example.com", "correct-horse-1", otherSecret, ip, false)
	code, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != 200 || !strings.Contains(body, "$0.00") || strings.Contains(body, "$0.39") || strings.Contains(body, ">Atlas<") {
		t.Fatalf("other settings %d", code)
	}
}

func machineToken(t *testing.T, h *harness, agent store.Agent) string {
	t.Helper()
	_, token, err := h.st.VMCredentials(context.Background(), agent.UserID, agent.ID)
	if err != nil || token == "" {
		t.Fatal(err)
	}
	return token
}
