package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/grouptool"
)

func TestMachinePlacesAGroup(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("place-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("place-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	ip := "203.0.113.125:9"
	group, err := h.st.CreateGroup(ctx, user.ID, "Standup", []string{caller.ID, sibling.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	research, err := h.st.CreateSection(ctx, user.ID, "Research")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := h.st.CreateSection(ctx, other.ID, "Private")
	if err != nil {
		t.Fatal(err)
	}

	code, body := machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": "Research", "user_id": other.ID,
	}))
	if code != http.StatusNotFound {
		t.Fatalf("other token %d", code)
	}
	if placedSection(t, h, user.ID, "Standup") != "" {
		t.Fatal("other token placed the group")
	}

	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": group.ID, "section": research.ID,
	}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d", code)
	}
	if placedSection(t, h, user.ID, "Standup") != "" || groupTitle(t, h, user.ID, "Standup") != "Standup" {
		t.Fatal("sibling path placed the group")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Say which group.") {
		t.Fatalf("blank %d %s", code, snippet(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Missing",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "No group is named Missing.") {
		t.Fatalf("missing %d %s", code, snippet(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": "Research", "user_id": other.ID, "agent_id": sibling.ID, "title": "Renamed", "value": "secret-token",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Moved Standup to Research.") || strings.Contains(body, other.ID) || strings.Contains(body, "secret-token") || strings.Contains(body, "Renamed") {
		t.Fatalf("place %d %s", code, snippet(body))
	}
	if placedSection(t, h, user.ID, "Standup") != research.ID || groupTitle(t, h, user.ID, "Standup") != "Standup" {
		t.Fatal("place did not keep the title")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": group.ID, "section": "Research",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Standup is already in Research.") {
		t.Fatalf("already %d %s", code, snippet(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": foreign.ID,
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That section was not found.") {
		t.Fatalf("foreign section %d %s", code, snippet(body))
	}
	if placedSection(t, h, user.ID, "Standup") != research.ID {
		t.Fatal("foreign section moved the group")
	}
	if _, err := h.st.GetSection(ctx, other.ID, foreign.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := h.st.CreateSection(ctx, user.ID, "Lab"); err != nil {
		t.Fatal(err)
	}
	lab, err := h.st.CreateSection(ctx, user.ID, "Lab")
	if err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": "Lab",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "More than one section is named Lab.") {
		t.Fatalf("ambiguous %d %s", code, snippet(body))
	}
	if placedSection(t, h, user.ID, "Standup") != research.ID {
		t.Fatal("ambiguous section moved the group")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": lab.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Moved Standup to Lab.") {
		t.Fatalf("by id %d %s", code, snippet(body))
	}
	if placedSection(t, h, user.ID, "Standup") != lab.ID {
		t.Fatal("id place missed")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": "",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Moved Standup back to Groups.") {
		t.Fatalf("empty %d %s", code, snippet(body))
	}
	if placedSection(t, h, user.ID, "Standup") != "" {
		t.Fatal("empty section left a place")
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": "groups",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Standup is already in Groups.") {
		t.Fatalf("groups %d %s", code, snippet(body))
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": research.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Moved Standup to Research.") {
		t.Fatalf("return %d %s", code, snippet(body))
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(grouptool.ToolPlaceGroup, map[string]any{
		"group": "Standup", "section": "groups",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Moved Standup back to Groups.") {
		t.Fatalf("groups word %d %s", code, snippet(body))
	}
	if placedSection(t, h, user.ID, "Standup") != "" || groupTitle(t, h, user.ID, "Standup") != "Standup" {
		t.Fatal("groups word left a place")
	}
	if _, err := h.st.GetAgent(ctx, other.ID, otherAgent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPlaceFenceMovesTheGroup(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("place-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	if _, err := h.st.CreateGroup(ctx, user.ID, "Standup", []string{caller.ID, sibling.ID}, nil); err != nil {
		t.Fatal(err)
	}
	research, err := h.st.CreateSection(ctx, user.ID, "Research")
	if err != nil {
		t.Fatal(err)
	}

	replyRoutine(t, h, user.ID, caller, "Leave this.\n\n```castbot-organize\n{\"action\":\"place_group\",\"group\":\"NAME\",\"section\":\"Research\"}\n```\n")
	held := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(held, "castbot-organize") || !strings.Contains(held, "Leave this.") || strings.Contains(held, "Moved Standup") {
		t.Fatalf("placeholder %s", snippet(held))
	}
	if placedSection(t, h, user.ID, "Standup") != "" {
		t.Fatal("placeholder placed the group")
	}

	replyRoutine(t, h, user.ID, caller, "Moving it.\n\n```castbot-organize\n{\"action\":\"place_group\",\"group\":\"Standup\",\"section\":\"Research\",\"user_id\":\"other\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(body, "castbot-organize") || !strings.Contains(body, "Moving it.") {
		t.Fatalf("fence %s", snippet(body))
	}
	if placedSection(t, h, user.ID, "Standup") != research.ID || groupTitle(t, h, user.ID, "Standup") != "Standup" {
		t.Fatal("fence did not place the group")
	}

	replyRoutine(t, h, user.ID, caller, "```castbot-organize\n{\"action\":\"place_group\",\"group\":\"Standup\",\"section\":\"groups\"}\n```")
	back := agentReplyBody(t, h, user.ID, caller.ID)
	if strings.Contains(back, "castbot-organize") || !strings.Contains(back, "Moved Standup back to Groups.") {
		t.Fatalf("back fence %s", snippet(back))
	}
	if placedSection(t, h, user.ID, "Standup") != "" {
		t.Fatal("back fence left a place")
	}
}

func placedSection(t *testing.T, h *harness, userID, title string) string {
	t.Helper()
	cards, err := h.st.ListGroupCards(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, card := range cards {
		if card.Title == title && card.UserID == userID {
			return card.SectionID
		}
	}
	t.Fatalf("group %s missing", title)
	return ""
}

func groupTitle(t *testing.T, h *harness, userID, title string) string {
	t.Helper()
	rows := groupsTitled(t, h, userID, title)
	if len(rows) != 1 {
		t.Fatalf("titles %d", len(rows))
	}
	return rows[0].Title
}
