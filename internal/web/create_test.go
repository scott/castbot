package web

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

type countDescribe struct {
	mu   sync.Mutex
	n    int
	last string
}

func (c *countDescribe) Describe(_ context.Context, text string) (Proposal, error) {
	c.mu.Lock()
	c.n++
	c.last = text
	c.mu.Unlock()
	return localProposal(text), nil
}

func (c *countDescribe) calls() (int, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n, c.last
}

func createLab(t *testing.T) (*harness, *host.Fake, *countDescribe) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	fake.OnCreate = func(spec host.CreateSpec) {
		guest, err := h.st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			return
		}
		_ = h.st.SaveVMToken(context.Background(), spec.AgentID, guest)
	}
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s20", "https://castbot.example")
	desc := &countDescribe{}
	h.srv.SetDescribe(desc)
	return h, fake, desc
}

func addModelKey(t *testing.T, h *harness, userID string) {
	t.Helper()
	_, err := h.st.CreateSecret(context.Background(), userID, store.SecretInput{
		Name: "PROVIDER_A_KEY", Label: "provider-a-main", Provider: "a", Value: "demo-model-key",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func follow(t *testing.T, h *harness, ip string, code int, hdr http.Header) string {
	t.Helper()
	if code != http.StatusSeeOther {
		t.Fatalf("status %d", code)
	}
	loc := hdr.Get("Location")
	code, body, _ := h.do(http.MethodGet, loc, "", ip, nil)
	if code != 200 {
		t.Fatalf("follow %s %d", loc, code)
	}
	return body
}

func agentRows(t *testing.T, h *harness, userID string) []store.Agent {
	t.Helper()
	rows, err := h.st.ListAgents(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func waitBoot(t *testing.T, h *harness, userID, agentID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		agent, err := h.st.GetAgent(context.Background(), userID, agentID)
		if err == nil && (agent.Status == "ready" || agent.Status == "failed") {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("machine did not finish")
}

func waitGreet(t *testing.T, h *harness, userID, agentID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g, err := h.st.AgentGreeted(context.Background(), userID, agentID)
		if err == nil && g != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("greeting did not start")
}

func greetBodies(t *testing.T, h *harness, userID, agentID string) []string {
	t.Helper()
	th, err := h.st.DMThread(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := h.st.ListMessages(context.Background(), userID, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, msg := range msgs {
		if msg.Origin == "greet" {
			out = append(out, msg.Body)
		}
	}
	return out
}

func TestDescribeUsesTheFake(t *testing.T) {
	h, fake, desc := createLab(t)
	user, secret := h.account("describe@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.201:9"
	h.login("describe@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Check example-org/docs for broken links every morning.")), ip, nil)
	body := follow(t, h, ip, code, hdr)
	n, last := desc.calls()
	if n != 1 || !strings.Contains(last, "broken links") {
		t.Fatalf("describe calls %d %q", n, last)
	}
	if !strings.Contains(body, "Scout") || !strings.Contains(body, "broken links") || !strings.Contains(body, "Create Scout") {
		t.Fatalf("card missing\n%s", body)
	}
	if len(agentRows(t, h, user.ID)) != 0 || len(fake.Creates) != 0 {
		t.Fatal("describe created an agent or a machine")
	}
}

func TestAutoName(t *testing.T) {
	h, _, _ := createLab(t)
	user, secret := h.account("auto@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.202:9"
	h.login("auto@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Check example-org/docs for broken links every morning.")), ip, nil)
	body := follow(t, h, ip, code, hdr)
	if !strings.Contains(body, "Scout") || !strings.Contains(body, "Picked by castbot") {
		t.Fatalf("auto name\n%s", body)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Scout", Title: "Taken", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	code, _, hdr = h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Check example-org/other for broken links every morning.")), ip, nil)
	body = follow(t, h, ip, code, hdr)
	if strings.Contains(body, `sname">Scout</p>`) || !strings.Contains(body, "Create Relay") {
		t.Fatal("auto name collided")
	}
}

func TestNameFromText(t *testing.T) {
	h, _, _ := createLab(t)
	user, secret := h.account("named@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.203:9"
	h.login("named@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Make me an agent called Scout that checks example-org/docs for broken links.")), ip, nil)
	body := follow(t, h, ip, code, hdr)
	if !strings.Contains(body, "Create Scout") || strings.Contains(body, "Picked by castbot") {
		t.Fatalf("name from text\n%s", body)
	}
}

func TestNameConflict(t *testing.T) {
	h, fake, _ := createLab(t)
	user, secret := h.account("clash@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.204:9"
	h.login("clash@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Docs", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Make me an agent called Atlas for the Spanish docs in example-org/docs.")), ip, nil)
	body := follow(t, h, ip, code, hdr)
	if !strings.Contains(body, "You already have an agent named Atlas") || !strings.Contains(body, "Atlas Spanish") || !strings.Contains(body, "Pick a different name") {
		t.Fatalf("conflict copy\n%s", body)
	}
	if !strings.Contains(body, ">Edit<") || !strings.Contains(body, "Advanced") || !strings.Contains(body, "disabled") {
		t.Fatal("edit or create state")
	}
	before := len(agentRows(t, h, user.ID))
	code, _, hdr = h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
	if code == http.StatusSeeOther {
		follow(t, h, ip, code, hdr)
	}
	if len(agentRows(t, h, user.ID)) != before || len(fake.Creates) != 0 {
		t.Fatalf("conflict create %d agents %d", code, len(agentRows(t, h, user.ID)))
	}
	code, _, hdr = h.do(http.MethodPost, "/new-agent/save", h.form(url.Values{"name": {"Atlas Spanish"}, "job": {"Check the Spanish docs."}, "instructions": {"Ask before you push."}}), ip, nil)
	body = follow(t, h, ip, code, hdr)
	if strings.Contains(body, "disabled") || !strings.Contains(body, "Create Atlas Spanish") {
		t.Fatalf("renamed card\n%s", body)
	}
}

func TestConfirmOnlyOnTheCard(t *testing.T) {
	h, fake, _ := createLab(t)
	user, secret := h.account("confirm@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.205:9"
	h.login("confirm@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Check example-org/docs for broken links every morning.")), ip, nil)
	follow(t, h, ip, code, hdr)
	if len(agentRows(t, h, user.ID)) != 0 {
		t.Fatal("describe inserted an agent")
	}
	code, body, _ := h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"nope"}}), ip, nil)
	if code != http.StatusBadRequest || len(agentRows(t, h, user.ID)) != 0 || len(fake.Creates) != 0 {
		t.Fatalf("bare create %d %s", code, body)
	}
	code, _, hdr = h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("card create %d", code)
	}
	created := agentRows(t, h, user.ID)
	if len(created) != 1 {
		t.Fatalf("agents %d", len(created))
	}
	waitBoot(t, h, user.ID, created[0].ID)
	if len(fake.Creates) != 1 {
		t.Fatalf("creates %d", len(fake.Creates))
	}
	if strings.Contains(hdr.Get("Location"), "/edit") || strings.Contains(hdr.Get("Location"), "/machine") {
		t.Fatal(hdr.Get("Location"))
	}
}

func TestAdvancedCreateStarts(t *testing.T) {
	h, fake, _ := createLab(t)
	user, secret := h.account("advanced@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.206:9"
	h.login("advanced@example.com", "correct-horse-1", secret, ip, false)
	code, blank, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != 200 || strings.Contains(blank, `value="Scout"`) || !strings.Contains(blank, "Create and start") || !strings.Contains(blank, "Back to the chat") {
		t.Fatalf("blank advanced %d", code)
	}
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Make me an agent called Scout that checks example-org/docs for broken links.")), ip, nil)
	follow(t, h, ip, code, hdr)
	code, pre, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != 200 || !strings.Contains(pre, `value="Scout"`) || !strings.Contains(pre, "From your chat") || !strings.Contains(pre, "broken links") {
		t.Fatalf("prefill\n%s", pre)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	fields := map[string][]string{
		"form": {"agent"}, "confirm": {"advanced"}, "name": {"Scout"}, "title": {"Link checker"},
		"section": {sec.ID}, "model": {model}, "instructions": {"Check the docs."}, "job": {"Check example-org/docs for broken links."},
	}
	code, body, hdr := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther || strings.Contains(hdr.Get("Location"), "/edit") || !strings.HasPrefix(hdr.Get("Location"), "/agents/") {
		t.Fatalf("advanced create %d %s %s", code, hdr.Get("Location"), body)
	}
	made := agentRows(t, h, user.ID)
	if len(made) != 1 {
		t.Fatalf("agents %d", len(made))
	}
	waitBoot(t, h, user.ID, made[0].ID)
	if len(fake.Creates) != 1 {
		t.Fatalf("creates %d", len(fake.Creates))
	}
	if !strings.HasPrefix(hdr.Get("Location"), "/agents/") {
		t.Fatal(hdr.Get("Location"))
	}
}

func TestCreateRedirectsWithoutAMachineStep(t *testing.T) {
	h, fake, _ := createLab(t)
	user, secret := h.account("redirect@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.207:9"
	h.login("redirect@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Make me an agent called Scout that checks example-org/docs for broken links.")), ip, nil)
	follow(t, h, ip, code, hdr)
	code, _, hdr = h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
	loc := hdr.Get("Location")
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/agents/") || strings.Contains(loc, "/edit") || strings.Contains(loc, "/machine") {
		t.Fatalf("redirect %d %s", code, loc)
	}
	made := agentRows(t, h, user.ID)
	if len(made) != 1 {
		t.Fatal("no agent")
	}
	waitBoot(t, h, user.ID, made[0].ID)
	if len(fake.Creates) != 1 || fake.Creates[0].CPU != 1 || fake.Creates[0].MemoryMiB != 2048 || fake.Creates[0].Idle != 15*time.Minute {
		t.Fatalf("create spec %+v", fake.Creates)
	}
	body := follow(t, h, ip, code, hdr)
	if !strings.Contains(body, "is starting up") && !strings.Contains(body, "is up") {
		t.Fatalf("boot card\n%s", body)
	}
}

func TestStartingUpHoldsTheWaitingMessage(t *testing.T) {
	h, fake, _ := createLab(t)
	hold := make(chan struct{})
	fake.WaitCreate = hold
	user, secret := h.account("wait@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.208:9"
	h.login("wait@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Make me an agent called Scout that checks example-org/docs for broken links.")), ip, nil)
	follow(t, h, ip, code, hdr)
	code, _, hdr = h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	agent := agentRows(t, h, user.ID)[0]
	page := follow(t, h, ip, code, hdr)
	if !strings.Contains(page, "Scout is starting up") {
		t.Fatalf("starting card\n%s", page)
	}
	code, _, hdr = h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(urlValues("Start with the pricing page.")), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	th, err := h.st.DMThread(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := h.st.ListMessages(context.Background(), user.ID, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	var waiting bool
	for _, msg := range msgs {
		if msg.AuthorKind == "user" && strings.Contains(msg.Body, "pricing page") {
			if msg.Origin != "waiting" {
				t.Fatalf("origin %s", msg.Origin)
			}
			waiting = true
		}
	}
	if !waiting {
		t.Fatal("waiting message was dropped")
	}
	runs, err := h.st.ListThreadRuns(context.Background(), user.ID, th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("ran before greet: %d", len(runs))
	}
	close(hold)
	waitGreet(t, h, user.ID, agent.ID)
	bodies := greetBodies(t, h, user.ID, agent.ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "pricing page") {
		t.Fatalf("first turn %v", bodies)
	}
	msgs, _ = h.st.ListMessages(context.Background(), user.ID, th.ID)
	for _, msg := range msgs {
		if strings.Contains(msg.Body, "pricing page") && msg.Origin == "waiting" {
			t.Fatal("still waiting after greet")
		}
	}
}

func TestGreetOnce(t *testing.T) {
	h, fake, _ := createLab(t)
	user, secret := h.account("once@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.209:9"
	h.login("once@example.com", "correct-horse-1", secret, ip, false)
	agent := makeChatAgent(t, h, user.ID, ip, "Scout", "Check example-org/docs for broken links.")
	waitGreet(t, h, user.ID, agent.ID)
	if n := len(greetBodies(t, h, user.ID, agent.ID)); n != 1 {
		t.Fatalf("greet messages %d", n)
	}
	if _, _, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil); len(greetBodies(t, h, user.ID, agent.ID)) != 1 {
		t.Fatal("reload greeted again")
	}
	h.srv.maybeGreet(context.Background(), user.ID, agent.ID)
	if err := h.srv.reconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeGreet(context.Background(), user.ID, agent.ID)
	if n := len(greetBodies(t, h, user.ID, agent.ID)); n != 1 {
		t.Fatalf("second ready %d", n)
	}
	if len(fake.Creates) != 1 {
		t.Fatalf("creates %d", len(fake.Creates))
	}
}

func TestGreetingWithAJob(t *testing.T) {
	h, _, _ := createLab(t)
	user, secret := h.account("job@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.210:9"
	h.login("job@example.com", "correct-horse-1", secret, ip, false)
	agent := makeChatAgent(t, h, user.ID, ip, "Scout", "Make me an agent called Scout that checks example-org/docs for broken links.")
	waitGreet(t, h, user.ID, agent.ID)
	bodies := greetBodies(t, h, user.ID, agent.ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "Hi Alex, I am Scout") || !strings.Contains(bodies[0], "Nice to meet you") || !strings.Contains(bodies[0], "broken links") || strings.Contains(bodies[0], "What should my job be") || strings.Contains(bodies[0], "!") {
		t.Fatalf("job greeting %v", bodies)
	}
}

func TestGreetingWithoutAJob(t *testing.T) {
	h, _, _ := createLab(t)
	user, secret := h.account("nojob@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.211:9"
	h.login("nojob@example.com", "correct-horse-1", secret, ip, false)
	agent := makeChatAgent(t, h, user.ID, ip, "Juno", "Call it Juno. I will tell it what to do later.")
	waitGreet(t, h, user.ID, agent.ID)
	bodies := greetBodies(t, h, user.ID, agent.ID)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "What should my job be") || strings.Contains(bodies[0], "My job:") || strings.Contains(bodies[0], "!") {
		t.Fatalf("empty job greeting %v", bodies)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		fresh, err := h.st.GetAgent(context.Background(), user.ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Activity == "waiting for a job" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("activity %q", fresh.Activity)
		}
		time.Sleep(20 * time.Millisecond)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(body, "Watch a repo for new issues") || !strings.Contains(body, "Tell Juno what its job is") || !strings.Contains(body, "waiting for a job") {
		t.Fatalf("ask job page %d\n%s", code, body)
	}
}

func TestBootFailureAndTryAgain(t *testing.T) {
	h, fake, _ := createLab(t)
	h.srv.SetBootBudget(200 * time.Millisecond)
	fake.WaitCreate = make(chan struct{})
	user, secret := h.account("boot@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.212:9"
	h.login("boot@example.com", "correct-horse-1", secret, ip, false)
	started := time.Now()
	agent := makeChatAgent(t, h, user.ID, ip, "Scout", "Make me an agent called Scout that checks example-org/docs for broken links.")
	deadline := time.Now().Add(3 * time.Second)
	for {
		fresh, err := h.st.GetAgent(context.Background(), user.ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("budget ignored, status %s after %s", fresh.Status, time.Since(started))
		}
		time.Sleep(20 * time.Millisecond)
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(body, "did not start") || !strings.Contains(body, "4 minutes") || !strings.Contains(body, "Try again") || !strings.Contains(body, "Machine details") || !strings.Contains(body, "?panel=machine") || !strings.Contains(body, "says hello only once") {
		t.Fatalf("failure card\n%s", body)
	}
	fake.WaitCreate = nil
	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/boot/retry", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("retry %d", code)
	}
	waitGreet(t, h, user.ID, agent.ID)
	if n := len(greetBodies(t, h, user.ID, agent.ID)); n != 1 {
		t.Fatalf("greet after retry %d", n)
	}
	if len(fake.Creates) < 1 {
		t.Fatal("retry did not provision")
	}
	_ = hdr
}

func TestNoSecretsInInstructionsOrLogs(t *testing.T) {
	h, _, desc := createLab(t)
	var logs bytes.Buffer
	h.srv.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	user, secret := h.account("redact@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.213:9"
	h.login("redact@example.com", "correct-horse-1", secret, ip, false)
	token := strings.Join([]string{"sk", "demo", "EXAMPLE", "0000", "notlive"}, "-")
	text := "Make me an agent called Scout that checks example-org/docs for broken links. Token " + token
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues(text)), ip, nil)
	body := follow(t, h, ip, code, hdr)
	if strings.Contains(body, token) || !strings.Contains(body, "I left the token out") || !strings.Contains(body, "********") {
		t.Fatal("page kept or hid the note")
	}
	_, sent := desc.calls()
	if strings.Contains(sent, token) {
		t.Fatal("model client saw the token")
	}
	draft, err := h.st.LoadCreateDraft(context.Background(), user.ID)
	if err != nil || strings.Contains(draft.Payload, token) {
		t.Fatal("draft kept the token")
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("log kept the token")
	}
	code, _, hdr = h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	agent := agentRows(t, h, user.ID)[0]
	bundle, err := h.st.LatestBundle(context.Background(), user.ID, agent.ID)
	if err != nil || strings.Contains(bundle.Instructions, token) || strings.Contains(bundle.YAML, token) {
		t.Fatal("instructions kept the token")
	}
}

func clearServerProviderKeys(t *testing.T) {
	t.Helper()
	for _, slot := range modelSlots() {
		if name := castcfg.ProviderKeyEnv(slot.Manifest); name != "" {
			t.Setenv(name, "")
		}
	}
}

func TestCreateUsesServerKey(t *testing.T) {
	clearServerProviderKeys(t)
	slot, ok := slotForModel(defaultModelID())
	if !ok || slot.Env == "" {
		t.Fatal("default model has no provider slot")
	}
	envName := castcfg.ProviderKeyEnv(slot.Manifest)
	if envName == "" {
		t.Fatal("provider env name")
	}
	const serverKey = "server-side-demo"
	const userKey = "demo-model-key"
	scout := "Make me an agent called Scout that checks example-org/docs for broken links."

	t.Run("server key only", func(t *testing.T) {
		t.Setenv(envName, serverKey)
		h, fake, _ := createLab(t)
		user, secret := h.account("server-only@example.com", "Alex Rivera", "correct-horse-1")
		ip := "203.0.113.230:9"
		h.login("server-only@example.com", "correct-horse-1", secret, ip, false)
		card := describeCard(t, h, ip, scout)
		if strings.Contains(card, serverKey) {
			t.Fatal("card showed the server key")
		}
		if !cardReady(card, "Scout") || !strings.Contains(card, serverKeyShort) || strings.Contains(card, "From your vault · "+serverKeyShort) {
			t.Fatal("server key did not enable the card")
		}
		form := advancedPage(t, h, ip, serverKey)
		if strings.Contains(form, "Add a model key first") || !strings.Contains(form, "Create and start") || !strings.Contains(form, serverKeyShort) {
			t.Fatal("server key did not show on the form")
		}
		assertServerKeyNoteOnce(t, h, ip, serverKey)
		agent := createFromCard(t, h, fake, user.ID, ip, "Scout", serverKey)
		assertRunsWith(t, h, user.ID, agent.ID, ip, slot.Env, serverKey, "")
		relay := createFromForm(t, h, user, ip, "Relay", serverKey)
		if relay.StatusCode != http.StatusSeeOther {
			t.Fatal("form create did not succeed")
		}
		made := agentNamed(t, h, user.ID, "Relay")
		assertRunsWith(t, h, user.ID, made.ID, ip, slot.Env, serverKey, "")
		if len(fake.Creates) != 2 {
			t.Fatalf("creates %d", len(fake.Creates))
		}
	})

	t.Run("no key", func(t *testing.T) {
		t.Setenv(envName, "")
		h, fake, _ := createLab(t)
		user, secret := h.account("no-key@example.com", "Alex Rivera", "correct-horse-1")
		ip := "203.0.113.231:9"
		h.login("no-key@example.com", "correct-horse-1", secret, ip, false)
		card := describeCard(t, h, ip, scout)
		if cardReady(card, "Scout") || !strings.Contains(card, "Add a model key first") || !strings.Contains(card, "Add a key in Secrets") || !strings.Contains(card, "disabled") || !strings.Contains(card, "your own "+slot.Title+" key") {
			t.Fatal("missing key did not block the card")
		}
		code, _, hdr := h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
		if code == http.StatusSeeOther {
			follow(t, h, ip, code, hdr)
		}
		if len(agentRows(t, h, user.ID)) != 0 || len(fake.Creates) != 0 {
			t.Fatalf("card create without a key %d", code)
		}
		got := createFromForm(t, h, user, ip, "Scout", "")
		if got.StatusCode != http.StatusBadRequest || !strings.Contains(got.Body, "Add a model key first") || !strings.Contains(got.Body, "your own "+slot.Title+" key") {
			t.Fatal("form create without a key was not blocked")
		}
		if len(agentRows(t, h, user.ID)) != 0 || len(fake.Creates) != 0 {
			t.Fatal("form create without a key inserted an agent")
		}
	})

	t.Run("user key wins", func(t *testing.T) {
		t.Setenv(envName, serverKey)
		h, fake, _ := createLab(t)
		user, secret := h.account("user-wins@example.com", "Alex Rivera", "correct-horse-1")
		addModelKey(t, h, user.ID)
		ip := "203.0.113.232:9"
		h.login("user-wins@example.com", "correct-horse-1", secret, ip, false)
		card := describeCard(t, h, ip, scout)
		if strings.Contains(card, serverKey) || strings.Contains(card, userKey) {
			t.Fatal("card showed a key")
		}
		if !cardReady(card, "Scout") || !strings.Contains(card, "provider-a-main") || strings.Contains(card, serverKeyShort) {
			t.Fatal("vault key did not win on the card")
		}
		form := advancedPage(t, h, ip, serverKey)
		if strings.Contains(form, userKey) || !strings.Contains(form, "provider-a-main") || strings.Contains(form, serverKeyShort) || strings.Contains(form, "Add a model key first") {
			t.Fatal("vault key did not win on the form")
		}
		agent := createFromCard(t, h, fake, user.ID, ip, "Scout", serverKey)
		assertRunsWith(t, h, user.ID, agent.ID, ip, slot.Env, userKey, serverKey)
		code, secrets, _ := h.do(http.MethodGet, "/settings/secrets", "", ip, nil)
		if code != http.StatusOK || strings.Contains(secrets, serverKeyLabel) || !strings.Contains(secrets, "provider-a-main") || strings.Contains(secrets, serverKey) || strings.Contains(secrets, userKey) {
			t.Fatal("vault key did not replace the server note")
		}
		got := createFromForm(t, h, user, ip, "Relay", serverKey)
		if got.StatusCode != http.StatusSeeOther {
			t.Fatal("form create with a vault key did not succeed")
		}
		made := agentNamed(t, h, user.ID, "Relay")
		assertRunsWith(t, h, user.ID, made.ID, ip, slot.Env, userKey, serverKey)
	})
}

func describeCard(t *testing.T, h *harness, ip, text string) string {
	t.Helper()
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues(text)), ip, nil)
	return follow(t, h, ip, code, hdr)
}

func cardReady(body, name string) bool {
	return strings.Contains(body, "Create "+name) && !strings.Contains(body, "Add a model key first") && !strings.Contains(body, "keymiss") && !strings.Contains(body, "disabled")
}

func createFromCard(t *testing.T, h *harness, fake *host.Fake, userID, ip, name, secret string) store.Agent {
	t.Helper()
	before := len(fake.Creates)
	code, body, hdr := h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/agents/") {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatal("card create failed")
		}
		t.Fatalf("card create %d %s", code, hdr.Get("Location"))
	}
	page := follow(t, h, ip, code, hdr)
	if secret != "" && strings.Contains(page, secret) {
		t.Fatal("created page showed the key")
	}
	agent := agentNamed(t, h, userID, name)
	if len(fake.Creates) != before+1 {
		t.Fatalf("creates %d", len(fake.Creates))
	}
	return agent
}

type formResult struct {
	StatusCode int
	Body       string
}

func advancedPage(t *testing.T, h *harness, ip, secret string) string {
	t.Helper()
	code, body, _ := h.do(http.MethodGet, "/new-agent?advanced=1", "", ip, nil)
	if code != http.StatusOK {
		t.Fatalf("advanced %d", code)
	}
	if secret != "" && strings.Contains(body, secret) {
		t.Fatal("form showed the key")
	}
	return body
}

func createFromForm(t *testing.T, h *harness, user store.User, ip, name, secret string) formResult {
	t.Helper()
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{
		"form": {"agent"}, "confirm": {"advanced"}, "name": {name}, "title": {"Link checker"},
		"section": {sec.ID}, "model": {defaultModelID()}, "instructions": {"Check the docs."},
		"job": {"Check example-org/docs for broken links."},
	}
	code, body, _ := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if secret != "" && strings.Contains(body, secret) {
		t.Fatal("form response showed the key")
	}
	return formResult{StatusCode: code, Body: body}
}

func assertServerKeyNoteOnce(t *testing.T, h *harness, ip, secret string) {
	t.Helper()
	code, body, _ := h.do(http.MethodGet, "/settings/secrets", "", ip, nil)
	if code != http.StatusOK {
		t.Fatalf("secrets %d", code)
	}
	if secret != "" && strings.Contains(body, secret) {
		t.Fatal("secrets page showed the key")
	}
	if strings.Count(body, serverKeyLabel) != 1 {
		t.Fatalf("server key note count %d", strings.Count(body, serverKeyLabel))
	}
	if !strings.Contains(body, `class="lbl mono">`+serverKeyShort+`</span>`) {
		t.Fatal("secrets row did not label the server key")
	}
}

func assertRunsWith(t *testing.T, h *harness, userID, agentID, ip, envName, want, other string) {
	t.Helper()
	waitBoot(t, h, userID, agentID)
	_, token, err := h.st.VMCredentials(context.Background(), userID, agentID)
	if err != nil || token == "" {
		t.Fatal("machine token missing")
	}
	code, body, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agentID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK {
		t.Fatalf("secrets pull %d", code)
	}
	if !strings.Contains(body, envName) || !strings.Contains(body, want) {
		t.Fatal("pull did not include the key the agent runs with")
	}
	if other != "" && strings.Contains(body, other) {
		t.Fatal("pull included the other key")
	}
	bundle, err := h.st.LatestBundle(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bundle.YAML, want) || strings.Contains(bundle.Instructions, want) || (other != "" && (strings.Contains(bundle.YAML, other) || strings.Contains(bundle.Instructions, other))) {
		t.Fatal("bundle stored a key")
	}
}

func agentNamed(t *testing.T, h *harness, userID, name string) store.Agent {
	t.Helper()
	for _, row := range agentRows(t, h, userID) {
		if row.Name == name {
			return row
		}
	}
	t.Fatalf("missing %s", name)
	return store.Agent{}
}

func TestRepromptUnclear(t *testing.T) {
	h, fake, desc := createLab(t)
	user, secret := h.account("unclear@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.215:9"
	h.login("unclear@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("hi")), ip, nil)
	body := follow(t, h, ip, code, hdr)
	n, _ := desc.calls()
	if n != 0 || strings.Contains(body, "Create ") || !strings.Contains(body, "What should your agent do") || len(agentRows(t, h, user.ID)) != 0 || len(fake.Creates) != 0 {
		t.Fatalf("reprompt calls %d\n%s", n, body)
	}
}

func TestDraftExpiresAfter24Hours(t *testing.T) {
	h, _, _ := createLab(t)
	user, secret := h.account("draft@example.com", "Alex Rivera", "correct-horse-1")
	addModelKey(t, h, user.ID)
	ip := "203.0.113.216:9"
	h.login("draft@example.com", "correct-horse-1", secret, ip, false)
	start := time.Date(2026, 11, 12, 18, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return start })
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Make me an agent called Scout that checks example-org/docs for broken links.")), ip, nil)
	follow(t, h, ip, code, hdr)
	h.st.SetNow(func() time.Time { return start.Add(23 * time.Hour) })
	code, body, _ := h.do(http.MethodGet, "/new-agent", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Create Scout") {
		t.Fatal("draft dropped early")
	}
	h.st.SetNow(func() time.Time { return start.Add(25 * time.Hour) })
	code, body, _ = h.do(http.MethodGet, "/new-agent", "", ip, nil)
	if code != 200 || strings.Contains(body, "Create Scout") {
		t.Fatal("draft kept past 24h")
	}
}

func TestCreateAuthzAndCSRF(t *testing.T) {
	h, _, _ := createLab(t)
	owner, ownerSecret := h.account("owner@example.com", "Alex Rivera", "correct-horse-1")
	_, otherSecret := h.account("other@example.com", "Blair", "correct-horse-1")
	addModelKey(t, h, owner.ID)
	ip := "203.0.113.217:9"
	bip := "203.0.113.218:9"
	h.login("owner@example.com", "correct-horse-1", ownerSecret, ip, false)
	agent := makeChatAgent(t, h, owner.ID, ip, "Scout", "Make me an agent called Scout that checks example-org/docs for broken links.")
	draft, err := h.st.LoadCreateDraft(context.Background(), owner.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	// The create consumed the draft. Save one owned by the owner for the id check.
	saved, err := h.st.SaveCreateDraft(context.Background(), owner.ID, `{"turns":[]}`)
	if err != nil {
		t.Fatal(err)
	}
	draft = saved
	h.do(http.MethodPost, "/logout", h.form(url.Values{}), ip, nil)
	h.login("other@example.com", "correct-horse-1", otherSecret, bip, false)
	code, _, _ := h.do(http.MethodGet, "/new-agent/drafts/"+draft.ID, "", bip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("draft %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}, "draft": {draft.ID}}), bip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("create %d", code)
	}
	code, _, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", bip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("chat %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/boot/retry", h.form(url.Values{}), bip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("retry %d", code)
	}
	code, body, _ := h.do(http.MethodPost, "/new-agent/describe", "body=Check+example-org/docs+for+broken+links", bip, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
		t.Fatalf("csrf %d %s", code, body)
	}
}

func TestDescribeHTMXSwapsThePane(t *testing.T) {
	h, _, _ := createLab(t)
	_, secret := h.account("hx-describe@example.com", "Alex Rivera", "correct-horse-1")
	ip := "203.0.113.253:9"
	h.login("hx-describe@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/new-agent", "", ip, nil)
	if code != 200 {
		t.Fatalf("get %d", code)
	}
	for _, want := range []string{
		`class="comp-wrap"`,
		`data-create-compose="1"`,
		`hx-post="/new-agent/describe"`,
		`hx-target="#nmain"`,
		`hx-select="#nmain"`,
		`hx-swap="outerHTML"`,
		`hx-push-url="false"`,
		`id="nmain"`,
		`id="create-adv"`,
		`id="nfoot"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %s", want)
		}
	}
	if strings.Contains(body, `hx-swap-oob=`) {
		t.Fatal("the first page should not mark swaps out of band")
	}
	code, body, _ = h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues("Check example-org/docs for broken links every morning.")), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatalf("hx %d %s", code, snippet(body))
	}
	if strings.Contains(body, "<textarea") || strings.Contains(body, "hx-post=") || strings.Contains(body, "<html") {
		t.Fatal("swap included the composer or a full document")
	}
	for _, want := range []string{
		`id="nmain"`,
		`data-placeholder="Reply to change something`,
		`hx-swap-oob="outerHTML"`,
		"Create Scout",
		"broken links",
		`id="create-adv"`,
		`id="nfoot"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("swap missing %s\n%s", want, snippet(body))
		}
	}
	if strings.Contains(body, "nmain hero") {
		t.Fatal("setup card still uses the empty hero layout")
	}
	if !strings.Contains(body, `id="create-adv" hx-swap-oob="outerHTML"></span>`) {
		t.Fatal("header Advanced link stayed after the first message")
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func makeChatAgent(t *testing.T, h *harness, userID, ip, name, text string) store.Agent {
	t.Helper()
	code, _, hdr := h.do(http.MethodPost, "/new-agent/describe", h.form(urlValues(text)), ip, nil)
	page := follow(t, h, ip, code, hdr)
	if !strings.Contains(page, "Create "+name) {
		t.Fatalf("card for %s\n%s", name, page)
	}
	code, body, hdr := h.do(http.MethodPost, "/new-agent/create", h.form(url.Values{"confirm": {"card"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("create %d %s", code, body)
	}
	var agent store.Agent
	for _, row := range agentRows(t, h, userID) {
		if row.Name == name {
			agent = row
		}
	}
	if agent.ID == "" {
		t.Fatalf("missing %s", name)
	}
	if !strings.HasPrefix(hdr.Get("Location"), "/agents/"+agent.ID) {
		t.Fatal(hdr.Get("Location"))
	}
	return agent
}
