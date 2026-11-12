package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/cron"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestRoutineMinIntervalCopy(t *testing.T) {
	h, _, agent, ip := routineFixture(t, "min@example.com")
	ok := url.Values{
		"name": {"Hourly check"}, "cron": {"0 * * * *"}, "tz": {"America/Denver"},
		"prompt": {"Check the docs."}, "missed": {"once"}, "approval": {"wait"},
	}
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/routines", h.form(ok), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("hourly %d %s", code, hdr.Get("Location"))
	}
	quarter := ok
	quarter.Set("name", "Quarter")
	quarter.Set("cron", "0,15,30,45 * * * *")
	code, _, hdr = h.do(http.MethodPost, "/agents/"+agent.ID+"/routines", h.form(quarter), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("15 min %d %s", code, hdr.Get("Location"))
	}
	bad := ok
	bad.Set("name", "Too close")
	bad.Set("cron", "*/10 * * * *")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/routines", h.form(bad), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "Runs closer than 15 min apart keep the machine awake all day.") {
		t.Fatalf("refused %d %s", code, body)
	}
}

func TestRoutineZonesAndDSTPreview(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "zones@example.com")
	if err := h.st.SetTimezone(context.Background(), user.ID, "Pacific/Auckland"); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return time.Date(2026, 3, 7, 7, 0, 0, 0, loc) })
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=routines&routine=new", "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	auckland := strings.Index(body, `value="Pacific/Auckland"`)
	denver := strings.Index(body, `value="America/Denver"`)
	tokyo := strings.Index(body, `value="Asia/Tokyo"`)
	if auckland < 0 || denver < 0 || tokyo < 0 || auckland > denver || auckland > tokyo {
		t.Fatalf("zone order auckland %d denver %d tokyo %d", auckland, denver, tokyo)
	}
	if !strings.Contains(body, "Your zone. 8:00 AM stays 8:00 AM when daylight saving time changes.") {
		t.Fatal("missing zone help")
	}
	code, preview, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/routines/preview?cron="+url.QueryEscape("0 8 * * *")+"&tz=America/Denver", "", ip, nil)
	if code != 200 || strings.Contains(preview, "7:00 AM") || strings.Contains(preview, "9:00 AM") || strings.Count(preview, "8:00 AM") < 3 {
		t.Fatalf("dst preview %d %s", code, preview)
	}
}

func TestRoutineApprovalWaitTenMinutes(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "wait@example.com")
	loc, _ := time.LoadLocation("America/Denver")
	now := time.Date(2026, 11, 12, 13, 0, 0, 0, loc)
	h.st.SetNow(func() time.Time { return now })
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	block := make(chan struct{})
	client := &scriptClient{block: block, frames: []runtime.Frame{{
		Data:      `{"jsonrpc":"2.0","result":{"metadata":{"permissionRequest":{"id":"req-1","tool":"git_push","arguments":{"remote":"origin"}}}}}`,
		DataEvent: true,
	}}}
	h.srv.SetClient(client)
	t.Cleanup(func() { close(block) })
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/routines/"+routine.ID+"/run", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("run %d", code)
	}
	row := waitRoutine(t, h, user.ID, routine.ID, func(row store.RoutineRun) bool { return row.RunID != "" })
	var approval store.Approval
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := h.st.ListRunApprovals(context.Background(), user.ID, row.RunID)
		if err == nil && len(rows) == 1 {
			approval = rows[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if approval.ID == "" {
		t.Fatal("no approval")
	}
	exp, err := time.Parse(time.RFC3339, approval.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Sub(now) != run.DefaultApprovalWait {
		t.Fatalf("wait %s", exp.Sub(now))
	}
	code, body, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != 200 || !strings.Contains(body, "Denied automatically in 10 min if not answered. The run finishes without it.") || strings.Contains(body, "24 h") || strings.Contains(body, "24 hour") {
		t.Fatalf("card %d %s", code, body)
	}
	if !strings.Contains(body, "Tool git_push is set to Ask · from the routine "+routine.Name) {
		t.Fatal("missing stamp")
	}
	h.st.SetNow(func() time.Time { return now.Add(run.DefaultApprovalWait) })
	h.srv.ExpireDue(context.Background())
	got, err := h.st.GetApproval(context.Background(), user.ID, approval.ID)
	if err != nil || got.Status != "expired" {
		t.Fatalf("expired %v %s", err, got.Status)
	}
}

func TestRoutineApprovalDenyPolicy(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "deny@example.com")
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalDeny, store.MissedOnce)
	client := &scriptClient{responded: make(chan bool, 1), frames: []runtime.Frame{
		{Data: `{"jsonrpc":"2.0","result":{"metadata":{"permissionRequest":{"id":"req-9","tool":"git_push","arguments":{"remote":"origin"}}}}}`, DataEvent: true},
		{Data: `{"jsonrpc":"2.0","result":{"parts":[{"kind":"text","text":"Finished without the push."}]}}`, DataEvent: true},
		{Data: "[DONE]", DataEvent: true},
	}}
	h.srv.SetClient(client)
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/routines/"+routine.ID+"/run", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	select {
	case allow := <-client.responded:
		if allow {
			t.Fatal("deny policy approved")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no respond")
	}
	row := waitRoutine(t, h, user.ID, routine.ID, func(row store.RoutineRun) bool {
		return row.Status == store.RoutineDone || row.Status == store.RoutineFailed
	})
	if row.Status != store.RoutineDone {
		t.Fatalf("status %s", row.Status)
	}
	code, body, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != 200 || !strings.Contains(body, "Denied by the routine setting") {
		t.Fatalf("card %d %s", code, body)
	}
}

func TestRoutineRunNowOpensThread(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "now@example.com")
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	block := make(chan struct{})
	h.srv.SetClient(&scriptClient{block: block})
	t.Cleanup(func() { close(block) })
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/routines/"+routine.ID+"/run", h.form(nil), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "thread=") {
		t.Fatalf("redirect %d %s", code, hdr.Get("Location"))
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 || rows[0].Source != store.RoutineSourceManual {
		t.Fatalf("%v %+v", err, rows)
	}
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/routines/"+routine.ID+"/run", h.form(nil), ip, nil)
	rows, err = h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("second run %d %v len %d", code, err, len(rows))
	}
	code, body, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != 200 || !strings.Contains(body, "Reply in this run's thread") || !strings.Contains(body, "Routine prompt, sent by castbot") {
		t.Fatalf("thread %d", code)
	}
}

func TestRoutineQueueBehindReply(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "queue@example.com")
	loc, _ := time.LoadLocation("America/Denver")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 7, 0, 0, 0, loc) })
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	block := make(chan struct{})
	client := &scriptClient{block: block}
	h.srv.SetClient(client)
	code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello"}}), ip, nil)
	if code != http.StatusSeeOther && code != http.StatusOK {
		t.Fatal(code)
	}
	waitBusy(t, h, user.ID, agent.ID)
	waitOpens(t, client, 1)
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 8, 0, 0, 0, loc) })
	h.srv.Tick(context.Background())
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 || rows[0].Status != store.RoutineQueued || rows[0].Detail != "Waits for the reply in progress" || rows[0].RunID != "" {
		t.Fatalf("%v %+v", err, rows)
	}
	if client.Opens() != 1 {
		t.Fatal("routine started during the reply")
	}
	close(block)
	waitRoutine(t, h, user.ID, routine.ID, func(row store.RoutineRun) bool { return row.RunID != "" })
	waitOpens(t, client, 2)
}

func TestRoutineClaimThenChat(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "claim-chat@example.com")
	loc, _ := time.LoadLocation("America/Denver")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 7, 0, 0, 0, loc) })
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	block := make(chan struct{})
	client := &scriptClient{block: block}
	h.srv.SetClient(client)
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 8, 0, 0, 0, loc) })
	claimed, err := h.st.ClaimDue(context.Background())
	if err != nil || len(claimed) != 1 || claimed[0].RunID != "" || !claimed[0].Launch {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello"}}), ip, nil)
	if code != http.StatusSeeOther && code != http.StatusOK {
		t.Fatal(code)
	}
	if client.Opens() != 0 {
		t.Fatal("chat started while a routine slot was claimed")
	}
	dm := dmThread(t, h, user.ID, agent.ID)
	chatRuns, err := h.st.ListThreadRuns(context.Background(), user.ID, dm.ID)
	if err != nil || len(chatRuns) != 1 || chatRuns[0].Status != "queued" {
		t.Fatalf("chat run %v %+v", err, chatRuns)
	}
	h.srv.Tick(context.Background())
	waitOpens(t, client, 1)
	if client.Opens() != 1 {
		t.Fatalf("opens %d", client.Opens())
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 || rows[0].RunID == "" {
		t.Fatalf("routine did not start %v %+v", err, rows)
	}
	chatRuns, err = h.st.ListThreadRuns(context.Background(), user.ID, dm.ID)
	if err != nil || len(chatRuns) != 1 || chatRuns[0].Status != "queued" {
		t.Fatalf("chat left the queue %v %+v", err, chatRuns)
	}
	close(block)
	waitOpens(t, client, 2)
}

func TestRoutineCrashReclaim(t *testing.T) {
	h, user, agent, _ := routineFixture(t, "reclaim@example.com")
	loc, _ := time.LoadLocation("America/Denver")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 7, 0, 0, 0, loc) })
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	client := &scriptClient{block: make(chan struct{})}
	h.srv.SetClient(client)
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 8, 0, 0, 0, loc) })
	claimed, err := h.st.ClaimDue(context.Background())
	if err != nil || len(claimed) != 1 || claimed[0].RunID != "" {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	h.srv.Tick(context.Background())
	waitOpens(t, client, 1)
	h.srv.Tick(context.Background())
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if client.Opens() != 1 {
			t.Fatalf("reclaim started %d runs", client.Opens())
		}
		time.Sleep(20 * time.Millisecond)
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 || rows[0].RunID == "" {
		t.Fatalf("slot %v %+v", err, rows)
	}

	h2, user2, agent2, _ := routineFixture(t, "reclaim-chat@example.com")
	h2.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 7, 0, 0, 0, loc) })
	routine2 := makeRoutine(t, h2, user2.ID, agent2.ID, store.ApprovalWait, store.MissedOnce)
	client2 := &scriptClient{}
	h2.srv.SetClient(client2)
	h2.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 8, 0, 0, 0, loc) })
	if _, err := h2.st.ClaimDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	dm := dmThread(t, h2, user2.ID, agent2.ID)
	runRow, err := h2.st.CreateRun(context.Background(), user2.ID, dm.ID, agent2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.st.MarkStreaming(context.Background(), user2.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	h2.srv.Tick(context.Background())
	if client2.Opens() != 0 {
		t.Fatal("reclaim started beside a chat run")
	}
	rows, err = h2.st.ListRoutineRuns(context.Background(), user2.ID, routine2.ID, 0)
	if err != nil || len(rows) != 1 || rows[0].Status != store.RoutineQueued || rows[0].Detail != "Waits for the reply in progress" || rows[0].RunID != "" {
		t.Fatalf("parked %v %+v", err, rows)
	}
}

func TestRoutineOneAtATime(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "two@example.com")
	_ = ip
	loc, _ := time.LoadLocation("America/Denver")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 7, 0, 0, 0, loc) })
	a := makeNamed(t, h, user.ID, agent.ID, "A", store.ApprovalWait, store.MissedOnce)
	b := makeNamed(t, h, user.ID, agent.ID, "B", store.ApprovalWait, store.MissedOnce)
	block := make(chan struct{})
	client := &scriptClient{block: block}
	h.srv.SetClient(client)
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 8, 0, 0, 0, loc) })
	h.srv.Tick(context.Background())
	arows, _ := h.st.ListRoutineRuns(context.Background(), user.ID, a.ID, 0)
	brows, _ := h.st.ListRoutineRuns(context.Background(), user.ID, b.ID, 0)
	started := 0
	for _, row := range append(arows, brows...) {
		if row.RunID != "" {
			started++
		}
		if row.Status == store.RoutineQueued && row.Detail != "Waits for the reply in progress" {
			t.Fatalf("queue detail %s", row.Detail)
		}
	}
	if started != 1 || len(arows)+len(brows) != 2 {
		t.Fatalf("started %d a %d b %d", started, len(arows), len(brows))
	}
	waitOpens(t, client, 1)
	close(block)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		arows, _ = h.st.ListRoutineRuns(context.Background(), user.ID, a.ID, 0)
		brows, _ = h.st.ListRoutineRuns(context.Background(), user.ID, b.ID, 0)
		n := 0
		for _, row := range append(arows, brows...) {
			if row.RunID != "" {
				n++
			}
		}
		if n == 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("second routine did not start")
}

func TestRoutineWakeFailedCodes(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		h, user, agent, ip := routineFixture(t, "wake-time@example.com")
		fake := pauseMachine(t, h, user.ID, agent.ID, "paused")
		wait := make(chan struct{})
		fake.WakeWait = wait
		t.Cleanup(func() { close(wait) })
		h.srv.SetWakeBudget(40 * time.Millisecond)
		body := runAndRead(t, h, user, agent, ip)
		if strings.Contains(body, "Routine prompt, sent by castbot") || !strings.Contains(body, "(WAKE_TIMEOUT).") || !strings.Contains(body, "after 90 s") || !strings.Contains(body, "within 90 s") {
			t.Fatalf("timeout page missing code\n%s", body)
		}
		if strings.Contains(strings.Join(fake.Calls, " "), "stream") {
			t.Fatal("prompt was sent")
		}
		if !hasCode(t, h, user.ID, agent.ID, store.WakeTimeout) {
			t.Fatal("event")
		}
	})
	t.Run("provider", func(t *testing.T) {
		h, user, agent, ip := routineFixture(t, "wake-prov@example.com")
		fake := pauseMachine(t, h, user.ID, agent.ID, "paused")
		fake.WakeErr = errors.New("provider-secret-do-not-store")
		body := runAndRead(t, h, user, agent, ip)
		if strings.Contains(body, "provider-secret-do-not-store") || !strings.Contains(body, "(PROVIDER_ERROR).") || strings.Contains(body, "90 s") {
			t.Fatal("provider text leaked, code missing, or 90 s shown")
		}
		if strings.Contains(strings.Join(fake.Calls, " "), "stream") {
			t.Fatal("prompt was sent")
		}
		if !hasCode(t, h, user.ID, agent.ID, store.WakeProvider) {
			t.Fatal("event")
		}
	})
	t.Run("health", func(t *testing.T) {
		h, user, agent, ip := routineFixture(t, "wake-health@example.com")
		fake := pauseMachine(t, h, user.ID, agent.ID, "running")
		fake.ProbeErr = errors.New("probe-secret-do-not-store")
		body := runAndRead(t, h, user, agent, ip)
		if strings.Contains(body, "probe-secret-do-not-store") || !strings.Contains(body, "(HEALTH_FAILED).") || strings.Contains(body, "90 s") {
			t.Fatal("health text leaked, code missing, or 90 s shown")
		}
		if strings.Contains(strings.Join(fake.Calls, " "), "stream") {
			t.Fatal("prompt was sent")
		}
		if !hasCode(t, h, user.ID, agent.ID, store.WakeHealth) {
			t.Fatal("event")
		}
	})
}

func TestRoutinePanelLast30(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "hist@example.com")
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	loc, _ := time.LoadLocation("America/Denver")
	base := time.Date(2026, 10, 14, 8, 0, 0, 0, loc)
	for i := 1; i <= 31; i++ {
		when := base.AddDate(0, 0, i)
		h.st.SetNow(func() time.Time { return when })
		row, err := h.st.ClaimManual(context.Background(), user.ID, routine.ID)
		if err != nil {
			t.Fatal(err)
		}
		rr, _, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.FinishRoutineRun(context.Background(), user.ID, runRow.ID, store.RoutineDone, fmt.Sprintf("slot-%02d", i)); err != nil {
			t.Fatal(err)
		}
		_ = rr
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=routines&routine="+routine.ID, "", ip, nil)
	if code != 200 || !strings.Contains(body, "See all") || strings.Contains(body, "slot-01") || !strings.Contains(body, "slot-31") {
		t.Fatalf("panel %d has see all %v slot01 %v", code, strings.Contains(body, "See all"), strings.Contains(body, "slot-01"))
	}
	if !strings.Contains(body, "?threads="+routine.ID) || strings.Contains(body, "all=1") {
		t.Fatal("see all did not open the routine threads")
	}
	_, all, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?threads="+routine.ID, "", ip, nil)
	if !strings.Contains(all, "slot-01") || !strings.Contains(all, "slot-31") {
		t.Fatal("filtered threads hid a run")
	}
}

func TestRoutineDeleteLabel(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "del@example.com")
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	row, err := h.st.ClaimManual(context.Background(), user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, th, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/routines/"+routine.ID+"/delete", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code, hdr.Get("Location"))
	}
	got, err := h.st.GetThread(context.Background(), user.ID, th.ID)
	if err != nil || got.Kind != "routine" || got.Title != "deleted routine" {
		t.Fatalf("%v %+v", err, got)
	}
	still, err := h.st.GetRun(context.Background(), user.ID, runRow.ID)
	if err != nil || still.Status != "queued" {
		t.Fatalf("run cut %v %s", err, still.Status)
	}
	live, err := h.st.ListRoutines(context.Background(), user.ID, agent.ID)
	if err != nil || len(live) != 0 {
		t.Fatalf("schedule %v %d", err, len(live))
	}
	_, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?thread="+th.ID, "", ip, nil)
	if !strings.Contains(body, "deleted routine") {
		t.Fatal("label missing")
	}
}

func TestRoutineAuthzAndCSRF(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "owner@example.com")
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	row, err := h.st.ClaimManual(context.Background(), user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, secret := h.account("other@example.com", "Other", "correct-horse-1")
	h.login("other@example.com", "correct-horse-1", secret, ip, false)
	for _, path := range []string{
		"/agents/" + agent.ID + "/routines/preview?cron=0+8+*+*+*&tz=America/Denver",
		"/agents/" + agent.ID + "?panel=routines&routine=" + routine.ID,
		"/agents/" + agent.ID + "?threads=" + routine.ID,
		"/runs/" + runRow.ID + "/events",
	} {
		code, _, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusNotFound {
			t.Fatalf("authz %s %d", path, code)
		}
	}
	for _, path := range []string{
		"/agents/" + agent.ID + "/routines/" + routine.ID + "/run",
		"/agents/" + agent.ID + "/routines/" + routine.ID + "/toggle",
		"/agents/" + agent.ID + "/routines/" + routine.ID + "/delete",
		"/agents/" + agent.ID + "/routines",
	} {
		code, _, _ := h.do(http.MethodPost, path, h.form(nil), ip, nil)
		if code != http.StatusNotFound {
			t.Fatalf("authz post %s %d", path, code)
		}
	}
	_ = other
}

func TestRoutineCSRF(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "csrf@example.com")
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	posts := []string{
		"/agents/" + agent.ID + "/routines",
		"/agents/" + agent.ID + "/routines/" + routine.ID + "/toggle",
		"/agents/" + agent.ID + "/routines/" + routine.ID + "/run",
		"/agents/" + agent.ID + "/routines/" + routine.ID + "/delete",
	}
	for _, path := range posts {
		body := "name=Nope&cron=0+8+*+*+1-5&tz=America/Denver&prompt=Hi&missed=once&approval=wait"
		if path != posts[0] {
			body = "on=0"
		}
		code, text, _ := h.do(http.MethodPost, path, body, ip, nil)
		if code != http.StatusForbidden || !strings.Contains(text, "The form expired.") {
			t.Fatalf("csrf %s %d %s", path, code, text)
		}
	}
}

func TestRoutineFinishedLateMarker(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "late-mark@example.com")
	loc, _ := time.LoadLocation("America/Denver")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 7, 0, 0, 0, loc) })
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 12, 10, 0, 0, 0, loc) })
	claimed, err := h.st.ClaimDue(context.Background())
	if err != nil || len(claimed) != 1 {
		t.Fatal(err, len(claimed))
	}
	rr, _, _, err := h.st.BeginRoutineExecution(context.Background(), user.ID, claimed[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishRoutineRun(context.Background(), user.ID, rr.RunID, store.RoutineLate, "ran once at 10:00 AM"); err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if !strings.Contains(body, "Ran late") || strings.Contains(body, `rst ok">Done`) {
		t.Fatal("finished late marker")
	}
	_ = routine
}

func TestRoutineDeletedStamp(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "stamp@example.com")
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	row, err := h.st.ClaimManual(context.Background(), user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, th, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.RecordPermission(context.Background(), user.ID, runRow.ID, []byte(`{"id":"req-del","tool":"git_push","arguments":{}}`), h.st.Now().Add(10*time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	if err := h.st.DeleteRoutine(context.Background(), user.ID, routine.ID); err != nil {
		t.Fatal(err)
	}
	_, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?thread="+th.ID, "", ip, nil)
	if !strings.Contains(body, "from a deleted routine") || strings.Contains(body, "from the routine deleted routine") {
		t.Fatal("deleted stamp")
	}
}

func TestRoutineStoreInvalidIs400(t *testing.T) {
	h, _, agent, ip := routineFixture(t, "bad-cron@example.com")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/routines", h.form(url.Values{
		"name": {"Nope"}, "cron": {"0 8 31 2 *"}, "tz": {"America/Denver"},
		"prompt": {"Hi"}, "missed": {"once"}, "approval": {"wait"},
	}), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, cron.BadSchedule) {
		t.Fatalf("invalid %d %s", code, body)
	}
}

func TestChatWakeKeepsRunBudget(t *testing.T) {
	h, user, agent, _ := routineFixture(t, "wake-budget@example.com")
	fake := pauseMachine(t, h, user.ID, agent.ID, "paused")
	fake.WakeWait = make(chan struct{})
	vm, err := h.st.LiveVM(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.srv.SetWakeBudget(40 * time.Millisecond)
	t.Run("chat", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		_, err := h.srv.wakeForWork(ctx, fake, user.ID, agent.ID, vm, "https://vm.example", "token", "paused", false)
		if time.Since(start) < 200*time.Millisecond {
			t.Fatalf("chat wake used the routine budget (%v)", err)
		}
	})
	t.Run("routine", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		_, err := h.srv.wakeForWork(ctx, fake, user.ID, agent.ID, vm, "https://vm.example", "token", "paused", true)
		if err == nil || time.Since(start) > 700*time.Millisecond {
			t.Fatalf("routine wake ignored the 90s budget (%v)", err)
		}
	})
}

func TestRoutineHoldDuringReplace(t *testing.T) {
	h, user, agent, ip := routineFixture(t, "hold@example.com")
	_ = ip
	loc, _ := time.LoadLocation("America/Denver")
	due := time.Date(2026, 11, 12, 8, 0, 0, 0, loc)
	h.st.SetNow(func() time.Time { return due.Add(-time.Hour) })
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	fake := pauseMachine(t, h, user.ID, agent.ID, "running")
	if _, _, err := h.st.BeginReplacement(context.Background(), user.ID, agent.ID, "web:test", "example.invalid/castbot-runtime:next", false); err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return due })
	h.srv.Tick(context.Background())
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 || rows[0].Status != store.RoutineHeld || strings.Contains(strings.Join(fake.Calls, " "), "wake") {
		t.Fatalf("held %v %+v calls %v", err, rows, fake.Calls)
	}
	repl, err := h.st.ActiveReplacement(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishReplacement(context.Background(), user.ID, repl.ID, false); err != nil {
		t.Fatal(err)
	}
	row := waitRoutine(t, h, user.ID, routine.ID, func(row store.RoutineRun) bool { return row.RunID != "" })
	if row.Status == store.RoutineLate {
		t.Fatalf("on time marked late")
	}
	if !strings.Contains(strings.Join(fake.Calls, " "), "wake") && row.Status == store.RoutineHeld {
		t.Fatal("still held")
	}
}

func routineFixture(t *testing.T, email string) (*harness, store.User, store.Agent, string) {
	t.Helper()
	h := newHarness(t)
	h.srv.pauseSweeps()
	ip := "203.0.113.40:9"
	user, secret := h.account(email, "Alex Rivera", "correct-horse-1")
	if err := h.st.SetTimezone(context.Background(), user.ID, "America/Denver"); err != nil {
		t.Fatal(err)
	}
	user, err := h.st.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.login(email, "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	return h, user, agent, ip
}

func makeRoutine(t *testing.T, h *harness, userID, agentID, approval, missed string) store.Routine {
	t.Helper()
	return makeNamed(t, h, userID, agentID, "Weekly link sweep", approval, missed)
}

func makeNamed(t *testing.T, h *harness, userID, agentID, name, approval, missed string) store.Routine {
	t.Helper()
	if approval == "" {
		approval = store.ApprovalWait
	}
	if missed == "" {
		missed = store.MissedOnce
	}
	routine, err := h.st.CreateRoutine(context.Background(), userID, agentID, store.RoutineInput{
		Name: name, Cron: "0 8 * * 1-5", Timezone: "America/Denver",
		Prompt:  "Check docs/ for broken links and pages that disagree with docs/pricing.md. Fix what you can on a branch and tell me what you changed.",
		Enabled: true, ApprovalPolicy: approval, MissedPolicy: missed,
	})
	if err != nil {
		t.Fatal(err)
	}
	return routine
}

func waitRoutine(t *testing.T, h *harness, userID, routineID string, ok func(store.RoutineRun) bool) store.RoutineRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last store.RoutineRun
	for time.Now().Before(deadline) {
		rows, err := h.st.ListRoutineRuns(context.Background(), userID, routineID, 1)
		if err == nil && len(rows) > 0 {
			last = rows[0]
			if ok(last) {
				return last
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("routine did not settle: %+v", last)
	return last
}

func dmThread(t *testing.T, h *harness, userID, agentID string) store.Thread {
	t.Helper()
	threads, err := h.st.ListThreadsByAgent(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.Kind == "dm" {
			return th
		}
	}
	t.Fatal("no dm thread")
	return store.Thread{}
}

func waitOpens(t *testing.T, client *scriptClient, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.Opens() >= n {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("opens %d want %d", client.Opens(), n)
}

func waitBusy(t *testing.T, h *harness, userID, agentID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		busy, err := h.st.AgentBusy(context.Background(), userID, agentID)
		if err == nil && busy {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("agent did not become busy")
}

func pauseMachine(t *testing.T, h *harness, userID, agentID, state string) *host.Fake {
	t.Helper()
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	if _, err := h.st.BeginVM(context.Background(), userID, agentID, host.MachineName(agentID), "example.invalid/castbot-runtime:s09"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), userID, agentID, "vm-shot", "https://vm.example", state, ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agentID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agentID, token); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-shot"] = host.Machine{ID: "vm-shot", State: state, Endpoint: "https://vm.example"}
	fake.SetForce(state)
	return fake
}

func runAndRead(t *testing.T, h *harness, user store.User, agent store.Agent, ip string) string {
	t.Helper()
	routine := makeRoutine(t, h, user.ID, agent.ID, store.ApprovalWait, store.MissedOnce)
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/routines/"+routine.ID+"/run", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitRoutine(t, h, user.ID, routine.ID, func(row store.RoutineRun) bool { return row.Status == store.RoutineFailed })
	code, body, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	if !strings.Contains(body, "This run did not start.") || !strings.Contains(body, "the prompt was never sent") {
		t.Fatalf("banner missing\n%s", body)
	}
	return body
}

func hasCode(t *testing.T, h *harness, userID, agentID, code string) bool {
	t.Helper()
	events, err := h.st.ListVMEvents(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev.Kind == store.EventWakeFailed && ev.Code == code {
			return true
		}
	}
	return false
}

type scriptClient struct {
	mu        sync.Mutex
	frames    []runtime.Frame
	block     <-chan struct{}
	opens     int
	responded chan bool
}

func (c *scriptClient) Opens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

func (c *scriptClient) Open(ctx context.Context, _ runtime.Send) (runtime.Stream, error) {
	c.mu.Lock()
	c.opens++
	frames := append([]runtime.Frame(nil), c.frames...)
	block := c.block
	c.mu.Unlock()
	return &scriptStream{frames: frames, block: block, ctx: ctx}, nil
}

func (c *scriptClient) Respond(_ context.Context, _ string, allow bool) error {
	if c.responded != nil {
		c.responded <- allow
	}
	return nil
}

type scriptStream struct {
	frames []runtime.Frame
	i      int
	block  <-chan struct{}
	ctx    context.Context
	held   bool
}

func (s *scriptStream) Next() (runtime.Frame, error) {
	if s.i < len(s.frames) {
		f := s.frames[s.i]
		s.i++
		return f, nil
	}
	if s.block != nil && !s.held {
		s.held = true
		select {
		case <-s.block:
		case <-s.ctx.Done():
			return runtime.Frame{}, s.ctx.Err()
		}
	}
	return runtime.Frame{}, io.EOF
}

func (s *scriptStream) Close() error { return nil }
