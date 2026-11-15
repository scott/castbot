package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestRollbackPageAndAuthz(t *testing.T) {
	ip := "203.0.113.90:9"
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:ff6d7ea", "https://castbot.example")
	user, secret := h.account("ada-rollback@example.com", "Ada", "correct-horse-1")
	h.login("ada-rollback@example.com", "correct-horse-1", secret, ip, false)
	_, otherSecret := h.account("bea-rollback@example.com", "Bea", "correct-horse-1")
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready", Model: "model-large",
	})
	if err != nil {
		t.Fatal(err)
	}
	const current = "example.invalid/castbot-runtime:ff6d7ea"
	const previous = "example.invalid/castbot-runtime:7b5e414"
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, "castbot-now", current); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-now", "https://now.example", "running", ""); err != nil {
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
	if err := h.st.NoteRuntimeRollback(context.Background(), user.ID, agent.ID, "offer", previous); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-now"] = host.Machine{ID: "vm-now", Name: "castbot-now", State: "running", Endpoint: "https://now.example", Image: current, Tags: []string{"agent:" + agent.ID}}
	fake.SeedDisk("https://now.example", []byte("mem"), map[string][]byte{"a.txt": []byte("aaa")})
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

	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Roll back runtime") || !strings.Contains(body, "Previous runtime 7b5e414 can be restored for 7 days.") {
		t.Fatalf("panel %d", code)
	}
	if strings.Contains(body, token) || strings.Contains(body, "now.example") || strings.Contains(body, previous) {
		t.Fatal("page leaked a secret, an endpoint, or the full image")
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/rollback?opener=rollback-open", "", ip, nil)
	for _, line := range []string{"Roll back this runtime?", "previous runtime (7b5e414)", "Safe if something goes wrong.", "copies this agent's memory and workspace files", "p-confirm"} {
		if code != 200 || !strings.Contains(body, line) {
			t.Fatalf("confirm missing %q (%d)", line, code)
		}
	}

	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/rollback", "csrf=nope", ip, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
		t.Fatalf("csrf %d", code)
	}
	h.login("bea-rollback@example.com", "correct-horse-1", otherSecret, "203.0.113.91:9", false)
	if code, _, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine/rollback", "", "203.0.113.91:9", nil); code != http.StatusNotFound {
		t.Fatalf("other confirm %d", code)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/rollback", h.form(url.Values{}), "203.0.113.91:9", nil); code != http.StatusNotFound {
		t.Fatalf("other post %d", code)
	}
	// The same authenticator step cannot be used twice. Move the clock past it.
	h.st.SetNow(func() time.Time { return time.Now().Add(time.Minute) })
	h.login("ada-rollback@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/rollback", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "/machine") {
		t.Fatalf("start %d %s", code, hdr.Get("Location"))
	}
	waitReplace(t, h, agent, ip, "Now on 7b5e414.")
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Previous runtime ff6d7ea can be restored for 7 days.") {
		t.Fatal("rollback did not offer the runtime it left")
	}
	if strings.Contains(body, ">aaa<") || strings.Contains(body, ">mem<") {
		t.Fatal("page shows file contents")
	}
}
