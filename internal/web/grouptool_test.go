package web

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func TestMachineOrganizesGroupsAndSections(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const password = "correct-horse-1"
	user, secret := h.account("organize-owner@example.com", "Ada Owner", password)
	other, _ := h.account("organize-other@example.com", "Bea", password)
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	moss := seedCaller(t, h, user, "Moss")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	ip := "203.0.113.123:9"
	hash, err := auth.Hash(password, h.st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	memberSecret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	_, invite, err := h.st.CreateInvite(ctx, user.ID, "pip-organize@example.com", "Pip Lane")
	if err != nil {
		t.Fatal(err)
	}
	member, err := h.st.AcceptInvite(ctx, invite, hash, memberSecret)
	if err != nil {
		t.Fatal(err)
	}
	samSecret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	samTwoSecret, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	_, samInvite, err := h.st.CreateInvite(ctx, user.ID, "sam-one@example.com", "Sam")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AcceptInvite(ctx, samInvite, hash, samSecret); err != nil {
		t.Fatal(err)
	}
	_, samInvite, err = h.st.CreateInvite(ctx, user.ID, "sam-two@example.com", "Sam")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AcceptInvite(ctx, samInvite, hash, samTwoSecret); err != nil {
		t.Fatal(err)
	}
	memberSec, err := h.st.SystemSection(ctx, member.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	left, _, err := h.st.CreateAgent(ctx, member.ID, store.AgentInput{SectionID: memberSec.ID, Name: "Left", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	right, _, err := h.st.CreateAgent(ctx, member.ID, store.AgentInput{SectionID: memberSec.ID, Name: "Right", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	notes, err := h.st.CreateGroup(ctx, member.ID, "Notes", []string{left.ID, right.ID}, []string{user.ID})
	if err != nil {
		t.Fatal(err)
	}

	create := routinePayload(grouptool.ToolCreateGroup, map[string]any{
		"title": "Standup", "agents": []string{"Ada", sibling.ID},
		"user_id": other.ID, "agent_id": sibling.ID,
	})
	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", create)
	if code != http.StatusNotFound || strings.Contains(body, "Standup") {
		t.Fatalf("other token %d %s", code, body)
	}
	if groups := groupsTitled(t, h, user.ID, "Standup"); len(groups) != 0 {
		t.Fatal("other token created a group")
	}
	if _, err := h.st.ListGroups(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolCreateGroup, map[string]any{
		"title": "Standup", "agents": []string{"Ada"},
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "at least two") {
		t.Fatalf("one agent %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolCreateGroup, map[string]any{
		"title": "  ", "agents": []string{"Ada", "Pip"},
	}))
	if code != http.StatusOK || !strings.Contains(body, "Give the group a title.") {
		t.Fatalf("blank title %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", create)
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Created Standup with Ada and Pip.") {
		t.Fatalf("create %d %s", code, body)
	}
	made := groupsTitled(t, h, user.ID, "Standup")
	if len(made) != 1 || made[0].UserID != user.ID || !strings.Contains(body, made[0].ID) {
		t.Fatalf("saved %+v %s", made, body)
	}
	if _, err := h.st.GroupAccess(ctx, other.ID, made[0].ID); err == nil {
		t.Fatal("other user can open the group")
	}
	roster, err := h.st.GroupAgents(ctx, user.ID, made[0].ID)
	if err != nil || len(roster) != 2 {
		t.Fatalf("roster %+v %v", roster, err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolUpdateGroup, map[string]any{
		"group": "Standup", "title": "Standup", "user_id": other.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "already named Standup") {
		t.Fatalf("same title %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolUpdateGroup, map[string]any{
		"group": notes.ID, "title": "Taken",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Only the person who created Notes") {
		t.Fatalf("member group %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPinGroup, map[string]any{"group": "Notes"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Pinned Notes.") {
		t.Fatalf("pin member group %d %s", code, body)
	}
	pinned, err := h.st.ListGroupCards(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	sawPin := false
	for _, card := range pinned {
		if card.ID == notes.ID && card.Pinned {
			sawPin = true
		}
	}
	if !sawPin {
		t.Fatalf("pin missing %+v", pinned)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPinGroup, map[string]any{"group": notes.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "already pinned") {
		t.Fatalf("pin again %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolUnpinGroup, map[string]any{"group": "Notes"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Unpinned Notes.") {
		t.Fatalf("unpin %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolAddMembers, map[string]any{
		"group": "Standup", "agents": []string{"Moss", moss.ID}, "people": []string{"Sam"},
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "More than one person is named Sam") {
		t.Fatalf("ambiguous person %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolAddMembers, map[string]any{
		"group": "Standup", "agents": []string{"Moss"}, "people": []string{"sam-one@example.com", "me"},
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Added Moss and Sam to Standup.") {
		t.Fatalf("add %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolAddMembers, map[string]any{
		"group": made[0].ID, "people": []string{"Pip Lane"}, "agents": []string{"Ada"},
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Added Pip Lane to Standup.") {
		t.Fatalf("add person %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolAddMembers, map[string]any{
		"group": "Standup", "agents": []string{"Ada", "Pip", "Moss"}, "people": []string{"Pip Lane"},
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "already in Standup") {
		t.Fatalf("already %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolRemoveMembers, map[string]any{
		"group": "Standup", "agents": []string{"Pip", "Moss"},
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "at least two") {
		t.Fatalf("drop below two %d %s", code, body)
	}
	still, err := h.st.GroupAgents(ctx, user.ID, made[0].ID)
	if err != nil || len(still) != 3 {
		t.Fatalf("agents changed %+v %v", still, err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolRemoveMembers, map[string]any{
		"group": "Standup", "people": []string{"me", "Pip Lane"},
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Removed Pip Lane from Standup.") || !strings.Contains(body, "You stay in the group.") {
		t.Fatalf("remove %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolRemoveMembers, map[string]any{
		"group": "Standup", "people": []string{"Pip Lane"},
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "not in Standup") {
		t.Fatalf("remove missing %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolCreateSection, map[string]any{
		"name": "Writing", "user_id": other.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Added the section Writing.") {
		t.Fatalf("section %d %s", code, body)
	}
	writing := sectionsNamed(t, h, user.ID, "Writing")
	if len(writing) != 1 || writing[0].UserID != user.ID {
		t.Fatalf("section row %+v", writing)
	}
	if _, err := h.st.GetSection(ctx, other.ID, writing[0].ID); err == nil {
		t.Fatal("other user can read the section")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolMoveAgents, map[string]any{
		"agents": []string{"Pip", "Bea"}, "section": "Writing",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "No agent is named Bea.") {
		t.Fatalf("other agent %d %s", code, body)
	}
	pip, err := h.st.GetAgent(ctx, user.ID, sibling.ID)
	if err != nil || pip.SectionID == writing[0].ID {
		t.Fatal("partial move")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolMoveAgents, map[string]any{
		"agents": []string{"Pip"}, "section": "Writing", "agent_id": otherAgent.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Moved Pip to Writing.") {
		t.Fatalf("move %d %s", code, body)
	}
	pip, err = h.st.GetAgent(ctx, user.ID, sibling.ID)
	if err != nil || pip.SectionID != writing[0].ID {
		t.Fatalf("pip section %+v %v", pip, err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolMoveAgents, map[string]any{
		"agents": []string{"Pip"}, "section": "Writing",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "already in Writing") {
		t.Fatalf("already moved %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolDeleteSection, map[string]any{"section": writing[0].ID}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Move the agents out of Writing") {
		t.Fatalf("delete occupied %d %s", code, body)
	}
	if _, err := h.st.GetSection(ctx, user.ID, writing[0].ID); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolCreateSection, map[string]any{"name": "Writing"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) {
		t.Fatalf("second section %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolMoveAgents, map[string]any{
		"agents": []string{caller.ID}, "section": "Writing",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "More than one section is named Writing") {
		t.Fatalf("ambiguous section %d %s", code, body)
	}
	ada, err := h.st.GetAgent(ctx, user.ID, caller.ID)
	if err != nil || ada.SectionID == writing[0].ID {
		t.Fatal("ambiguous move applied")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolMoveAgents, map[string]any{
		"agents": []string{"Pip"}, "out": true,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Moved Pip to Agents.") {
		t.Fatalf("move out %d %s", code, body)
	}
	pip, err = h.st.GetAgent(ctx, user.ID, sibling.ID)
	agentsSec, err2 := h.st.SystemSection(ctx, user.ID, "agents")
	if err != nil || err2 != nil || pip.SectionID != agentsSec.ID {
		t.Fatalf("back %+v %v %v", pip, err, err2)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolRenameSection, map[string]any{
		"section": "Agents", "name": "Mine",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That section stays.") {
		t.Fatalf("rename system %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolDeleteSection, map[string]any{"section": "Pinned"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That section stays.") {
		t.Fatalf("delete system %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolRenameSection, map[string]any{
		"section": writing[0].ID, "name": "Drafts",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Renamed the section to Drafts.") {
		t.Fatalf("rename %d %s", code, body)
	}
	renamed, err := h.st.GetSection(ctx, user.ID, writing[0].ID)
	if err != nil || renamed.Name != "Drafts" {
		t.Fatalf("renamed %+v %v", renamed, err)
	}
	for _, sec := range sectionsNamed(t, h, user.ID, "Writing") {
		code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolDeleteSection, map[string]any{"section": sec.ID}))
		if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Removed Writing.") {
			t.Fatalf("delete section %d %s", code, body)
		}
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolDeleteSection, map[string]any{"section": "Drafts"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Removed Drafts.") {
		t.Fatalf("delete renamed %d %s", code, body)
	}
	open, err := h.st.CreateRun(ctx, user.ID, made[0].ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolUpdateGroup, map[string]any{
		"group": "Standup", "title": "Planning",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Wait until the reply finishes.") {
		t.Fatalf("busy %d %s", code, body)
	}
	got, err := h.st.GetThread(ctx, user.ID, made[0].ID)
	if err != nil || got.Title != "Standup" {
		t.Fatalf("title changed %q %v", got.Title, err)
	}
	if _, err := h.st.FailRun(ctx, user.ID, open.ID, "stopped", 0); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolUpdateGroup, map[string]any{
		"group": made[0].ID, "title": "Planning",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Renamed the group to Planning.") {
		t.Fatalf("rename group %d %s", code, body)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolDeleteGroup, map[string]any{"group": "Planning"}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Deleted Planning.") {
		t.Fatalf("delete %d %s", code, body)
	}
	if _, err := h.st.GroupAccess(ctx, user.ID, made[0].ID); err == nil {
		t.Fatal("deleted group still opens")
	}
	ada, err = h.st.GetAgent(ctx, user.ID, caller.ID)
	if err != nil || ada.ID == "" {
		t.Fatal("delete removed the agent")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolDeleteGroup, map[string]any{"group": "Planning"}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "No group is named Planning.") {
		t.Fatalf("delete missing %d %s", code, body)
	}
	h.login("organize-owner@example.com", password, secret, "203.0.113.123:4000", false)
	code, pageBody, _ := h.do(http.MethodGet, "/?list=1", "", "203.0.113.123:4000", nil)
	if code != http.StatusOK {
		t.Fatalf("home %d", code)
	}
	start := strings.Index(pageBody, `id="sb-sections"`)
	agentsAt := strings.Index(pageBody, `id="sb-kind-agents"`)
	foot := strings.Index(pageBody, `class="sb-foot"`)
	if start < 0 || agentsAt < start || foot < agentsAt || strings.Count(pageBody, `id="sb-sections"`) != 1 {
		t.Fatalf("sidebar wrapper start %d agents %d foot %d", start, agentsAt, foot)
	}
	if !strings.Contains(pageBody[start:foot], "</div>") || strings.Contains(pageBody[start:foot], `class="sb-foot"`) {
		t.Fatal("foot is inside the sections")
	}
}

func TestOrganizeFenceAndSidebar(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("organize-fence@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("organize-fence-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	pip := seedCaller(t, h, user, "Pip")
	_ = seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	th, err := h.st.DMThread(ctx, user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ToggleFold(ctx, user.ID, "groups"); err != nil {
		t.Fatal(err)
	}
	code, body := machinePost(t, h, "203.0.113.124:9", token, caller.ID, "/agents", routinePayload(grouptool.ToolCreateGroup, map[string]any{
		"title": "Standup", "agents": []string{"Ada", "Pip"},
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) {
		t.Fatalf("create %d %s", code, body)
	}
	var buf bytes.Buffer
	if err := h.srv.writeSidebar(ctx, &buf, user, runRow, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	frame := buf.String()
	for _, needle := range []string{`id="sb-sections"`, `hx-swap-oob="outerHTML"`, "Standup", `data-name="Ada"`, `data-name="Pip"`, `class="ag on"`} {
		if !strings.Contains(frame, needle) {
			t.Fatalf("missing %s in %s", needle, frame)
		}
	}
	if strings.Contains(frame, `data-name="Bea"`) || strings.Contains(frame, "<!doctype") || strings.Contains(frame, `id="stage"`) || strings.Contains(frame, `class="sb-foot"`) {
		t.Fatalf("frame replaced the page %s", frame)
	}
	folds, err := h.st.FoldedSections(ctx, user.ID)
	if err != nil || folds["groups"] {
		t.Fatalf("groups stayed folded %+v %v", folds, err)
	}
	buf.Reset()
	if err := h.srv.writeSidebar(ctx, &buf, user, runRow, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("second frame %s", buf.String())
	}
	msg, ev, err := h.st.FinishRun(ctx, user.ID, runRow.ID, "Adding it.\n\n```castbot-organize\n{\"action\":\"create_group\",\"title\":\"Standup\",\"agents\":[\"Ada\",\"Pip\"]}\n```\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err = h.st.GetRun(ctx, user.ID, runRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	fw := &frameWriter{w: &buf, seq: ev.Seq, from: -1}
	if err := h.srv.writeDone(ctx, fw, user, runRow, msg.ID, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "castbot-organize") || !strings.Contains(buf.String(), "Adding it.") {
		t.Fatalf("fence reply %s", buf.String())
	}
	if len(groupsTitled(t, h, user.ID, "Standup")) != 1 {
		t.Fatal("fence created a second group")
	}
	h.srv.maybeGroup(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: runRow.ID})
	if len(groupsTitled(t, h, user.ID, "Standup")) != 1 {
		t.Fatal("hook created a second group")
	}
	sectionRun, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	msg, ev, err = h.st.FinishRun(ctx, user.ID, sectionRun.ID, "```castbot-organize\n{\"action\":\"create_section\",\"name\":\"Writing\"}\n```", 0)
	if err != nil {
		t.Fatal(err)
	}
	sectionRun, err = h.st.GetRun(ctx, user.ID, sectionRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	fw = &frameWriter{w: &buf, seq: ev.Seq, from: -1}
	if err := h.srv.writeDone(ctx, fw, user, sectionRun, msg.ID, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Added the section Writing.") || !strings.Contains(buf.String(), `id="sb-sections"`) || strings.Contains(buf.String(), "castbot-organize") || strings.Contains(buf.String(), `data-name="Bea"`) {
		t.Fatalf("section frame %s", buf.String())
	}
	if len(sectionsNamed(t, h, user.ID, "Writing")) != 1 {
		t.Fatal("section missing")
	}
	again, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, again.ID, "```castbot-organize\n{\"action\":\"create_section\",\"name\":\"Writing\"}\n```", 0); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeGroup(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: again.ID})
	if len(sectionsNamed(t, h, user.ID, "Writing")) != 1 {
		t.Fatal("second fence created a section")
	}
	reply, err := h.st.LatestAgentReply(ctx, user.ID, again.ID)
	if err != nil || !strings.Contains(reply.Body, "already there") || strings.Contains(reply.Body, "castbot-organize") {
		t.Fatalf("dedupe reply %q %v", reply.Body, err)
	}
	kept, err := h.st.CreateRun(ctx, user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(ctx, user.ID, kept.ID, "Leave this.\n```castbot-organize\n{\"action\":\"create_group\",\"title\":\"TITLE\",\"agents\":[\"NAME\",\"Pip\"]}\n```", 0); err != nil {
		t.Fatal(err)
	}
	h.srv.maybeGroup(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: kept.ID})
	reply, err = h.st.LatestAgentReply(ctx, user.ID, kept.ID)
	if err != nil || !strings.Contains(reply.Body, "TITLE") || !strings.Contains(reply.Body, "Leave this.") {
		t.Fatalf("placeholder removed %q %v", reply.Body, err)
	}
	if len(groupsTitled(t, h, user.ID, "Standup")) != 1 {
		t.Fatal("placeholder created a group")
	}
	_ = pip
}

func groupsTitled(t *testing.T, h *harness, userID, title string) []store.Thread {
	t.Helper()
	rows, err := h.st.ListGroups(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Thread
	for _, row := range rows {
		if row.Title == title {
			out = append(out, row)
		}
	}
	return out
}

func sectionsNamed(t *testing.T, h *harness, userID, name string) []store.Section {
	t.Helper()
	rows, err := h.st.ListSections(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Section
	for _, row := range rows {
		if row.Name == name {
			out = append(out, row)
		}
	}
	return out
}
