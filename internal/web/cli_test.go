package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castlet"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

type castHost struct {
	*host.Fake
}

func (c *castHost) Stream(ctx context.Context, endpoint, agentToken string, req runtime.Send) (runtime.Stream, error) {
	client := runtime.NewHTTP(endpoint)
	client.SetToken(agentToken)
	return client.Open(ctx, req)
}

func TestExportShowsTheTokenOnce(t *testing.T) {
	ip := "203.0.113.210:9"
	h, fake, agent := machineUser(t, ip)
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-cli", "https://demo.microvm.ondigitalocean.com", "running", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, agentToken); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-cli"] = host.Machine{ID: "vm-cli", State: "running", Endpoint: "https://demo.microvm.ondigitalocean.com"}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/cli", h.form(cliForm()), ip, nil)
	if code != http.StatusOK {
		t.Fatalf("export %d", code)
	}
	token := blobToken(t, body)
	if strings.Count(body, token) != 1 || !strings.Contains(body, "Add to Cast CLI") || !strings.Contains(body, "microvm") {
		t.Fatal("token was not shown once")
	}
	if !strings.Contains(body, "CLI name") || !strings.Contains(body, ">atlas<") || !strings.Contains(body, "display_name") {
		t.Fatal("cli name was not shown")
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, token) {
		t.Fatal("token leaked on a later page")
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/bundle", "", ip, nil)
	if strings.Contains(body, token) {
		t.Fatal("bundle leaked")
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("log leaked")
	}
	if len(fake.Pushed) == 0 {
		t.Fatal("running machine was not told about the token")
	}
	for _, raw := range fake.Pushed {
		if strings.Contains(string(raw), token) {
			t.Fatal("push carried the raw token")
		}
	}
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/cli/"+cliID(t, h, agent)+"/revoke", h.form(cliForm()), ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Revoked") || strings.Contains(body, token) {
		t.Fatalf("revoke %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/machine", "", ip, nil)
	if !strings.Contains(body, "Revoked") || strings.Contains(body, token) {
		t.Fatal("revoke page")
	}

	_, bSecret := h.account("b-cli@example.com", "User B", "correct-horse-1")
	h.do(http.MethodPost, "/logout", h.form(cliForm()), ip, nil)
	h.login("b-cli@example.com", "correct-horse-1", bSecret, "203.0.113.211:9", false)
	for _, path := range []struct {
		method, path string
	}{
		{http.MethodPost, "/agents/" + agent.ID + "/cli"},
		{http.MethodGet, "/agents/" + agent.ID + "/machine"},
		{http.MethodPost, "/agents/" + agent.ID + "/cli/" + cliID(t, h, agent) + "/revoke"},
	} {
		code, _, _ := h.do(path.method, path.path, h.form(cliForm()), "203.0.113.211:9", nil)
		if code != http.StatusNotFound {
			t.Fatalf("B %s %d", path.path, code)
		}
	}
}

func TestHistoryParity(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	fake := host.NewFake()
	h.srv.SetHost(&castHost{Fake: fake}, "example.invalid/castbot-runtime:s06", "https://castbot.example")
	user, secret := h.account("parity@example.com", "Owner", "correct-horse-1")
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	_, cliToken, err := h.st.IssueCLIToken(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(h.srv.Handler())
	t.Cleanup(app.Close)
	sup, err := castletNew(t, app.URL, up.URL, agent.ID, agentToken)
	if err != nil {
		t.Fatal(err)
	}
	guest := httptest.NewServer(sup)
	t.Cleanup(guest.Close)
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-cli", guest.URL, "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, agentToken); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-cli"] = host.Machine{ID: "vm-cli", State: "running", Endpoint: guest.URL}
	for _, text := range []string{"one", "two", "three"} {
		if code := cliSend(guest.URL, cliToken, "sess-cli-1", text); code != http.StatusOK {
			t.Fatalf("cli %s %d", text, code)
		}
	}
	thread := cliThread(t, h.st, user.ID, agent.ID, "sess-cli-1")
	beforeWeb := bodies(t, h.st, user.ID, thread.ID)
	if len(beforeWeb) != 6 {
		t.Fatalf("cli messages %d", len(beforeWeb))
	}
	if code := cliSend(guest.URL, agentToken, "sess-cli-1", "four"); code != http.StatusOK {
		t.Fatalf("control %d", code)
	}
	mid := bodies(t, h.st, user.ID, thread.ID)
	if strings.Contains(strings.Join(mid, "\n"), "four") {
		t.Fatal("control plane turn was pushed early")
	}
	pulled := getJournal(t, guest.URL, agentToken)
	postJournal(t, app.URL, agent.ID, agentToken, pulled)
	postJournal(t, app.URL, agent.ID, agentToken, pulled)
	got := bodies(t, h.st, user.ID, thread.ID)
	want := journalTexts(t, pulled)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("parity mismatch %d vs %d", len(got), len(want))
	}
	ip := "203.0.113.212:9"
	h.login("parity@example.com", "correct-horse-1", secret, ip, false)
	code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(cliForm("body", "from the web", "thread", thread.ID)), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK {
		t.Fatalf("web %d", code)
	}
	deadline := time.Now().Add(4 * time.Second)
	var transcript []runtime.Turn
	for time.Now().Before(deadline) {
		transcript = echo.Transcript("sess-cli-1")
		if containsTurn(transcript, "from the web") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !containsTurn(transcript, "from the web") {
		t.Fatal("web turn missed the cli session")
	}
	if containsTurn(echo.Transcript(thread.ID), "from the web") {
		t.Fatal("web turn used the thread id as the session")
	}
}

func TestWebTurnsStayOnTheDMThread(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	fake := host.NewFake()
	h.srv.SetHost(&castHost{Fake: fake}, "example.invalid/castbot-runtime:s06", "https://castbot.example")
	user, _ := h.account("dm-journal@example.com", "Owner", "correct-horse-1")
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(h.srv.Handler())
	t.Cleanup(app.Close)
	sup, err := castletNew(t, app.URL, up.URL, agent.ID, agentToken)
	if err != nil {
		t.Fatal(err)
	}
	guest := httptest.NewServer(sup)
	t.Cleanup(guest.Close)
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-dm", guest.URL, "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, agentToken); err != nil {
		t.Fatal(err)
	}
	first, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, "user", user.ID, "yes")
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, "user", user.ID, "yes")
	if err != nil {
		t.Fatal(err)
	}
	before := bodies(t, h.st, user.ID, thread.ID)
	for _, id := range []string{first.ID, second.ID} {
		payload := `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"role":"user","messageId":"` + id + `","parts":[{"kind":"text","text":"yes"}]},"metadata":{"sessionId":"` + thread.ID + `","streamId":"run-` + id + `"}}}`
		req, err := http.NewRequest(http.MethodPost, guest.URL+"/", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Cast-Authorization", "Bearer "+agentToken)
		req.Header.Set("Accept", "text/event-stream")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("send %d", res.StatusCode)
		}
	}
	pulled := getJournal(t, guest.URL, agentToken)
	postJournal(t, app.URL, agent.ID, agentToken, pulled)
	postJournal(t, app.URL, agent.ID, agentToken, pulled)
	if strings.Join(bodies(t, h.st, user.ID, thread.ID), "\n") != strings.Join(before, "\n") {
		t.Fatal("web turns were copied")
	}
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.Kind == "cli" {
			t.Fatal("web thread id opened a cli thread")
		}
	}
}

type failPush struct {
	*host.Fake
}

func (f failPush) PushCLI(context.Context, string, string, []byte) error {
	return errors.New("unreachable")
}

func TestRevokeReportsAPushFailure(t *testing.T) {
	ip := "203.0.113.214:9"
	h, fake, agent := machineUser(t, ip)
	h.srv.SetHost(&failPush{Fake: fake}, "example.invalid/castbot-runtime:s06", "https://castbot.example")
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-cli", "https://demo.microvm.ondigitalocean.com", "running", ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, agentToken); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-cli"] = host.Machine{ID: "vm-cli", State: "running", Endpoint: "https://demo.microvm.ondigitalocean.com"}
	id, _, err := h.st.IssueCLIToken(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/cli/"+id+"/revoke", h.form(cliForm()), ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Revoked") || !strings.Contains(body, "did not accept the token change") || !strings.Contains(body, "The token stops working within 30 seconds.") {
		t.Fatalf("revoke %d", code)
	}
}

func TestCLIUseUpdatesLastUsed(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	fake := host.NewFake()
	h.srv.SetHost(&castHost{Fake: fake}, "example.invalid/castbot-runtime:s06", "https://castbot.example")
	user, _ := h.account("used-cli@example.com", "Owner", "correct-horse-1")
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper"})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentToken, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	id, cliToken, err := h.st.IssueCLIToken(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(h.srv.Handler())
	t.Cleanup(app.Close)
	sup, err := castletNew(t, app.URL, up.URL, agent.ID, agentToken)
	if err != nil {
		t.Fatal(err)
	}
	guest := httptest.NewServer(sup)
	t.Cleanup(guest.Close)
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-used", guest.URL, "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, agentToken); err != nil {
		t.Fatal(err)
	}
	if code := cliSend(guest.URL, cliToken, "sess-used", "hello"); code != http.StatusOK {
		t.Fatalf("cli %d", code)
	}
	listed, err := h.st.ListCLITokens(context.Background(), user.ID, agent.ID)
	if err != nil || len(listed) != 1 || listed[0].ID != id || listed[0].LastUsedAt == "" {
		t.Fatalf("last used %+v %v", listed, err)
	}
}

func TestApplyPendingRechecksStatus(t *testing.T) {
	ip := "203.0.113.213:9"
	h, fake, agent := machineUser(t, ip)
	if _, err := h.st.BeginVM(context.Background(), agent.UserID, agent.ID, host.MachineName(agent.ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), agent.UserID, agent.ID, "vm-pause", "https://vm.example", "running", ""); err != nil {
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
	fake.Machines["vm-pause"] = host.Machine{ID: "vm-pause", State: "running", Endpoint: "https://vm.example"}
	fake.SetForce("paused")
	if err := h.st.SetPendingBundle(context.Background(), agent.UserID, agent.ID, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := h.srv.applyPending(context.Background(), agent.UserID, agent.ID); err != nil {
		t.Fatal(err)
	}
	calls, edge := fake.Snapshot()
	if containsCall(calls, "reload") || containsCall(calls, "wake") || edge != 0 {
		t.Fatalf("calls %v edge %d", calls, edge)
	}
	pending, err := h.st.PendingBundle(context.Background(), agent.UserID, agent.ID)
	if err != nil || pending != "v1" {
		t.Fatalf("pending %s %v", pending, err)
	}
	vm, err := h.st.LiveVM(context.Background(), agent.UserID, agent.ID)
	if err != nil || vm.State != "paused" {
		t.Fatalf("state %+v %v", vm, err)
	}
}

func TestWorkaroundDocDoesNotClaimCLIChat(t *testing.T) {
	raw, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, needle := range []string{"cast shutdown", "cast upgrade", "does not complete CLI chat", "~/.cast", "SHA-256", "volume_config", "unpatched Cast does not refuse", "0600"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("missing %s", needle)
		}
	}
	if strings.Contains(text, "\u2014") || strings.Contains(text, "\u2013") {
		t.Fatal("em dash")
	}
}

func cliForm(kv ...string) url.Values {
	out := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = []string{kv[i+1]}
	}
	return out
}

func blobToken(t *testing.T, body string) string {
	t.Helper()
	best := ""
	for i := 0; i < len(body); i++ {
		if !strings.HasPrefix(body[i:], "cbt_") {
			continue
		}
		j := i + 4
		for j < len(body) {
			c := body[j]
			if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
				j++
				continue
			}
			break
		}
		if j-i > len(best) {
			best = body[i:j]
		}
	}
	if len(best) < 20 {
		t.Fatal("blob missing")
	}
	return best
}

func castletNew(t *testing.T, control, runtimeURL, agentID, token string) (*castlet.Supervisor, error) {
	t.Helper()
	sup, err := castlet.New(castlet.Config{
		Token: token, RuntimeURL: runtimeURL, AgentDir: t.TempDir(),
		ControlURL: control, AgentID: agentID, AllowHTTP: true,
	})
	if err != nil {
		return nil, err
	}
	sup.MarkReady()
	return sup, nil
}

func cliID(t *testing.T, h *harness, agent store.Agent) string {
	t.Helper()
	listed, err := h.st.ListCLITokens(context.Background(), agent.UserID, agent.ID)
	if err != nil || len(listed) == 0 {
		t.Fatal(err)
	}
	return listed[0].ID
}

func cliSend(base, token, session, text string) int {
	body := `{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"` + text + `"}]},"metadata":{"sessionId":"` + session + `"}}}`
	req, err := http.NewRequest(http.MethodPost, base+"/", strings.NewReader(body))
	if err != nil {
		return 0
	}
	req.Header.Set("X-Cast-Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func cliThread(t *testing.T, st *store.Store, userID, agentID, session string) store.Thread {
	t.Helper()
	threads, err := st.ListThreadsByAgent(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.Kind == "cli" && th.CastSessionID == session {
			return th
		}
	}
	t.Fatal("missing cli thread")
	return store.Thread{}
}

func bodies(t *testing.T, st *store.Store, userID, threadID string) []string {
	t.Helper()
	msgs, err := st.ListMessages(context.Background(), userID, threadID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		out = append(out, m.Body)
	}
	return out
}

func getJournal(t *testing.T, base, token string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, base+"/castlet/journal?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Cast-Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("journal %d", res.StatusCode)
	}
	return string(raw)
}

func postJournal(t *testing.T, base, agentID, token, raw string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+"/api/castlet/agents/"+agentID+"/journal", strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("ingest %d %s", res.StatusCode, body)
	}
}

func journalTexts(t *testing.T, raw string) []string {
	t.Helper()
	var body struct {
		Entries []store.JournalEntry `json:"entries"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range body.Entries {
		if e.Kind == "request" || e.Kind == "reply" {
			if strings.TrimSpace(e.Text) != "" {
				out = append(out, e.Text)
			}
		}
	}
	return out
}

func containsTurn(turns []runtime.Turn, text string) bool {
	for _, turn := range turns {
		if strings.Contains(turn.Text, text) {
			return true
		}
	}
	return false
}
