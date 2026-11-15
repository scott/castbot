package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestAlwaysApproveCoversThePrompt(t *testing.T) {
	secret := "SECRETTKN-96"
	ask := newSeqAsk(secret)
	up := httptest.NewServer(ask)
	t.Cleanup(up.Close)
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	h.srv.pauseSweeps()
	ip := "203.0.113.96:9"
	user, pass := h.account("always@example.com", "Demo User", "correct-horse-1")
	h.login("always@example.com", "correct-horse-1", pass, ip, false)
	agent := h.newAgent(ip, "Atlas")
	sendPrompt(t, h, agent, ip, "Draft the notes")
	first := waitPending(t, h, user.ID)
	page := h.poll(ip, "/agents/"+agent.ID+"?panel=approvals", "Always approve")
	if !strings.Contains(page, ">Approve<") || !strings.Contains(page, ">Deny<") || !strings.Contains(page, ">Always approve<") {
		t.Fatalf("pane %s", page)
	}
	// The chat and the Approvals pane each render the card, so Deny appears twice.
	// Deny stays the only danger button.
	if n := strings.Count(page, "btn danger"); n < 1 || n != strings.Count(page, ">Deny<") {
		t.Fatalf("danger %d %s", n, page)
	}
	code, body, _ := h.do(http.MethodPost, alwaysPath(first), "csrf=nope", ip, nil)
	if code != http.StatusForbidden {
		t.Fatalf("csrf %d %s", code, body)
	}
	code, body = postAlways(h, first, ip)
	if code != http.StatusOK || !strings.Contains(body, store.GrantLine) || strings.Contains(body, ">Always approve<") {
		t.Fatalf("card %d %s", code, body)
	}
	deadline := time.Now().Add(8 * time.Second)
	var rows []store.Approval
	for {
		var err error
		rows, err = h.st.ListAgentApprovals(context.Background(), user.ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) >= 2 && rows[0].Status == "approved" && rows[1].Status == "approved" && rows[0].Via == store.GrantVia && rows[1].Via == store.GrantVia {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("later card %#v", rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, row := range rows {
		if row.Via != store.GrantVia || row.Status != "approved" {
			t.Fatalf("row %#v", row)
		}
	}
	assertGrantAudits(t, h, user.ID, 2, secret)
	h.waitRuns(user.ID, agentThread(t, h, user.ID, agent.ID))
	msgID, err := h.st.PromptMessageID(context.Background(), user.ID, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// The run is marked done before the after-run hook closes the grant.
	deadline = time.Now().Add(2 * time.Second)
	var active bool
	for {
		active, err = h.st.PromptGrantActive(context.Background(), user.ID, msgID)
		if err != nil || !active {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || active {
		t.Fatalf("grant stayed open %v %v", active, err)
	}
	sendPrompt(t, h, agent, ip, "A later prompt")
	next := waitPending(t, h, user.ID)
	if next.RunID == first.RunID || next.Status != "pending" {
		t.Fatalf("next %#v", next)
	}
	time.Sleep(150 * time.Millisecond)
	still, err := h.st.GetApproval(context.Background(), user.ID, next.ID)
	if err != nil || still.Status != "pending" {
		t.Fatalf("next decided %#v %v", still, err)
	}
	again, err := h.st.PromptGrantActive(context.Background(), user.ID, msgID)
	if err != nil || again {
		t.Fatalf("old grant %v %v", again, err)
	}
	if _, err := h.st.GetPromptGrant(context.Background(), user.ID, still.ID); err == nil {
		t.Fatal("grant keyed on the approval")
	}
}

func TestAlwaysApproveSweepDenyAndScope(t *testing.T) {
	h := newPermitHarness(t)
	h.srv.pauseSweeps()
	ctx := context.Background()
	ip := "203.0.113.97:9"
	user, pass := h.account("sweep@example.com", "Demo User", "correct-horse-1")
	h.login("sweep@example.com", "correct-horse-1", pass, ip, false)
	chief := makeAgent(t, h.st, user.ID, "Atlas")
	spec := makeAgent(t, h.st, user.ID, "Nova")
	threads, err := h.st.ListThreadsByAgent(ctx, user.ID, chief.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	thread := threads[0]
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Draft the notes")
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, thread.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, msg.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	job, err := h.st.EnsureAgentJob(ctx, user.ID, thread.ID, msg.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := h.st.CreateBatch(ctx, user.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	specThreads, err := h.st.ListThreadsByAgent(ctx, user.ID, spec.ID)
	if err != nil || len(specThreads) == 0 {
		t.Fatal(err)
	}
	specRun, err := h.st.CreateRun(ctx, user.ID, specThreads[0].ID, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AddDispatch(ctx, user.ID, batch.ID, specThreads[0].ID, spec.ID, specRun.ID, "running", "Look up the notes", ""); err != nil {
		t.Fatal(err)
	}
	otherThread, err := h.st.CreateThread(ctx, user.ID, "dm", chief.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	otherMsg, err := h.st.CreateMessage(ctx, user.ID, otherThread.ID, "user", user.ID, "A different prompt")
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := h.st.CreateRun(ctx, user.ID, otherThread.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, otherMsg.ID, otherRun.ID); err != nil {
		t.Fatal(err)
	}
	secret := "SECRETTKN-96"
	clicked := mustPermission(t, h.st, user.ID, runRow.ID, "req-click", "bash", secret)
	waiting := mustPermission(t, h.st, user.ID, runRow.ID, "req-wait", "git", secret)
	special := mustPermission(t, h.st, user.ID, specRun.ID, "req-spec", "bash", secret)
	elsewhere := mustPermission(t, h.st, user.ID, otherRun.ID, "req-other", "bash", secret)
	hash := strings.Repeat("ab", 32)
	mine, err := h.st.CreateConnectApproval(ctx, user.ID, chief.ID, thread.ID, runRow.ID, connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/demo/issues", hash, h.st.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	hash2 := strings.Repeat("cd", 32)
	otherCard, err := h.st.CreateConnectApproval(ctx, user.ID, chief.ID, otherThread.ID, otherRun.ID, connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/other", hash2, h.st.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	hash3 := strings.Repeat("ef", 32)
	loose, err := h.st.CreateConnectApproval(ctx, user.ID, chief.ID, thread.ID, "", connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/loose", hash3, h.st.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	secretRow, _, err := h.st.CreateSecretRequest(ctx, user.ID, chief.ID, thread.ID, runRow.ID, connect.GitHub, "Need a token", h.st.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveConnectionLevels(ctx, user.ID, connect.GitHub, map[string]string{"create_issue": "ask"}); err != nil {
		t.Fatal(err)
	}
	levelsBefore, err := h.st.ConnectionLevels(ctx, user.ID, connect.GitHub)
	if err != nil {
		t.Fatal(err)
	}

	denyBody := h.poll(ip, "/agents/"+chief.ID+"?panel=approvals", "Always approve")
	if strings.Count(denyBody, ">Always approve<") < 2 || !strings.Contains(denyBody, "POST /repos/octo/demo/issues") {
		t.Fatalf("pane cards %s", denyBody)
	}
	code, body := postAlways(h, clicked, ip)
	if code != http.StatusOK || !strings.Contains(body, store.GrantLine) || strings.Contains(body, "When Nova needs") || !strings.Contains(body, "Waiting on you") {
		t.Fatalf("always %d %s", code, body)
	}
	assertApprovedVia(t, h, user.ID, clicked.ID)
	assertApprovedVia(t, h, user.ID, waiting.ID)
	assertApprovedVia(t, h, user.ID, special.ID)
	if row, err := h.st.GetApproval(ctx, user.ID, elsewhere.ID); err != nil || row.Status != "pending" {
		t.Fatalf("other thread %#v %v", row, err)
	}
	if row, err := h.st.GetConnectApproval(ctx, user.ID, mine.ID); err != nil || row.Status != "approved" || row.Via != store.GrantVia {
		t.Fatalf("connect %#v %v", row, err)
	}
	if row, err := h.st.GetConnectApproval(ctx, user.ID, otherCard.ID); err != nil || row.Status != "pending" {
		t.Fatalf("other connect %#v %v", row, err)
	}
	if row, err := h.st.GetConnectApproval(ctx, user.ID, loose.ID); err != nil || row.Status != "pending" {
		t.Fatalf("loose %#v %v", row, err)
	}
	if row, err := h.st.GetSecretRequest(ctx, user.ID, secretRow.ID); err != nil || row.Status != "pending" {
		t.Fatalf("secret %#v %v", row, err)
	}
	levelsAfter, err := h.st.ConnectionLevels(ctx, user.ID, connect.GitHub)
	if err != nil || levelsAfter["create_issue"] != levelsBefore["create_issue"] || levelsAfter["create_issue"] != "ask" {
		t.Fatalf("levels %#v %#v %v", levelsBefore, levelsAfter, err)
	}
	assertGrantAudits(t, h, user.ID, 4, secret)

	page := h.poll(ip, "/agents/"+chief.ID, "Need a token")
	secretAt := strings.Index(page, `id="secret-`)
	if secretAt < 0 {
		t.Fatalf("secret card %s", page)
	}
	secretHTML := page[secretAt:]
	if cut := strings.Index(secretHTML, `class="appr`); cut > 0 {
		secretHTML = secretHTML[:cut]
	}
	if strings.Contains(secretHTML, "Always approve") || strings.Contains(secretHTML, "/always") {
		t.Fatalf("secret gained a button %s", secretHTML)
	}

	// A Deny that wins after the grant stays denied. The grant stays for the others.
	raced := mustPermission(t, h.st, user.ID, runRow.ID, "req-deny", "bash", secret)
	code, body = postDecision(h, raced, ip, false)
	// The pane still lists cards already approved for this prompt.
	// The denied card itself does not.
	head := body
	if i := strings.Index(body, `id="pending-bar"`); i > 0 {
		head = body[:i]
	}
	if code != http.StatusOK || strings.Contains(head, store.GrantLine) {
		t.Fatalf("deny %d %s", code, body)
	}
	if row, err := h.st.GetApproval(ctx, user.ID, raced.ID); err != nil || row.Status != "denied" {
		t.Fatalf("denied %#v %v", row, err)
	}
	active, err := h.st.PromptGrantActive(ctx, user.ID, msg.ID)
	if err != nil || !active {
		t.Fatalf("grant %v %v", active, err)
	}
	later := mustPermission(t, h.st, user.ID, specRun.ID, "req-later", "git", secret)
	h.srv.grantPermission(run.Job{UserID: user.ID, AgentID: spec.ID, RunID: specRun.ID}, later)
	assertApprovedVia(t, h, user.ID, later.ID)
	if row, err := h.st.GetApproval(ctx, user.ID, raced.ID); err != nil || row.Status != "denied" {
		t.Fatalf("deny flipped %#v %v", row, err)
	}

	if _, _, err := h.st.FinishRun(ctx, user.ID, runRow.ID, "Chief done", 2); err != nil {
		t.Fatal(err)
	}
	h.srv.settleGrant(run.Job{UserID: user.ID, RunID: runRow.ID}, "done")
	if active, err := h.st.PromptGrantActive(ctx, user.ID, msg.ID); err != nil || !active {
		t.Fatalf("ended while specialist is open %v %v", active, err)
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, specRun.ID, "Specialist done", 2); err != nil {
		t.Fatal(err)
	}
	h.srv.settleGrant(run.Job{UserID: user.ID, RunID: specRun.ID}, "done")
	if active, err := h.st.PromptGrantActive(ctx, user.ID, msg.ID); err != nil || active {
		t.Fatalf("grant after both finished %v %v", active, err)
	}
	retryRun, err := h.st.CreateRun(ctx, user.ID, thread.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, msg.ID, retryRun.ID); err != nil {
		t.Fatal(err)
	}
	retry := mustPermission(t, h.st, user.ID, retryRun.ID, "req-retry", "bash", secret)
	h.srv.grantPermission(run.Job{UserID: user.ID, AgentID: chief.ID, RunID: retryRun.ID}, retry)
	if row, err := h.st.GetApproval(ctx, user.ID, retry.ID); err != nil || row.Status != "pending" {
		t.Fatalf("retry %#v %v", row, err)
	}
}

func TestAlwaysApproveOneDecisionAndOtherUser(t *testing.T) {
	h, ask, user, agent, ip := newAsk(t, map[string]any{"command": "ls", "note": "SECRETTKN-96"}, nil)
	h.srv.pauseSweeps()
	sendPrompt(t, h, agent, ip, "Draft the notes")
	ap := waitPending(t, h, user.ID)
	form := h.form(url.Values{})
	path := alwaysPath(ap)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var codes [2]int
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i], _, _ = h.do(http.MethodPost, path, form, ip, nil)
		}(i)
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, code := range codes {
		if code == http.StatusOK {
			ok++
		}
	}
	if ok != 2 {
		t.Fatalf("codes %v", codes)
	}
	if calls := ask.Calls(); len(calls) != 1 || !calls[0] {
		t.Fatalf("calls %#v", calls)
	}
	assertGrantAudits(t, h, user.ID, 1, "SECRETTKN-96")
	msgID, err := h.st.PromptMessageID(context.Background(), user.ID, ap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetPromptGrant(context.Background(), user.ID, msgID); err != nil {
		t.Fatal(err)
	}
	code, body := postAlways(h, ap, ip)
	if code != http.StatusOK || strings.Count(body, store.GrantNote) > 0 && strings.Count(body, ">Always approve<") > 0 {
		t.Fatalf("second click %d %s", code, body)
	}
	assertGrantAudits(t, h, user.ID, 1, "SECRETTKN-96")

	h2, _, user2, agent2, ip2 := newAsk(t, nil, nil)
	sendPrompt(t, h2, agent2, ip2, "Draft the notes")
	ap2 := waitPending(t, h2, user2.ID)
	_, secret := h2.account("always-other@example.com", "Bea", "correct-horse-1")
	h2.jar = map[string]*http.Cookie{}
	h2.csrf = ""
	h2.login("always-other@example.com", "correct-horse-1", secret, "203.0.113.98:9", false)
	code, body, _ = h2.do(http.MethodPost, alwaysPath(ap2), h2.form(url.Values{}), "203.0.113.98:9", nil)
	if code != http.StatusNotFound {
		t.Fatalf("other %d %s", code, body)
	}
	if row, err := h2.st.GetApproval(context.Background(), user2.ID, ap2.ID); err != nil || row.Status != "pending" {
		t.Fatalf("still pending %#v %v", row, err)
	}
	if _, err := h2.st.GetPromptGrant(context.Background(), user2.ID, "missing"); err == nil {
		t.Fatal("other grant")
	}
}

func TestDenyDoesNotGrant(t *testing.T) {
	h, _, user, agent, ip := newAsk(t, nil, nil)
	sendPrompt(t, h, agent, ip, "Draft the notes")
	ap := waitPending(t, h, user.ID)
	code, body := postDecision(h, ap, ip, false)
	if code != http.StatusOK || strings.Contains(body, "Always approve") {
		t.Fatalf("deny %d %s", code, body)
	}
	msgID, err := h.st.PromptMessageID(context.Background(), user.ID, ap.RunID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := h.st.PromptGrantActive(context.Background(), user.ID, msgID)
	if err != nil || active {
		t.Fatalf("grant %v %v", active, err)
	}
	if _, err := h.st.GetPromptGrant(context.Background(), user.ID, msgID); err == nil {
		t.Fatal("row exists")
	}
}

func TestCLIAndSecretCardsHaveNoAlwaysApprove(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("cli-grant@example.com", "Demo User", "correct-horse-1")
	ip := "203.0.113.99:9"
	h.login("cli-grant@example.com", "correct-horse-1", secret, ip, false)
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.IngestJournal(context.Background(), user.ID, agent.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-10T18:00:00Z", SessionID: "sess-grant", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "request", Text: "from the cli"},
		{Seq: 2, At: "2026-11-10T18:00:01Z", SessionID: "", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "decision", RequestID: "req-grant-cli", Decision: "approved"},
		{Seq: 3, At: "2026-11-10T18:00:02Z", SessionID: "sess-grant", Caller: "cli:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: "permission", Tool: "bash", RequestID: "req-grant-cli"},
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
	page := h.poll(ip, "/agents/"+agent.ID+"?thread="+url.QueryEscape(thread), "Answer it in the Cast CLI")
	if strings.Contains(page, "Always approve") || strings.Contains(page, ">Approve<") || strings.Contains(page, ">Deny<") {
		t.Fatalf("cli card %s", page)
	}
}

func TestStopEndsThePromptGrant(t *testing.T) {
	h := newPermitHarness(t)
	h.srv.pauseSweeps()
	ctx := context.Background()
	ip := "203.0.113.100:9"
	user, pass := h.account("grant-stop@example.com", "Demo User", "correct-horse-1")
	h.login("grant-stop@example.com", "correct-horse-1", pass, ip, false)
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	threads, err := h.st.ListThreadsByAgent(ctx, user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	thread := threads[0]
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Draft the notes")
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, msg.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	ap := mustPermission(t, h.st, user.ID, runRow.ID, "req-stop", "bash", "SECRETTKN-96")
	if code, body := postAlways(h, ap, ip); code != http.StatusOK || !strings.Contains(body, store.GrantLine) {
		t.Fatalf("always %d %s", code, body)
	}
	fake := pauseMachine(t, h, user.ID, agent.ID, "running")
	fake.CancelOK = true
	code, _, _ := h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK {
		t.Fatalf("stop %d", code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		active, err := h.st.PromptGrantActive(ctx, user.ID, msg.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grant stayed open after stop")
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func postAlways(h *harness, ap store.Approval, ip string) (int, string) {
	code, body, _ := h.do(http.MethodPost, alwaysPath(ap), h.form(url.Values{}), ip, nil)
	return code, body
}

func alwaysPath(ap store.Approval) string {
	return "/runs/" + ap.RunID + "/approvals/" + ap.ID + "/always"
}

func mustPermission(t *testing.T, st *store.Store, userID, runID, requestID, tool, secret string) store.Approval {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": requestID, "tool": tool,
		"arguments": map[string]any{"command": "ls", "note": secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := st.RecordPermission(context.Background(), userID, runID, raw, st.Now().Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func assertApprovedVia(t *testing.T, h *harness, userID, id string) {
	t.Helper()
	row, err := h.st.GetApproval(context.Background(), userID, id)
	if err != nil || row.Status != "approved" || row.Via != store.GrantVia {
		t.Fatalf("approved %#v %v", row, err)
	}
}

func assertGrantAudits(t *testing.T, h *harness, userID string, want int, secret string) {
	t.Helper()
	rows, err := h.st.ListAudit(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Action != "approval.approved" && row.Action != "connection.approved" {
			continue
		}
		if !strings.Contains(row.Detail, store.GrantNote) {
			continue
		}
		n++
		if secret != "" && strings.Contains(row.Detail, secret) {
			t.Fatalf("secret in audit %s", row.Detail)
		}
	}
	if n != want {
		t.Fatalf("grant audits %d %#v", n, rows)
	}
}

func newPermitHarness(t *testing.T) *harness {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`)
	}))
	t.Cleanup(up.Close)
	return newHarnessWith(t, runtime.NewHTTP(up.URL))
}

// seqAsk sends two permissions. The second arrives only after the first is approved.
type seqAsk struct {
	secret string
	mu     sync.Mutex
	wait   map[string]chan bool
	calls  []bool
}

func newSeqAsk(secret string) *seqAsk {
	return &seqAsk{secret: secret, wait: map[string]chan bool{}}
}

func (a *seqAsk) Calls() []bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]bool(nil), a.calls...)
}

func (a *seqAsk) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var req map[string]any
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req["method"] == "permission/respond" {
		a.respond(w, req)
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	a.stream(w, r, req)
}

func (a *seqAsk) stream(w http.ResponseWriter, r *http.Request, req map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	id, _ := req["id"].(string)
	if id == "" {
		id = "1"
	}
	bash := a.arm("perm-bash")
	if !a.writePermission(w, id, "perm-bash", "bash") {
		return
	}
	flush()
	if !a.waitCh(r.Context(), bash) {
		a.writeDone(w, id, "Error: Permission denied for bash.")
		flush()
		return
	}
	git := a.arm("perm-git")
	if !a.writePermission(w, id, "perm-git", "git") {
		return
	}
	flush()
	if !a.waitCh(r.Context(), git) {
		a.writeDone(w, id, "Error: Permission denied for git.")
		flush()
		return
	}
	a.writeDone(w, id, "both clear")
	flush()
}

func (a *seqAsk) arm(id string) chan bool {
	ch := make(chan bool, 1)
	a.mu.Lock()
	a.wait[id] = ch
	a.mu.Unlock()
	return ch
}

func (a *seqAsk) writePermission(w http.ResponseWriter, rpcID, requestID, tool string) bool {
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      rpcID,
		"result": map[string]any{
			"parts": []any{},
			"metadata": map[string]any{
				"permissionRequest": map[string]any{
					"id":   requestID,
					"tool": tool,
					"arguments": map[string]any{
						"command": "ls",
						"note":    a.secret,
					},
				},
			},
		},
	})
	_, err := io.WriteString(w, "data: "+string(payload)+"\n\n")
	return err == nil
}

func (a *seqAsk) waitCh(ctx context.Context, ch chan bool) bool {
	select {
	case allow := <-ch:
		return allow
	case <-ctx.Done():
		return false
	}
}

func (a *seqAsk) writeDone(w http.ResponseWriter, rpcID, text string) {
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      rpcID,
		"result": map[string]any{
			"parts": []map[string]string{{"kind": "text", "text": text}},
		},
	})
	_, _ = io.WriteString(w, "data: "+string(frame)+"\n\n")
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func (a *seqAsk) respond(w http.ResponseWriter, req map[string]any) {
	params, _ := req["params"].(map[string]any)
	id, _ := params["id"].(string)
	approved, ok := params["approved"].(bool)
	a.mu.Lock()
	ch := a.wait[id]
	if ch != nil && ok {
		delete(a.wait, id)
		a.calls = append(a.calls, approved)
	}
	a.mu.Unlock()
	if ch == nil || !ok {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","error":{"code":-32602,"message":"No pending permission request"}}`)
		return
	}
	ch <- approved
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`)
}
