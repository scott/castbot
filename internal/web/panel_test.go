package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/store"
)

func TestWorkspaceNavGone(t *testing.T) {
	h, user, ip := panelSession(t, "nav-s19@example.com", "Alex Rivera")
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) })
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	code, _, hdr := h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/agents/"+agent.ID {
		t.Fatalf("home %d %s", code, hdr.Get("Location"))
	}
	code, chat, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	code, body, _ := h.do(http.MethodGet, "/?list=1", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	for _, page := range []string{chat, body} {
		for _, gone := range []string{`class="nav"`, `class="bnav"`, `href="/approvals"`, `href="/routines"`, `href="/files"`, `href="/usage"`, "Pick an agent", "Agents that need you show a shield with a count."} {
			if strings.Contains(page, gone) {
				t.Fatalf("page still has %s", gone)
			}
		}
	}
	if !strings.Contains(body, "Alex Rivera") || !strings.Contains(body, "Atlas") {
		t.Fatal("agent list dropped the account or the agent")
	}
	if !strings.Contains(chat, `href="/?list=1"`) || !strings.Contains(chat, `aria-label="Back to agents"`) {
		t.Fatal("chat back does not open the agent list")
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

func TestPanelTabClickDoesNotReloadChat(t *testing.T) {
	h, _, ip := panelSession(t, "tab-reload@example.com", "Alex Rivera")
	_, js, _ := h.do(http.MethodGet, "/static/secrets.js", "", ip, nil)
	guard := `tab.hasAttribute("hx-get")`
	replace := `window.location.replace(tab.href)`
	gi := strings.Index(js, guard)
	ri := strings.Index(js, replace)
	if gi < 0 || ri < 0 || gi > ri {
		t.Fatal("panel tab click still forces a full page load")
	}
	if !strings.Contains(js[gi:ri], "return") {
		t.Fatal("hx-get tabs still reach location.replace")
	}
}

func TestAgentSwitchKeepsShell(t *testing.T) {
	h, user, ip := panelSession(t, "switch-s19@example.com", "Alex Rivera")
	atlas := makeAgent(t, h.st, user.ID, "Atlas")
	pip := makeAgent(t, h.st, user.ID, "Pip")
	code, full, _ := h.do(http.MethodGet, "/agents/"+atlas.ID+"?panel=machine", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(full, `id="stage"`) || !strings.Contains(full, `id="chat-col"`) || !strings.Contains(full, `class="sb"`) {
		t.Fatal("agent page is missing the stage shell")
	}
	if strings.Index(full, `class="sb"`) > strings.Index(full, `id="stage"`) {
		t.Fatal("sidebar is inside the swapped stage")
	}
	hx := map[string]string{"HX-Request": "true", "HX-Target": "stage"}
	code, body, hdr := h.do(http.MethodGet, "/agents/"+pip.ID+"?panel=machine", "", ip, hx)
	if code != http.StatusOK || hdr.Get("HX-Redirect") != "" {
		t.Fatalf("switch %d %q", code, hdr.Get("HX-Redirect"))
	}
	if strings.Contains(body, "<!doctype") || strings.Contains(body, `class="sb"`) || strings.Contains(body, "<html") {
		t.Fatal("agent switch redrew the shell")
	}
	for _, needle := range []string{
		`id="chat-col"`,
		`id="agent-panel"`,
		`id="stage-meta"`,
		`data-agent-id="` + pip.ID + `"`,
		`data-agent="` + pip.ID + `"`,
		"State from the cloud API. Checking it does not wake the machine.",
		">Pip<",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("switch missing %s", needle)
		}
	}
	if strings.Contains(body, `data-agent-id="`+atlas.ID+`"`) || strings.Contains(body, ">Atlas<") {
		t.Fatal("switch included the agent that was left")
	}
	group, err := h.st.CreateGroup(context.Background(), user.ID, "Standup", []string{atlas.ID, pip.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	code, body, hdr = h.do(http.MethodGet, "/groups/"+group.ID+"?panel=group", "", ip, hx)
	if code != http.StatusOK || hdr.Get("HX-Redirect") != "" {
		t.Fatalf("group switch %d %q", code, hdr.Get("HX-Redirect"))
	}
	if strings.Contains(body, "<!doctype") || strings.Contains(body, `class="sb"`) || strings.Contains(body, "<html") {
		t.Fatal("group switch redrew the shell")
	}
	if !strings.Contains(body, `id="chat-col"`) || !strings.Contains(body, `id="agent-panel"`) || !strings.Contains(body, ">Standup<") {
		t.Fatal("group switch dropped the chat or the panel")
	}
	_, js, _ := h.do(http.MethodGet, "/static/app.js", "", ip, nil)
	for _, needle := range []string{
		"function switchChat",
		"function agentSwitchURL",
		`target: "#stage"`,
		`target.id !== "stage"`,
		"__castbotShown",
	} {
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
		"pushState",
		"replaceState",
		"function onEscape",
		".rmodal",
	} {
		if !strings.Contains(js, needle) {
			t.Fatalf("app.js missing %s", needle)
		}
	}
	close := jsFunc(t, js, "closeAgentPanel")
	if strings.Contains(close, "history.back()") || !strings.Contains(close, "chatURL()") || !strings.Contains(close, "hidePanel()") {
		t.Fatalf("panel close walks history: %s", close)
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
	close := jsFunc(t, js, "closeSettings")
	if strings.Contains(close, "history.back()") || !strings.Contains(close, "castbot.back") || !strings.Contains(close, "castbot.lastAgent") || !strings.Contains(close, "location.assign") {
		t.Fatalf("settings close walks history: %s", close)
	}
	if !strings.Contains(jsFunc(t, js, "settleSwitchedChat"), "rememberView()") {
		t.Fatal("switching chats does not remember the page settings should return to")
	}
}

func TestDialogCloseFollowsItsLink(t *testing.T) {
	h, _, ip := panelSession(t, "dialog-close@example.com", "Alex Rivera")
	_, js, _ := h.do(http.MethodGet, "/static/secrets.js", "", ip, nil)
	if strings.Contains(js, "history.back()") || !strings.Contains(js, "location.replace(href)") || !strings.Contains(js, `close.getAttribute("href")`) {
		t.Fatal("dialog close still walks history")
	}
}

func TestUsageSectionEstimate(t *testing.T) {
	h, user, ip := panelSession(t, "use-s19@example.com", "Alex Rivera")
	makeAgent(t, h.st, user.ID, "Atlas")
	code, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	for _, line := range []string{
		"This month",
		"$0.00",
		"An estimate, not a bill.",
		"Estimated machine time for this month.",
		"1 agent, each on its own machine",
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
	if strings.Contains(body, "Machine time is not metered") || strings.Contains(body, "$7.42") {
		t.Fatal("settings still uses the unmetered banner")
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
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=machine", "", ip, map[string]string{"HX-Request": "true", "HX-Target": "stage"})
	if code != http.StatusNotFound || strings.Contains(body, "ls demo") || strings.Contains(body, "Atlas") || strings.Contains(body, `id="chat-col"`) {
		t.Fatalf("user B switch -> %d %s", code, body)
	}
}

func TestPanelPartialRedirectsOutsideChrome(t *testing.T) {
	h, user, ip := panelSession(t, "chrome-s19@example.com", "Alex Rivera")
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	sec, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{
		Name: "NOTES_TOKEN", Value: "demo-only-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	routine, err := h.st.CreateRoutine(context.Background(), user.ID, agent.ID, store.RoutineInput{
		Name: "Morning docs check", Cron: "0 8 * * 1-5", Timezone: "America/Denver",
		Prompt: "Check the docs.", Enabled: true, ApprovalPolicy: store.ApprovalWait, MissedPolicy: store.MissedSkip,
	})
	if err != nil {
		t.Fatal(err)
	}
	hx := map[string]string{"HX-Request": "true", "HX-Target": "agent-panel"}
	stage := map[string]string{"HX-Request": "true", "HX-Target": "stage"}
	list := "/agents/" + agent.ID + "?panel=secrets"
	code, body, hdr := h.do(http.MethodGet, list, "", ip, hx)
	if code != http.StatusOK || hdr.Get("HX-Redirect") != "" || !strings.Contains(body, "NOTES_TOKEN") || strings.Contains(body, "Give Atlas a secret") {
		t.Fatalf("plain panel %d redirect %q", code, hdr.Get("HX-Redirect"))
	}
	for _, path := range []string{
		list + "&give=1",
		list + "&revoke=" + sec.ID,
		"/agents/" + agent.ID + "?panel=routines&routine=" + routine.ID + "&delete=1",
	} {
		code, body, hdr := h.do(http.MethodGet, path, "", ip, hx)
		if code != http.StatusNoContent || hdr.Get("HX-Redirect") != path {
			t.Fatalf("%s -> %d %q", path, code, hdr.Get("HX-Redirect"))
		}
		if body != "" || strings.Contains(body, "demo-only-value") {
			t.Fatal("panel swap included chrome that lives outside the panel")
		}
		code, body, hdr = h.do(http.MethodGet, path, "", ip, stage)
		if code != http.StatusNoContent || hdr.Get("HX-Redirect") != path || body != "" {
			t.Fatalf("switch %s -> %d %q", path, code, hdr.Get("HX-Redirect"))
		}
	}
	code, body, hdr = h.do(http.MethodGet, list+"&give=1", "", ip, nil)
	if code != http.StatusOK || hdr.Get("HX-Redirect") != "" || !strings.Contains(body, "Give Atlas a secret") || strings.Contains(body, "demo-only-value") {
		t.Fatal("full load did not paint the give drawer")
	}
	code, body, _ = h.do(http.MethodGet, list+"&revoke="+sec.ID, "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Take NOTES_TOKEN away from Atlas?") || strings.Contains(body, "demo-only-value") {
		t.Fatal("full load did not paint the revoke confirm")
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=routines&routine="+routine.ID+"&delete=1", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Delete Morning docs check?") {
		t.Fatal("full load did not paint the delete confirm")
	}
}

func TestPanelNestedBack(t *testing.T) {
	h, _, ip := panelSession(t, "back-s19@example.com", "Alex Rivera")
	_, js, _ := h.do(http.MethodGet, "/static/app.js", "", ip, nil)
	if !strings.Contains(js, `hasAttribute("data-panel-nested")`) || !strings.Contains(js, `setAttribute("data-hist-now", "push")`) {
		t.Fatal("a nested panel link does not push a history entry")
	}
	if !strings.Contains(js, `panelOpen() ? "replace" : "push"`) {
		t.Fatal("switching tabs does not replace the open panel entry")
	}
	if !strings.Contains(js, "function hidePanel") || !strings.Contains(js, "if (!panel)") || !strings.Contains(js, "hidePanel()") {
		t.Fatal("leaving the panel does not return to chat")
	}
	if !strings.Contains(js, `setAttribute("data-panel-nested", "")`) {
		t.Fatal("panel links are not marked nested")
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

func jsFunc(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "function "+name+"(")
	if start < 0 {
		t.Fatalf("missing function %s", name)
	}
	rest := src[start:]
	open := strings.Index(rest, "{")
	if open < 0 {
		t.Fatalf("missing body %s", name)
	}
	depth := 0
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return rest[:i+1]
			}
		}
	}
	t.Fatalf("unclosed function %s", name)
	return ""
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

func TestApprovalsPaneFoldsHistory(t *testing.T) {
	h := newHarness(t)
	// The scheduler reads this clock from another goroutine.
	var clockMu sync.Mutex
	clock := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	})
	user, secret := h.account("hist-s19@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.41:9"
	h.login("hist-s19@example.com", "correct-horse-1", secret, ip, false)
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	ctx := context.Background()
	runRow, err := h.st.CreateRun(ctx, user.ID, agentThread(t, h, user.ID, agent.ID), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	const n = 21
	for i := 0; i < n; i++ {
		at := time.Date(2026, 11, 17, 15, i, 0, 0, time.UTC)
		clockMu.Lock()
		clock = at
		clockMu.Unlock()
		raw := fmt.Sprintf(`{"id":"req-hist-%02d","tool":"tool-%02d","arguments":{"command":"echo hist-%02d"}}`, i, i, i)
		ap, err := h.st.RecordPermission(ctx, user.ID, runRow.ID, []byte(raw), at.Add(time.Hour), i+1)
		if err != nil {
			t.Fatal(err)
		}
		status := "approved"
		if i == n-1 {
			status = "denied"
		}
		if _, ok, err := h.st.ClaimDecision(ctx, user.ID, ap.ID, runRow.ID, status, user.ID); err != nil || !ok {
			t.Fatalf("claim %d %v", i, err)
		}
	}
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=approvals", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if strings.Contains(body, "ahist") || !strings.Contains(body, "Nothing is waiting.") {
		t.Fatalf("history still a bullet list %s", body)
	}
	earlier := between(body, `id="approval-earlier"`, `id="approval-filter-empty"`)
	if earlier == "" {
		t.Fatal("missing history")
	}
	if strings.Count(earlier, `class="appr appr-fold`) != n || strings.Count(earlier, `id="hist-appr-`) != n {
		t.Fatalf("folded %d ids %d", strings.Count(earlier, `class="appr appr-fold`), strings.Count(earlier, `id="hist-appr-`))
	}
	if strings.Contains(earlier, " open") || strings.Contains(earlier, ">Approve<") || strings.Contains(earlier, ">Deny<") {
		t.Fatal("history card is expanded or still decidable")
	}
	if !strings.Contains(earlier, "M9 6l6 6-6 6") || !strings.Contains(earlier, ">Denied<") || !strings.Contains(earlier, ">Approved<") {
		t.Fatal("collapsed summary missing")
	}
	if !strings.Contains(earlier, "echo hist-00") || !strings.Contains(earlier, "echo hist-20") {
		t.Fatal("expand body missing the command")
	}
	newest := strings.Index(earlier, ">tool-20<")
	oldest := strings.Index(earlier, ">tool-00<")
	if newest < 0 || oldest < newest {
		t.Fatalf("order newest %d oldest %d", newest, oldest)
	}
	if strings.Count(earlier, ">Today<") != 1 || strings.Contains(earlier, ">Yesterday<") {
		t.Fatal("same-day history was not one Today group")
	}
	if strings.Contains(detailsOpen(earlier, ">tool-20<"), " hidden") || !strings.Contains(detailsOpen(earlier, ">tool-00<"), " hidden") {
		t.Fatal("first page did not keep the newest cards")
	}
	if strings.Count(earlier, ` hidden>`) != n-approvalHistoryPage {
		t.Fatalf("hidden cards %d", strings.Count(earlier, ` hidden>`))
	}
	if !strings.Contains(earlier, "Showing 8 of 21") || !strings.Contains(earlier, ">Show more<") || strings.Contains(elementOpen(earlier, "approval-earlier-more"), " hidden") {
		t.Fatal("show more missing")
	}
	if !strings.Contains(body, `id="approval-filter"`) || !strings.Contains(body, `aria-label="Search approvals"`) || !strings.Contains(body, "No approvals match.") || !strings.Contains(earlier, `data-find="`) {
		t.Fatal("missing search")
	}
	if !strings.Contains(earlier, "echo hist-00") || !strings.Contains(strings.ToLower(earlier), "echo hist-20") {
		t.Fatal("search text missing")
	}
	_, js, _ := h.do(http.MethodGet, "/static/app.js", "", ip, nil)
	for _, needle := range []string{"approval-earlier-more", "approvalHistoryShown", "appr-day", "Showing "} {
		if !strings.Contains(js, needle) {
			t.Fatalf("app.js missing %s", needle)
		}
	}
	_, otherSecret := h.account("other-hist@example.com", "Other", "correct-horse-1")
	h.login("other-hist@example.com", "correct-horse-1", otherSecret, "203.0.113.42:9", false)
	code, other, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=approvals", "", "203.0.113.42:9", nil)
	if code != http.StatusNotFound || strings.Contains(other, "echo hist-") || strings.Contains(other, "tool-20") {
		t.Fatalf("other %d %s", code, other)
	}
}

func TestApprovalsPaneGroupsByDay(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 17, 18, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return now })
	user, secret := h.account("days-s19@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.51:9"
	h.login("days-s19@example.com", "correct-horse-1", secret, ip, false)
	agent := makeAgent(t, h.st, user.ID, "Atlas")
	ctx := context.Background()
	runRow, err := h.st.CreateRun(ctx, user.ID, agentThread(t, h, user.ID, agent.ID), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Denver is UTC-7 in November, so 06:00 UTC on the 17th is still the 16th there.
	stamps := []struct {
		at   time.Time
		tool string
	}{
		{time.Date(2025, 11, 17, 18, 0, 0, 0, time.UTC), "year-ago"},
		{time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC), "nov-15"},
		{time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC), "yest"},
	}
	for i := 0; i < approvalHistoryPage; i++ {
		stamps = append(stamps, struct {
			at   time.Time
			tool string
		}{time.Date(2026, 11, 17, 17, i, 0, 0, time.UTC), fmt.Sprintf("today-%02d", i)})
	}
	for i, item := range stamps {
		h.st.SetNow(func() time.Time { return item.at })
		raw := fmt.Sprintf(`{"id":"req-day-%02d","tool":"%s","arguments":{"command":"echo %s"}}`, i, item.tool, item.tool)
		ap, err := h.st.RecordPermission(ctx, user.ID, runRow.ID, []byte(raw), item.at.Add(time.Hour), i+1)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := h.st.ClaimDecision(ctx, user.ID, ap.ID, runRow.ID, "approved", user.ID); err != nil || !ok {
			t.Fatalf("claim %s %v", item.tool, err)
		}
	}
	h.st.SetNow(func() time.Time { return now })
	code, body, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"?panel=approvals", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	earlier := between(body, `id="approval-earlier"`, `id="approval-filter-empty"`)
	today := strings.Index(earlier, ">Today<")
	yesterday := strings.Index(earlier, ">Yesterday<")
	nov15 := strings.Index(earlier, ">Nov 15<")
	year := strings.Index(earlier, ">Nov 17, 2025<")
	if today < 0 || yesterday < today || nov15 < yesterday || year < nov15 {
		t.Fatalf("day order today %d yesterday %d nov15 %d year %d", today, yesterday, nov15, year)
	}
	newest := strings.Index(earlier, ">today-07<")
	oldestToday := strings.Index(earlier, ">today-00<")
	if newest < 0 || oldestToday < newest || yesterday < oldestToday {
		t.Fatal("today cards are out of order")
	}
	if strings.Contains(detailsOpen(earlier, ">today-07<"), " hidden") || strings.Contains(detailsOpen(earlier, ">today-00<"), " hidden") {
		t.Fatal("today page was hidden")
	}
	if !strings.Contains(sectionOpen(earlier, ">Yesterday<"), " hidden") || !strings.Contains(detailsOpen(earlier, ">yest<"), " hidden") {
		t.Fatal("yesterday was on the first page")
	}
	if !strings.Contains(sectionOpen(earlier, ">Nov 15<"), " hidden") || !strings.Contains(sectionOpen(earlier, ">Nov 17, 2025<"), " hidden") {
		t.Fatal("older days were on the first page")
	}
	if !strings.Contains(earlier, "Showing 8 of 11") || !strings.Contains(earlier, `data-find="`) || !strings.Contains(strings.ToLower(earlier), "yesterday") {
		t.Fatal("clipped history lost its count or search text")
	}
	if strings.Contains(elementOpen(earlier, "approval-earlier-more"), " hidden") {
		t.Fatal("show more hidden while older days remain")
	}

	short := makeAgent(t, h.st, user.ID, "Nova")
	shortRun, err := h.st.CreateRun(ctx, user.ID, agentThread(t, h, user.ID, short.ID), short.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return now })
	raw := `{"id":"req-one","tool":"one-tool","arguments":{"command":"echo one"}}`
	ap, err := h.st.RecordPermission(ctx, user.ID, shortRun.ID, []byte(raw), now.Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := h.st.ClaimDecision(ctx, user.ID, ap.ID, shortRun.ID, "approved", user.ID); err != nil || !ok {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+short.ID+"?panel=approvals", "", ip, nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	one := between(body, `id="approval-earlier"`, `id="approval-filter-empty"`)
	if !strings.Contains(one, ">Today<") || !strings.Contains(one, ">one-tool<") || strings.Contains(one, "Showing ") || !strings.Contains(elementOpen(one, "approval-earlier-more"), " hidden") {
		t.Fatalf("short history %s", one)
	}
}

func elementOpen(html, id string) string {
	needle := `id="` + id + `"`
	idx := strings.Index(html, needle)
	if idx < 0 {
		return ""
	}
	start := strings.LastIndex(html[:idx], "<")
	end := strings.Index(html[idx:], ">")
	if start < 0 || end < 0 {
		return ""
	}
	return html[start : idx+end+1]
}

func detailsOpen(html, marker string) string {
	idx := strings.Index(html, marker)
	if idx < 0 {
		return ""
	}
	open := strings.LastIndex(html[:idx], "<details")
	if open < 0 {
		return ""
	}
	return html[open:idx]
}

func sectionOpen(html, marker string) string {
	idx := strings.Index(html, marker)
	if idx < 0 {
		return ""
	}
	open := strings.LastIndex(html[:idx], "<section")
	if open < 0 {
		return ""
	}
	return html[open:idx]
}
