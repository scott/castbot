package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestLostApprovalFailsTheRun(t *testing.T) {
	ask := runtime.NewAsk(runtime.AskOptions{Tool: "bash", Args: map[string]any{"command": "ls"}})
	up := httptest.NewServer(&loseAsk{ask: ask})
	t.Cleanup(up.Close)
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	h.srv.pauseSweeps()
	user, agent := s07cOwner(t, h, "lost-owner@example.com")
	runRow, _ := seedClaim(t, h, user, agent, "req-lost", time.Now().Add(time.Minute))
	h.srv.recoverDecisions(context.Background(), 0)
	got, err := h.st.GetRun(context.Background(), user.ID, runRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Error != lostApprovalNotice {
		t.Fatalf("run %+v", got)
	}
	rows, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	inactive := 0
	for _, row := range rows {
		if row.Action == "approval.approved" {
			t.Fatal("lost prompt was audited as approved")
		}
		if row.Action == "approval.inactive" && row.ActorKind == "user" && row.ActorID == user.ID {
			inactive++
		}
	}
	if inactive != 1 {
		t.Fatalf("inactive audits %d", inactive)
	}
}

func TestJournalPullErrorRetriesThenGivesUp(t *testing.T) {
	h, fake, agent := machineUser(t, "203.0.113.241:9")
	h.srv.pauseSweeps()
	var logBuf bytes.Buffer
	h.srv.SetLogger(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	user, err := h.st.GetUser(context.Background(), agent.UserID)
	if err != nil {
		t.Fatal(err)
	}
	readyMachine(t, h, fake, agent, "vm-pull", "paused")
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "running", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	fake.SetMachineState("vm-pull", "running")
	runRow, ap := seedClaim(t, h, user, agent, "req-pull", time.Now().Add(time.Minute))
	if _, err := h.st.IngestJournal(context.Background(), user.ID, agent.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:00Z", Caller: "control_plane", Kind: "decision", RequestID: "req-pull", Decision: "approved", Tool: "bash"},
	}); err != nil {
		t.Fatal(err)
	}
	gate := &gateHost{Fake: fake, pullErr: errors.New("journal down")}
	h.srv.SetHost(gate, "example.invalid/castbot-runtime:s07", "https://castbot.example")
	h.srv.recoverDecisions(context.Background(), 0)
	if gate.approves != 0 {
		t.Fatal("a failed pull still posted the decision")
	}
	got, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || got.Status != "approved" || got.Sent {
		t.Fatalf("%+v %v", got, err)
	}
	for i := 0; i < journalPullLimit-1; i++ {
		clearDenyBackoff(h.srv)
		h.srv.recoverDecisions(context.Background(), 0)
	}
	got, err = h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || got.Status != "inactive" || !got.Sent {
		t.Fatalf("after give-up %+v %v", got, err)
	}
	runGot, err := h.st.GetRun(context.Background(), user.ID, runRow.ID)
	if err != nil || runGot.Status == "failed" {
		t.Fatalf("run %+v %v", runGot, err)
	}
	if gate.approves != 0 {
		t.Fatal("give-up posted the decision")
	}
	if !strings.Contains(logBuf.String(), "approval journal pull gave up") || strings.Contains(logBuf.String(), "journal down") {
		t.Fatalf("log %s", logBuf.String())
	}
	rows, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Action == "approval.approved" {
			t.Fatal("failed pull confirmed the decision")
		}
	}
}

func TestRecoveryIgnoresItsOwnJournaledDecision(t *testing.T) {
	h, fake, agent := machineUser(t, "203.0.113.242:9")
	h.srv.pauseSweeps()
	user, err := h.st.GetUser(context.Background(), agent.UserID)
	if err != nil {
		t.Fatal(err)
	}
	readyMachine(t, h, fake, agent, "vm-own", "running")
	_, ap := seedClaim(t, h, user, agent, "req-own", time.Now().Add(time.Minute))
	gate := &gateHost{Fake: fake, approveErrs: []error{errors.New("runtime down"), host.ErrNoRequest}}
	h.srv.SetHost(gate, "example.invalid/castbot-runtime:s07", "https://castbot.example")
	h.srv.recoverDecisions(context.Background(), 0)
	snap, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || !snap.RecoverSet || snap.RecoverSeq != 0 || snap.Status != "approved" {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	if _, err := h.st.IngestJournal(context.Background(), user.ID, agent.ID, []store.JournalEntry{
		{Seq: 1, At: "2026-11-10T12:00:01Z", Caller: "control_plane", Kind: "decision", RequestID: "req-own", Decision: "approved", Tool: "bash"},
	}); err != nil {
		t.Fatal(err)
	}
	clearDenyBackoff(h.srv)
	h.srv.recoverDecisions(context.Background(), 0)
	got, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || got.Status != "inactive" || got.RecoverSeq != 0 {
		t.Fatalf("%+v %v", got, err)
	}
	rows, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Action == "approval.approved" {
			t.Fatal("recovery trusted its own journal row")
		}
	}
}

func TestNoMachineStopsApprovalRetry(t *testing.T) {
	h, fake, agent := machineUser(t, "203.0.113.243:9")
	h.srv.pauseSweeps()
	user, err := h.st.GetUser(context.Background(), agent.UserID)
	if err != nil {
		t.Fatal(err)
	}
	_, ap := seedClaim(t, h, user, agent, "req-gone", time.Now().Add(-time.Minute))
	if err := h.st.ReleaseDecision(context.Background(), user.ID, ap.ID, "approved", user.ID); err != nil {
		t.Fatal(err)
	}
	h.srv.ExpireDue(context.Background())
	got, err := h.st.GetApproval(context.Background(), user.ID, ap.ID)
	if err != nil || got.Status != "inactive" || got.DecidedBy != "system:no_machine" {
		t.Fatalf("%+v %v", got, err)
	}
	assertAudit(t, h, user.ID, "approval.inactive", "system", "system:no_machine", 1)
	calls, _ := fake.Snapshot()
	for _, call := range calls {
		if call == "approve" {
			t.Fatal("no machine still posted")
		}
	}
	h.srv.ExpireDue(context.Background())
	calls, _ = fake.Snapshot()
	for _, call := range calls {
		if call == "approve" {
			t.Fatal("second sweep posted")
		}
	}
}

func TestPausedApprovalWaitsForRunning(t *testing.T) {
	h, fake, agent := machineUser(t, "203.0.113.244:9")
	h.srv.pauseSweeps()
	user, err := h.st.GetUser(context.Background(), agent.UserID)
	if err != nil {
		t.Fatal(err)
	}
	readyMachine(t, h, fake, agent, "vm-hold", "paused")
	_, ap := seedClaim(t, h, user, agent, "req-pause", time.Now().Add(-time.Minute))
	if err := h.st.ReleaseDecision(context.Background(), user.ID, ap.ID, "approved", user.ID); err != nil {
		t.Fatal(err)
	}
	h.srv.ExpireDue(context.Background())
	if statusCalls(fake) != 1 || approveCalls(fake) != 0 {
		t.Fatalf("status %d approve %d", statusCalls(fake), approveCalls(fake))
	}
	h.srv.ExpireDue(context.Background())
	if statusCalls(fake) != 1 || approveCalls(fake) != 0 {
		t.Fatalf("held status %d approve %d", statusCalls(fake), approveCalls(fake))
	}
	if err := h.st.ApplyVMState(context.Background(), agent.UserID, agent.ID, "running", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	fake.SetMachineState("vm-hold", "running")
	h.srv.ExpireDue(context.Background())
	_, approvals := fake.Decisions()
	if len(approvals) != 1 || approvals[0].Allow || approvals[0].RequestID != "req-pause" {
		t.Fatalf("approvals %+v", approvals)
	}
}

func TestNoMachineHidesEnterHint(t *testing.T) {
	ip := "203.0.113.245:9"
	h, fake, agent := machineUser(t, ip)
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || strings.Contains(body, "Enter to send") || !strings.Contains(body, "No machine yet. Start it to chat.") {
		t.Fatalf("draft %d %s", code, snippet(body))
	}
	readyMachine(t, h, fake, agent, "vm-hint", "running")
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(body, "Enter to send") {
		t.Fatalf("running %d %s", code, snippet(body))
	}
}

func TestKeepaliveGapLogHasNoBody(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.246:9"
	user, secret := h.account("gap-owner@example.com", "Owner", "correct-horse-1")
	h.login("gap-owner@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	planted := "sk-live-1234567890abcd"
	if _, err := h.st.CreateMessage(context.Background(), user.ID, agentThread(t, h, user.ID, agent.ID), "user", user.ID, "token "+planted); err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), user.ID, agentThread(t, h, user.ID, agent.ID), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var logBuf lockedLog
	h.srv.SetLogger(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	h.srv.SetKeepalive(21 * time.Second)
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 28*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, live.URL+"/runs/"+runRow.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range h.cookieList() {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	go func() { _, _ = io.Copy(io.Discard, res.Body) }()
	deadline := time.Now().Add(26 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logBuf.String(), "gap_ms") && strings.Contains(logBuf.String(), runRow.ID) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "gap_ms") || !strings.Contains(logged, runRow.ID) {
		t.Fatalf("log %s", logged)
	}
	if strings.Contains(logged, planted) || strings.Contains(logged, "token ") {
		t.Fatalf("log leaked the message %s", logged)
	}
}

func TestSweepThrowawayMachinesDeletesBothPrefixes(t *testing.T) {
	fake := host.NewFake()
	fake.Machines["a"] = host.Machine{ID: "a", Name: "castbot-s07-test-one", State: "running"}
	fake.Machines["b"] = host.Machine{ID: "b", Name: "castbot-s07c-test-two", State: "running"}
	fake.Machines["c"] = host.Machine{ID: "c", Name: "Welcome", State: "running"}
	fake.Machines["d"] = host.Machine{ID: "d", Name: "cast-demo-agent-1", State: "running", Tags: []string{"castbot-s00"}}
	fake.Machines["e"] = host.Machine{ID: "e", Name: "castbot-s07-test-probe", State: "running", Tags: []string{"castbot-s00"}}
	err := sweepThrowawayMachines(context.Background(), fake)
	if err == nil {
		t.Fatal("protected tag was deleted")
	}
	if _, ok := fake.Machines["a"]; ok {
		t.Fatal("s07 prefix left")
	}
	if _, ok := fake.Machines["b"]; ok {
		t.Fatal("s07c prefix left")
	}
	if _, ok := fake.Machines["c"]; !ok {
		t.Fatal("Welcome was deleted")
	}
	if _, ok := fake.Machines["d"]; !ok {
		t.Fatal("demo machine was deleted")
	}
	if _, ok := fake.Machines["e"]; !ok {
		t.Fatal("tagged probe was deleted")
	}
}

func TestSecretFingerprintIsHMAC(t *testing.T) {
	h, _, agent := machineUser(t, "203.0.113.247:9")
	sec, err := h.st.PutSecret(context.Background(), agent.UserID, "DEMO_TOKEN", "demo-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), agent.UserID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	full, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := h.srv.secretFingerprint(context.Background(), agent.UserID, full)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.New()
	sum.Write([]byte("DEMO_TOKEN"))
	sum.Write([]byte{0})
	sum.Write([]byte("demo-secret-value"))
	sum.Write([]byte{0})
	plain := hex.EncodeToString(sum.Sum(nil))
	if fp == plain || fp == "" {
		t.Fatal("fingerprint is still a plain sha256")
	}
}

func TestLegacyPlainSecretFPIsRewritten(t *testing.T) {
	h, fake, agent := machineUser(t, "203.0.113.248:9")
	sec, err := h.st.PutSecret(context.Background(), agent.UserID, "DEMO_TOKEN", "demo-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), agent.UserID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	readyMachine(t, h, fake, agent, "vm-fp", "running")
	full, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := h.srv.plainSecretFP(context.Background(), agent.UserID, full)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSecretFP(context.Background(), agent.UserID, agent.ID, plain); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.RewriteLegacySecretFP(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := h.st.SecretFP(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := h.srv.secretFingerprint(context.Background(), agent.UserID, full)
	if err != nil || stored == plain || stored != want {
		t.Fatalf("stored %s plain %s want %s %v", stored, plain, want, err)
	}
	if err := h.srv.refreshSecrets(context.Background(), agent.UserID, full, "https://vm.example", "agent-token"); err != nil {
		t.Fatal(err)
	}
	calls, _ := fake.Snapshot()
	for _, call := range calls {
		if call == "reload" {
			t.Fatal("legacy rewrite still reloaded")
		}
	}
}

func TestSecretsPullRecordsFingerprint(t *testing.T) {
	ip := "203.0.113.249:9"
	h, fake, agent := machineUser(t, ip)
	sec, err := h.st.PutSecret(context.Background(), agent.UserID, "DEMO_TOKEN", "demo-secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), agent.UserID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	token := readyMachine(t, h, fake, agent, "vm-sec", "running")
	code, body, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || strings.Contains(body, "demo-secret-value") == false {
		t.Fatalf("secrets %d", code)
	}
	stored, err := h.st.SecretFP(context.Background(), agent.UserID, agent.ID)
	if err != nil || stored == "" {
		t.Fatalf("fingerprint %q %v", stored, err)
	}
	full, err := h.st.GetAgent(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := h.srv.secretFingerprint(context.Background(), agent.UserID, full)
	if err != nil || stored != want {
		t.Fatalf("stored %s want %s %v", stored, want, err)
	}
	if err := h.srv.refreshSecrets(context.Background(), agent.UserID, full, "https://vm.example", token); err != nil {
		t.Fatal(err)
	}
	calls, _ := fake.Snapshot()
	for _, call := range calls {
		if call == "reload" {
			t.Fatal("first message reloaded after the secrets pull")
		}
	}
}

func s07cOwner(t *testing.T, h *harness, email string) (store.User, store.Agent) {
	t.Helper()
	user, secret := h.account(email, "Owner", "correct-horse-1")
	h.login(email, "correct-horse-1", secret, "203.0.113.240:9", false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	return user, agent
}

func seedClaim(t *testing.T, h *harness, user store.User, agent store.Agent, requestID string, expires time.Time) (store.Run, store.Approval) {
	t.Helper()
	runRow, err := h.st.CreateRun(context.Background(), user.ID, agentThread(t, h, user.ID, agent.ID), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"id": requestID, "tool": "bash", "arguments": map[string]any{"command": "ls"}})
	ap, err := h.st.RecordPermission(context.Background(), user.ID, runRow.ID, raw, expires, 1)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := h.st.ClaimDecision(context.Background(), user.ID, ap.ID, runRow.ID, "approved", user.ID)
	if err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	return runRow, claimed
}

func readyMachine(t *testing.T, h *harness, fake *host.Fake, agent store.Agent, microID, state string) string {
	t.Helper()
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s07"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, microID, "https://vm.example", state, ""); err != nil {
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
	fake.Machines[microID] = host.Machine{ID: microID, Name: host.MachineName(agent.ID), State: state, Endpoint: "https://vm.example"}
	return token
}

func clearDenyBackoff(s *Server) {
	s.denyMu.Lock()
	s.denyNext = map[string]time.Time{}
	s.denyFails = map[string]int{}
	s.denyMu.Unlock()
}

func statusCalls(fake *host.Fake) int {
	calls, _ := fake.Snapshot()
	n := 0
	for _, call := range calls {
		if call == "status" {
			n++
		}
	}
	return n
}

func approveCalls(fake *host.Fake) int {
	calls, _ := fake.Snapshot()
	n := 0
	for _, call := range calls {
		if call == "approve" {
			n++
		}
	}
	return n
}

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type gateHost struct {
	*host.Fake
	pullErr     error
	approveErrs []error
	approves    int
}

func (g *gateHost) PullJournal(ctx context.Context, endpoint, token string, after int) ([]byte, error) {
	if g.pullErr != nil {
		return nil, g.pullErr
	}
	return g.Fake.PullJournal(ctx, endpoint, token, after)
}

func (g *gateHost) Approve(ctx context.Context, endpoint, token, requestID string, allow bool) error {
	g.approves++
	if g.approves <= len(g.approveErrs) {
		return g.approveErrs[g.approveErrsIndex()]
	}
	return g.Fake.Approve(ctx, endpoint, token, requestID, allow)
}

func (g *gateHost) approveErrsIndex() int {
	return g.approves - 1
}
