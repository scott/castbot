package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/chattool"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func TestMachineChatStopsRetriesAndOpensThreads(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("chat-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("chat-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	sibToken := guestToken(t, h, sibling)
	ip := "203.0.113.90:9"
	routine, err := h.st.CreateThread(ctx, user.ID, "routine", caller.ID, "Weekly link sweep")
	if err != nil {
		t.Fatal(err)
	}
	cli, err := h.st.CreateThread(ctx, user.ID, "cli", caller.ID, "Laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, user.ID, routine.ID, "user", user.ID, "Check the links."); err != nil {
		t.Fatal(err)
	}
	dm := dmThread(t, h, user.ID, caller.ID)
	if _, err := h.st.CreateMessage(ctx, user.ID, dm.ID, "user", user.ID, "Only in the chat."); err != nil {
		t.Fatal(err)
	}
	queued, err := h.st.CreateRun(ctx, user.ID, routine.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	cliRun, err := h.st.CreateRun(ctx, user.ID, cli.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(chattool.ToolStop, map[string]any{"thread": routine.ID}))
	if code != http.StatusNotFound || strings.Contains(body, "Stopped") {
		t.Fatalf("other token %d %s", code, body)
	}
	if runStatus(t, h, user.ID, queued.ID) != "queued" {
		t.Fatal("other token stopped the run")
	}
	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(chattool.ToolStop, map[string]any{"thread": routine.ID, "user_id": other.ID}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, sibToken, sibling.ID, "/agents", routinePayload(chattool.ToolStop, map[string]any{"thread": routine.ID}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "No thread matches that.") {
		t.Fatalf("sibling token %d %s", code, body)
	}
	if runStatus(t, h, user.ID, queued.ID) != "queued" {
		t.Fatal("sibling token stopped the run")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolList, map[string]any{"user_id": other.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Chat. Chat. "+dm.ID) || !strings.Contains(body, "Weekly link sweep. Routine. "+routine.ID) || !strings.Contains(body, "Laptop. Cast CLI. "+cli.ID) || strings.Contains(body, "Pip") {
		t.Fatalf("list %d %s", code, body)
	}
	code, listed := machinePost(t, h, ip, otherToken, otherAgent.ID, "/agents", routinePayload(chattool.ToolList, map[string]any{}))
	if code != http.StatusOK || strings.Contains(listed, routine.ID) || strings.Contains(listed, dm.ID) {
		t.Fatalf("other list %d %s", code, listed)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolOpen, map[string]any{"thread": "sweep", "agent_id": sibling.ID}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Open a thread from Chat.") || strings.Contains(body, "Check the links.") || strings.Contains(body, "Only in the chat.") {
		t.Fatalf("open %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolOpen, map[string]any{"thread": "Chat"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Open a thread from Chat.") || strings.Contains(body, "Only in the chat.") || strings.Contains(body, "Check the links.") {
		t.Fatalf("open chat %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolStop, map[string]any{"thread": "Laptop"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "A Cast CLI thread has no Stop.") {
		t.Fatalf("cli stop %d %s", code, body)
	}
	if runStatus(t, h, user.ID, cliRun.ID) != "queued" {
		t.Fatal("cli stop changed the run")
	}
	second, err := h.st.CreateThread(ctx, user.ID, "routine", caller.ID, "Morning digest")
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := h.st.CreateRun(ctx, user.ID, second.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolStop, map[string]any{"user_id": other.ID, "agent_id": sibling.ID}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "More than one reply is in progress.") {
		t.Fatalf("two stops %d %s", code, body)
	}
	if runStatus(t, h, user.ID, queued.ID) != "queued" || runStatus(t, h, user.ID, secondRun.ID) != "queued" {
		t.Fatal("unnamed stop picked a run")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolStop, map[string]any{"thread": "Weekly link sweep"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Stopped the reply.") {
		t.Fatalf("stop %d %s", code, body)
	}
	if runStatus(t, h, user.ID, queued.ID) != "stopped" || runStatus(t, h, user.ID, cliRun.ID) != "queued" || runStatus(t, h, user.ID, secondRun.ID) != "queued" {
		t.Fatal("stop touched the wrong run")
	}
	msgs, err := h.st.ListMessages(ctx, user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, msg := range msgs {
		if msg.Body == store.StoppedLine && msg.Origin == "notice" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("stopped line missing %+v", msgs)
	}

	failThread, err := h.st.CreateThread(ctx, user.ID, "routine", caller.ID, "Evening note")
	if err != nil {
		t.Fatal(err)
	}
	failRun(t, h, user.ID, caller.ID, dm.ID, "Please retry the chat.")
	failRun(t, h, user.ID, caller.ID, failThread.ID, "Please retry the note.")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolRetry, map[string]any{}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "More than one reply failed.") {
		t.Fatalf("two retries %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(chattool.ToolRetry, map[string]any{"thread": "Chat"}))
	if code != http.StatusNotFound {
		t.Fatalf("other retry %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolRetry, map[string]any{"thread": "Chat", "user_id": other.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Retrying.") {
		t.Fatalf("retry %d %s", code, body)
	}
	dmRuns, err := h.st.ListThreadRuns(ctx, user.ID, dm.ID)
	if err != nil {
		t.Fatal(err)
	}
	retried := 0
	for _, row := range dmRuns {
		if row.Status == "retried" {
			retried++
		}
	}
	if retried != 1 || len(dmRuns) < 2 {
		t.Fatalf("retry rows %+v", dmRuns)
	}
	noteRuns, err := h.st.ListThreadRuns(ctx, user.ID, failThread.ID)
	if err != nil || len(noteRuns) != 1 || noteRuns[0].Status != "failed" {
		t.Fatalf("note was retried %+v %v", noteRuns, err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolRetry, map[string]any{"thread": "Chat"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Nothing failed.") {
		t.Fatalf("second retry %d %s", code, body)
	}
}

func TestMachineChatStopStillRunning(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("chat-live@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	dm := dmThread(t, h, user.ID, caller.ID)
	runRow, err := h.st.CreateRun(ctx, user.ID, dm.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(ctx, user.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	code, body := machinePost(t, h, "203.0.113.91:9", token, caller.ID, "/agents", routinePayload(chattool.ToolStop, nil))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "The prompt is still running.") {
		t.Fatalf("no cancel %d %s", code, body)
	}
	if runStatus(t, h, user.ID, runRow.ID) != "streaming" {
		t.Fatal("missing cancel stopped the run")
	}

	fake := host.NewFake()
	fake.CancelOK = true
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-chat", "running", fake)
	if err := h.st.SaveVMToken(ctx, caller.ID, token); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, "203.0.113.91:9", token, caller.ID, "/agents", routinePayload(chattool.ToolStop, map[string]any{"thread": "Chat"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Stopped the reply.") {
		t.Fatalf("cancel %d %s", code, body)
	}
	if runStatus(t, h, user.ID, runRow.ID) != "stopped" || len(fake.CancelIDs) != 1 || fake.CancelIDs[0] != runRow.ID {
		t.Fatalf("cancel ids %v status %s", fake.CancelIDs, runStatus(t, h, user.ID, runRow.ID))
	}
}

func TestChatFenceStopsOne(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("chat-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	routine, err := h.st.CreateThread(ctx, user.ID, "routine", caller.ID, "Weekly link sweep")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := h.st.CreateRun(ctx, user.ID, routine.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	invalid := "Leave this.\n\n```castbot-chat\n{\"action\":\"open_thread\",\"thread\":\"THREAD\"}\n```\n"
	replyRoutine(t, h, user.ID, caller, invalid)
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-chat") || !strings.Contains(kept, "Leave this.") {
		t.Fatalf("invalid fence removed %s", kept)
	}
	if runStatus(t, h, user.ID, queued.ID) != "queued" {
		t.Fatal("invalid fence stopped the run")
	}
	replyRoutine(t, h, user.ID, caller, "Stopping it.\n\n```castbot-chat\n{\"action\":\"stop_reply\",\"thread\":\""+routine.ID+"\",\"user_id\":\"other\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-chat") || !strings.Contains(body, "Stopping it.") {
		t.Fatalf("fence stayed %s", body)
	}
	if runStatus(t, h, user.ID, queued.ID) != "stopped" {
		t.Fatal("fence did not stop the run")
	}
}

func TestOpenThreadOnlyFromChat(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("chat-open@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	other := seedCaller(t, h, user, "Pip")
	token := guestToken(t, h, caller)
	ip := "203.0.113.92:9"
	routine, err := h.st.CreateThread(ctx, user.ID, "routine", caller.ID, "Weekly link sweep")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, user.ID, routine.ID, "user", user.ID, "Check the links."); err != nil {
		t.Fatal(err)
	}
	group, err := h.st.CreateGroup(ctx, user.ID, "Standup", []string{caller.ID, other.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, user.ID, group.ID, "user", user.ID, "Only in the standup."); err != nil {
		t.Fatal(err)
	}
	dm := dmThread(t, h, user.ID, caller.ID)
	if _, err := h.st.CreateMessage(ctx, user.ID, dm.ID, "user", user.ID, "Only in the chat."); err != nil {
		t.Fatal(err)
	}
	refuse := func(label string) {
		t.Helper()
		code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolOpen, map[string]any{"thread": "sweep"}))
		if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Open a thread from Chat.") || strings.Contains(body, "Check the links.") || strings.Contains(body, "Only in the standup.") || strings.Contains(body, "Only in the chat.") {
			t.Fatalf("%s %d %s", label, code, body)
		}
	}
	refuse("no turn")
	queued, err := h.st.CreateRun(ctx, user.ID, dm.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	refuse("queued chat")
	if _, err := h.st.FailRun(ctx, user.ID, queued.ID, "This run did not start.", 0); err != nil {
		t.Fatal(err)
	}
	routineRun := streamOn(t, h, user.ID, routine.ID, caller.ID)
	refuse("routine")
	if _, err := h.st.FailRun(ctx, user.ID, routineRun, "This run did not start.", 0); err != nil {
		t.Fatal(err)
	}
	groupRun := streamOn(t, h, user.ID, group.ID, caller.ID)
	refuse("group")
	dmRun := streamOn(t, h, user.ID, dm.ID, caller.ID)
	refuse("two turns")
	if _, err := h.st.FailRun(ctx, user.ID, groupRun, "This run did not start.", 0); err != nil {
		t.Fatal(err)
	}
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolOpen, map[string]any{"thread": "sweep", "user_id": other.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Opened Weekly link sweep. Routine.") || !strings.Contains(body, "user: Check the links.") || strings.Contains(body, "Only in the standup.") || strings.Contains(body, "Only in the chat.") {
		t.Fatalf("open routine %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(chattool.ToolOpen, map[string]any{"thread": "Chat"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Opened Chat.") || !strings.Contains(body, "Only in the chat.") || strings.Contains(body, "Check the links.") || strings.Contains(body, "Only in the standup.") {
		t.Fatalf("open chat %d %s", code, body)
	}
	if runStatus(t, h, user.ID, dmRun) != "streaming" {
		t.Fatal("open changed the chat run")
	}
}

func TestChatFenceOpenOnlyFromChat(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("chat-fence-open@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	other := seedCaller(t, h, user, "Pip")
	routine, err := h.st.CreateThread(ctx, user.ID, "routine", caller.ID, "Weekly link sweep")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, user.ID, routine.ID, "user", user.ID, "Check the links."); err != nil {
		t.Fatal(err)
	}
	group, err := h.st.CreateGroup(ctx, user.ID, "Standup", []string{caller.ID, other.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(ctx, user.ID, group.ID, "user", user.ID, "Only in the standup."); err != nil {
		t.Fatal(err)
	}
	dm := dmThread(t, h, user.ID, caller.ID)
	if _, err := h.st.CreateMessage(ctx, user.ID, dm.ID, "user", user.ID, "Only in the chat."); err != nil {
		t.Fatal(err)
	}
	openSweep := "```castbot-chat\n{\"action\":\"open_thread\",\"thread\":\"Weekly link sweep\"}\n```\n"
	openStandup := "```castbot-chat\n{\"action\":\"open_thread\",\"thread\":\"Standup\"}\n```\n"
	finishChatFence(t, h, user.ID, caller, routine.ID, openStandup)
	body := lastAgentBody(t, h, user.ID, routine.ID)
	if strings.Contains(body, "castbot-chat") || !strings.Contains(body, "Open a thread from Chat.") || strings.Contains(body, "Check the links.") || strings.Contains(body, "Only in the standup.") || strings.Contains(body, "Only in the chat.") {
		t.Fatalf("routine fence %s", body)
	}
	finishChatFence(t, h, user.ID, caller, group.ID, openSweep)
	body = lastAgentBody(t, h, user.ID, group.ID)
	if strings.Contains(body, "castbot-chat") || !strings.Contains(body, "Open a thread from Chat.") || strings.Contains(body, "Check the links.") || strings.Contains(body, "Only in the standup.") || strings.Contains(body, "Only in the chat.") {
		t.Fatalf("group fence %s", body)
	}
	replyRoutine(t, h, user.ID, caller, openSweep)
	body = agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-chat") || !strings.Contains(body, "Opened Weekly link sweep. Routine.") || !strings.Contains(body, "user: Check the links.") || strings.Contains(body, "Only in the standup.") || strings.Contains(body, "Only in the chat.") {
		t.Fatalf("chat fence %s", body)
	}
}

func streamOn(t *testing.T, h *harness, userID, threadID, agentID string) string {
	t.Helper()
	runRow, err := h.st.CreateRun(context.Background(), userID, threadID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), userID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	return runRow.ID
}

func finishChatFence(t *testing.T, h *harness, userID string, caller store.Agent, threadID, body string) {
	t.Helper()
	runRow, err := h.st.CreateRun(context.Background(), userID, threadID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(context.Background(), userID, runRow.ID, body, 0); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeChat(run.Job{UserID: userID, ThreadID: threadID, AgentID: caller.ID, RunID: runRow.ID})
}

func lastAgentBody(t *testing.T, h *harness, userID, threadID string) string {
	t.Helper()
	msgs, err := h.st.ListMessages(context.Background(), userID, threadID)
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, msg := range msgs {
		if msg.AuthorKind == "agent" {
			body = msg.Body
		}
	}
	return body
}

func failRun(t *testing.T, h *harness, userID, agentID, threadID, body string) {
	t.Helper()
	runRow, err := h.st.CreateRun(context.Background(), userID, threadID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := h.st.CreateMessage(context.Background(), userID, threadID, "user", userID, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(context.Background(), userID, msg.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.FailRun(context.Background(), userID, runRow.ID, "This run did not start.", 0); err != nil {
		t.Fatal(err)
	}
}

func runStatus(t *testing.T, h *harness, userID, runID string) string {
	t.Helper()
	row, err := h.st.GetRun(context.Background(), userID, runID)
	if err != nil {
		t.Fatal(err)
	}
	return row.Status
}
