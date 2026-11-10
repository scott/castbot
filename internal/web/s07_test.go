package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

type slogRec struct {
	mu    sync.Mutex
	lines []string
}

func (h *slogRec) Enabled(context.Context, slog.Level) bool { return true }
func (h *slogRec) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.lines = append(h.lines, r.Message+" "+r.Level.String())
	r.Attrs(func(a slog.Attr) bool {
		h.lines[len(h.lines)-1] += " " + a.Key + "=" + a.Value.String()
		return true
	})
	h.mu.Unlock()
	return nil
}
func (h *slogRec) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *slogRec) WithGroup(string) slog.Handler      { return h }

func (h *slogRec) has(parts ...string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, line := range h.lines {
		ok := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (h *slogRec) count(part string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, line := range h.lines {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

func TestLifecycleLogsAndDurableRunningStamp(t *testing.T) {
	ip := "203.0.113.230:9"
	h, fake, agent := machineUser(t, ip)
	rec := &slogRec{}
	h.srv.SetLogger(slog.New(rec))
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(nil), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	waitKinds(t, h, agent, store.EventCreateRequested, store.EventCreateAccepted, store.EventRunning)
	for _, kind := range []string{store.EventCreateRequested, store.EventCreateAccepted, store.EventRunning} {
		if !rec.has("event="+kind, "agent_id="+agent.ID, "vm_id=", "microvm_id=") {
			t.Fatalf("missing log %s", kind)
		}
	}
	vm, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID)
	if err != nil || vm.CreatedRunning == "" {
		t.Fatalf("stamp %+v %v", vm, err)
	}
	first := vm.CreatedRunning
	h.st.SetNow(func() time.Time { return time.Now().Add(2 * time.Hour) })
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "running", vm.Endpoint, ""); err != nil {
		t.Fatal(err)
	}
	vm, err = h.st.LiveVM(context.Background(), agent.UserID, agent.ID)
	if err != nil || vm.CreatedRunning != first {
		t.Fatalf("overwritten %q %q", first, vm.CreatedRunning)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/bootstrap", "", ip, map[string]string{"Authorization": "Bearer " + plain})
	if code != http.StatusOK {
		t.Fatalf("bootstrap %d", code)
	}
	waitKinds(t, h, agent, store.EventGuestReady)
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/pause", h.form(nil), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/wake", h.form(nil), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitKinds(t, h, agent, store.EventPauseRequested, store.EventPauseObserved, store.EventWakeRequested, store.EventWakeRunning, store.EventWakeReady)
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine/delete", h.form(map[string][]string{"confirm": {agent.Name}}), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitKinds(t, h, agent, store.EventDelete)
	if !rec.has("event=" + store.EventDelete) {
		t.Fatal("delete was not logged")
	}
	other, _ := h.account("other-s07@example.com", "Other", "correct-horse-1")
	if _, err := h.st.ListVMEvents(context.Background(), other.ID, agent.ID); err != store.ErrNotFound {
		t.Fatalf("authz %v", err)
	}
	_ = fake
}

func waitKinds(t *testing.T, h *harness, agent store.Agent, kinds ...string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var have []store.VMEvent
	for time.Now().Before(deadline) {
		var err error
		have, err = h.st.ListVMEvents(context.Background(), agent.UserID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		missing := false
		for _, kind := range kinds {
			if !kindPresent(have, kind) {
				missing = true
			}
		}
		if !missing {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("missing kinds %v in %d events", kinds, len(have))
}

func kindPresent(events []store.VMEvent, kind string) bool {
	for _, ev := range events {
		if ev.Kind == kind {
			return true
		}
	}
	return false
}

func TestReconcilerMarksPausedAndBacksOff(t *testing.T) {
	ip := "203.0.113.231:9"
	h, fake, agent := machineUser(t, ip)
	rec := &slogRec{}
	h.srv.SetLogger(slog.New(rec))
	if code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(nil), ip, nil); code != http.StatusSeeOther {
		t.Fatal(code)
	}
	waitMachine(t, h, agent, ip, "Pause")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.SetListErr(errList("machine api status 500: vm-abc"))
	h.srv.RunReconciler(ctx, 20*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rec.count("err_kind=http_500") < 1 {
		time.Sleep(15 * time.Millisecond)
	}
	if rec.count("err_kind=http_500") != 1 {
		t.Fatalf("first error logs %d", rec.count("err_kind=http_500"))
	}
	fake.SetListErr(errList("machine api status 503: vm-xyz"))
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && rec.count("err_kind=http_503") < 1 {
		time.Sleep(15 * time.Millisecond)
	}
	fake.SetListErr(errList("machine api status 503: vm-other"))
	time.Sleep(150 * time.Millisecond)
	if rec.count("err_kind=http_503") != 1 || rec.count("err_kind=http_500") != 1 {
		t.Fatal("error kinds were logged again")
	}
	if h.srv.reconcileDelayForTest() < 20*time.Millisecond {
		t.Fatal("backoff did not grow")
	}
	before, edge := fake.Snapshot()
	fake.SetListErr(nil)
	fake.SetMachineState("vm-test", "paused")
	deadline = time.Now().Add(3 * time.Second)
	var status string
	for time.Now().Before(deadline) {
		got, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		status = got.Status
		if status == "paused" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if status != "paused" {
		t.Fatalf("status %s", status)
	}
	after, edgeAfter := fake.Snapshot()
	if edgeAfter != edge {
		t.Fatalf("edge calls %d to %d", edge, edgeAfter)
	}
	for _, call := range after[len(before):] {
		if call != "list" {
			t.Fatalf("call %s", call)
		}
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !kindPresent(mustEvents(t, h, agent), store.EventPauseObserved) {
		time.Sleep(15 * time.Millisecond)
	}
	if !kindPresent(mustEvents(t, h, agent), store.EventPauseObserved) {
		t.Fatal("no pause event")
	}
}

type errList string

func (e errList) Error() string { return string(e) }

func mustEvents(t *testing.T, h *harness, agent store.Agent) []store.VMEvent {
	t.Helper()
	events, err := h.st.ListVMEvents(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestDraftStartAndNoEchoWithoutTheFlag(t *testing.T) {
	ip := "203.0.113.232:9"
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	h.srv.SetHost(host.NewFake(), "example.invalid/castbot-runtime:s07", "https://castbot.example")
	user, secret := h.account("draft-s07@example.com", "Owner", "correct-horse-1")
	h.login("draft-s07@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || strings.Count(body, "No machine yet. Start it to chat.") != 1 || !strings.Contains(body, ">Start<") || !strings.Contains(body, "Enter to send") {
		t.Fatalf("start state %d", code)
	}
	comp := strings.Index(body, `id="composer"`)
	end := strings.Index(body[comp:], "</form>")
	start := strings.Index(body, `class="start-form"`)
	if comp < 0 || end < 0 || start < 0 || start < comp+end {
		t.Fatal("Start is inside the composer")
	}
	if strings.Contains(body, "Hello! I'm") || strings.Contains(body, "You said:") {
		t.Fatal("echo greeting on the draft page")
	}
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(map[string][]string{"body": {"hello"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code)
	}
	deadline := time.Now().Add(3 * time.Second)
	var run store.Run
	for time.Now().Before(deadline) {
		threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
		if err != nil || len(threads) == 0 {
			t.Fatal(err)
		}
		runs, err := h.st.ListThreadRuns(context.Background(), user.ID, threads[0].ID)
		if err != nil || len(runs) == 0 {
			t.Fatal(err)
		}
		run = runs[len(runs)-1]
		if run.Status == "failed" || run.Status == "done" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if run.Status != "failed" || run.Error != "No machine yet. Start it to chat." {
		t.Fatalf("run %+v", run)
	}
	if strings.Contains(run.Error, "Hello! I'm") {
		t.Fatal("echo")
	}
	code, hxBody, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(nil), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || hdr.Get("HX-Redirect") != "/agents/"+agent.ID+"/machine" || strings.Contains(hxBody, "ch-h") {
		t.Fatalf("htmx start %d %s", code, hdr.Get("HX-Redirect"))
	}
}

func TestMissingProviderKeyFailsFast(t *testing.T) {
	ip := "203.0.113.233:9"
	h, fake, agent := machineUser(t, ip)
	const model = "digitalocean/deepseek-v3.2"
	if _, err := h.st.PutBundle(context.Background(), agent.UserID, agent.ID, "1", "name: Atlas\nbrain:\n  model: "+model+"\n", "Be concise.\n"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetAgentModel(context.Background(), agent.UserID, agent.ID, model); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-key", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
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
	fake.Machines["vm-key"] = host.Machine{ID: "vm-key", State: "running", Endpoint: "https://vm.example"}
	fake.Frames = []runtime.Frame{{
		Data:      `{"jsonrpc":"2.0","result":{"parts":[{"kind":"text","text":"ok"}]}}`,
		DataEvent: true,
	}}
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", "")
	started := time.Now()
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(map[string][]string{"body": {"hello"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatalf("send %d", code)
	}
	runID := runIDFrom(t, body)
	row := waitRun(t, h, agent.UserID, runID, "failed", 5*time.Second)
	if time.Since(started) > 5*time.Second {
		t.Fatal("missing key took too long")
	}
	want := castcfg.MissingProviderKey("digitalocean")
	if row.Error != want {
		t.Fatalf("error %q", row.Error)
	}
	if strings.Contains(row.Error, "Hello! I'm") || strings.Contains(row.Error, "You said:") {
		t.Fatal("echo")
	}
	events, err := h.st.ListRunEvents(context.Background(), agent.UserID, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if strings.Contains(ev.Payload, "fixture-s07-key") { // gitleaks:allow
			t.Fatal("key in run events")
		}
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || strings.Contains(page, "fixture-s07-key") || !strings.Contains(page, want) { // gitleaks:allow
		t.Fatalf("page %d", code)
	}
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", "fixture-s07-key") // gitleaks:allow
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(map[string][]string{"body": {"hello again"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatalf("send with key %d", code)
	}
	keyed := runIDFrom(t, body)
	keyedRow := waitRun(t, h, agent.UserID, keyed, "done", 5*time.Second)
	if keyedRow.Error != "" {
		t.Fatal(keyedRow.Error)
	}
	keyedEvents, err := h.st.ListRunEvents(context.Background(), agent.UserID, keyed, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range keyedEvents {
		if strings.Contains(ev.Payload, "fixture-s07-key") { // gitleaks:allow
			t.Fatal("key in run events")
		}
	}
	code, secrets, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || !strings.Contains(secrets, "DIGITALOCEAN_MODEL_ACCESS_KEY") || !strings.Contains(secrets, "fixture-s07-key") { // gitleaks:allow
		t.Fatalf("secrets %d", code)
	}
	code, page, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/secrets", "", ip, nil)
	if code != 200 || strings.Contains(page, "fixture-s07-key") { // gitleaks:allow
		t.Fatal("key on the owner page")
	}
	bundle, err := h.st.LatestBundle(context.Background(), agent.UserID, agent.ID)
	if err != nil || strings.Contains(bundle.YAML, "fixture-s07-key") || !strings.Contains(bundle.YAML, model) { // gitleaks:allow
		t.Fatal("bundle")
	}
}

func TestUnseenCLITokenIsRevokedWhenThePageIsNotSent(t *testing.T) {
	ip := "203.0.113.234:9"
	h, fake, agent := machineUser(t, ip)
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-cli", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-cli"] = host.Machine{ID: "vm-cli", State: "running", Endpoint: "https://vm.example"}
	boot, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	vmToken, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, vmToken); err != nil {
		t.Fatal(err)
	}
	id, _, err := h.st.IssueCLIToken(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	user, err := h.st.GetUser(context.Background(), agent.UserID)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/agents/"+agent.ID+"/cli", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	h.srv.finishExport(httptest.NewRecorder(), req, user, agent.ID, id)
	tokens, err := h.st.ListCLITokens(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(tokens) != 1 || tokens[0].RevokedAt == "" || tokens[0].ViewedAt != "" {
		t.Fatalf("%v %+v", err, tokens)
	}
	if len(fake.Pushed) == 0 {
		t.Fatal("revoke was not pushed")
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Not shown") || !strings.Contains(body, "Revoked") {
		t.Fatalf("list %d", code)
	}
}

func TestProviderKeyReloadAfterBoot(t *testing.T) {
	ip := "203.0.113.237:9"
	h, fake, agent := machineUser(t, ip)
	const model = "digitalocean/deepseek-v3.2"
	if err := h.st.SetAgentModel(context.Background(), agent.UserID, agent.ID, model); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-key", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
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
	fake.Machines["vm-key"] = host.Machine{ID: "vm-key", State: "running", Endpoint: "https://vm.example"}
	fake.Frames = []runtime.Frame{{
		Data:      `{"jsonrpc":"2.0","result":{"parts":[{"kind":"text","text":"ok"}]}}`,
		DataEvent: true,
	}}
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", "fixture-s07-key") // gitleaks:allow
	send := func() {
		t.Helper()
		code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(map[string][]string{"body": {"hello"}}), ip, map[string]string{"HX-Request": "true"})
		if code != 200 {
			t.Fatalf("send %d", code)
		}
		row := waitRun(t, h, agent.UserID, runIDFrom(t, body), "done", 5*time.Second)
		if row.Error != "" {
			t.Fatal(row.Error)
		}
	}
	before, _ := fake.Snapshot()
	send()
	after, _ := fake.Snapshot()
	if countCall(before, after, "reload") != 1 {
		t.Fatalf("first reload %v", after)
	}
	before = after
	send()
	after, _ = fake.Snapshot()
	if countCall(before, after, "reload") != 0 {
		t.Fatal("reloaded again with the same key")
	}
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", "fixture-s07-key-rotated") // gitleaks:allow
	before = after
	send()
	after, _ = fake.Snapshot()
	if countCall(before, after, "reload") != 1 {
		t.Fatal("rotated key did not reload")
	}
}

func TestEdgeResumeAndUnknownFirstRunning(t *testing.T) {
	ip := "203.0.113.238:9"
	h, fake, agent := machineUser(t, ip)
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-edge", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-edge"] = host.Machine{ID: "vm-edge", State: "paused", Endpoint: "https://vm.example"}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if code != 200 || !strings.Contains(body, "First running") || !strings.Contains(body, "unknown") {
		t.Fatalf("page %d", code)
	}
	fake.Machines["vm-edge"] = host.Machine{ID: "vm-edge", State: "running", Endpoint: "https://vm.example"}
	if code, _, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil); code != 200 {
		t.Fatal(code)
	}
	if !kindPresent(mustEvents(t, h, agent), store.EventEdgeResume) {
		t.Fatal("edge resume was not logged")
	}
}

func TestGuestReadyIsPerMachine(t *testing.T) {
	ip := "203.0.113.239:9"
	h, _, agent := machineUser(t, ip)
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-a", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	h.srv.noteGuestReady(ctx, agent.UserID, agent.ID, "vm-a", 0)
	h.srv.noteGuestReady(ctx, agent.UserID, agent.ID, "vm-a", 0)
	if countKind(mustEvents(t, h, agent), store.EventGuestReady) != 1 {
		t.Fatal("duplicate guest ready")
	}
	if err := h.st.MarkVMDeleted(ctx, agent.UserID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(ctx, agent.UserID, agent.ID, "vm-b", "https://vm.example", "running", ""); err != nil {
		t.Fatal(err)
	}
	h.srv.noteGuestReady(ctx, agent.UserID, agent.ID, "vm-b", 0)
	if countKind(mustEvents(t, h, agent), store.EventGuestReady) != 2 {
		t.Fatal("second machine did not record guest ready")
	}
}

func countKind(events []store.VMEvent, kind string) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}
