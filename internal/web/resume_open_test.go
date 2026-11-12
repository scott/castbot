package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func TestOpenPausedChatResumesOnce(t *testing.T) {
	ip := "203.0.113.42:9"
	h, fake, agent := pausedRig(t, ip, "resume-once@example.com")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	post := `hx-post="/agents/` + agent.ID + `/resume"`
	if code != 200 || !strings.Contains(body, post) || !strings.Contains(body, `hx-trigger="load"`) || !strings.Contains(body, "Waking up") || !strings.Contains(body, "actico") || !strings.Contains(body, ">Waking<") {
		t.Fatalf("chat %d wakes %d", code, wakeCount(fake))
	}
	if wakeCount(fake) != 0 {
		t.Fatal("GET woke the machine")
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=files", "", ip, nil)
	if code != 200 || strings.Contains(body, post) || wakeCount(fake) != 0 {
		t.Fatalf("files %d", code)
	}
	code, body, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/resume", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/agents/"+agent.ID {
		t.Fatalf("redirect %d %s", code, hdr.Get("Location"))
	}
	if wakeCount(fake) != 1 {
		t.Fatalf("wakes %d", wakeCount(fake))
	}
	got, err := h.st.GetAgent(t.Context(), agent.UserID, agent.ID)
	if err != nil || got.Status != "ready" {
		t.Fatalf("status %s %v", got.Status, err)
	}
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/resume", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || strings.Contains(body, `hx-trigger=`) || wakeCount(fake) != 1 {
		t.Fatalf("second %d wakes %d", code, wakeCount(fake))
	}
	if !strings.Contains(body, "Running") {
		t.Fatal("chip")
	}
}

func TestResumeSkipsRunningAndStarting(t *testing.T) {
	ip := "203.0.113.44:9"
	h, fake, agent := pausedRig(t, ip, "resume-run@example.com")
	if err := h.st.ApplyVMState(t.Context(), agent.UserID, agent.ID, "running", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	m := fake.Machines["vm-"+agent.ID]
	m.State = "running"
	fake.Machines["vm-"+agent.ID] = m
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	post := `hx-post="/agents/` + agent.ID + `/resume"`
	if code != 200 || strings.Contains(body, post) {
		t.Fatalf("running page %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/resume", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || wakeCount(fake) != 0 {
		t.Fatalf("running post %d wakes %d", code, wakeCount(fake))
	}

	h, fake, agent = pausedRig(t, ip, "resume-start@example.com")
	if err := h.st.ApplyVMState(t.Context(), agent.UserID, agent.ID, "creating", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	m = fake.Machines["vm-"+agent.ID]
	m.State = "creating"
	fake.Machines["vm-"+agent.ID] = m
	got, err := h.st.GetAgent(t.Context(), agent.UserID, agent.ID)
	if err != nil || got.Activity != "Waking" {
		t.Fatalf("activity %s %v", got.Activity, err)
	}
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/resume", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || wakeCount(fake) != 0 || !strings.Contains(body, `hx-trigger="every 2s"`) || !strings.Contains(body, "Waking up") {
		t.Fatalf("starting %d wakes %d", code, wakeCount(fake))
	}
}

func TestResumeDoublePostOneWake(t *testing.T) {
	ip := "203.0.113.45:9"
	h, fake, agent := pausedRig(t, ip, "resume-double@example.com")
	wake := make(chan struct{})
	fake.WakeWait = wake
	var once sync.Once
	release := func() { once.Do(func() { close(wake) }) }
	t.Cleanup(release)
	body := h.form(url.Values{})
	h.mu.Lock()
	var cookies []*http.Cookie
	for _, c := range h.jar {
		cookies = append(cookies, c)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	codes := make([]int, 2)
	started := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/agents/"+agent.ID+"/resume", strings.NewReader(body))
			req.RemoteAddr = ip
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			for _, c := range cookies {
				req.AddCookie(c)
			}
			rec := httptest.NewRecorder()
			started <- struct{}{}
			h.srv.Handler().ServeHTTP(rec, req)
			codes[i] = rec.Code
			io.Copy(io.Discard, rec.Body)
		}(i)
	}
	<-started
	<-started
	release()
	wg.Wait()
	if wakeCount(fake) != 1 {
		t.Fatalf("wakes %d", wakeCount(fake))
	}
	for i, code := range codes {
		if code != 200 {
			t.Fatalf("post %d status %d", i, code)
		}
	}
	got, err := h.st.GetAgent(t.Context(), agent.UserID, agent.ID)
	if err != nil || got.Status != "ready" {
		t.Fatalf("status %s %v", got.Status, err)
	}
}

func TestResumeHostErrorStaysOnChat(t *testing.T) {
	ip := "203.0.113.46:9"
	h, fake, agent := pausedRig(t, ip, "resume-err@example.com")
	fake.WakeErr = errors.New("provider down")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/resume", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || !strings.Contains(body, "The machine did not change.") || strings.Contains(body, "provider down") {
		t.Fatalf("error page %d", code)
	}
	if wakeCount(fake) != 1 {
		t.Fatalf("wakes %d", wakeCount(fake))
	}
	got, err := h.st.GetAgent(t.Context(), agent.UserID, agent.ID)
	if err != nil || got.Status != "paused" {
		t.Fatalf("status %s %v", got.Status, err)
	}
}

func TestResumeRejectsMissingCSRFAndStrangers(t *testing.T) {
	ip := "203.0.113.47:9"
	h, fake, agent := pausedRig(t, ip, "resume-owner@example.com")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/resume", "x=1", ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") || wakeCount(fake) != 0 {
		t.Fatalf("csrf %d wakes %d", code, wakeCount(fake))
	}

	anon := newHarness(t)
	anon.srv.SetHost(fake, "example.invalid/castbot-runtime:s42", "https://castbot.example")
	code, _, hdr := anon.do(http.MethodPost, "/agents/"+agent.ID+"/resume", anon.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" || wakeCount(fake) != 0 {
		t.Fatalf("anon %d %s", code, hdr.Get("Location"))
	}

	_, secret := h.account("resume-other@example.com", "Other", "correct-horse-1")
	h.login("resume-other@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/resume", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusNotFound || wakeCount(fake) != 0 {
		t.Fatalf("other %d %s", code, body)
	}
}

func TestCloseAndTapCSS(t *testing.T) {
	raw, err := os.ReadFile("static/castbot.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for _, needle := range []string{
		".sclose:focus-visible { outline: 2px solid var(--acc); outline-offset: 2px; }",
		".ph-top .sclose { width: 44px; height: 44px; margin-left: auto; }",
		".inchat { min-height: 44px; min-width: 44px; }",
	} {
		if !strings.Contains(css, needle) {
			t.Fatalf("missing %s", needle)
		}
	}
}

func pausedRig(t *testing.T, ip, email string) (*harness, *host.Fake, store.Agent) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s42", "https://castbot.example")
	user, secret := h.account(email, "Owner", "correct-horse-1")
	h.login(email, "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(t.Context(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(t.Context(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper",
	})
	if err != nil {
		t.Fatal(err)
	}
	readyMachine(t, h, fake, agent, "vm-"+agent.ID, "paused")
	return h, fake, agent
}

func wakeCount(fake *host.Fake) int {
	calls, _ := fake.Snapshot()
	n := 0
	for _, call := range calls {
		if call == "wake" {
			n++
		}
	}
	return n
}
