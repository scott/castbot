package web

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/cron"
	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/selftool"
	"github.com/scott/castbot/internal/store"
)

func routinePayload(name string, args map[string]any) string {
	raw, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func TestMachineRoutineCreateListUpdateDelete(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("routine-owner@example.com", "Ada", "correct-horse-1")
	if err := h.st.SetTimezone(context.Background(), user.ID, "America/Denver"); err != nil {
		t.Fatal(err)
	}
	user, err := h.st.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := h.account("routine-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	otherAgent := seedCaller(t, h, other, "Bea")
	sibling := seedCaller(t, h, user, "Pip")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 18, 7, 0, 0, 0, loc) })
	ip := "203.0.113.80:9"
	create := routinePayload(routinetool.ToolCreate, map[string]any{
		"name": "Morning digest", "cron": "0 8 * * 1-5", "prompt": "Check the inbox.",
		"user_id": other.ID, "agent_id": sibling.ID,
	})
	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", create)
	if code != http.StatusNotFound || strings.Contains(body, "Morning digest") {
		t.Fatalf("other token %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", create)
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Every weekday at 8:00 AM") || !strings.Contains(body, "America/Denver") || !strings.Contains(body, "On.") || !strings.Contains(body, "Next: Today, 8:00 AM") {
		t.Fatalf("create %d %s", code, body)
	}
	rows, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	saved := rows[0]
	if saved.UserID != user.ID || saved.AgentID != caller.ID || saved.Timezone != "America/Denver" || saved.MissedPolicy != store.MissedOnce || saved.ApprovalPolicy != store.ApprovalWait || !saved.Enabled || saved.Prompt != "Check the inbox." {
		t.Fatalf("saved %+v", saved)
	}
	if _, err := h.st.GetLiveRoutine(context.Background(), other.ID, saved.ID); err == nil {
		t.Fatal("other user can read the routine")
	}
	sib, err := h.st.ListRoutines(context.Background(), user.ID, sibling.ID)
	if err != nil || len(sib) != 0 {
		t.Fatalf("sibling %+v %v", sib, err)
	}
	code, listed := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(routinetool.ToolList, map[string]any{"user_id": other.ID}))
	if code != http.StatusOK || !strings.Contains(listed, saved.ID) || !strings.Contains(listed, "Morning digest") || strings.Contains(listed, "Bea") {
		t.Fatalf("list %d %s", code, listed)
	}
	code, listed = machinePost(t, h, ip, otherToken, otherAgent.ID, "/agents", routinePayload(routinetool.ToolList, map[string]any{}))
	if code != http.StatusOK || strings.Contains(listed, saved.ID) || !strings.Contains(listed, "No routines yet.") {
		t.Fatalf("other list %d %s", code, listed)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(routinetool.ToolUpdate, map[string]any{
		"id": saved.ID, "name": "Weekday digest", "agent_id": sibling.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Weekday digest") {
		t.Fatalf("rename %d %s", code, body)
	}
	renamed, err := h.st.GetLiveRoutine(context.Background(), user.ID, saved.ID)
	if err != nil || renamed.Name != "Weekday digest" || renamed.Prompt != "Check the inbox." || renamed.Cron != "0 8 * * 1-5" || renamed.AgentID != caller.ID {
		t.Fatalf("renamed %+v %v", renamed, err)
	}
	off := false
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(routinetool.ToolUpdate, map[string]any{"id": saved.ID, "enabled": off}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Off.") || !strings.Contains(body, "Paused") {
		t.Fatalf("pause %d %s", code, body)
	}
	paused, err := h.st.GetLiveRoutine(context.Background(), user.ID, saved.ID)
	if err != nil || paused.Enabled || paused.Name != "Weekday digest" {
		t.Fatalf("paused %+v %v", paused, err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(routinetool.ToolUpdate, map[string]any{"id": saved.ID, "enabled": true}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "On.") || !strings.Contains(body, "Next:") {
		t.Fatalf("resume %d %s", code, body)
	}
	resumed, err := h.st.GetLiveRoutine(context.Background(), user.ID, saved.ID)
	if err != nil || !resumed.Enabled || resumed.NextRunAt == "" {
		t.Fatalf("resumed %+v %v", resumed, err)
	}
	sibToken := guestToken(t, h, sibling)
	code, body = machinePost(t, h, ip, sibToken, sibling.ID, "/agents", routinePayload(routinetool.ToolDelete, map[string]any{"id": saved.ID}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "not found") {
		t.Fatalf("sibling delete %d %s", code, body)
	}
	if _, err := h.st.GetLiveRoutine(context.Background(), user.ID, saved.ID); err != nil {
		t.Fatal("sibling delete removed the routine")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(routinetool.ToolDelete, map[string]any{"id": saved.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Deleted Weekday digest.") {
		t.Fatalf("delete %d %s", code, body)
	}
	left, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
	if err != nil || len(left) != 0 {
		t.Fatalf("left %+v %v", left, err)
	}
}

func TestMachineRoutineValidationAddsNoRow(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("routine-bad@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.81:9"
	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"cron": "0 8 * * 1-5", "prompt": "Check the inbox."}, "Name is required."},
		{map[string]any{"name": strings.Repeat("n", 81), "cron": "0 8 * * 1-5", "prompt": "Check the inbox."}, "Name is required."},
		{map[string]any{"name": "Nope", "cron": "not a cron", "prompt": "Check the inbox."}, cron.BadSchedule},
		{map[string]any{"name": "Nope", "cron": "*/10 * * * *", "prompt": "Check the inbox."}, cron.TooClose},
		{map[string]any{"name": "Nope", "cron": "0 8 * * 1-5", "prompt": "Check the inbox.", "timezone": "Not/AZone"}, "Pick a time zone."},
		{map[string]any{"name": "Nope", "cron": "0 8 * * 1-5", "prompt": ""}, "Say what the agent should do."},
		{map[string]any{"name": "Nope", "cron": "0 8 * * 1-5", "prompt": strings.Repeat("p", 8001)}, "Say what the agent should do."},
		{map[string]any{"name": "Nope", "cron": "0 8 * * 1-5", "prompt": "Check the inbox.", "missed": "later"}, "Pick what happens when a run is missed."},
		{map[string]any{"name": "Nope", "cron": "0 8 * * 1-5", "prompt": "Check the inbox.", "approval": "maybe"}, "Pick what happens when a step needs approval."},
	}
	for _, tc := range cases {
		before, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
		if err != nil {
			t.Fatal(err)
		}
		code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(routinetool.ToolCreate, tc.args))
		after, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
		if err != nil {
			t.Fatal(err)
		}
		if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, tc.want) || len(after) != len(before) {
			t.Fatalf("%s -> %d %s", tc.want, code, body)
		}
	}
	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(routinetool.ToolCreate, map[string]any{
		"name": "Evening note", "cron": "0 18 * * *", "prompt": "Send the note.", "missed": "skip", "approval": "deny", "timezone": "America/Denver",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) {
		t.Fatalf("explicit %d %s", code, body)
	}
	rows, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
	if err != nil || len(rows) != 1 || rows[0].MissedPolicy != store.MissedSkip || rows[0].ApprovalPolicy != store.ApprovalDeny || !rows[0].Enabled {
		t.Fatalf("explicit row %+v %v", rows, err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(routinetool.ToolUpdate, map[string]any{"id": rows[0].ID, "cron": "nope"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, cron.BadSchedule) {
		t.Fatalf("bad update %d %s", code, body)
	}
	kept, err := h.st.GetLiveRoutine(context.Background(), user.ID, rows[0].ID)
	if err != nil || kept.Cron != "0 18 * * *" {
		t.Fatalf("update wrote a bad cron %+v %v", kept, err)
	}
}

func TestRoutineFenceCreateUpdateDelete(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("routine-fence@example.com", "Ada", "correct-horse-1")
	if err := h.st.SetTimezone(context.Background(), user.ID, "Pacific/Auckland"); err != nil {
		t.Fatal(err)
	}
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	descPrompt := "Check the inbox and draft a short note."
	fence := "I will add that.\n\n```castbot-routine\n{\"action\":\"create\",\"name\":\"Morning digest\",\"cron\":\"0 8 * * 1-5\",\"prompt\":\"" + descPrompt + "\"}\n```\n"
	replyRoutine(t, h, user.ID, caller, fence)
	rows, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
	if err != nil || len(rows) != 1 || rows[0].Timezone != "Pacific/Auckland" || rows[0].AgentID != caller.ID || !rows[0].Enabled {
		t.Fatalf("fence create %+v %v", rows, err)
	}
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-routine") || !strings.Contains(body, "I will add that.") {
		t.Fatalf("fence stayed %s", body)
	}
	replyRoutine(t, h, user.ID, caller, fence)
	rows, err = h.st.ListRoutines(context.Background(), user.ID, caller.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("second fence %+v %v", rows, err)
	}
	code, toolBody := machinePost(t, h, "203.0.113.82:9", token, caller.ID, "/agents", routinePayload(routinetool.ToolCreate, map[string]any{
		"name": "Morning digest", "cron": "0 8 * * 1-5", "prompt": descPrompt,
	}))
	if code != http.StatusOK || strings.Contains(toolBody, `"error":true`) {
		t.Fatalf("tool %d %s", code, toolBody)
	}
	before, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
	if err != nil || len(before) != 2 {
		t.Fatalf("tool created %+v %v", before, err)
	}
	replyRoutine(t, h, user.ID, caller, fence)
	after, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
	if err != nil || len(after) != len(before) {
		t.Fatalf("tool then fence %+v %v", after, err)
	}
	id := rows[0].ID
	replyRoutine(t, h, user.ID, caller, "```castbot-routine\n{\"action\":\"update\",\"id\":\""+id+"\",\"name\":\"Dawn digest\"}\n```")
	updated, err := h.st.GetLiveRoutine(context.Background(), user.ID, id)
	if err != nil || updated.Name != "Dawn digest" || updated.Prompt != descPrompt {
		t.Fatalf("fence update %+v %v", updated, err)
	}
	replyRoutine(t, h, user.ID, caller, "```castbot-routine\n{\"action\":\"delete\",\"id\":\""+id+"\"}\n```")
	if _, err := h.st.GetLiveRoutine(context.Background(), user.ID, id); err == nil {
		t.Fatal("fence delete left the row")
	}
	invalid := "Leave this.\n\n```castbot-routine\nnot json\n```\n"
	replyRoutine(t, h, user.ID, caller, invalid)
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-routine") || !strings.Contains(kept, "Leave this.") {
		t.Fatalf("invalid fence removed %s", kept)
	}
	count, err := h.st.ListRoutines(context.Background(), user.ID, caller.ID)
	if err != nil || len(count) != len(after)-1 {
		t.Fatalf("invalid fence created %+v want %d", count, len(after)-1)
	}
}

func TestServedBundleCarriesRoutineSkill(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("routine-bundle@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	ip := "203.0.113.83:9"
	code, packed, hdr := h.do(http.MethodGet, "/api/castlet/agents/"+caller.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK {
		t.Fatalf("bundle %d", code)
	}
	if hdr.Get("X-Castbot-Bundle-SHA256") != bundle.Sum([]byte(packed)) {
		t.Fatal("served sha does not match the body")
	}
	stored, err := h.st.LatestBundle(context.Background(), user.ID, caller.ID)
	if err != nil || strings.Contains(stored.Instructions, routinetool.Heading) || strings.Contains(stored.Instructions, agentskill.Heading) {
		t.Fatal("stored bundle was rewritten")
	}
	dir := t.TempDir()
	if err := bundle.Install(dir, []byte(packed)); err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile(filepath.Join(dir, routinetool.SkillFile))
	if err != nil || !strings.Contains(string(skill), routinetool.SkillName) || !strings.Contains(string(skill), "I can set that up as a routine for you") {
		t.Fatal("routine skill was not installed")
	}
	account, err := os.ReadFile(filepath.Join(dir, selftool.SkillFile))
	if err != nil || !strings.Contains(string(account), selftool.SkillName) || !strings.Contains(string(account), selftool.Fence) {
		t.Fatal("account skill was not installed")
	}
	groups, err := os.ReadFile(filepath.Join(dir, grouptool.SkillFile))
	if err != nil || !strings.Contains(string(groups), grouptool.SkillName) || !strings.Contains(string(groups), grouptool.Fence) {
		t.Fatal("groups skill was not installed")
	}
	notes, err := os.ReadFile(filepath.Join(dir, "instructions.md"))
	if err != nil || !strings.Contains(string(notes), routinetool.Heading) || !strings.Contains(string(notes), agentskill.Heading) || !strings.Contains(string(notes), selftool.Heading) || !strings.Contains(string(notes), grouptool.Heading) || !strings.Contains(string(notes), "Be brief.") {
		t.Fatal("guides missing")
	}
	tr := tar.NewReader(bytes.NewReader([]byte(packed)))
	saw := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "agent.yaml" {
			body, _ := io.ReadAll(tr)
			text := string(body)
			for _, name := range []string{routinetool.ToolCreate, routinetool.ToolUpdate, routinetool.ToolDelete, routinetool.ToolList, agentskill.ToolCreate, selftool.ToolUsage, grouptool.ToolCreateGroup, grouptool.ToolMoveAgents} {
				if !strings.Contains(text, name+": always-allow") && !strings.Contains(text, name+":always-allow") {
					if !strings.Contains(text, name) || !strings.Contains(text, "always-allow") {
						t.Fatalf("served yaml missing %s\n%s", name, text)
					}
				}
			}
			saw = true
		}
	}
	if !saw {
		t.Fatal("served tar has no agent.yaml")
	}
}

func replyRoutine(t *testing.T, h *harness, userID string, caller store.Agent, body string) {
	t.Helper()
	th, err := h.st.DMThread(context.Background(), userID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), userID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(context.Background(), userID, runRow.ID, body, 0); err != nil {
		t.Fatal(err)
	}
	h.srv.settleTalk(run.Job{UserID: userID, ThreadID: th.ID, AgentID: caller.ID, RunID: runRow.ID}, "done")
}

func agentReplyBody(t *testing.T, h *harness, userID, agentID string) string {
	t.Helper()
	th, err := h.st.DMThread(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := h.st.ListMessages(context.Background(), userID, th.ID)
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
