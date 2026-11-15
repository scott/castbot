package web

import (
	"bytes"
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

func stopUser(t *testing.T, email string) (*harness, store.User, store.Agent, store.Thread, string) {
	t.Helper()
	h := newHarness(t)
	h.srv.pauseSweeps()
	ip := "203.0.113.50:9"
	user, secret := h.account(email, "Ada Lovelace", "correct-horse-1")
	h.login(email, "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	return h, user, agent, thread, ip
}

func TestStopButtonOnWorkingRow(t *testing.T) {
	h, user, agent, thread, ip := stopUser(t, "stop-row@example.com")
	ctx := context.Background()
	runRow, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(ctx, user.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{"text": "Half a reply"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendRunEvent(ctx, user.ID, runRow.ID, "text", string(payload), 1); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?thread="+url.QueryEscape(thread.ID), "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	action := `action="/runs/` + runRow.ID + `/stop"`
	if !strings.Contains(body, action) || !strings.Contains(body, ">Stop<") || !strings.Contains(body, ">Working<") {
		t.Fatalf("stop row missing\n%s", body)
	}
	if strings.Contains(body, `id="tail-`+runRow.ID+`" hidden`) {
		t.Fatal("working row hidden")
	}
	if !strings.Contains(body, `</span><form class="stop"`) {
		t.Fatal("stop form is inside the label")
	}
	comp := strings.Index(body, `id="composer"`)
	stop := strings.Index(body, action)
	if comp < 0 || stop < 0 || stop > comp {
		t.Fatal("stop form is inside the composer")
	}
	tok := tokenFrom(body)
	form := body[stop:]
	if tok == "" || !strings.Contains(form[:strings.Index(form, "</form>")], `value="`+tok+`"`) {
		t.Fatal("csrf")
	}
}

func TestStopButtonStaysMountedWhileWorking(t *testing.T) {
	row := string(actRowHTML(activityOut{Label: "Working", HideThread: true}, "", "run1", "tok"))
	if !strings.Contains(row, ">Stop<") || !strings.Contains(row, `value="tok"`) || !strings.Contains(row, `</span><form class="stop"`) {
		t.Fatal(row)
	}
	slot := actSlotHTML("run1", template.HTML(row), actHidden(activityOut{Label: "Working", HideThread: true}, "run1"))
	if strings.Contains(slot, " hidden") {
		t.Fatal(slot)
	}
	st := &liveSend{run: store.Run{ID: "run1"}, stopID: "run1", csrf: "tok"}
	st.noteAct(activityOut{Label: "Thinking"}, "")
	var buf bytes.Buffer
	if err := st.putAct(nil, &buf, activityOut{Label: "Working", HideThread: true}, ""); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if strings.Contains(got, "event: act\n") || !strings.Contains(got, "event: actxt") || strings.Contains(got, "<form") {
		t.Fatalf("label swap %s", got)
	}
}

func TestStopQueuedRun(t *testing.T) {
	h, user, agent, thread, ip := stopUser(t, "stop-queued@example.com")
	ctx := context.Background()
	fake := pauseMachine(t, h, user.ID, agent.ID, "running")
	runRow, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"text": "Half a reply"})
	if _, err := h.st.AppendRunEvent(ctx, user.ID, runRow.ID, "text", string(payload), 1); err != nil {
		t.Fatal(err)
	}
	code, _, hdr := h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", h.form(nil), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "thread=") {
		t.Fatalf("redirect %d %s", code, hdr.Get("Location"))
	}
	got, err := h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil || got.Status != "stopped" {
		t.Fatalf("%s %v", got.Status, err)
	}
	if len(fake.CancelIDs) != 0 {
		t.Fatalf("cancel %v", fake.CancelIDs)
	}
	code, body, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Half a reply") || !strings.Contains(body, store.StoppedLine) {
		t.Fatalf("thread %d", code)
	}
	if strings.Contains(body, `/runs/`+runRow.ID+`/retry`) || strings.Contains(body, `action="/runs/`+runRow.ID+`/stop"`) {
		t.Fatal("retry or stop still offered")
	}
	code, _, _ = h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	msgs, err := h.st.ListMessages(ctx, user.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	notes := 0
	for _, msg := range msgs {
		if msg.Body == store.StoppedLine {
			notes++
		}
	}
	if notes != 1 || got.Status != "stopped" {
		t.Fatalf("notes %d status %s", notes, got.Status)
	}
	fresh, err := h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil || fresh.Status != "stopped" {
		t.Fatal(fresh.Status)
	}
}

func TestStopStreamsAfterCancel(t *testing.T) {
	h, user, agent, thread, ip := stopUser(t, "stop-live@example.com")
	ctx := context.Background()
	fake := pauseMachine(t, h, user.ID, agent.ID, "running")
	fake.CancelOK = true
	runRow, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(ctx, user.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"text": "Half a reply"})
	if _, err := h.st.AppendRunEvent(ctx, user.ID, runRow.ID, "text", string(payload), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.RecordPermission(ctx, user.ID, runRow.ID, json.RawMessage(`{"id":"req-stop","tool":"bash","arguments":{"command":"ls"}}`), h.st.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	card, err := h.st.CreateConnectApproval(ctx, user.ID, agent.ID, thread.ID, runRow.ID, connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/demo/issues", hash, h.st.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := h.st.DecideConnectApproval(ctx, user.ID, card.ID, "approved"); err != nil || !ok {
		t.Fatal("approve")
	}
	code, body, _ := h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", h.form(nil), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK || !strings.Contains(body, "Half a reply") || !strings.Contains(body, store.StoppedLine) || strings.Contains(body, `/runs/`+runRow.ID+`/retry`) {
		t.Fatalf("fragment %d %s", code, body)
	}
	got, err := h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil || got.Status != "stopped" {
		t.Fatalf("%s %v", got.Status, err)
	}
	if len(fake.CancelIDs) != 1 || fake.CancelIDs[0] != runRow.ID {
		t.Fatalf("cancel %v", fake.CancelIDs)
	}
	if strings.Contains(strings.Join(fake.Calls, " "), "pause") || strings.Contains(strings.Join(fake.Calls, " "), "delete") {
		t.Fatalf("machine calls %v", fake.Calls)
	}
	vm, err := h.st.LiveVM(ctx, user.ID, agent.ID)
	if err != nil || vm.State != "running" {
		t.Fatalf("machine %s %v", vm.State, err)
	}
	if _, line, err := h.st.ReplacePrecheck(ctx, user.ID, agent.ID); err != nil || line != "" {
		t.Fatalf("replace %q %v", line, err)
	}
	rows, err := h.st.ListRunApprovals(ctx, user.ID, runRow.ID)
	if err != nil || len(rows) != 1 || rows[0].Status != "inactive" || rows[0].DecidedBy != "system:stop" || !rows[0].Sent {
		t.Fatalf("approval %+v %v", rows, err)
	}
	for _, row := range mustWebAudit(t, h, user.ID) {
		if row.Action == "approval.approved" {
			t.Fatal("approved")
		}
	}
	denials, err := h.st.ListRestartDenials(ctx)
	if err != nil || len(denials) != 0 {
		t.Fatalf("denials %+v %v", denials, err)
	}
	gotCard, err := h.st.GetConnectApproval(ctx, user.ID, card.ID)
	if err != nil || gotCard.Status != "expired" {
		t.Fatalf("card %s %v", gotCard.Status, err)
	}
	ok, err := h.st.ConsumeConnectApproval(ctx, user.ID, card.ID)
	if err != nil || ok {
		t.Fatal("consumed")
	}
}

func TestStopCancelRefusedLeavesRunOpen(t *testing.T) {
	h, user, agent, thread, ip := stopUser(t, "stop-refuse@example.com")
	ctx := context.Background()
	fake := pauseMachine(t, h, user.ID, agent.ID, "running")
	runRow, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(ctx, user.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", h.form(nil), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK || !strings.Contains(body, "The prompt is still running.") || !strings.Contains(body, `action="/runs/`+runRow.ID+`/stop"`) {
		t.Fatalf("still %d %s", code, body)
	}
	got, err := h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil || got.Status != "streaming" {
		t.Fatalf("%s %v", got.Status, err)
	}
	if len(fake.CancelIDs) != 1 || fake.CancelIDs[0] != runRow.ID {
		t.Fatalf("cancel %v", fake.CancelIDs)
	}
	code, _, hdr := h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", h.form(nil), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=running") {
		t.Fatalf("redirect %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "The prompt is still running.") {
		t.Fatalf("banner %d", code)
	}
	got, err = h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil || got.Status != "streaming" {
		t.Fatal(got.Status)
	}
}

func TestStopRejectsOtherUserAndBadCSRF(t *testing.T) {
	h, user, agent, thread, ip := stopUser(t, "stop-auth@example.com")
	ctx := context.Background()
	runRow, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", "csrf=nope", ip, nil)
	if code != http.StatusForbidden {
		t.Fatalf("csrf %d", code)
	}
	got, err := h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil || got.Status != "queued" {
		t.Fatal(got.Status)
	}
	_, secret := h.account("stop-other@example.com", "Bea", "correct-horse-1")
	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	h.login("stop-other@example.com", "correct-horse-1", secret, "203.0.113.51:9", false)
	code, body, _ := h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", h.form(nil), "203.0.113.51:9", nil)
	if code != http.StatusNotFound {
		t.Fatalf("other %d %s", code, body)
	}
	got, err = h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil || got.Status != "queued" {
		t.Fatal(got.Status)
	}
}

func TestStopLeavesTheOtherAgentRunning(t *testing.T) {
	h, user, chief, thread, ip := stopUser(t, "stop-one@example.com")
	ctx := context.Background()
	sec, err := h.st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	scout, scoutThread, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: sec.ID, Name: "Scout", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Ask Scout too")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.EnsureAgentJob(ctx, user.ID, thread.ID, msg.ID, chief.ID); err != nil {
		t.Fatal(err)
	}
	fake := pauseMachine(t, h, user.ID, chief.ID, "running")
	fake.CancelOK = true
	chiefRun, err := h.st.CreateRun(ctx, user.ID, thread.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, msg.ID, chiefRun.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(ctx, user.ID, chiefRun.ID); err != nil {
		t.Fatal(err)
	}
	scoutRun, err := h.st.CreateRun(ctx, user.ID, scoutThread.ID, scout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(ctx, user.ID, scoutRun.ID); err != nil {
		t.Fatal(err)
	}
	code, _, _ := h.do(http.MethodPost, "/runs/"+chiefRun.ID+"/stop", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	got, err := h.st.GetRun(ctx, user.ID, chiefRun.ID)
	if err != nil || got.Status != "stopped" {
		t.Fatal(got.Status)
	}
	other, err := h.st.GetRun(ctx, user.ID, scoutRun.ID)
	if err != nil || other.Status != "streaming" {
		t.Fatal(other.Status)
	}
	if len(fake.CancelIDs) != 1 || fake.CancelIDs[0] != chiefRun.ID {
		t.Fatalf("cancel %v", fake.CancelIDs)
	}
}

func TestStopStartsTheNextQueuedMessage(t *testing.T) {
	h, user, agent, thread, ip := stopUser(t, "stop-next@example.com")
	ctx := context.Background()
	base := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return base })
	first, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, msg.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return base.Add(time.Second) })
	second, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "second please")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, user.ID, next.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	code, _, _ := h.do(http.MethodPost, "/runs/"+first.ID+"/stop", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	deadline := time.Now().Add(5 * time.Second)
	var got store.Run
	for time.Now().Before(deadline) {
		got, err = h.st.GetRun(ctx, user.ID, second.ID)
		if err == nil && got.Status != "queued" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if got.Status == "queued" {
		t.Fatal("next message stayed queued")
	}
}

func TestStopRoutineLeavesTheSchedule(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "stop-routine@example.com")
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 7, 0, 0, 0, loc) })
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	slot, err := h.st.ClaimManual(context.Background(), user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, slot.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := h.st.GetRoutine(context.Background(), user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := h.do(http.MethodPost, "/runs/"+runRow.ID+"/stop", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	after, err := h.st.GetRoutine(context.Background(), user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled != before.Enabled || after.Cron != before.Cron || after.NextRunAt != before.NextRunAt {
		t.Fatalf("schedule %+v", after)
	}
	rr, err := h.st.GetRoutineRun(context.Background(), user.ID, slot.ID)
	if err != nil || rr.FinishedAt == "" || rr.Status != store.RoutineFailed || rr.Detail != store.StoppedLine {
		t.Fatalf("fire %+v %v", rr, err)
	}
	block := make(chan struct{})
	client := &scriptClient{block: block}
	h.srv.SetClient(client)
	t.Cleanup(func() { close(block) })
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 8, 0, 0, 0, loc) })
	h.srv.Tick(context.Background())
	waitOpens(t, client, 1)
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	started := 0
	for _, row := range rows {
		if row.ID != slot.ID && row.RunID != "" {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("next fire %+v", rows)
	}
}

func TestStopIgnoresCLIAndAClosedBrowser(t *testing.T) {
	block := make(chan struct{})
	client := &scriptClient{block: block}
	h := newHarnessWith(t, client)
	h.srv.pauseSweeps()
	t.Cleanup(func() { close(block) })
	ip := "203.0.113.52:9"
	user, secret := h.account("stop-cli@example.com", "Ada Lovelace", "correct-horse-1")
	h.login("stop-cli@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	cli, err := h.st.CreateThread(context.Background(), user.ID, "cli", agent.ID, "CLI")
	if err != nil {
		t.Fatal(err)
	}
	cliRun, err := h.st.CreateRun(context.Background(), user.ID, cli.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), user.ID, cliRun.ID); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?thread="+url.QueryEscape(cli.ID), "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, `/runs/`+cliRun.ID+`/stop`) {
		t.Fatalf("cli button %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/runs/"+cliRun.ID+"/stop", h.form(nil), ip, nil)
	got, err := h.st.GetRun(context.Background(), user.ID, cliRun.ID)
	if err != nil || got.Status != "streaming" {
		t.Fatal(got.Status)
	}
	other, otherThread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Nova", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ = h.do(http.MethodPost, "/agents/"+other.ID+"/messages", h.form(url.Values{"body": {"hello"}, "thread": {otherThread.ID}}), ip, nil)
	if code != http.StatusSeeOther && code != http.StatusOK {
		t.Fatal(code)
	}
	waitBusy(t, h, user.ID, other.ID)
	var live store.Run
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && live.ID == "" {
		for _, row := range mustRuns(t, h, user.ID, otherThread.ID) {
			if row.Status == "streaming" {
				live = row
			}
		}
		if live.ID == "" {
			time.Sleep(15 * time.Millisecond)
		}
	}
	if live.ID == "" {
		t.Fatal("chat run did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/runs/"+live.ID+"/events", nil).WithContext(ctx)
	req.RemoteAddr = ip
	h.mu.Lock()
	for _, c := range h.jar {
		req.AddCookie(c)
	}
	h.mu.Unlock()
	done := make(chan struct{})
	go func() {
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("event stream did not return")
	}
	time.Sleep(40 * time.Millisecond)
	got, err = h.st.GetRun(context.Background(), user.ID, live.ID)
	if err != nil || got.Status != "streaming" {
		t.Fatalf("closed browser %s %v", got.Status, err)
	}
}

func mustRuns(t *testing.T, h *harness, userID, threadID string) []store.Run {
	t.Helper()
	rows, err := h.st.ListThreadRuns(context.Background(), userID, threadID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func mustWebAudit(t *testing.T, h *harness, userID string) []store.Audit {
	t.Helper()
	rows, err := h.st.ListAudit(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
