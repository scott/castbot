package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		{Seq: 2, At: "2026-11-10T18:00:01Z", SessionID: "sess-s07b", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "permission", Tool: "bash"},
		{Seq: 3, At: "2026-11-10T18:00:02Z", SessionID: "sess-s07b", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "decision", Tool: "bash", Decision: "approved"},
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
