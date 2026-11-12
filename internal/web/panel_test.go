package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/store"
)

func TestWorkspaceNavGone(t *testing.T) {
	h, user, ip := panelSession(t, "nav-s19@example.com", "Alex Rivera")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) })
	makeAgent(t, h.st, user.ID, "Atlas")
	code, body, _ := h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	for _, gone := range []string{`class="nav"`, `class="bnav"`, `href="/approvals"`, `href="/routines"`, `href="/files"`, `href="/usage"`} {
		if strings.Contains(body, gone) {
			t.Fatalf("home still has %s", gone)
		}
	}
	if !strings.Contains(body, "Pick an agent") || !strings.Contains(body, "Agents that need you show a shield with a count.") {
		t.Fatalf("missing pick state: %s", body)
	}
	if !strings.Contains(body, "Alex Rivera") {
		t.Fatal("home dropped the account name")
	}
}

func TestAgentPageHasNoTopTabs(t *testing.T) {
	h, user, ip := panelSession(t, "tabs-s19@example.com", "Alex Rivera")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	_, closed, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if strings.Contains(closed, `class="tabs"`) {
		t.Fatal("agent top tabs are still on the page")
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=agent", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if strings.Contains(body, `class="tabs"`) {
		t.Fatal("opening the panel brought the top tabs back")
	}
	for _, tab := range []string{">Agent<", ">Machine<", ">Secrets<", ">Approvals<", ">Routines<", ">Files<"} {
		if !strings.Contains(body, tab) {
			t.Fatalf("panel missing tab %s", tab)
		}
	}
	secrets := strings.Index(body, ">Secrets<")
	approvals := strings.Index(body, ">Approvals<")
	if secrets < 0 || approvals < 0 || secrets > approvals {
		t.Fatal("Secrets is not before Approvals")
	}
}

func TestPanelScopedToAgent(t *testing.T) {
	h, user, ip := panelSession(t, "scope-s19@example.com", "Alex Rivera")
	atlas := makeAgent(t, h.st, user.ID, "Atlas")
	pip := makeAgent(t, h.st, user.ID, "Pip")
	panelApproval(t, h, user.ID, pip.ID, "git_push", "push pip only")
	for _, c := range []struct{ path, fine string }{
		{"/agents/" + atlas.ID + "?panel=approvals", "Only Atlas's approvals"},
		{"/agents/" + atlas.ID + "?panel=routines", "Only Atlas&#39;s routines"},
		{"/agents/" + atlas.ID + "?panel=files", "Only Atlas's files"},
	} {
		code, body, _ := h.do(http.MethodGet, c.path, "", ip, nil)
		if code != http.StatusOK {
			t.Fatalf("%s %d", c.path, code)
		}
		if strings.Contains(body, "push pip only") {
			t.Fatalf("%s included the other agent's approval arguments", c.path)
		}
		if !strings.Contains(body, c.fine) {
			t.Fatalf("%s missing the agent fine print", c.path)
		}
	}
}

func TestPanelSwapKeepsComposer(t *testing.T) {
	h, user, ip := panelSession(t, "swap-s19@example.com", "Alex Rivera")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	code, partial, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, map[string]string{
		"HX-Request": "true",
		"HX-Target":  "agent-panel",
	})
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if strings.Contains(partial, `id="composer"`) || strings.Contains(partial, "<textarea") {
		t.Fatal("panel partial includes the composer")
	}
	if !strings.Contains(partial, "State from the cloud API. Checking it does not wake the machine.") {
		t.Fatal("machine panel lost its fine print")
	}
	code, full, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(full, `id="composer"`) || !strings.Contains(full, `data-agent-id="`+agent.ID+`"`) {
		t.Fatal("full panel page dropped the composer")
	}
	_, js, _ := h.do(http.MethodGet, "/static/app.js", "", ip, nil)
	for _, needle := range []string{"draft:", "sessionStorage", "function closeAgentPanel", "scroll:"} {
		if !strings.Contains(js, needle) {
			t.Fatalf("app.js missing %s", needle)
		}
	}
}

func TestPanelCloseAndHistoryHooks(t *testing.T) {
	h, user, ip := panelSession(t, "close-s19@example.com", "Alex Rivera")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	_, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=agent", "", ip, nil)
	if !strings.Contains(body, `aria-label="Close agent panel (Esc)"`) {
		t.Fatal("desktop close is missing")
	}
	if !strings.Contains(body, `data-close-panel`) {
		t.Fatal("close control is not marked")
	}
	_, js, _ := h.do(http.MethodGet, "/static/app.js", "", ip, nil)
	for _, needle := range []string{
		"function closeAgentPanel",
		"history.back()",
		"pushState",
		"replaceState",
		"function onEscape",
		".rmodal",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("app.js missing %s", needle)
		}
	}
}

func TestPhonePanelClose(t *testing.T) {
	h, user, ip := panelSession(t, "phone-s19@example.com", "Alex Rivera")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	_, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=agent", "", ip, nil)
	if !strings.Contains(body, `aria-label="Back to chat"`) || !strings.Contains(body, ">Chat<") {
		t.Fatal("phone Chat close is missing")
	}
	if !strings.Contains(body, `aria-label="Close panel and return to chat"`) {
		t.Fatal("peek strip is missing")
	}
}

func TestSettingsCloseReturns(t *testing.T) {
	h, _, ip := panelSession(t, "set-s19@example.com", "Alex Rivera")
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if !strings.Contains(body, `data-sclose`) || !strings.Contains(body, `aria-label="Close settings"`) {
		t.Fatal("settings close button is missing")
	}
	if strings.Contains(body, `sclose" href="/"`) {
		t.Fatal("settings close still sends the person home")
	}
	_, js, _ := h.do(http.MethodGet, "/static/app.js", "", ip, nil)
	if !strings.Contains(js, "function closeSettings") || !strings.Contains(js, "history.back()") || !strings.Contains(js, "castbot.lastAgent") {
		t.Fatal("settings close does not return to the previous view")
	}
}

func TestUsageSectionUnmetered(t *testing.T) {
	h, user, ip := panelSession(t, "use-s19@example.com", "Alex Rivera")
	makeAgent(t, h.st, user.ID, "Atlas")
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	for _, line := range []string{
		"Machine time is not metered in this version.",
		"The estimate arrives with the machine panel update.",
		"1 agents, each on its own machine",
		"Machines pause after idle time, so a paused agent costs disk only.",
		"Billed by your provider",
		"castbot never adds a charge on top.",
		"New here",
		`id="usage"`,
		`href="/settings#usage"`,
	} {
		if !strings.Contains(body, line) {
			t.Fatalf("usage section missing %q", line)
		}
	}
	if strings.Contains(body, "$7.42") || strings.Contains(body, "$0.00") {
		t.Fatal("settings still shows a dollar figure")
	}
	code, _, hdr := h.do(http.MethodGet, "/usage", "", ip, nil)
	if code != http.StatusFound || hdr.Get("Location") != "/settings#usage" {
		t.Fatalf("usage redirect %d %s", code, hdr.Get("Location"))
	}
	_, secrets, _ := h.do(http.MethodGet, "/settings/secrets", "", ip, nil)
	if strings.Contains(secrets, `href="/usage"`) || !strings.Contains(secrets, `href="/settings#usage"`) {
		t.Fatal("secrets index still points at /usage")
	}
}

func TestLegacyRoutesRedirect(t *testing.T) {
	h, user, ip := panelSession(t, "redir-s19@example.com", "Alex Rivera")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) })
	older := makeAgent(t, h.st, user.ID, "Atlas")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC) })
	newer := makeAgent(t, h.st, user.ID, "Pip")
	checks := []struct{ path, want string }{
		{"/approvals", "/agents/" + newer.ID + "?panel=approvals"},
		{"/routines", "/agents/" + newer.ID + "?panel=routines"},
		{"/files", "/agents/" + newer.ID + "?panel=files"},
		{"/agents/" + older.ID + "/machine", "/agents/" + older.ID + "?panel=machine"},
		{"/agents/" + older.ID + "/routines", "/agents/" + older.ID + "?panel=routines"},
		{"/agents/" + older.ID + "/files", "/agents/" + older.ID + "?panel=files"},
	}
	for _, c := range checks {
		code, _, hdr := h.do(http.MethodGet, c.path, "", ip, nil)
		if code != http.StatusFound || hdr.Get("Location") != c.want {
			t.Fatalf("%s -> %d %q, want 302 %q", c.path, code, hdr.Get("Location"), c.want)
		}
	}
	empty, _, eip := panelSession(t, "empty-s19@example.com", "Alex Rivera")
	for _, path := range []string{"/approvals", "/routines", "/files"} {
		code, _, hdr := empty.do(http.MethodGet, path, "", eip, nil)
		if code != http.StatusFound || hdr.Get("Location") != "/" {
			t.Fatalf("no agents %s -> %d %s", path, code, hdr.Get("Location"))
		}
	}
}

func TestApprovalsRedirectOldest(t *testing.T) {
	h, user, ip := panelSession(t, "old-s19@example.com", "Alex Rivera")
	first := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return first })
	atlas := makeAgent(t, h.st, user.ID, "Atlas")
	later := first.Add(time.Hour)
	h.st.SetNow(func() time.Time { return later })
	makeAgent(t, h.st, user.ID, "Pip")
	h.st.SetNow(func() time.Time { return first })
	panelApproval(t, h, user.ID, atlas.ID, "git_push", "push demo")
	pip := namedAgent(t, h, user.ID, "Pip")
	h.st.SetNow(func() time.Time { return later })
	panelApproval(t, h, user.ID, pip.ID, "bash", "ls later")
	code, _, hdr := h.do(http.MethodGet, "/approvals", "", ip, nil)
	want := "/agents/" + atlas.ID + "?panel=approvals"
	if code != http.StatusFound || hdr.Get("Location") != want {
		t.Fatalf("oldest redirect %d %s", code, hdr.Get("Location"))
	}
}

func TestSameAgentApprovalAlerts(t *testing.T) {
	h, user, ip := panelSession(t, "same-s19@example.com", "Alex Rivera")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	panelApproval(t, h, user.ID, agent.ID, "git_push", "push demo")
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=agent", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	for _, needle := range []string{
		`class="apill"`,
		`aria-label="1 approval waiting. Open approvals"`,
		`class="tbadge"`,
		"Helper · needs you",
		"(1) Atlas · castbot",
		`id="pending-bar"`,
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("same-agent page missing %s", needle)
		}
	}
	if strings.Contains(body, `id="pending-bar" hidden`) {
		t.Fatal("pending bar is hidden on the agent that is waiting")
	}
	if strings.Contains(body, "s19toast") {
		t.Fatal("the agent you are in also toasted")
	}
	if strings.Contains(body, `href="/approvals"`) {
		t.Fatal("an in-app link still points at /approvals")
	}
}

func TestOtherAgentApprovalToastOnce(t *testing.T) {
	h, user, ip := panelSession(t, "toast-s19@example.com", "Alex Rivera")
	atlas := makeAgent(t, h.st, user.ID, "Atlas")
	pip := makeAgent(t, h.st, user.ID, "Pip")
	panelApproval(t, h, user.ID, atlas.ID, "git_push", "push demo")
	code, body, _ := h.do(http.MethodGet, "/agents/"+pip.ID, "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if !strings.Contains(body, `class="toast s19toast" role="status"`) {
		t.Fatal("missing the other-agent toast")
	}
	if !strings.Contains(body, "Atlas needs your approval:") || !strings.Contains(body, "git_push") {
		t.Fatal("toast does not name the agent and the tool")
	}
	if strings.Contains(body, "push demo") {
		t.Fatal("toast included the approval arguments")
	}
	if !strings.Contains(body, `aria-label="Dismiss"`) || !strings.Contains(body, ">Review<") {
		t.Fatal("toast actions are missing")
	}
	if strings.Contains(body, `class="apill"`) {
		t.Fatal("Pip showed a pill for Atlas's approval")
	}
	code, again, _ := h.do(http.MethodGet, "/agents/"+pip.ID, "", ip, nil)
	if code != http.StatusOK || strings.Contains(again, "s19toast") {
		t.Fatal("the same approval toasted a second time")
	}
}

func TestPanelAuthz(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.account("own-s19@example.com", "Owner", "correct-horse-1")
	_, otherSecret := h.account("else-s19@example.com", "Other", "correct-horse-1")
	agent := makeAgent(t, h.st, owner.ID, "Atlas")
	panelApproval(t, h, owner.ID, agent.ID, "bash", "ls demo")
	ip := "203.0.113.19:9"
	h.login("else-s19@example.com", "correct-horse-1", otherSecret, ip, false)
	for _, path := range []string{
		"/agents/" + agent.ID + "?panel=approvals",
		"/agents/" + agent.ID + "?panel=routines",
		"/agents/" + agent.ID + "?panel=files",
		"/agents/" + agent.ID + "?panel=secrets",
		"/agents/" + agent.ID + "?panel=machine",
		"/agents/" + agent.ID + "/machine",
	} {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusNotFound || strings.Contains(body, "ls demo") || strings.Contains(body, "Atlas") {
			t.Fatalf("user B %s -> %d %s", path, code, body)
		}
	}
}

func TestPanelCSRFUnchanged(t *testing.T) {
	h, user, ip := panelSession(t, "csrf-s19@example.com", "Alex Rivera")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", "csrf=nope&body=hi", ip, nil)
	if code != http.StatusForbidden {
		t.Fatalf("bad csrf %d %s", code, body)
	}
}

func panelSession(t *testing.T, email, name string) (*harness, store.User, string) {
	t.Helper()
	h := newHarness(t)
	user, secret := h.account(email, name, "correct-horse-1")
	ip := "203.0.113.19:9"
	h.login(email, "correct-horse-1", secret, ip, false)
	return h, user, ip
}

func panelApproval(t *testing.T, h *harness, userID, agentID, tool, command string) store.Approval {
	t.Helper()
	ctx := context.Background()
	runRow, err := h.st.CreateRun(ctx, userID, agentThread(t, h, userID, agentID), agentID)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"id":"req-s19","tool":"` + tool + `","arguments":{"command":"` + command + `"}}`)
	ap, err := h.st.RecordPermission(ctx, userID, runRow.ID, raw, h.st.Now().Add(24*time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

func namedAgent(t *testing.T, h *harness, userID, name string) store.Agent {
	t.Helper()
	agents, err := h.st.ListAgents(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no agent %s", name)
	return store.Agent{}
}
