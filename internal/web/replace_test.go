package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/store"
)

func TestReplacePageCopyAndRefusals(t *testing.T) {
	ip := "203.0.113.77:9"
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:ff6d7ea", "https://castbot.example")
	user, secret := h.account("ada-replace@example.com", "Ada", "correct-horse-1")
	h.login("ada-replace@example.com", "correct-horse-1", secret, ip, false)
	other, otherSecret := h.account("bea-replace@example.com", "Bea", "correct-horse-1")
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready", Model: "model-large",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-old", "https://old.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	boot, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-old"] = host.Machine{ID: "vm-old", Name: "castbot-old", State: "paused", Endpoint: "https://old.example", Image: "example.invalid/castbot-runtime:7b5e414", Tags: []string{"agent:" + agent.ID}}
	fake.SeedDisk("https://old.example", []byte("mem"), map[string][]byte{"a.txt": []byte("aaa")})
	fake.OnCreate = func(spec host.CreateSpec) {
		next, err := h.st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := h.st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}

	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "7b5e414 · Update available") || !strings.Contains(body, "A newer runtime is available (ff6d7ea).") {
		t.Fatalf("available page %d", code)
	}
	if strings.Contains(body, token) || strings.Contains(body, "old.example") {
		t.Fatal("page leaked a secret or an endpoint")
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/replace", "", ip, nil)
	for _, line := range []string{"Replace this machine?", "Runtime now", "changes to", "Runtime after", "Safe if something goes wrong.", "Memory (copied)", "Workspace files (copied)", "Atlas is paused now, so castbot wakes it once to read it."} {
		if code != 200 || !strings.Contains(body, line) {
			t.Fatalf("confirm missing %q (%d)", line, code)
		}
	}
	if strings.Contains(body, "no-copy") || strings.Contains(body, "--no-copy") {
		t.Fatal("confirm mentions no-copy")
	}

	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/replace", "csrf=nope", ip, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
		t.Fatalf("csrf %d", code)
	}

	run, err := h.st.CreateRun(context.Background(), user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/replace", h.form(url.Values{}), ip, nil)
	if code != http.StatusConflict || !strings.Contains(body, "Finish or stop the reply in progress first.") {
		t.Fatalf("run block %d %s", code, body)
	}
	if _, _, err := h.st.FinishRun(context.Background(), user.ID, run.ID, "done", 1); err != nil {
		t.Fatal(err)
	}

	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/replace", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "/machine") {
		t.Fatalf("start %d %s", code, hdr.Get("Location"))
	}
	waitReplace(t, h, agent, ip, "Machine replaced.")
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if !strings.Contains(body, "Machine replaced.") || !strings.Contains(body, "Copied memory and 1 file.") || strings.Contains(body, "1 files.") {
		t.Fatalf("success copy missing")
	}
	if strings.Contains(body, ">aaa<") || strings.Contains(body, ">mem<") {
		t.Fatal("page shows file contents")
	}
	for _, path := range []string{"/bundle", "/secrets", "/cli-tokens"} {
		req := httptestReq(t, http.MethodGet, "/api/castlet/agents/"+agent.ID+path, token)
		rec := serve(h, req)
		if rec != http.StatusNotFound {
			t.Fatalf("old token %s %d", path, rec)
		}
	}
	req := httptestReq(t, http.MethodPost, "/api/castlet/agents/"+agent.ID+"/journal", token)
	if serve(h, req) != http.StatusNotFound {
		t.Fatal("old token journal")
	}
	h.login("bea-replace@example.com", "correct-horse-1", otherSecret, "203.0.113.78:9", false)
	if code, _, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/replace", "", "203.0.113.78:9", nil); code != http.StatusNotFound {
		t.Fatalf("other confirm %d", code)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/replace", h.form(url.Values{}), "203.0.113.78:9", nil); code != http.StatusNotFound {
		t.Fatalf("other post %d", code)
	}
	if code, _, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/card", "", "203.0.113.78:9", nil); code != http.StatusNotFound {
		t.Fatalf("other card %d", code)
	}
	_ = other
}

func TestReplaceRefusesWhileRunning(t *testing.T) {
	ip := "203.0.113.79:9"
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:ff6d7ea", "https://castbot.example")
	user, secret := h.account("run-replace@example.com", "Ada", "correct-horse-1")
	h.login("run-replace@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.BeginReplacement(context.Background(), user.ID, agent.ID, "cli", "example.invalid/castbot-runtime:ff6d7ea", false); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "A replacement is already running.") || !strings.Contains(body, "Starting a new machine") {
		t.Fatalf("cli progress %d", code)
	}
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hi"}, "thread": {thread.ID}}), ip, nil)
	if code != http.StatusConflict || !strings.Contains(body, "This machine is being replaced.") {
		t.Fatalf("message %d %s", code, body)
	}
	for _, path := range []string{"/machine/pause", "/machine/wake", "/machine/delete", "/cli"} {
		code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+path, h.form(url.Values{"confirm": {"Atlas"}}), ip, nil)
		if code != http.StatusConflict || !strings.Contains(body, "This machine is being replaced.") {
			t.Fatalf("%s %d %s", path, code, body)
		}
	}
	run, err := h.st.CreateRun(context.Background(), user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/approve", "/deny"} {
		code, body, _ = h.do(http.MethodPost, "/runs/"+run.ID+"/approvals/missing"+path, h.form(url.Values{}), ip, nil)
		if code != http.StatusConflict || !strings.Contains(body, "This machine is being replaced.") {
			t.Fatalf("%s %d %s", path, code, body)
		}
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/card", "", ip, nil)
	if code != 200 || !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Fatalf("poll %d", code)
	}
}

func TestReplaceFailureCopy(t *testing.T) {
	ip := "203.0.113.80:9"
	h := newHarness(t)
	fake := host.NewFake()
	fake.FailClass = "read_old"
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:ff6d7ea", "https://castbot.example")
	user, secret := h.account("fail-replace@example.com", "Ada", "correct-horse-1")
	h.login("fail-replace@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, "castbot-old", "example.invalid/castbot-runtime:7b5e414"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-old", "https://old.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	boot, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-old"] = host.Machine{ID: "vm-old", State: "running", Endpoint: "https://old.example", Tags: []string{"agent:" + agent.ID}}
	fake.OnCreate = func(spec host.CreateSpec) {
		next, err := h.st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			t.Errorf("exchange %v", err)
			return
		}
		if _, err := h.st.PlaceVMToken(context.Background(), spec.AgentID, next); err != nil {
			t.Errorf("place %v", err)
		}
	}
	_, err = replace.Run(context.Background(), h.st, fake, replace.Options{
		UserID: user.ID, AgentID: agent.ID, ActorKind: "user", ActorID: user.ID, Actor: "web:" + user.Email,
		Image: "example.invalid/castbot-runtime:ff6d7ea", PublicURL: "https://castbot.example", DataDir: t.TempDir(), BootWait: time.Second,
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "The machine was not replaced.") || !strings.Contains(body, "The old machine could not be read.") || !strings.Contains(body, "Try again") {
		t.Fatalf("failure page %d", code)
	}
}

func waitReplace(t *testing.T, h *harness, agent store.Agent, ip, needle string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
		last = body
		if code == 200 && strings.Contains(body, needle) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s\n%s", needle, last)
}

func httptestReq(t *testing.T, method, path, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(`{"entries":[]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "203.0.113.77:9"
	return req
}

func serve(h *harness, req *http.Request) int {
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rec.Code
}
