package web

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/edittool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func TestEditAgentChangesOnlyTheCaller(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("edit-owner@example.com", "Ada Owner", "correct-horse-1")
	other, _ := h.account("edit-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	ip := "203.0.113.140:9"

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{"name": "Stolen", "user_id": user.ID}))
	if code != http.StatusNotFound || strings.Contains(body, "Stolen") {
		t.Fatalf("other token %d %s", code, body)
	}
	if agentName(t, h, user.ID, caller.ID) != "Ada" {
		t.Fatal("other token renamed the agent")
	}

	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{"name": "Stolen"}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d %s", code, body)
	}
	if agentName(t, h, user.ID, sibling.ID) != "Pip" {
		t.Fatal("caller token renamed a sibling")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{
		"name": "Quill", "user_id": other.ID, "agent_id": sibling.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Name is Quill.") {
		t.Fatalf("rename %d %s", code, body)
	}
	if agentName(t, h, user.ID, caller.ID) != "Quill" || agentName(t, h, user.ID, sibling.ID) != "Pip" || agentName(t, h, other.ID, otherAgent.ID) != "Bea" {
		t.Fatal("rename changed the wrong agent")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{"name": "Pip"}))
	if code != http.StatusOK || !strings.Contains(body, "Choose a name that is not already used.") || agentName(t, h, user.ID, caller.ID) != "Quill" {
		t.Fatalf("conflict %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{"color": "Mint"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Color is Mint.") {
		t.Fatalf("color %d %s", code, body)
	}
	if savedColor(t, h, user.ID, caller.ID) != "c2" {
		t.Fatal("color was not saved")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{
		"user_id": other.ID, "agent_id": sibling.ID, "section": "Work", "avatar": "aGVsbG8=",
	}))
	if code != http.StatusOK || !strings.Contains(body, "Say what should change.") || agentName(t, h, user.ID, caller.ID) != "Quill" {
		t.Fatalf("ignored keys %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{"delegate": "yes"}))
	if code != http.StatusOK || !strings.Contains(body, "That request was rejected.") || loadedAgent(t, h, user.ID, caller.ID).CanDelegate {
		t.Fatalf("bad delegate %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{"color": "purple"}))
	if code != http.StatusOK || !strings.Contains(body, "That color isn't available.") || savedColor(t, h, user.ID, caller.ID) != "c2" {
		t.Fatalf("bad color %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{
		"delegate": true, "job": "Sort mail sk-abcdefghijklmnop",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || strings.Contains(body, "sk-abcdefghijklmnop") || !strings.Contains(body, "Can hand work to other agents: Yes.") {
		t.Fatalf("delegate %d %s", code, body)
	}
	got := loadedAgent(t, h, user.ID, caller.ID)
	if !got.CanDelegate || strings.Contains(got.Description, "sk-") || got.Description == "" {
		t.Fatalf("saved job %+v", got.Description)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{}))
	if code != http.StatusOK || !strings.Contains(body, "Say what should change.") {
		t.Fatalf("empty %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", `{"name":"`+edittool.ToolUpdate+`","arguments":["nope"]}`)
	if code != http.StatusOK || !strings.Contains(body, "That request was rejected.") {
		t.Fatalf("bad args %d %s", code, body)
	}
}

func TestEditAgentModelAndInstructions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("edit-model@example.com", "Ada Owner", "correct-horse-1")
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "demo-model-key")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.141:9"
	next := "openrouter/openai/gpt-4o"
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{
		"model": next, "instructions": "Stay short.", "title": "Editor",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Instructions are updated.") || !strings.Contains(body, "Title is Editor.") {
		t.Fatalf("model %d %s", code, body)
	}
	if strings.Contains(body, "demo-model-key") {
		t.Fatal("model key returned")
	}
	got := loadedAgent(t, h, user.ID, caller.ID)
	if got.Model != next || got.Title != "Editor" || got.Name != "Ada" {
		t.Fatalf("saved %+v", got)
	}
	bundle, err := h.st.LatestBundle(ctx, user.ID, caller.ID)
	if err != nil || !strings.Contains(bundle.Instructions, "Stay short.") || !strings.Contains(bundle.YAML, next) {
		t.Fatalf("bundle %q %v", bundle.Instructions, err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(edittool.ToolUpdate, map[string]any{"model": "not-a-model"}))
	if code != http.StatusOK || !strings.Contains(body, "That model is not in the provider manifest.") || loadedAgent(t, h, user.ID, caller.ID).Model != next {
		t.Fatalf("bad model %d %s", code, body)
	}
}

func TestEditAgentFenceUpdatesTheSidebar(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("edit-fence@example.com", "Ada Owner", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg, ev, err := h.st.FinishRun(ctx, user.ID, runRow.ID, "```castbot-edit\n{\"action\":\"update_agent\",\"name\":\"Quill\",\"user_id\":\"other\"}\n```", 0)
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
	frame := buf.String()
	if strings.Contains(frame, "castbot-edit") || !strings.Contains(frame, "Name is Quill.") || !strings.Contains(frame, "Quill") {
		t.Fatalf("fence frame %s", frame)
	}
	if agentName(t, h, user.ID, caller.ID) != "Quill" {
		t.Fatal("fence did not rename")
	}

	kept, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, kept.ID, "Leave this.\n```castbot-edit\n{\"action\":\"update_agent\",\"name\":\"NAME\"}\n```", 0); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeEdit(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: kept.ID})
	reply, err := h.st.LatestAgentReply(ctx, user.ID, kept.ID)
	if err != nil || !strings.Contains(reply.Body, "NAME") || !strings.Contains(reply.Body, "Leave this.") {
		t.Fatalf("placeholder removed %q %v", reply.Body, err)
	}
}

func agentName(t *testing.T, h *harness, userID, agentID string) string {
	t.Helper()
	return loadedAgent(t, h, userID, agentID).Name
}

func savedColor(t *testing.T, h *harness, userID, agentID string) string {
	t.Helper()
	return loadedAgent(t, h, userID, agentID).Color
}

func loadedAgent(t *testing.T, h *harness, userID, agentID string) store.Agent {
	t.Helper()
	agent, err := h.st.GetAgent(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}
