package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/handoff"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestAgentsDelegate(t *testing.T) {
	client := &coordClient{}
	h := newHarnessWith(t, client)
	ip := "203.0.113.87:4000"
	user, chief, _, thread := talkCrew(t, h, ip)
	ctx := context.Background()
	if _, err := h.st.PutBundle(ctx, user.ID, chief.ID, "1", "name: Chief\n", "You coordinate the other agents.\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "SECRET-OWNER-TURN"); err != nil {
		t.Fatal(err)
	}
	code, roster, _ := h.do(http.MethodGet, "/agents/"+chief.ID+"/roster", "", ip, nil)
	if code != 200 || !strings.Contains(roster, `"title":"Writer"`) || !strings.Contains(roster, `"description":"Drafts"`) {
		t.Fatalf("roster %d %s", code, roster)
	}
	code, _, _ = h.do(http.MethodPost, "/agents/"+chief.ID+"/messages", h.form(url.Values{"body": {"Please split this job."}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("post %d", code)
	}
	msgs := waitTalk(t, h, user.ID, thread.ID, func(rows []store.Message) bool {
		text := talkText(rows)
		return strings.Contains(text, "Combined answer for the owner.") && strings.Contains(text, "thread=")
	})
	text := talkText(msgs)
	if !strings.Contains(text, "I asked the other agents.") {
		t.Fatalf("missing fallback\n%s", text)
	}
	if strings.Contains(text, "Agents you can ask") || strings.Contains(text, "castbot-handoff") {
		t.Fatalf("roster leaked onto the owner thread\n%s", text)
	}
	var dmID, chatID string
	threads, err := h.st.ListThreadsByAgent(ctx, user.ID, chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.Kind == "agent_dm" {
			dmID = th.ID
		}
		if th.Kind == "agent_chat" {
			chatID = th.ID
		}
		if th.ID != "" && (th.Kind == "agent_dm" || th.Kind == "agent_chat") && !strings.Contains(text, th.ID) {
			t.Fatalf("link missing %s\n%s", th.ID, text)
		}
	}
	if dmID == "" || chatID == "" {
		t.Fatalf("threads dm %q chat %q", dmID, chatID)
	}
	client.mu.Lock()
	sends := append([]runtime.Send(nil), client.sends...)
	client.mu.Unlock()
	specsSeen := 0
	chiefSawSecret := false
	for _, send := range sends {
		blob := send.Text
		for _, turn := range send.History {
			blob += "\n" + turn.Text
			if strings.Contains(turn.Text, "SECRET-OWNER-TURN") {
				chiefSawSecret = true
			}
		}
		if !strings.Contains(send.Text, "asked you to do this") {
			continue
		}
		specsSeen++
		if len(send.History) != 0 || strings.Contains(blob, "SECRET-OWNER-TURN") || strings.Contains(send.Text, "Please split this job.") {
			t.Fatalf("specialist saw owner turns\n%#v", send)
		}
	}
	if specsSeen != 3 {
		t.Fatalf("specialist sends %d", specsSeen)
	}
	if !chiefSawSecret {
		t.Fatal("chief history dropped the owner turn")
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+chief.ID+"?thread="+url.QueryEscape(thread.ID), "", ip, nil)
	if code != 200 || !strings.Contains(page, "Combined answer for the owner.") || !strings.Contains(page, dmID) || !strings.Contains(page, chatID) {
		t.Fatalf("owner page %d\n%s", code, page)
	}
	code, page, _ = h.do(http.MethodGet, "/agents/"+chief.ID+"?thread="+url.QueryEscape(dmID), "", ip, nil)
	if code != 200 || !strings.Contains(page, "Do the first part.") || !strings.Contains(page, "Finished the task.") || !strings.Contains(page, `class="who">Ada`) || !strings.Contains(page, `class="who">Chief`) {
		t.Fatalf("dm page %d\n%s", code, page)
	}
	code, page, _ = h.do(http.MethodGet, "/agents/"+chief.ID+"?thread="+url.QueryEscape(chatID), "", ip, nil)
	if code != 200 || !strings.Contains(page, "Do the shared part.") || !strings.Contains(page, `class="who">Bea`) || !strings.Contains(page, `class="who">Cam`) {
		t.Fatalf("chat page %d\n%s", code, page)
	}
	h.srv.runs.Wait(2 * time.Second)
}

func TestHandoffCapAndSpecialistRules(t *testing.T) {
	hold := make(chan struct{})
	client := &coordClient{specialistHold: hold}
	h := newHarnessWith(t, client)
	ip := "203.0.113.88:4000"
	user, chief, specs, thread := talkCrew(t, h, ip)
	ctx := context.Background()
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Eight tasks")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		code, body := postTalk(t, h, chief.ID, msg.ID, "dm", "Part "+strconv.Itoa(i), []string{specs[0].ID}, ip)
		if code != 200 {
			t.Fatalf("send %d %d %s", i, code, body)
		}
	}
	code, body := postTalk(t, h, chief.ID, msg.ID, "dm", "Part 9", []string{specs[0].ID}, ip)
	if code != http.StatusConflict || body != "Rejected\n" {
		t.Fatalf("ninth %d %q", code, body)
	}
	close(hold)

	code, body = postTalk(t, h, specs[0].ID, msg.ID, "dm", "Ask Bea", []string{specs[1].ID}, ip)
	if code != http.StatusConflict || body != "Rejected\n" {
		t.Fatalf("third agent %d %q", code, body)
	}
	code, body = postTalk(t, h, specs[0].ID, msg.ID, "chat", "A group", []string{chief.ID, specs[1].ID}, ip)
	if code != http.StatusConflict || body != "Rejected\n" {
		t.Fatalf("specialist chat %d %q", code, body)
	}
	code, body = postTalk(t, h, specs[0].ID, msg.ID, "dm", "A note for the chief", []string{chief.ID}, ip)
	if code != 200 {
		t.Fatalf("specialist dm %d %s", code, body)
	}
	var relay struct {
		Threads []string `json:"threads"`
	}
	if err := json.Unmarshal([]byte(body), &relay); err != nil || len(relay.Threads) != 1 {
		t.Fatalf("relay threads %s", body)
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+chief.ID+"?thread="+url.QueryEscape(thread.ID), "", ip, nil)
	if code != 200 || strings.Contains(page, "Reply to the owner if this changes the answer.") {
		t.Fatalf("relay prompt on the owner thread %d", code)
	}
	code, page, _ = h.do(http.MethodGet, "/agents/"+chief.ID+"?thread="+url.QueryEscape(relay.Threads[0]), "", ip, nil)
	if code != 200 || !strings.Contains(page, "A note for the chief") || !strings.Contains(page, `class="who">Ada`) {
		t.Fatalf("relay thread %d\n%s", code, page)
	}
	code, body = postTalk(t, h, chief.ID, msg.ID, "dm", "", []string{specs[0].ID}, ip)
	if code != http.StatusBadRequest {
		t.Fatalf("empty task %d %s", code, body)
	}
	h.srv.runs.Wait(2 * time.Second)
}

func TestHandoffAuthz(t *testing.T) {
	h := newHarnessWith(t, &coordClient{})
	ip := "203.0.113.89:4000"
	user, chief, specs, thread := talkCrew(t, h, ip)
	other, secret := h.account("other87@example.com", "Other", "correct-horse-1")
	ctx := context.Background()
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Private job")
	if err != nil {
		t.Fatal(err)
	}
	code, body := postTalk(t, h, chief.ID, msg.ID, "dm", "Do the first part.", []string{specs[0].ID}, ip)
	if code != 200 {
		t.Fatalf("seed %d %s", code, body)
	}
	var payload struct {
		Threads []string `json:"threads"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || len(payload.Threads) != 1 {
		t.Fatalf("threads %s %v", body, err)
	}
	useSession(t, h, other.ID, ip)
	_ = secret
	code, _, _ = h.do(http.MethodGet, "/agents/"+chief.ID+"/roster", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("roster %d", code)
	}
	code, _, _ = h.do(http.MethodGet, "/threads/"+payload.Threads[0], "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("thread %d", code)
	}
	code, body = postTalk(t, h, chief.ID, msg.ID, "dm", "Steal it", []string{specs[0].ID}, ip)
	if code != http.StatusNotFound {
		t.Fatalf("send %d %s", code, body)
	}
}

func TestSpecialistApprovalNamesAgent(t *testing.T) {
	client := &coordClient{}
	h := newHarnessWith(t, client)
	ip := "203.0.113.90:4000"
	user, chief, specs, thread := talkCrew(t, h, ip)
	ctx := context.Background()
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Need a tool")
	if err != nil {
		t.Fatal(err)
	}
	code, body := postTalk(t, h, chief.ID, msg.ID, "dm", "NEED-APPROVAL please", []string{specs[0].ID}, ip)
	if code != 200 {
		t.Fatalf("handoff %d %s", code, body)
	}
	var payload struct {
		Threads []string `json:"threads"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || len(payload.Threads) != 1 {
		t.Fatalf("threads %s", body)
	}
	threadID := payload.Threads[0]
	var approval store.Approval
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := h.st.ListThreadApprovals(ctx, user.ID, threadID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 && rows[0].Status == "pending" {
			approval = rows[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if approval.ID == "" {
		t.Fatal("no approval")
	}
	runRow, err := h.st.GetRun(ctx, user.ID, approval.RunID)
	if err != nil || runRow.Status != "waiting_approval" {
		t.Fatalf("run %#v %v", runRow, err)
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+chief.ID+"?thread="+url.QueryEscape(threadID), "", ip, nil)
	if code != 200 || !strings.Contains(page, `class="stamp">Ada`) || !strings.Contains(page, "bash") {
		t.Fatalf("card %d\n%s", code, page)
	}
	code, _, _ = h.do(http.MethodPost, "/runs/"+approval.RunID+"/approvals/"+approval.ID+"/approve", h.form(nil), ip, nil)
	if code != 200 {
		t.Fatalf("approve %d", code)
	}
	waitTalk(t, h, user.ID, threadID, func(rows []store.Message) bool {
		return strings.Contains(talkText(rows), "Finished the task.")
	})
	h.srv.runs.Wait(2 * time.Second)
}

func TestSpecialistWakeAndCap(t *testing.T) {
	client := &coordClient{}
	h := newHarnessWith(t, client)
	ip := "203.0.113.91:4000"
	user, chief, specs, thread := talkCrew(t, h, ip)
	ctx := context.Background()
	fake := host.NewFake()
	text, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"result":  map[string]any{"parts": []map[string]string{{"kind": "text", "text": "Finished the task."}}},
	})
	fake.Frames = []runtime.Frame{{Data: string(text), DataEvent: true}}
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s06", "https://castbot.example")
	plain, err := h.st.IssueBootstrap(ctx, user.ID, specs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(ctx, specs[0].ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, user.ID, specs[0].ID, host.MachineName(specs[0].ID), "example.invalid/castbot-runtime:s06"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(ctx, user.ID, specs[0].ID, "vm-ada", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(ctx, specs[0].ID, token); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-ada"] = host.Machine{ID: "vm-ada", State: "paused", Endpoint: "https://vm.example"}
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Wake Ada")
	if err != nil {
		t.Fatal(err)
	}
	code, body := postTalk(t, h, chief.ID, msg.ID, "dm", "Do the first part.", []string{specs[0].ID}, ip)
	if code != 200 {
		t.Fatalf("wake send %d %s", code, body)
	}
	var payload struct {
		Threads []string `json:"threads"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || len(payload.Threads) != 1 {
		t.Fatalf("threads %s", body)
	}
	waitTalk(t, h, user.ID, payload.Threads[0], func(rows []store.Message) bool {
		return strings.Contains(talkText(rows), "Finished the task.")
	})
	waitTalk(t, h, user.ID, thread.ID, func(rows []store.Message) bool {
		text := talkText(rows)
		return strings.Contains(text, "Combined answer for the owner.") && !strings.Contains(text, "did not run")
	})

	if err := h.st.ApplyVMState(ctx, user.ID, specs[0].ID, "paused", "https://vm.example", ""); err != nil {
		t.Fatal(err)
	}
	fake.ForceState = ""
	fake.Machines["vm-ada"] = host.Machine{ID: "vm-ada", State: "paused", Endpoint: "https://vm.example"}
	fake.WakeErr = errWake
	msg2, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "Wake fails")
	if err != nil {
		t.Fatal(err)
	}
	code, body = postTalk(t, h, chief.ID, msg2.ID, "dm", "Try again", []string{specs[0].ID}, ip)
	if code != 200 {
		t.Fatalf("fail send %d %s", code, body)
	}
	waitTalk(t, h, user.ID, thread.ID, func(rows []store.Message) bool {
		return strings.Contains(talkText(rows), "Ada did not run. The machine is paused.")
	})
	h.srv.runs.Wait(2 * time.Second)
}

func TestSpecialistMachineCap(t *testing.T) {
	h := newHarnessWith(t, &coordClient{})
	ip := "203.0.113.92:4000"
	user, chief, specs, thread := talkCrew(t, h, ip)
	ctx := context.Background()
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s06", "https://castbot.example")
	if err := h.st.SetQuota(ctx, user.ID, 0); err != nil {
		t.Fatal(err)
	}
	msg, err := h.st.CreateMessage(ctx, user.ID, thread.ID, "user", user.ID, "No room")
	if err != nil {
		t.Fatal(err)
	}
	code, body := postTalk(t, h, chief.ID, msg.ID, "dm", "Do the first part.", []string{specs[0].ID}, ip)
	if code != 200 {
		t.Fatalf("cap send %d %s", code, body)
	}
	rows, err := h.st.ListMessages(ctx, user.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(talkText(rows), "Ada did not run. This account has reached its machine limit.") {
		t.Fatalf("cap copy\n%s", talkText(rows))
	}
	runs, err := h.st.ListThreadRuns(ctx, user.ID, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.AgentID == specs[0].ID {
			t.Fatal("specialist run started at the cap")
		}
	}
}

var errWake = wakeErr{}

type wakeErr struct{}

func (wakeErr) Error() string { return "down" }

func talkCrew(t *testing.T, h *harness, ip string) (store.User, store.Agent, []store.Agent, store.Thread) {
	t.Helper()
	user, secret := h.account("crew87@example.com", "Owner", "correct-horse-1")
	h.login("crew87@example.com", "correct-horse-1", secret, ip, false)
	ctx := context.Background()
	sec, err := h.st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	chief, thread, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: sec.ID, Name: "Chief", Title: "Lead", Description: "Coordinates", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	var specs []store.Agent
	for _, in := range []store.AgentInput{
		{SectionID: sec.ID, Name: "Ada", Title: "Writer", Description: "Drafts", Status: "ready"},
		{SectionID: sec.ID, Name: "Bea", Title: "Editor", Description: "Checks", Status: "ready"},
		{SectionID: sec.ID, Name: "Cam", Title: "Research", Description: "Looks things up", Status: "ready"},
	} {
		ag, _, err := h.st.CreateAgent(ctx, user.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, ag)
	}
	return user, chief, specs, thread
}

func postTalk(t *testing.T, h *harness, agentID, messageID, kind, task string, agents []string, ip string) (int, string) {
	t.Helper()
	vals := url.Values{"message": {messageID}, "kind": {kind}, "task": {task}}
	for _, id := range agents {
		vals.Add("agent", id)
	}
	code, body, _ := h.do(http.MethodPost, "/agents/"+agentID+"/handoff", h.form(vals), ip, nil)
	return code, body
}

func waitTalk(t *testing.T, h *harness, userID, threadID string, ok func([]store.Message) bool) []store.Message {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last []store.Message
	for time.Now().Before(deadline) {
		rows, err := h.st.ListMessages(context.Background(), userID, threadID)
		if err != nil {
			t.Fatal(err)
		}
		last = rows
		if ok(rows) {
			return rows
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout\n%s", talkText(last))
	return nil
}

func talkText(rows []store.Message) string {
	var b strings.Builder
	for _, row := range rows {
		b.WriteString(row.Body)
		b.WriteByte('\n')
	}
	return b.String()
}

type coordClient struct {
	mu             sync.Mutex
	sends          []runtime.Send
	holds          map[string]chan struct{}
	specialistHold <-chan struct{}
	seq            int
}

func (c *coordClient) Open(ctx context.Context, req runtime.Send) (runtime.Stream, error) {
	c.mu.Lock()
	c.sends = append(c.sends, req)
	hold := c.specialistHold
	c.mu.Unlock()
	if strings.Contains(req.Text, "NEED-APPROVAL") {
		return c.permStream(ctx), nil
	}
	body := coordReply(req.Text)
	frames := talkFrames(body)
	if hold != nil && strings.Contains(req.Text, "asked you to do this") {
		return &holdStream{ctx: ctx, hold: hold, frames: frames}, nil
	}
	return &scriptStream{frames: frames}, nil
}

func (c *coordClient) Respond(_ context.Context, requestID string, _ bool) error {
	c.mu.Lock()
	ch := c.holds[requestID]
	delete(c.holds, requestID)
	c.mu.Unlock()
	if ch == nil {
		return runtime.ErrNoRequest
	}
	close(ch)
	return nil
}

func (c *coordClient) permStream(ctx context.Context) runtime.Stream {
	c.mu.Lock()
	c.seq++
	id := "perm-" + strconv.Itoa(c.seq)
	ch := make(chan struct{})
	if c.holds == nil {
		c.holds = map[string]chan struct{}{}
	}
	c.holds[id] = ch
	c.mu.Unlock()
	perm, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"result": map[string]any{
			"metadata": map[string]any{
				"permissionRequest": map[string]any{
					"id": id, "tool": "bash", "arguments": map[string]any{"command": "ls"},
				},
			},
		},
	})
	frames := []runtime.Frame{{Data: string(perm), DataEvent: true}}
	frames = append(frames, talkFrames("Finished the task.")...)
	return &permStream{ctx: ctx, frames: frames, hold: ch}
}

func coordReply(text string) string {
	switch {
	case strings.Contains(text, "asked you to do this"):
		return "Finished the task."
	case strings.Contains(text, "Specialist replies"):
		return "Combined answer for the owner."
	case strings.Contains(text, "Agents you can ask"):
		return coordFence(text)
	default:
		return "Noted."
	}
}

func coordFence(text string) string {
	chief := ""
	const mark = "You are the chief "
	if i := strings.Index(text, mark); i >= 0 {
		rest := text[i+len(mark):]
		if j := strings.IndexAny(rest, ".\n"); j >= 0 {
			chief = strings.TrimSpace(rest[:j])
		}
	}
	var ids []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "- id ") {
			continue
		}
		rest := strings.TrimPrefix(line, "- id ")
		id := rest
		if k := strings.Index(rest, " | "); k >= 0 {
			id = rest[:k]
		}
		id = strings.TrimSpace(id)
		if id == "" || id == chief || id == "AGENT" {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) < 3 {
		return "I could not read the roster."
	}
	return handoff.Format(handoff.Plan{
		DM:   &handoff.Direct{Agent: ids[0], Task: "Do the first part."},
		Chat: &handoff.Group{Agents: []string{ids[1], ids[2]}, Task: "Do the shared part."},
	})
}

func talkFrames(body string) []runtime.Frame {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"result":  map[string]any{"parts": []map[string]string{{"kind": "text", "text": body}}},
	})
	return []runtime.Frame{
		{Data: string(raw), DataEvent: true},
		{Data: "[DONE]", DataEvent: true},
	}
}

type holdStream struct {
	ctx    context.Context
	hold   <-chan struct{}
	frames []runtime.Frame
	i      int
	waited bool
}

func (s *holdStream) Next() (runtime.Frame, error) {
	if !s.waited {
		s.waited = true
		select {
		case <-s.hold:
		case <-s.ctx.Done():
			return runtime.Frame{}, s.ctx.Err()
		}
	}
	if s.i >= len(s.frames) {
		return runtime.Frame{}, io.EOF
	}
	f := s.frames[s.i]
	s.i++
	return f, nil
}

func (s *holdStream) Close() error { return nil }

type permStream struct {
	ctx    context.Context
	frames []runtime.Frame
	hold   <-chan struct{}
	i      int
	held   bool
}

func (s *permStream) Next() (runtime.Frame, error) {
	if s.i == 1 && !s.held {
		s.held = true
		select {
		case <-s.hold:
		case <-s.ctx.Done():
			return runtime.Frame{}, s.ctx.Err()
		}
	}
	if s.i >= len(s.frames) {
		return runtime.Frame{}, io.EOF
	}
	f := s.frames[s.i]
	s.i++
	return f, nil
}

func (s *permStream) Close() error { return nil }
