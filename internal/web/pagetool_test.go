package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/pagetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

func TestMachinePageTools(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	user, _ := h.account("page-owner@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("page-other@example.com", "Bea", "correct-horse-1")
	member := acceptHostedInvite(t, h, user, "page-guest@example.com", "Cara")
	caller := seedCaller(t, h, user, "Ada")
	sibling := seedCaller(t, h, user, "Pip")
	otherAgent := seedCaller(t, h, other, "Bea")
	memberAgent := seedCaller(t, h, member.User, "Cara")
	token := guestToken(t, h, caller)
	otherToken := guestToken(t, h, otherAgent)
	sibToken := guestToken(t, h, sibling)
	ip := "203.0.113.161:9"

	code, body := machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolList, map[string]any{}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "No pages yet.") {
		t.Fatalf("empty list %d", code)
	}
	code, body = machinePost(t, h, ip, otherToken, caller.ID, "/agents", routinePayload(pagetool.ToolCreate, map[string]any{
		"title": "Stolen", "document": samplePage("page-body-secret"),
	}))
	if code != http.StatusNotFound || strings.Contains(body, "Saved") {
		t.Fatalf("other token %d", code)
	}
	code, body = machinePost(t, h, ip, token, sibling.ID, "/agents", routinePayload(pagetool.ToolCreate, map[string]any{
		"title": "Sibling path", "document": samplePage("page-body-secret"), "user_id": other.ID,
	}))
	if code != http.StatusNotFound {
		t.Fatalf("sibling path %d", code)
	}
	if n := len(mustAgentPages(t, h, user.ID, caller.ID)); n != 0 {
		t.Fatalf("rejected calls stored %d", n)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolCreate, map[string]any{
		"title": "Trials", "document": samplePage("page-body-secret"), "user_id": other.ID, "agent_id": sibling.ID,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Saved Trials.") || !strings.Contains(body, "/p/") || !strings.Contains(body, "Revision 1.") || !strings.Contains(body, "only_you") {
		t.Fatalf("create %d", code)
	}
	made := mustAgentPages(t, h, user.ID, caller.ID)
	if len(made) != 1 || made[0].UserID != user.ID || made[0].AgentID != caller.ID || made[0].Visibility != store.PagePrivate {
		t.Fatalf("owner row %+v", made)
	}
	if owned := mustOwnedPages(t, h, other.ID); len(owned) != 0 {
		t.Fatal("forged user id stored a page")
	}
	secret := made[0]

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolCreate, map[string]any{
		"title": "Broken", "document": map[string]any{"v": 2, "blocks": []any{}},
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "That document was not saved.") || !strings.Contains(body, "Set v to 1.") || len(mustAgentPages(t, h, user.ID, caller.ID)) != 1 {
		t.Fatalf("bad document %d %s", code, body)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolUpdate, map[string]any{
		"page": secret.ID, "document": samplePage("page-body-updated"), "revision": 1,
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Revision 2.") || !strings.Contains(body, "/p/"+secret.Token) {
		t.Fatalf("update %d", code)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolUpdate, map[string]any{
		"page": secret.Token, "document": samplePage("page-body-stale"), "revision": 1,
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "The page changed. Read it and try again.") {
		t.Fatalf("stale %d", code)
	}
	current, err := h.st.PageForAgent(ctx, user.ID, caller.ID, secret.ID)
	if err != nil || current.Revision != 2 || !strings.Contains(current.Body, "page-body-updated") || strings.Contains(current.Body, "page-body-stale") {
		t.Fatalf("stale wrote %v rev %d", err, current.Revision)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolList, map[string]any{}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || strings.Contains(body, "page-body-updated") || strings.Contains(body, "page-body-secret") || !strings.Contains(body, "Trials") || !strings.Contains(body, secret.ID) {
		t.Fatalf("list %d", code)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolRead, map[string]any{"page": secret.Token}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "page-body-updated") || !strings.Contains(body, "Revision: 2") {
		t.Fatalf("read %d", code)
	}

	code, body = machinePost(t, h, ip, sibToken, sibling.ID, "/agents", routinePayload(pagetool.ToolUpdate, map[string]any{
		"page": secret.ID, "document": samplePage("page-body-sibling"), "revision": 2,
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "No page matches that.") {
		t.Fatalf("sibling update %d", code)
	}
	current, err = h.st.PageForAgent(ctx, user.ID, caller.ID, secret.ID)
	if err != nil || strings.Contains(current.Body, "page-body-sibling") || current.Revision != 2 {
		t.Fatal("sibling update stored")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolShare, map[string]any{
		"page": secret.ID, "visibility": "invited",
	}))
	if code != http.StatusOK || !strings.Contains(body, `"error":true`) || !strings.Contains(body, "Say who should see it.") {
		t.Fatalf("empty invited %d", code)
	}
	if current, err = h.st.PageForAgent(ctx, user.ID, caller.ID, secret.ID); err != nil || current.Visibility != store.PagePrivate {
		t.Fatal("empty invited changed visibility")
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolShare, map[string]any{
		"page": secret.ID, "visibility": "invited", "people": []string{member.DisplayName},
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Shared Trials.") || !strings.Contains(body, "invited") {
		t.Fatalf("person share %d", code)
	}
	if _, err := h.st.PageForUser(ctx, member.ID, secret.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PageForAgent(ctx, member.ID, memberAgent.ID, secret.Token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("person grant became an agent grant: %v", err)
	}
	if _, err := h.st.PageForAgent(ctx, user.ID, sibling.ID, secret.Token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("sibling read a person grant: %v", err)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolShare, map[string]any{
		"page": secret.ID, "visibility": "invited", "agents": []string{sibling.Name},
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Shared Trials.") {
		t.Fatalf("agent share %d", code)
	}
	if _, err := h.st.PageForAgent(ctx, user.ID, sibling.ID, secret.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PageForUser(ctx, member.ID, secret.Token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replaced person grant still reads: %v", err)
	}
	if _, err := h.st.PageForAgent(ctx, member.ID, memberAgent.ID, secret.Token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("member agent reads an agent grant for someone else: %v", err)
	}

	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolRename, map[string]any{
		"page": secret.Token, "title": "Trials renamed",
	}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Renamed Trials renamed.") || !strings.Contains(body, "/p/"+secret.Token) || !strings.Contains(body, "Revision 2.") {
		t.Fatalf("rename %d", code)
	}
	code, body = machinePost(t, h, ip, token, caller.ID, "/agents", routinePayload(pagetool.ToolDelete, map[string]any{"page": secret.ID}))
	if code != http.StatusOK || strings.Contains(body, `"error":true`) || !strings.Contains(body, "Deleted Trials renamed.") {
		t.Fatalf("delete %d", code)
	}
	if _, err := h.st.PageForAgent(ctx, user.ID, caller.ID, secret.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted page still reads: %v", err)
	}
}

func TestPageFenceCreatesOnce(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("page-fence@example.com", "Ada", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	settlePageTwice(t, h, user.ID, caller, "Leave this.\n\n```castbot-page\n{\"action\":\"create_page\",\"title\":\"TITLE\",\"document\":\"{}\"}\n```\n")
	kept := agentReplyBody(t, h, user.ID, caller.ID)
	if !strings.Contains(kept, "castbot-page") || !strings.Contains(kept, "Leave this.") || len(mustAgentPages(t, h, user.ID, caller.ID)) != 0 {
		t.Fatal("invalid fence was applied")
	}
	rawDoc, err := json.Marshal(`{"v":1,"blocks":[{"type":"markdown","text":"Hello from the page"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	settlePageTwice(t, h, user.ID, caller, "Saved it.\n\n```castbot-page\n{\"action\":\"create_page\",\"title\":\"Trials\",\"document\":"+string(rawDoc)+",\"user_id\":\"other\"}\n```\n")
	body := agentReplyBody(t, h, user.ID, caller.ID)
	rows := mustAgentPages(t, h, user.ID, caller.ID)
	if len(rows) != 1 || rows[0].UserID != user.ID || strings.Contains(body, "castbot-page") || !strings.Contains(body, "Saved it.") || !strings.Contains(body, "/p/"+rows[0].Token) {
		t.Fatal("fence create")
	}
}

func settlePageTwice(t *testing.T, h *harness, userID string, caller store.Agent, body string) {
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
	job := run.Job{UserID: userID, ThreadID: th.ID, AgentID: caller.ID, RunID: runRow.ID}
	h.srv.settleTalk(job, "done")
	h.srv.settleTalk(job, "done")
}

func samplePage(text string) map[string]any {
	return map[string]any{"v": 1, "blocks": []any{map[string]any{"type": "markdown", "text": text}}}
}

func mustAgentPages(t *testing.T, h *harness, userID, agentID string) []store.Page {
	t.Helper()
	rows, err := h.st.ListAgentPages(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func mustOwnedPages(t *testing.T, h *harness, userID string) []store.Page {
	t.Helper()
	rows, err := h.st.ListOwnedPages(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
