package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/machinetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func TestMachinePowerPausesOnlyTheCaller(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("power-owner@example.com", "Ada Owner", "correct-horse-1")
	other, _ := h.account("power-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	ip := "203.0.113.123:9"
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-ada", "running", fake)
	seedPower(t, h, user.ID, sibling.ID, "vm-pip", "running", fake)
	seedPower(t, h, other.ID, otherAgent.ID, "vm-bea", "running", fake)
	if err := h.st.SaveVMToken(ctx, caller.ID, token); err != nil {
		t.Fatal(err)
	}

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(machinetool.ToolPause, map[string]any{"user_id": user.ID}))
	if code != http.StatusNotFound || strings.Contains(body, "paused") {
		t.Fatalf("other token %d %s", code, body)
	}
	if agentStatus(t, h, user.ID, caller.ID) != "ready" || agentStatus(t, h, other.ID, otherAgent.ID) != "ready" {
		t.Fatal("other token changed a machine")
	}

	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(machinetool.ToolPause, nil))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d %s", code, body)
	}
	if agentStatus(t, h, user.ID, sibling.ID) != "ready" {
		t.Fatal("caller token paused a sibling")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolPause, map[string]any{
		"user_id": other.ID, "agent_id": sibling.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "The machine is paused. A chat message wakes it.") {
		t.Fatalf("pause %d %s", code, body)
	}
	if agentStatus(t, h, user.ID, caller.ID) != "paused" || vmState(t, h, user.ID, caller.ID) != "paused" {
		t.Fatal("caller did not pause")
	}
	if agentStatus(t, h, user.ID, sibling.ID) != "ready" || agentStatus(t, h, other.ID, otherAgent.ID) != "ready" {
		t.Fatal("pause changed another machine")
	}
	pauses := countCalls(fake, "pause")
	if pauses != 1 {
		t.Fatalf("pause calls %d", pauses)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolPause, nil))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "The machine is paused.") {
		t.Fatalf("second pause %d %s", code, body)
	}
	if countCalls(fake, "pause") != 1 {
		t.Fatal("second pause called the host")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", `{"name":"`+machinetool.ToolWake+`","arguments":["nope"]}`)
	if code != http.StatusOK || !strings.Contains(body, "That request was rejected.") {
		t.Fatalf("bad args %d %s", code, body)
	}
	if agentStatus(t, h, user.ID, caller.ID) != "paused" {
		t.Fatal("bad args woke the machine")
	}

	if _, _, err := h.st.BeginReplacement(ctx, user.ID, caller.ID, "web:test", "example.invalid/castbot-runtime:next", false); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolWake, nil))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "This machine is being replaced.") {
		t.Fatalf("replace lock %d %s", code, body)
	}
	if countCalls(fake, "wake") != 0 || agentStatus(t, h, user.ID, caller.ID) != "paused" {
		t.Fatal("wake ran during replace")
	}
}

func TestMachinePowerWakesAndRefreshes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("power-wake@example.com", "Ada Owner", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.124:9"
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	seedPower(t, h, user.ID, caller.ID, "vm-ada", "paused", fake)
	if err := h.st.SaveVMToken(ctx, caller.ID, token); err != nil {
		t.Fatal(err)
	}

	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolWake, map[string]any{"user_id": "other"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "The machine is running. It pauses after it is idle.") {
		t.Fatalf("wake %d %s", code, body)
	}
	if agentStatus(t, h, user.ID, caller.ID) != "ready" || countCalls(fake, "wake") != 1 {
		t.Fatalf("status %s calls %v", agentStatus(t, h, user.ID, caller.ID), fake.Calls)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolWake, nil))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || countCalls(fake, "wake") != 1 {
		t.Fatalf("second wake %d %s calls %v", code, body, fake.Calls)
	}

	fake.SetForce("")
	fake.Machines["vm-ada"] = host.Machine{ID: "vm-ada", State: "paused", Endpoint: "https://vm-ada.example"}
	before := len(fake.Calls)
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolRefresh, nil))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "The machine is paused. A chat message wakes it.") || !strings.Contains(body, "This does not wake it.") {
		t.Fatalf("refresh %d %s", code, body)
	}
	if agentStatus(t, h, user.ID, caller.ID) != "paused" {
		t.Fatal("refresh did not copy the cloud state")
	}
	joined := strings.Join(fake.Calls[before:], " ")
	if !strings.Contains(joined, "status") || strings.Contains(joined, "wake") {
		t.Fatalf("refresh calls %s", joined)
	}

	delete(fake.Machines, "vm-ada")
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolRefresh, nil))
	if code != http.StatusOK || !strings.Contains(body, "The machine could not be checked. It was not woken.") {
		t.Fatalf("refresh miss %d %s", code, body)
	}
	if strings.Contains(strings.Join(fake.Calls, " "), "wake") && countCalls(fake, "wake") != 1 {
		t.Fatal("failed refresh woke the machine")
	}
}

func TestMachinePowerStartsAndRetries(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("power-start@example.com", "Ada Owner", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.125:9"

	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolStart, nil))
	if code != http.StatusOK || !strings.Contains(body, "No machine host is configured.") {
		t.Fatalf("no host %d %s", code, body)
	}
	if vmState(t, h, user.ID, caller.ID) != "" {
		t.Fatal("start without a host reserved a machine")
	}

	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	h.srv.SetBootBudget(5 * time.Second)
	if err := h.st.SetQuota(ctx, user.ID, 0); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolStart, nil))
	if code != http.StatusOK || !strings.Contains(body, "This account has reached its machine limit.") {
		t.Fatalf("quota %d %s", code, body)
	}
	if err := h.st.SetQuota(ctx, user.ID, 4); err != nil {
		t.Fatal(err)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolRetry, nil))
	if code != http.StatusOK || !strings.Contains(body, "The machine is not waiting on a failed start.") {
		t.Fatalf("retry draft %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolStart, map[string]any{"user_id": "other"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "The machine is starting.") {
		t.Fatalf("start %d %s", code, body)
	}
	waitVM(t, h, user.ID, caller.ID, "running")
	if len(fake.Creates) != 1 {
		t.Fatalf("creates %d", len(fake.Creates))
	}
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, again.ID, "```castbot-machine\n{\"action\":\"start_machine\"}\n```", 0); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeMachine(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: again.ID})
	reply, err := h.st.LatestAgentReply(ctx, user.ID, again.ID)
	if err != nil || strings.Contains(reply.Body, "castbot-machine") || strings.Contains(reply.Body, "already has a machine") {
		t.Fatalf("recent start %q %v", reply.Body, err)
	}
	if len(fake.Creates) != 1 {
		t.Fatal("fence started a second machine")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolStart, nil))
	if code != http.StatusOK || !strings.Contains(body, "This agent already has a machine.") {
		t.Fatalf("second start %d %s", code, body)
	}
	if len(fake.Creates) != 1 {
		t.Fatal("second start created a machine")
	}

	if err := h.st.ApplyVMState(ctx, user.ID, caller.ID, "failed", "", ""); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolStart, nil))
	if code != http.StatusOK || !strings.Contains(body, "The last start failed. Try again starts a new machine.") {
		t.Fatalf("start failed row %d %s", code, body)
	}
	if len(fake.Deletes) != 0 {
		t.Fatal("start deleted the failed machine")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(machinetool.ToolRetry, nil))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "The machine is starting.") {
		t.Fatalf("retry %d %s", code, body)
	}
	if len(fake.Deletes) != 1 {
		t.Fatalf("deletes %+v", fake.Deletes)
	}
	waitVM(t, h, user.ID, caller.ID, "running")
}

func TestMachinePowerFenceUpdatesTheChip(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("power-fence@example.com", "Ada Owner", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	token := guestToken(t, h, caller)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s09", "https://castbot.example")
	h.srv.SetBootBudget(5 * time.Second)
	seedPower(t, h, user.ID, caller.ID, "vm-ada", "running", fake)
	seedPower(t, h, user.ID, sibling.ID, "vm-pip", "running", fake)
	if err := h.st.SaveVMToken(ctx, caller.ID, token); err != nil {
		t.Fatal(err)
	}
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg, ev, err := h.st.FinishRun(ctx, user.ID, runRow.ID, "```castbot-machine\n{\"action\":\"pause_machine\",\"user_id\":\"other\"}\n```", 0)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err = h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	fw := &frameWriter{w: &buf, seq: ev.Seq, from: -1}
	if err := h.srv.writeDone(ctx, fw, user, runRow, msg.ID, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "castbot-machine") || !strings.Contains(buf.String(), "The machine is paused. A chat message wakes it.") || !strings.Contains(buf.String(), `id="vmchip"`) || !strings.Contains(buf.String(), "Paused") {
		t.Fatalf("fence frame %s", buf.String())
	}
	if agentStatus(t, h, user.ID, caller.ID) != "paused" || agentStatus(t, h, user.ID, sibling.ID) != "ready" {
		t.Fatal("fence paused the wrong agent")
	}

	kept, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, kept.ID, "Leave this.\n```castbot-machine\n{\"action\":\"ACTION\"}\n```", 0); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeMachine(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: kept.ID})
	reply, err := h.st.LatestAgentReply(ctx, user.ID, kept.ID)
	if err != nil || !strings.Contains(reply.Body, "ACTION") || !strings.Contains(reply.Body, "Leave this.") {
		t.Fatalf("placeholder removed %q %v", reply.Body, err)
	}

	bare, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, bare.ID, "```castbot-machine\n{\"action\":\"start_machine\"}\n```", 0); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeMachine(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: bare.ID})
	reply, err = h.st.LatestAgentReply(ctx, user.ID, bare.ID)
	if err != nil || strings.Contains(reply.Body, "castbot-machine") || !strings.Contains(reply.Body, "already has a machine") {
		t.Fatalf("paused start %q %v", reply.Body, err)
	}
	if len(fake.Creates) != 0 || agentStatus(t, h, user.ID, caller.ID) != "paused" {
		t.Fatal("start fence replaced a paused machine")
	}
}

func seedPower(t *testing.T, h *harness, userID, agentID, microID, state string, fake *host.Fake) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.st.BeginVM(ctx, userID, agentID, host.MachineName(agentID), "example.invalid/castbot-runtime:s09"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(ctx, userID, agentID, microID, "https://"+microID+".example", state, ""); err != nil {
		t.Fatal(err)
	}
	fake.Machines[microID] = host.Machine{ID: microID, State: state, Endpoint: "https://" + microID + ".example"}
}

func agentStatus(t *testing.T, h *harness, userID, agentID string) string {
	t.Helper()
	agent, err := h.st.GetAgent(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return agent.Status
}

func vmState(t *testing.T, h *harness, userID, agentID string) string {
	t.Helper()
	vm, err := h.st.LiveVM(context.Background(), userID, agentID)
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return vm.State
}

func countCalls(fake *host.Fake, name string) int {
	n := 0
	for _, call := range fake.Calls {
		if call == name {
			n++
		}
	}
	return n
}

func waitVM(t *testing.T, h *harness, userID, agentID, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if vmState(t, h, userID, agentID) == state {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("vm state %s", vmState(t, h, userID, agentID))
}
