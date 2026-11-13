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

func TestResumePollSkipsUnchangedChrome(t *testing.T) {
	ip := "203.0.113.75:9"
	h, fake, agent := pausedRig(t, ip, "resume-poll@example.com")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	act := strings.Index(body, `id="resume-act"`)
	live := strings.Index(body, `id="resume-live"`)
	if code != 200 || act < 0 || live < act || !strings.Contains(body[act:live], "actico") || !strings.Contains(body[act:live], "Waking up") || !strings.Contains(body, `hx-include="this"`) || !strings.Contains(body, `name="act" value="1"`) {
		t.Fatalf("page %d act %d live %d", code, act, live)
	}
	if wakeCount(fake) != 0 {
		t.Fatal("GET woke the machine")
	}

	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "creating", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	m := fake.Machines["vm-"+agent.ID]
	m.State = "creating"
	fake.Machines["vm-"+agent.ID] = m
	path := "/agents/" + agent.ID + "/resume"
	hx := map[string]string{"HX-Request": "true"}
	code, body, _ = h.do(http.MethodPost, path, h.form(url.Values{}), ip, hx)
	if code != 200 || wakeCount(fake) != 0 || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, "Waking up") || !strings.Contains(body, `class="act"`) || !strings.Contains(body, `hx-swap-oob="outerHTML"`) {
		t.Fatalf("first poll %d", code)
	}

	same := url.Values{
		"dot": {"start"}, "chip": {"start"}, "label": {"Waking"},
		"sub": {"Waking up"}, "subc": {"act-sub"}, "act": {"1"},
	}
	code, body, _ = h.do(http.MethodPost, path, h.form(same), ip, hx)
	if code != 200 || strings.Contains(body, `class="act"`) || strings.Contains(body, "actico") || strings.Contains(body, "hx-swap-oob") || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, `name="act" value="1"`) {
		t.Fatalf("unchanged poll %d %s", code, body)
	}

	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "running", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	m.State = "running"
	fake.Machines["vm-"+agent.ID] = m
	code, body, _ = h.do(http.MethodPost, path, h.form(same), ip, hx)
	if code != 200 || strings.Contains(body, `hx-trigger=`) || strings.Contains(body, `class="act"`) || !strings.Contains(body, "Running") || !strings.Contains(body, `hx-swap-oob="outerHTML"`) || !strings.Contains(body, `id="resume-act" hx-swap-oob="delete"`) {
		t.Fatalf("done poll %d %s", code, body)
	}
	if wakeCount(fake) != 0 {
		t.Fatalf("wakes %d", wakeCount(fake))
	}
}

func TestReplaceCardIsASmallFragment(t *testing.T) {
	ip := "203.0.113.76:9"
	h := newHarness(t)
	fake := host.NewFake()
	block := make(chan struct{})
	fake.StatusBlock = block
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:ff6d7ea", "https://castbot.example")
	user, secret := h.account("card-frag@example.com", "Ada", "correct-horse-1")
	h.login("card-frag@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready",
	})
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
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != 200 || !strings.Contains(page, "Starting a new machine") || !strings.Contains(page, `id="replace-progress"`) || strings.Contains(page, `id="machine-root" hx-get=`) {
		t.Fatalf("panel %d", code)
	}
	before := callCount(fake, "status")
	code, body, elapsed := doLimited(t, h, http.MethodGet, "/agents/"+agent.ID+"/machine/card", "", ip)
	if code != 200 || elapsed > 800*time.Millisecond || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, "Starting a new machine") || !strings.Contains(body, `id="replace-progress"`) {
		t.Fatalf("card %d in %s len %d", code, elapsed, len(body))
	}
	if strings.Contains(body, `id="machine-root"`) || strings.Contains(body, "Machine history") || strings.Contains(body, "Delete agent") || strings.Contains(body, "<!doctype") || strings.Contains(body, `id="msgs"`) || len(body) > 2500 || len(body) >= len(page)/2 {
		t.Fatalf("card too large %d page %d", len(body), len(page))
	}
	if callCount(fake, "status") != before || wakeCount(fake) != 0 {
		t.Fatalf("status %d wake %d", callCount(fake, "status"), wakeCount(fake))
	}
}

func TestBootFragmentStaysSmall(t *testing.T) {
	ip := "203.0.113.77:9"
	h := newHarness(t)
	fake := host.NewFake()
	block := make(chan struct{})
	fake.StatusBlock = block
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s42", "https://castbot.example")
	user, secret := h.account("boot-frag@example.com", "Ada", "correct-horse-1")
	h.login("boot-frag@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Wren", Title: "Inbox",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateOriginMessage(context.Background(), user.ID, thread.ID, "system", agent.ID, "You created Atlas.", "created"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, "user", user.ID, "plain-thread-sentence-9f3"); err != nil {
		t.Fatal(err)
	}
	before := callCount(fake, "status")
	code, body, elapsed := doLimited(t, h, http.MethodGet, "/agents/"+agent.ID+"/boot", "", ip)
	if code != 200 || elapsed > 800*time.Millisecond || !strings.Contains(body, "Atlas is starting up") || !strings.Contains(body, `id="boot"`) || !strings.Contains(body, `hx-trigger="every 2s"`) {
		t.Fatalf("boot %d in %s len %d", code, elapsed, len(body))
	}
	if strings.Contains(body, "plain-thread-sentence-9f3") || strings.Contains(body, "You created Atlas") || strings.Contains(body, "Wren") || strings.Contains(body, `id="msgs"`) || strings.Contains(body, "<!doctype") || len(body) > 4000 {
		t.Fatalf("boot fragment %d", len(body))
	}
	if callCount(fake, "status") != before || wakeCount(fake) != 0 {
		t.Fatalf("status %d wake %d", callCount(fake, "status"), wakeCount(fake))
	}
	_, otherSecret := h.account("boot-other@example.com", "Other", "correct-horse-1")
	h.login("boot-other@example.com", "correct-horse-1", otherSecret, ip, false)
	code, _, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/boot", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("other %d", code)
	}
}
