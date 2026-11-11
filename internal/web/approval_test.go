package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const demoSecret = "demo-secret-value"

func TestAskApproveFinishesAndDenyEnds(t *testing.T) {
	args := map[string]any{
		"command": "ls demo",
		"note":    "demo <tag>",
		"api_key": demoSecret,
	}
	t.Run("approve", func(t *testing.T) {
		h, ask, user, agent, ip := newAsk(t, args, nil)
		sendPrompt(t, h, agent, ip, "List the demo folder")
		ap := waitPending(t, h, user.ID)
		if strings.Contains(ap.Arguments, demoSecret) || strings.Contains(ap.Redacted, demoSecret) {
			t.Fatalf("stored secret %s %s", ap.Arguments, ap.Redacted)
		}
		if !strings.Contains(ap.Arguments, "ls demo") || !strings.Contains(ap.Redacted, "api_key") {
			t.Fatalf("scrub %s redacted %s", ap.Arguments, ap.Redacted)
		}
		code, body, _ := h.do(http.MethodPost, decisionPath(ap, true), "csrf=nope", ip, nil)
		if code != http.StatusForbidden {
			t.Fatalf("csrf %d %s", code, body)
		}
		if len(ask.Calls()) != 0 {
			t.Fatalf("csrf posted %#v", ask.Calls())
		}
		code, body = postDecision(h, ap, ip, true)
		if code != http.StatusOK || !strings.Contains(body, "Approved by you") || strings.Contains(body, ">Approve<") {
			t.Fatalf("card %d %s", code, body)
		}
		if strings.Contains(body, demoSecret) || !strings.Contains(body, "Hidden") || !strings.Contains(body, "demo &lt;tag&gt;") {
			t.Fatalf("args html %s", body)
		}
		if strings.Contains(body, "btn danger") {
			t.Fatalf("danger on approved card %s", body)
		}
		h.waitRuns(user.ID, agentThread(t, h, user.ID, agent.ID))
		if calls := ask.Calls(); len(calls) != 1 || !calls[0] {
			t.Fatalf("calls %#v", calls)
		}
		page := h.poll(ip, "/agents/"+agent.ID, runtime.AskFinished)
		if !strings.Contains(page, "Approved by you") || strings.Contains(page, demoSecret) || strings.Contains(page, ">Approve<") {
			t.Fatalf("page %s", page)
		}
		assertAudit(t, h, user.ID, "approval.approved", "user", user.ID, 1)
		assertNoSecret(t, h, user.ID)
	})
	t.Run("deny", func(t *testing.T) {
		h, ask, user, agent, ip := newAsk(t, args, nil)
		sendPrompt(t, h, agent, ip, "List the demo folder")
		ap := waitPending(t, h, user.ID)
		pending := h.poll(ip, "/agents/"+agent.ID, ">Deny<")
		if strings.Count(pending, "btn danger") != 1 || !strings.Contains(pending, ">Approve<") || strings.Contains(pending, demoSecret) {
			t.Fatalf("pending %s", pending)
		}
		code, body := postDecision(h, ap, ip, false)
		if code != http.StatusOK || !strings.Contains(body, "Denied") || strings.Contains(body, ">Deny<") {
			t.Fatalf("card %d %s", code, body)
		}
		h.waitRuns(user.ID, agentThread(t, h, user.ID, agent.ID))
		if calls := ask.Calls(); len(calls) != 1 || calls[0] {
			t.Fatalf("calls %#v", calls)
		}
		want := runtime.DenialMessage("bash")
		page := h.poll(ip, "/agents/"+agent.ID, want)
		if !strings.Contains(page, ">Denied<") && !strings.Contains(page, "Denied") {
			t.Fatalf("page %s", page)
		}
		if strings.Contains(page, runtime.AskFinished) || strings.Contains(page, ">Deny<") {
			t.Fatalf("page %s", page)
		}
		row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
		if err != nil || row.Status != "denied" {
			t.Fatalf("status %#v %v", row, err)
		}
		assertAudit(t, h, user.ID, "approval.denied", "user", user.ID, 1)
	})
}

func TestApprovalDoubleSubmitOneRespond(t *testing.T) {
	h, ask, user, agent, ip := newAsk(t, nil, nil)
	sendPrompt(t, h, agent, ip, "List the demo folder")
	ap := waitPending(t, h, user.ID)
	form := h.form(url.Values{})
	path := decisionPath(ap, true)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var codes [2]int
	var bodies [2]string
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i], bodies[i], _ = h.do(http.MethodPost, path, form, ip, nil)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < 2; i++ {
		if codes[i] != http.StatusOK || !strings.Contains(bodies[i], "Approved by you") {
			t.Fatalf("tab %d %d %s", i, codes[i], bodies[i])
		}
	}
	code, body := postDecision(h, ap, ip, true)
	if code != http.StatusOK || !strings.Contains(body, "Approved by you") {
		t.Fatalf("second click %d %s", code, body)
	}
	h.waitRuns(user.ID, agentThread(t, h, user.ID, agent.ID))
	if calls := ask.Calls(); len(calls) != 1 || !calls[0] {
		t.Fatalf("calls %#v", calls)
	}
	assertAudit(t, h, user.ID, "approval.approved", "user", user.ID, 1)
	assertAudit(t, h, user.ID, "approval.requested", "agent", agent.ID, 1)
}

func TestApprovalTimeoutPostsDeny(t *testing.T) {
	start := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	var clock atomic.Value
	clock.Store(start)
	h, ask, user, agent, ip := newAsk(t, map[string]any{"command": "ls demo", "api_key": demoSecret}, func() time.Time {
		return clock.Load().(time.Time)
	})
	sendPrompt(t, h, agent, ip, "List the demo folder")
	ap := waitPending(t, h, user.ID)
	wantExp := start.Add(10 * time.Minute).UTC().Format(time.RFC3339)
	if ap.ExpiresAt != wantExp {
		t.Fatalf("expires %s want %s", ap.ExpiresAt, wantExp)
	}
	clock.Store(start.Add(10 * time.Minute))
	h.srv.ExpireDue(context.Background())
	h.waitRuns(user.ID, agentThread(t, h, user.ID, agent.ID))
	if calls := ask.Calls(); len(calls) != 1 || calls[0] {
		t.Fatalf("calls %#v", calls)
	}
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "expired" || row.DecidedBy != "system:timeout" || !row.Sent {
		t.Fatalf("row %#v %v", row, err)
	}
	page := h.poll(ip, "/agents/"+agent.ID, "Expired")
	if strings.Contains(page, ">Approve<") || strings.Contains(page, ">Deny<") || strings.Contains(page, demoSecret) {
		t.Fatalf("page %s", page)
	}
	if !strings.Contains(page, runtime.DenialMessage("bash")) {
		t.Fatalf("denial missing %s", page)
	}
	assertAudit(t, h, user.ID, "approval.expired", "system", "system:timeout", 1)
	assertAudit(t, h, user.ID, "approval.denied", "", "", 0)
	assertNoSecret(t, h, user.ID)
}

func TestClosedStreamDoesNotDeny(t *testing.T) {
	h, ask, user, agent, ip := newAsk(t, nil, nil)
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	sendPrompt(t, h, agent, ip, "List the demo folder")
	ap := waitPending(t, h, user.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := readSSE(ctx, live.URL+"/runs/"+ap.RunID+"/events", h.cookieList(), "", func(ev []sseEvent) bool {
		return countEvent(ev, "card") > 0
	})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if len(ask.Calls()) != 0 {
		t.Fatalf("stream close posted %#v", ask.Calls())
	}
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "pending" {
		t.Fatalf("status %#v %v", row, err)
	}
	page := h.poll(ip, "/agents/"+agent.ID, ">Approve<")
	if !strings.Contains(page, ">Deny<") || !strings.Contains(page, "ls demo") {
		t.Fatalf("reload %s", page)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	again, err := readSSE(ctx, live.URL+"/runs/"+ap.RunID+"/events", h.cookieList(), "", func(ev []sseEvent) bool {
		return countEvent(ev, "card") > 0
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again[len(again)-1].Data, ">Approve<") {
		t.Fatalf("reconnect %#v", again)
	}
	assertAudit(t, h, user.ID, "approval.denied", "", "", 0)
	assertAudit(t, h, user.ID, "approval.expired", "", "", 0)
}

func TestApprovalAuthz(t *testing.T) {
	h := newHarness(t)
	owner, ownerSecret := h.account("owner-s07b@example.com", "Owner", "correct-horse-1")
	other, otherSecret := h.account("other-s07b@example.com", "Other", "correct-horse-1")
	ctx := context.Background()
	agent := makeAgent(t, h.st, owner.ID, "Atlas")
	thread := agentThread(t, h, owner.ID, agent.ID)
	runRow, err := h.st.CreateRun(ctx, owner.ID, thread, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	ap, err := h.st.RecordPermission(ctx, owner.ID, runRow.ID, []byte(`{"id":"req-owner","tool":"bash","arguments":{"command":"ls demo"}}`), h.st.Now().Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	otherAgent := makeAgent(t, h.st, other.ID, "Other")
	otherThread := agentThread(t, h, other.ID, otherAgent.ID)
	otherRun, err := h.st.CreateRun(ctx, other.ID, otherThread, otherAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	ip := "203.0.113.77:9"
	h.login("other-s07b@example.com", "correct-horse-1", otherSecret, ip, false)
	paths := []struct {
		method, path string
	}{
		{http.MethodGet, "/approvals/" + ap.ID},
		{http.MethodPost, "/runs/" + runRow.ID + "/approvals/" + ap.ID + "/approve"},
		{http.MethodPost, "/runs/" + runRow.ID + "/approvals/" + ap.ID + "/deny"},
		{http.MethodPost, "/runs/" + otherRun.ID + "/approvals/" + ap.ID + "/approve"},
		{http.MethodGet, "/runs/" + runRow.ID + "/events"},
		{http.MethodGet, "/agents/" + agent.ID},
	}
	for _, p := range paths {
		form := ""
		if p.method == http.MethodPost {
			form = h.form(url.Values{})
		}
		code, body, _ := h.do(p.method, p.path, form, ip, nil)
		if code != http.StatusNotFound || strings.Contains(body, "ls demo") || strings.Contains(body, ap.ID) {
			t.Fatalf("other %s %s -> %d %s", p.method, p.path, code, body)
		}
	}
	row, err := h.st.GetApproval(ctx, owner.ID, ap.ID)
	if err != nil || row.Status != "pending" {
		t.Fatalf("owner row %#v %v", row, err)
	}
	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	h.login("owner-s07b@example.com", "correct-horse-1", ownerSecret, "203.0.113.78:9", false)
	code, body, _ := h.do(http.MethodGet, "/approvals/"+ap.ID, "", "203.0.113.78:9", nil)
	if code != http.StatusOK || !strings.Contains(body, ">Approve<") || !strings.Contains(body, "ls demo") {
		t.Fatalf("owner card %d %s", code, body)
	}
}

func TestCLIApprovalIsReadOnly(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("cli-s07b@example.com", "Demo User", "correct-horse-1")
	ip := "203.0.113.79:9"
	h.login("cli-s07b@example.com", "correct-horse-1", secret, ip, false)
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.IngestJournal(context.Background(), user.ID, agent.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-10T18:00:00Z", SessionID: "sess-s07b", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "request", Text: "from the cli"},
		{Seq: 2, At: "2026-11-10T18:00:01Z", SessionID: "", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "decision", RequestID: "req-s07b", Decision: "approved"},
		{Seq: 3, At: "2026-11-10T18:00:02Z", SessionID: "sess-s07b", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "permission", Tool: "bash", RequestID: "req-s07b"},
	}); err != nil {
		t.Fatal(err)
	}
	var thread string
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.Kind == "cli" {
			thread = th.ID
		}
	}
	if thread == "" {
		t.Fatal("missing cli thread")
	}
	page := h.poll(ip, "/agents/"+agent.ID+"?thread="+url.QueryEscape(thread), "Answer it in the Cast CLI")
	if !strings.Contains(page, "An approval for bash was approved.") {
		t.Fatalf("decision missing %s", page)
	}
	if strings.Contains(page, ">Approve<") || strings.Contains(page, ">Deny<") || strings.Contains(page, "hx-post=\"/runs/") {
		t.Fatalf("cli card is writable %s", page)
	}
}

func TestApprovalTimeUsesUserZone(t *testing.T) {
	when := time.Date(2026, 11, 10, 20, 42, 0, 0, time.UTC)
	h, _, user, agent, ip := newAsk(t, nil, func() time.Time { return when })
	if err := h.st.SetTimezone(context.Background(), user.ID, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	sendPrompt(t, h, agent, ip, "List the demo folder")
	ap := waitPending(t, h, user.ID)
	code, body := postDecision(h, ap, ip, true)
	if code != http.StatusOK {
		t.Fatalf("approve %d %s", code, body)
	}
	if !strings.Contains(body, "Approved by you at 3:42 PM") || strings.Contains(body, "1:42 PM") {
		t.Fatalf("zone %s", body)
	}
	page := h.poll(ip, "/agents/"+agent.ID, "Approved by you at 3:42 PM")
	if strings.Contains(page, "1:42 PM") || strings.Contains(page, "Mountain") {
		t.Fatalf("page zone %s", page)
	}
}

func TestPausedMachineWakesBeforeApproval(t *testing.T) {
	h, ask, user, agent, ip := newAsk(t, nil, nil)
	sendPrompt(t, h, agent, ip, "List the demo folder")
	ap := waitPending(t, h, user.ID)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s07b", "https://castbot.example")
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07b"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-paused", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
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
	fake.Machines["vm-paused"] = host.Machine{ID: "vm-paused", State: "paused", Endpoint: "https://vm.example"}
	fake.SetForce("paused")
	code, body := postDecision(h, ap, ip, true)
	if code != http.StatusOK || !strings.Contains(body, "Approved by you") {
		t.Fatalf("card %d %s", code, body)
	}
	if len(ask.Calls()) != 0 {
		t.Fatalf("http respond %#v", ask.Calls())
	}
	calls, approvals := fake.Decisions()
	if len(approvals) != 1 || approvals[0].RequestID != ap.RequestID || !approvals[0].Allow {
		t.Fatalf("approve %#v", approvals)
	}
	wake, allow := -1, -1
	for i, call := range calls {
		if call == "wake" && wake < 0 {
			wake = i
		}
		if call == "approve" && allow < 0 {
			allow = i
		}
	}
	if wake < 0 || allow < 0 || wake > allow {
		t.Fatalf("calls %v", calls)
	}
}

func TestUnknownApprovalGoesInactive(t *testing.T) {
	h, ask, user, agent, ip := newAsk(t, nil, nil)
	sendPrompt(t, h, agent, ip, "List the demo folder")
	ap := waitPending(t, h, user.ID)
	ask.Forget()
	code, body := postDecision(h, ap, ip, true)
	if code != http.StatusOK || !strings.Contains(body, "This request is no longer active.") || strings.Contains(body, ">Approve<") {
		t.Fatalf("card %d %s", code, body)
	}
	if len(ask.Calls()) != 0 {
		t.Fatalf("calls %#v", ask.Calls())
	}
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "inactive" {
		t.Fatalf("status %#v %v", row, err)
	}
	assertAudit(t, h, user.ID, "approval.inactive", "user", user.ID, 1)
	assertAudit(t, h, user.ID, "approval.approved", "", "", 0)
	page := h.poll(ip, "/agents/"+agent.ID, "This request is no longer active.")
	if strings.Contains(page, ">Approve<") || strings.Contains(page, ">Deny<") {
		t.Fatalf("page %s", page)
	}
}

func newAsk(t *testing.T, args map[string]any, now func() time.Time) (*harness, *runtime.Ask, store.User, store.Agent, string) {
	t.Helper()
	if args == nil {
		args = map[string]any{"command": "ls demo"}
	}
	ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: args})
	up := httptest.NewServer(ask)
	t.Cleanup(up.Close)
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	if now != nil {
		h.st.SetNow(now)
	}
	ip := "203.0.113.41:9"
	email := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")) + "@example.com"
	user, secret := h.account(email, "Demo User", "correct-horse-1")
	h.login(email, "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	return h, ask, user, agent, ip
}

func sendPrompt(t *testing.T, h *harness, agent store.Agent, ip, text string) {
	t.Helper()
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {text}}), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK || !strings.Contains(body, "Replying") {
		t.Fatalf("post %d %s", code, body)
	}
}

func waitPending(t *testing.T, h *harness, userID string) store.Approval {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		rows, err := h.st.ListPendingApprovals(context.Background(), userID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 1 {
			return rows[0]
		}
		if len(rows) > 1 {
			t.Fatalf("%d pending", len(rows))
		}
		if time.Now().After(deadline) {
			t.Fatal("no approval")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func postDecision(h *harness, ap store.Approval, ip string, allow bool) (int, string) {
	code, body, _ := h.do(http.MethodPost, decisionPath(ap, allow), h.form(url.Values{}), ip, nil)
	return code, body
}

func decisionPath(ap store.Approval, allow bool) string {
	action := "deny"
	if allow {
		action = "approve"
	}
	return "/runs/" + ap.RunID + "/approvals/" + ap.ID + "/" + action
}

func makeAgent(t *testing.T, st *store.Store, userID, name string) store.Agent {
	t.Helper()
	sec, err := st.CreateSection(context.Background(), userID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(context.Background(), userID, store.AgentInput{
		SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func agentThread(t *testing.T, h *harness, userID, agentID string) string {
	t.Helper()
	threads, err := h.st.ListThreadsByAgent(context.Background(), userID, agentID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	return threads[0].ID
}

func assertAudit(t *testing.T, h *harness, userID, action, kind, actor string, want int) {
	t.Helper()
	rows, err := h.st.ListAudit(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Action != action {
			continue
		}
		n++
		if kind != "" && (row.ActorKind != kind || row.ActorID != actor) {
			t.Fatalf("actor %#v", row)
		}
	}
	if n != want {
		t.Fatalf("%s count %d %#v", action, n, rows)
	}
}

func TestMismatchedRunDoesNotClaim(t *testing.T) {
	h, ask, user, agent, ip := newAsk(t, nil, nil)
	sendPrompt(t, h, agent, ip, "List the demo folder")
	ap := waitPending(t, h, user.ID)
	other, err := h.st.CreateRun(context.Background(), user.ID, agentThread(t, h, user.ID, agent.ID), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodPost, "/runs/"+other.ID+"/approvals/"+ap.ID+"/approve", h.form(url.Values{}), ip, nil)
	if code != http.StatusNotFound || strings.Contains(body, "ls demo") {
		t.Fatalf("mismatch %d %s", code, body)
	}
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "pending" || row.DecidedBy != "" {
		t.Fatalf("row %#v %v", row, err)
	}
	if len(ask.Calls()) != 0 {
		t.Fatalf("calls %#v", ask.Calls())
	}
	code, body = postDecision(h, ap, ip, true)
	if code != http.StatusOK || !strings.Contains(body, "Approved by you") || len(ask.Calls()) != 1 {
		t.Fatalf("real approve %d calls %#v %s", code, ask.Calls(), body)
	}
}

func TestCanceledClickStillDecides(t *testing.T) {
	t.Run("delivered", func(t *testing.T) {
		ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "ls demo"}})
		up := httptest.NewServer(&slowAsk{ask: ask, delay: 400 * time.Millisecond})
		t.Cleanup(up.Close)
		h := newHarnessWith(t, runtime.NewHTTP(up.URL))
		ip := "203.0.113.142:9"
		user, secret := h.account("cancel-late-s07b@example.com", "Demo User", "correct-horse-1")
		h.login("cancel-late-s07b@example.com", "correct-horse-1", secret, ip, false)
		agent := h.newAgent(ip, "Atlas")
		live := httptest.NewServer(h.srv.Handler())
		t.Cleanup(live.Close)
		sendPrompt(t, h, agent, ip, "go")
		ap := waitPending(t, h, user.ID)
		req, err := http.NewRequest(http.MethodPost, live.URL+decisionPath(ap, true), strings.NewReader(url.Values{"csrf": {h.csrf}}.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range h.jar {
			req.AddCookie(c)
		}
		client := &http.Client{Timeout: 150 * time.Millisecond}
		if _, err := client.Do(req); err == nil {
			t.Fatal("client should give up while the agent is slow")
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && len(ask.Calls()) == 0 {
			time.Sleep(20 * time.Millisecond)
		}
		if calls := ask.Calls(); len(calls) != 1 || !calls[0] {
			t.Fatalf("calls %#v", calls)
		}
		// Sent and the audit row are written after the respond call returns.
		deadline = time.Now().Add(3 * time.Second)
		var row store.Approval
		for {
			row, err = h.st.GetApproval(context.Background(), user.ID, ap.ID)
			audits, aerr := h.st.ListAudit(context.Background(), user.ID)
			n := 0
			if aerr == nil {
				for _, a := range audits {
					if a.Action == "approval.approved" {
						n++
					}
				}
			}
			if err == nil && aerr == nil && row.Status == "approved" && row.Sent && n == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("row %#v audits %d err %v %v", row, n, err, aerr)
			}
			time.Sleep(20 * time.Millisecond)
		}
		assertAudit(t, h, user.ID, "approval.approved", "user", user.ID, 1)
		runRow, err := h.st.GetRun(context.Background(), user.ID, ap.RunID)
		if err != nil || runRow.Status == "waiting_approval" {
			t.Fatalf("run %#v %v", runRow, err)
		}
	})
	t.Run("never delivered", func(t *testing.T) {
		ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "ls demo"}})
		up := httptest.NewServer(&failAsk{ask: ask})
		t.Cleanup(up.Close)
		h := newHarnessWith(t, runtime.NewHTTP(up.URL))
		var off atomic.Int64
		h.st.SetNow(func() time.Time { return time.Now().Add(time.Duration(off.Load())) })
		ip := "203.0.113.143:9"
		user, secret := h.account("cancel-miss-s07b@example.com", "Demo User", "correct-horse-1")
		h.login("cancel-miss-s07b@example.com", "correct-horse-1", secret, ip, false)
		agent := h.newAgent(ip, "Atlas")
		sendPrompt(t, h, agent, ip, "go")
		ap := waitPending(t, h, user.ID)
		code, body := postDecision(h, ap, ip, true)
		if code != http.StatusBadGateway {
			t.Fatalf("post %d %s", code, body)
		}
		row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
		if err != nil || row.Status != "pending" {
			t.Fatalf("row %#v %v", row, err)
		}
		assertAudit(t, h, user.ID, "approval.approved", "", "", 0)
		if len(ask.Calls()) != 0 {
			t.Fatalf("calls %#v", ask.Calls())
		}
		off.Store(int64(11 * time.Minute))
		h.srv.ExpireDue(context.Background())
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && len(ask.Calls()) == 0 {
			h.srv.ExpireDue(context.Background())
			time.Sleep(20 * time.Millisecond)
		}
		if calls := ask.Calls(); len(calls) != 1 || calls[0] {
			t.Fatalf("deny %#v", calls)
		}
		row, err = h.st.GetApproval(context.Background(), user.ID, ap.ID)
		if err != nil || row.Status != "expired" || !row.Sent {
			t.Fatalf("expired %#v %v", row, err)
		}
	})
}

func TestWebJournalDoesNotDuplicateCard(t *testing.T) {
	h, _, user, agent, ip := newAsk(t, nil, nil)
	sendPrompt(t, h, agent, ip, "List the demo folder")
	ap := waitPending(t, h, user.ID)
	thread := agentThread(t, h, user.ID, agent.ID)
	if _, err := h.st.IngestJournal(context.Background(), user.ID, agent.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-10T22:12:17Z", SessionID: thread, Caller: "control_plane", Kind: "request", Text: "List the demo folder", MessageID: "msg-web-1", StreamID: ap.RunID},
		{Seq: 2, At: "2026-11-10T22:12:17Z", SessionID: "", Caller: "control_plane", Kind: "decision", RequestID: ap.RequestID, Decision: "approved"},
		{Seq: 3, At: "2026-11-10T22:12:17Z", SessionID: thread, Caller: "control_plane", Kind: "permission", Tool: "bash", RequestID: ap.RequestID},
		{Seq: 4, At: "2026-11-10T22:12:17Z", SessionID: thread, Caller: "control_plane", Kind: "reply", Text: "bash finished.", MessageID: "msg-web-1", StreamID: ap.RunID},
	}); err != nil {
		t.Fatal(err)
	}
	page := h.poll(ip, "/agents/"+agent.ID, ">Approve<")
	if strings.Count(page, `id="appr-`) != 1 || strings.Contains(page, "Answer it in the Cast CLI") {
		t.Fatalf("cards %d page %s", strings.Count(page, `class="appr`), page)
	}
	msgs, err := h.st.ListMessages(context.Background(), user.ID, thread)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Body, "Cast CLI") {
			t.Fatalf("web thread stored %q", m.Body)
		}
	}
}

func TestSecretArgNamesStayHidden(t *testing.T) {
	secrets := map[string]string{
		"privateKey": "hid-private", "accessKey": "hid-access", "sessionKey": "hid-session",
		"cookie": "hid-cookie", "passphrase": "hid-phrase", "pwd": "hid-pwd",
		"auth": "hid-auth", "pass": "hid-pass", "ssh_key": "hid-ssh", "sshKey": "hid-sshcam",
		"signing_key": "hid-sign", "encryption_key": "hid-enc", "master_key": "hid-master",
		"aws_key": "hid-aws", "stripe_key": "hid-stripe", "license_key": "hid-lic",
		"jwt": "hid-jwt", "pat": "hid-pat", "bearer": "hid-bearerkey",
		"basic_auth": "hid-basic", "auth_header": "hid-authh",
		"max_secret": "hid-maxsec", "secret_count": "hid-seccount",
		"note": "Bearer hid-token",
	}
	args := map[string]any{"command": "ls demo", "visible": "keep-me", "session": "work", "tokens": 512, "publicKey": "ssh-ed25519 AAAA demo", "ssh_public_key": "ssh-ed25519 AAAA demo", "token_count": 7}
	for k, v := range secrets {
		args[k] = v
	}
	h, _, user, agent, ip := newAsk(t, args, nil)
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	page := h.poll(ip, "/agents/"+agent.ID, ">Approve<")
	code, card, _ := h.do(http.MethodGet, "/approvals/"+ap.ID, "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	events, err := h.st.ListRunEvents(context.Background(), user.ID, ap.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var runEv strings.Builder
	for _, ev := range events {
		runEv.WriteString(ev.Payload)
	}
	audits, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	var aud strings.Builder
	for _, a := range audits {
		aud.WriteString(a.Detail)
	}
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	sseEvents, serr := readSSE(sctx, live.URL+"/runs/"+ap.RunID+"/events", h.cookieList(), "", func(batch []sseEvent) bool {
		for _, ev := range batch {
			if strings.Contains(ev.Data, "keep-me") {
				return true
			}
		}
		return false
	})
	cancel()
	if serr != nil {
		t.Fatal(serr)
	}
	var sse strings.Builder
	for _, ev := range sseEvents {
		sse.WriteString(ev.Data)
	}
	for name, body := range map[string]string{
		"page": page, "card": card, "sse": sse.String(), "run_events": runEv.String(),
		"arguments": ap.Arguments, "audit": aud.String(),
	} {
		for _, secret := range secrets {
			if strings.Contains(body, secret) {
				t.Fatalf("%s leaked %s", name, secret)
			}
		}
		if !strings.Contains(body, "keep-me") && name != "audit" {
			t.Fatalf("%s dropped a plain value %s", name, body)
		}
	}
	for _, plain := range []string{"work", "512", "ssh-ed25519 AAAA demo", "Bearer Hidden"} {
		if !strings.Contains(card, plain) {
			t.Fatalf("card hid %s: %s", plain, card)
		}
	}
}

func TestLongValueTruncatesOnRunes(t *testing.T) {
	long := strings.Repeat("é", 81)
	h, _, user, agent, ip := newAsk(t, map[string]any{"command": long}, nil)
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	_, card, _ := h.do(http.MethodGet, "/approvals/"+ap.ID, "", ip, nil)
	if !utf8.ValidString(card) {
		t.Fatal("card is not valid utf-8")
	}
	if !strings.Contains(card, strings.Repeat("é", 80)+"...") || strings.Contains(card, strings.Repeat("é", 81)+"...") {
		t.Fatalf("clip %s", card)
	}
}

func TestAbandonedApprovalIsDeniedWhenLive(t *testing.T) {
	h, ask, user, _, ip := newAsk(t, nil, nil)
	sendPrompt(t, h, mustAgent(t, h, user.ID), ip, "go")
	ap := waitPending(t, h, user.ID)
	if err := h.st.FailAbandoned(context.Background(), "The reply was interrupted."); err != nil {
		t.Fatal(err)
	}
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "inactive" || row.Sent {
		t.Fatalf("after restart %#v %v", row, err)
	}
	page := h.poll(ip, "/agents/"+row.AgentID, "no longer active")
	if strings.Contains(page, ">Approve<") {
		t.Fatal(page)
	}
	h.srv.ExpireDue(context.Background())
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(ask.Calls()) == 0 {
		h.srv.ExpireDue(context.Background())
		time.Sleep(20 * time.Millisecond)
	}
	if calls := ask.Calls(); len(calls) != 1 || calls[0] {
		t.Fatalf("deny %#v", calls)
	}
	row, err = h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || !row.Sent {
		t.Fatalf("sent %#v %v", row, err)
	}
}

func TestExpiryDoesNotWakePausedMachine(t *testing.T) {
	h, ask, user, agent, ip := newAsk(t, nil, nil)
	var off atomic.Int64
	h.st.SetNow(func() time.Time { return time.Now().Add(time.Duration(off.Load())) })
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s07b", "https://castbot.example")
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07b"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-paused", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
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
	fake.Machines["vm-paused"] = host.Machine{ID: "vm-paused", State: "paused", Endpoint: "https://vm.example"}
	fake.SetForce("paused")
	off.Store(int64(11 * time.Minute))
	h.srv.ExpireDue(context.Background())
	h.srv.ExpireDue(context.Background())
	calls, approvals := fake.Decisions()
	for _, call := range calls {
		if call == "wake" || call == "approve" {
			t.Fatalf("woke %v", calls)
		}
	}
	if len(approvals) != 0 || len(ask.Calls()) != 0 {
		t.Fatalf("posted approvals %v http %v", approvals, ask.Calls())
	}
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "expired" || row.Sent {
		t.Fatalf("row %#v %v", row, err)
	}
}

func TestRunLeavesWaitingAfterDecision(t *testing.T) {
	hub := newHoldHub()
	up := httptest.NewServer(hub)
	t.Cleanup(up.Close)
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	ip := "203.0.113.144:9"
	user, secret := h.account("stream-s07b@example.com", "Demo User", "correct-horse-1")
	h.login("stream-s07b@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	code, body := postDecision(h, ap, ip, true)
	if code != http.StatusOK || !strings.Contains(body, "Approved by you") || !strings.Contains(body, `hx-swap-oob="outerHTML"`) || !strings.Contains(body, `id="pending-bar"`) {
		t.Fatalf("card %d %s", code, body)
	}
	runRow, err := h.st.GetRun(context.Background(), user.ID, ap.RunID)
	if err != nil || runRow.Status != "streaming" {
		t.Fatalf("status %#v %v", runRow, err)
	}
	close(hub.release)
	h.waitRuns(user.ID, agentThread(t, h, user.ID, agent.ID))
	if calls := hub.calls(); len(calls) != 1 || !calls[0] {
		t.Fatalf("calls %#v", calls)
	}
}

func TestApprovalSurvivesCutProxy(t *testing.T) {
	hub := newHoldHub()
	up := httptest.NewServer(hub)
	t.Cleanup(up.Close)
	proxy := runtime.NewCutProxy(up.URL, 4*time.Second)
	px := httptest.NewServer(proxy)
	t.Cleanup(px.Close)
	h := newHarnessWith(t, runtime.NewHTTP(px.URL))
	ip := "203.0.113.145:9"
	user, secret := h.account("cut-s07b@example.com", "Demo User", "correct-horse-1")
	h.login("cut-s07b@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && proxy.Cuts() < 2 {
		time.Sleep(100 * time.Millisecond)
	}
	if proxy.Cuts() < 2 {
		t.Fatalf("cuts %d", proxy.Cuts())
	}
	rows, err := h.st.ListRunApprovals(context.Background(), user.ID, ap.RunID)
	if err != nil || len(rows) != 1 || rows[0].Status != "pending" {
		t.Fatalf("rows %#v %v", rows, err)
	}
	if len(hub.calls()) != 0 {
		t.Fatalf("responded during cuts %#v", hub.calls())
	}
	page := h.poll(ip, "/agents/"+agent.ID, ">Approve<")
	if !strings.Contains(page, ">Deny<") {
		t.Fatal(page)
	}
	code, body := postDecision(h, ap, ip, true)
	if code != http.StatusOK || !strings.Contains(body, "Approved by you") {
		t.Fatalf("approve %d %s", code, body)
	}
	close(hub.release)
	h.waitRuns(user.ID, agentThread(t, h, user.ID, agent.ID))
	runRow, err := h.st.GetRun(context.Background(), user.ID, ap.RunID)
	if err != nil || runRow.Status != "done" {
		t.Fatalf("run %#v %v", runRow, err)
	}
	if calls := hub.calls(); len(calls) != 1 || !calls[0] {
		t.Fatalf("calls %#v producers %d", calls, hub.starts.Load())
	}
	msgs, err := h.st.ListMessages(context.Background(), user.ID, runRow.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range msgs {
		n += strings.Count(m.Body, runtime.AskFinished)
	}
	if n != 1 {
		t.Fatalf("finished text %d", n)
	}
}

func TestDecisionStreamCarriesChrome(t *testing.T) {
	h, _, user, agent, ip := newAsk(t, nil, nil)
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	go func() {
		time.Sleep(150 * time.Millisecond)
		postDecision(h, ap, ip, true)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	events, err := readSSE(ctx, live.URL+"/runs/"+ap.RunID+"/events", h.cookieList(), "", func(batch []sseEvent) bool {
		for _, ev := range batch {
			if ev.Event == "done" && strings.Contains(ev.Data, `id="pending-bar"`) && strings.Contains(ev.Data, "hidden") {
				return true
			}
		}
		return false
	})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, ev := range events {
		if ev.Event == "done" && strings.Contains(ev.Data, `id="nav-approvals"`) && strings.Contains(ev.Data, "hidden") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("done event missing chrome %#v", events)
	}
	_ = user
}

func TestRecoveryDoesNotApproveAFailedRun(t *testing.T) {
	h, ask, user, _, ip := newAsk(t, nil, nil)
	sendPrompt(t, h, mustAgent(t, h, user.ID), ip, "go")
	ap := waitPending(t, h, user.ID)
	if _, claimed, err := h.st.ClaimDecision(context.Background(), user.ID, ap.ID, ap.RunID, "approved", user.ID); err != nil || !claimed {
		t.Fatal(err)
	}
	if err := h.st.FailAbandoned(context.Background(), "The reply was interrupted."); err != nil {
		t.Fatal(err)
	}
	h.srv.recoverDecisions(context.Background(), 0)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(ask.Calls()) == 0 {
		h.srv.recoverDecisions(context.Background(), 0)
		time.Sleep(20 * time.Millisecond)
	}
	if calls := ask.Calls(); len(calls) != 1 || calls[0] {
		t.Fatalf("deny %#v", calls)
	}
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "inactive" || !row.Sent {
		t.Fatalf("row %#v %v", row, err)
	}
	assertAudit(t, h, user.ID, "approval.approved", "", "", 0)
	assertAudit(t, h, user.ID, "approval.inactive", "user", user.ID, 1)
}

func TestRecoveryLostRequestStaysInactive(t *testing.T) {
	ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "ls demo"}})
	up := httptest.NewServer(&loseAsk{ask: ask})
	t.Cleanup(up.Close)
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	ip := "203.0.113.146:9"
	user, secret := h.account("lost-s07b@example.com", "Demo User", "correct-horse-1")
	h.login("lost-s07b@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	if _, claimed, err := h.st.ClaimDecision(context.Background(), user.ID, ap.ID, ap.RunID, "approved", user.ID); err != nil || !claimed {
		t.Fatal(err)
	}
	h.srv.recoverDecisions(context.Background(), 0)
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "inactive" {
		t.Fatalf("row %#v %v", row, err)
	}
	assertAudit(t, h, user.ID, "approval.approved", "", "", 0)
	assertAudit(t, h, user.ID, "approval.inactive", "user", user.ID, 1)
	if len(ask.Calls()) != 0 {
		t.Fatalf("calls %#v", ask.Calls())
	}
}

func TestRecoveryJournalKeepsApprovedAudit(t *testing.T) {
	ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "ls demo"}})
	up := httptest.NewServer(&loseAsk{ask: ask})
	t.Cleanup(up.Close)
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	ip := "203.0.113.147:9"
	user, secret := h.account("journal-s07b@example.com", "Demo User", "correct-horse-1")
	h.login("journal-s07b@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	sendPrompt(t, h, agent, ip, "go")
	ap := waitPending(t, h, user.ID)
	if _, claimed, err := h.st.ClaimDecision(context.Background(), user.ID, ap.ID, ap.RunID, "approved", user.ID); err != nil || !claimed {
		t.Fatal(err)
	}
	if _, err := h.st.IngestJournal(context.Background(), user.ID, agent.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-10T22:12:17Z", SessionID: "", Caller: "control_plane", Kind: "decision", RequestID: ap.RequestID, Decision: "approved"},
	}); err != nil {
		t.Fatal(err)
	}
	h.srv.recoverDecisions(context.Background(), 0)
	row, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || row.Status != "approved" || !row.Sent {
		t.Fatalf("row %#v %v", row, err)
	}
	assertAudit(t, h, user.ID, "approval.approved", "user", user.ID, 1)
	assertAudit(t, h, user.ID, "approval.inactive", "", "", 0)
}

func TestGuestRestartDeferralIsLogged(t *testing.T) {
	h := newHarness(t)
	var buf bytes.Buffer
	h.srv.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	user, _ := h.account("defer-log-s07b@example.com", "Demo User", "correct-horse-1")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	secret := "demo-secret-value-should-not-log"
	sec, err := h.st.PutSecret(context.Background(), user.ID, "provider", secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	fp, err := h.srv.secretFingerprint(context.Background(), user.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSecretFP(context.Background(), user.ID, agent.ID, fp); err != nil {
		t.Fatal(err)
	}
	h.srv.noteGuestDefer(context.Background(), user.ID, agent.ID, "pending approval")
	if buf.Len() != 0 {
		t.Fatalf("logged a restart that was not pending %s", buf.String())
	}
	if err := h.st.SetSecretFP(context.Background(), user.ID, agent.ID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	h.srv.noteGuestDefer(context.Background(), user.ID, agent.ID, "pending approval")
	h.srv.noteGuestDefer(context.Background(), user.ID, agent.ID, "pending approval")
	h.srv.noteGuestDefer(context.Background(), user.ID, agent.ID, "bound:pending approval")
	h.srv.noteGuestDefer(context.Background(), user.ID, agent.ID, "bound:pending approval")
	text := buf.String()
	if strings.Contains(text, secret) || strings.Contains(text, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatalf("log leaked a secret or a full fingerprint %s", text)
	}
	if strings.Count(text, "guest_restart_deferred") != 1 || strings.Count(text, "guest_restart_forced") != 1 || !strings.Contains(text, "pending approval") || !strings.Contains(text, "old_fp=aaaaaaaa") {
		t.Fatalf("log %s", text)
	}
}

type slowAsk struct {
	ask   *runtime.Ask
	delay time.Duration
}

func (s *slowAsk) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	if strings.Contains(string(body), "permission/respond") {
		time.Sleep(s.delay)
	}
	s.ask.ServeHTTP(w, r)
}

type loseAsk struct {
	ask *runtime.Ask
}

func (l *loseAsk) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), "permission/respond") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","error":{"code":-32602,"message":"No pending permission request"}}`)
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	l.ask.ServeHTTP(w, r)
}

type failAsk struct {
	ask  *runtime.Ask
	once atomic.Bool
}

func (f *failAsk) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if strings.Contains(string(body), "permission/respond") && f.once.CompareAndSwap(false, true) {
		http.Error(w, "down", http.StatusBadGateway)
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	f.ask.ServeHTTP(w, r)
}

type holdHub struct {
	mu      sync.Mutex
	runs    map[string]*holdRun
	respond []bool
	release chan struct{}
	starts  atomic.Int32
}

type holdRun struct {
	events  []string
	done    bool
	reqID   string
	answer  chan bool
	changed chan struct{}
}

func newHoldHub() *holdHub {
	return &holdHub{runs: map[string]*holdRun{}, release: make(chan struct{})}
}

func (z *holdHub) calls() []bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	out := append([]bool(nil), z.respond...)
	return out
}

func (z *holdHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
		Params struct {
			ID       string `json:"id"`
			Approved *bool  `json:"approved"`
			Metadata struct {
				StreamID     string `json:"streamId"`
				StreamOffset int    `json:"streamOffset"`
			} `json:"metadata"`
		} `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Method == "permission/respond" {
		z.mu.Lock()
		var hit *holdRun
		for _, run := range z.runs {
			if run.reqID == req.Params.ID && run.answer != nil {
				hit = run
			}
		}
		if hit != nil && req.Params.Approved != nil {
			z.respond = append(z.respond, *req.Params.Approved)
			hit.answer <- *req.Params.Approved
			hit.answer = nil
		}
		z.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if hit == nil {
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","error":{"code":-32602,"message":"No pending permission request"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`)
		return
	}
	sid := req.Params.Metadata.StreamID
	z.mu.Lock()
	run := z.runs[sid]
	if run == nil {
		run = &holdRun{reqID: fmt.Sprintf("hold-%d", time.Now().UnixNano()), answer: make(chan bool, 1), changed: make(chan struct{})}
		z.runs[sid] = run
		z.starts.Add(1)
		go z.produce(run)
	}
	z.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	_, _ = io.WriteString(w, ": cast-stream "+sid+"\n\n")
	if fl != nil {
		fl.Flush()
	}
	idx := req.Params.Metadata.StreamOffset
	for {
		z.mu.Lock()
		if idx < len(run.events) {
			ev := run.events[idx]
			idx++
			z.mu.Unlock()
			if _, err := io.WriteString(w, ev); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
			continue
		}
		if run.done {
			z.mu.Unlock()
			return
		}
		ch := run.changed
		z.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(time.Second):
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if fl != nil {
				fl.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (z *holdHub) push(run *holdRun, ev string, done bool) {
	z.mu.Lock()
	run.events = append(run.events, ev)
	run.done = done
	close(run.changed)
	run.changed = make(chan struct{})
	z.mu.Unlock()
}

func (z *holdHub) produce(run *holdRun) {
	z.mu.Lock()
	reqID := run.reqID
	answer := run.answer
	z.mu.Unlock()
	perm, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "result": map[string]any{"parts": []any{}, "metadata": map[string]any{"permissionRequest": map[string]any{"id": reqID, "tool": "bash", "arguments": map[string]any{"command": "ls demo"}}}}})
	z.push(run, "data: "+string(perm)+"\n\n", false)
	allow := <-answer
	<-z.release
	text := runtime.DenialMessage("bash")
	if allow {
		text = runtime.AskFinished
	}
	frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "result": map[string]any{"parts": []map[string]string{{"kind": "text", "text": text}}}})
	z.push(run, "data: "+string(frame)+"\n\n", false)
	z.push(run, "data: [DONE]\n\n", true)
}

func mustAgent(t *testing.T, h *harness, userID string) store.Agent {
	t.Helper()
	rows, err := h.st.ListAgents(context.Background(), userID)
	if err != nil || len(rows) == 0 {
		t.Fatal(err)
	}
	return rows[0]
}

func assertNoSecret(t *testing.T, h *harness, userID string) {
	t.Helper()
	rows, err := h.st.ListAudit(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Detail, demoSecret) {
			t.Fatalf("audit leaked %s", row.Detail)
		}
	}
}
