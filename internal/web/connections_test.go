package web

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

func TestConnectionsGridAndDetails(t *testing.T) {
	ip := "203.0.113.70:9"
	h := newHarness(t)
	_, secret := h.account("conn-grid@example.com", "Ada", "correct-horse-1")
	h.login("conn-grid@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/settings/connections", "", ip, nil)
	if code != 200 || strings.Contains(body, "\u2014") {
		t.Fatalf("grid %d", code)
	}
	for _, want := range []string{"GitHub", "DigitalOcean", "Shell Actions", `href="/settings/connections/github"`, `href="/settings/connections/digitalocean"`, `href="/settings/connections/shell"`, "Not connected", "Connected", `href="/settings/connections" class="on"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("grid missing %s", want)
		}
	}
	if strings.Count(body, "Not connected") < 2 || strings.Count(body, ">Connect<") < 2 || !strings.Contains(body, ">Disconnect<") {
		t.Fatalf("grid actions %s", snippet(body))
	}
	code, body, _ = h.do(http.MethodGet, "/settings/connections/github", "", ip, nil)
	if code != 200 || !strings.Contains(body, `id="conn-token"`) || !strings.Contains(body, "create_issue") {
		t.Fatalf("github detail %d", code)
	}
	if strings.Contains(body, `name="token" value=`) {
		t.Fatal("token value rendered")
	}
	code, body, _ = h.do(http.MethodGet, "/settings/connections/shell", "", ip, nil)
	if code != 200 || strings.Contains(body, `id="conn-token"`) || !strings.Contains(body, "bash") || !strings.Contains(body, "git_push") || !strings.Contains(body, ">Save<") || !strings.Contains(body, ">Disconnect<") {
		t.Fatalf("shell detail %d", code)
	}
	code, _, _ = h.do(http.MethodGet, "/settings/connections/nope", "", ip, nil)
	if code != http.StatusNotFound {
		t.Fatalf("unknown %d", code)
	}
}

func TestConnectGitHubFansOutAndDisconnectKeepsTheToken(t *testing.T) {
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "fixture-or-key")
	ip := "203.0.113.71:9"
	h := newHarness(t)
	user, secret := h.account("conn-fan@example.com", "Ada", "correct-horse-1")
	h.login("conn-fan@example.com", "correct-horse-1", secret, ip, false)
	model := firstModel(t)
	one := postNewAgent(t, h, ip, user.ID, "Atlas", model)
	two := postNewAgent(t, h, ip, user.ID, "Birch", model)

	code, body, _ := h.do(http.MethodPost, "/settings/connections/github", h.form(url.Values{
		"token": {"bad\ntoken"},
	}), ip, nil)
	if code != http.StatusBadRequest || strings.Contains(body, "bad\ntoken") {
		t.Fatalf("newline %d", code)
	}
	if _, err := h.st.ConnectionValue(context.Background(), user.ID, connect.GitHub); err == nil {
		t.Fatal("newline stored a token")
	}

	code, _, hdr := h.do(http.MethodPost, "/settings/connections/github", h.form(url.Values{
		"token":              {connectCanary},
		"level_create_issue": {"deny"},
		"level_get_me":       {"ask"},
	}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=connected") {
		t.Fatalf("connect %d %s", code, hdr.Get("Location"))
	}
	for _, agent := range []store.Agent{one, two} {
		on, err := h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, connect.GitHub)
		if err != nil || !on {
			t.Fatalf("grant %s", agent.Name)
		}
		spec := mustBundle(t, h, user.ID, agent.ID)
		if spec.Permissions["create_issue"] != "deny" || spec.Permissions["get_me"] != "ask" {
			t.Fatalf("%s levels %#v", agent.Name, spec.Permissions)
		}
	}
	code, page, _ := h.do(http.MethodGet, "/settings/connections/github", "", ip, nil)
	if code != 200 || strings.Contains(page, connectCanary) || !strings.Contains(page, "A token is saved") {
		t.Fatalf("detail leaked %d", code)
	}
	audits, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range audits {
		absent(t, "audit", row.Detail+row.Action)
	}
	token := guestToken(t, h, one)
	code, pull, _ := h.do(http.MethodGet, "/api/castlet/agents/"+one.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 {
		t.Fatalf("pull %d", code)
	}
	absent(t, "pull", pull)

	three := postNewAgent(t, h, ip, user.ID, "Cedar", model)
	on, err := h.st.ConnectionEnabled(context.Background(), user.ID, three.ID, connect.GitHub)
	if err != nil || !on {
		t.Fatal("new agent did not inherit")
	}
	spec := mustBundle(t, h, user.ID, three.ID)
	if spec.Permissions["create_issue"] != "deny" {
		t.Fatalf("inherited %#v", spec.Permissions)
	}

	code, _, hdr = h.do(http.MethodPost, "/settings/connections/github", h.form(url.Values{
		"level_create_issue": {"always-allow"},
	}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=saved") {
		t.Fatalf("blank token %d %s", code, hdr.Get("Location"))
	}
	saved, err := h.st.ConnectionValue(context.Background(), user.ID, connect.GitHub)
	if err != nil || saved != connectCanary {
		t.Fatal("blank token replaced the vault")
	}
	spec = mustBundle(t, h, user.ID, one.ID)
	if spec.Permissions["create_issue"] != "always-allow" {
		t.Fatalf("updated level %#v", spec.Permissions["create_issue"])
	}

	code, _, hdr = h.do(http.MethodPost, "/settings/connections/github/disconnect", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=disconnected") {
		t.Fatalf("disconnect %d %s", code, hdr.Get("Location"))
	}
	sw, err := h.st.ConnectionSwitchedOn(context.Background(), user.ID, connect.GitHub)
	if err != nil || sw {
		t.Fatal("switch stayed on")
	}
	saved, err = h.st.ConnectionValue(context.Background(), user.ID, connect.GitHub)
	if err != nil || saved != connectCanary {
		t.Fatal("disconnect dropped the token")
	}
	for _, agent := range []store.Agent{one, two, three} {
		on, err = h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, connect.GitHub)
		if err != nil || on {
			t.Fatalf("still granted %s", agent.Name)
		}
	}
	fields := map[string][]string{
		"form": {"agent"}, "name": {one.Name}, "title": {"Helper"}, "section": {one.SectionID},
		"model": {model}, "instructions": {"Be concise."},
	}
	code, body, _ = h.send(http.MethodPost, "/agents/"+one.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("edit %d %s", code, snippet(body))
	}
	on, err = h.st.ConnectionEnabled(context.Background(), user.ID, one.ID, connect.GitHub)
	if err != nil || on {
		t.Fatal("edit turned GitHub back on")
	}
	code, edit, _ := h.do(http.MethodGet, "/agents/"+one.ID+"/edit", "", ip, nil)
	if code != 200 || strings.Contains(edit, `id="connections"`) || strings.Contains(edit, `id="tools-card"`) || strings.Contains(edit, connectCanary) {
		t.Fatalf("edit page %d", code)
	}
}

func TestShellActionsAppliesToEveryAgent(t *testing.T) {
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "fixture-or-key")
	ip := "203.0.113.72:9"
	h := newHarness(t)
	user, secret := h.account("conn-shell@example.com", "Ada", "correct-horse-1")
	h.login("conn-shell@example.com", "correct-horse-1", secret, ip, false)
	model := firstModel(t)
	one := postNewAgent(t, h, ip, user.ID, "Atlas", model)
	two := postNewAgent(t, h, ip, user.ID, "Birch", model)
	code, _, hdr := h.do(http.MethodPost, "/settings/connections/shell", h.form(url.Values{
		"level_bash": {"deny"},
	}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("save %d %s", code, hdr.Get("Location"))
	}
	for _, agent := range []store.Agent{one, two} {
		spec := mustBundle(t, h, user.ID, agent.ID)
		if spec.Permissions["bash"] != "deny" || !hasTool(spec, "bash") || !hasTool(spec, "read") || !hasTool(spec, "git_push") {
			t.Fatalf("%s shell %#v %#v", agent.Name, spec.Tools, spec.Permissions["bash"])
		}
	}
	code, _, hdr = h.do(http.MethodPost, "/settings/connections/shell/disconnect", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("disconnect %d", code)
	}
	for _, agent := range []store.Agent{one, two} {
		assertNoShell(t, mustBundle(t, h, user.ID, agent.ID))
	}
	three := postNewAgent(t, h, ip, user.ID, "Cedar", model)
	assertNoShell(t, mustBundle(t, h, user.ID, three.ID))

	code, _, hdr = h.do(http.MethodPost, "/settings/connections/shell", h.form(url.Values{
		"level_bash": {"deny"},
		"level_read": {"ask"},
	}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=connected") {
		t.Fatalf("reconnect %d %s", code, hdr.Get("Location"))
	}
	four := postNewAgent(t, h, ip, user.ID, "Dove", model)
	spec := mustBundle(t, h, user.ID, four.ID)
	if spec.Permissions["bash"] != "deny" || spec.Permissions["read"] != "ask" || !hasTool(spec, "glob") {
		t.Fatalf("new agent policy %#v", spec.Permissions)
	}
	if _, err := h.st.ConnectionSecret(context.Background(), user.ID, connect.Shell); err == nil {
		t.Fatal("shell stored a secret")
	}
}

func TestConnectionPostDoesNotTouchAnotherUser(t *testing.T) {
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "fixture-or-key")
	ip := "203.0.113.73:9"
	h := newHarness(t)
	owner, secret := h.account("conn-owner@example.com", "Ada", "correct-horse-1")
	h.login("conn-owner@example.com", "correct-horse-1", secret, ip, false)
	model := firstModel(t)
	agent := postNewAgent(t, h, ip, owner.ID, "Atlas", model)
	code, _, _ := h.do(http.MethodPost, "/settings/connections/github", h.form(url.Values{
		"token":              {connectCanary},
		"level_create_issue": {"deny"},
	}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("owner connect %d", code)
	}
	other, otherSecret := h.account("conn-guest@example.com", "Bea", "correct-horse-1")
	h.login("conn-guest@example.com", "correct-horse-1", otherSecret, "203.0.113.74:9", false)
	code, page, _ := h.do(http.MethodPost, "/settings/connections/github", h.form(url.Values{
		"token":              {"guest-connection-value"},
		"level_create_issue": {"always-allow"},
	}), "203.0.113.74:9", nil)
	if code != http.StatusSeeOther && code != http.StatusBadRequest {
		t.Fatalf("guest post %d", code)
	}
	_ = page
	saved, err := h.st.ConnectionValue(context.Background(), owner.ID, connect.GitHub)
	if err != nil || saved != connectCanary {
		t.Fatal("owner token changed")
	}
	on, err := h.st.ConnectionSwitchedOn(context.Background(), owner.ID, connect.GitHub)
	if err != nil || !on {
		t.Fatal("owner switch changed")
	}
	levels, err := h.st.ConnectionLevels(context.Background(), owner.ID, connect.GitHub)
	if err != nil || levels["create_issue"] != "deny" {
		t.Fatalf("owner policy %#v", levels)
	}
	granted, err := h.st.ConnectionEnabled(context.Background(), owner.ID, agent.ID, connect.GitHub)
	if err != nil || !granted {
		t.Fatal("owner grant changed")
	}
	code, page, _ = h.do(http.MethodGet, "/settings/connections/github", "", "203.0.113.74:9", nil)
	if code != 200 {
		t.Fatalf("guest page %d", code)
	}
	absent(t, "guest page", page)
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/connections/github/revoke", h.form(url.Values{}), "203.0.113.74:9", nil)
	if code != http.StatusNotFound {
		t.Fatalf("guest revoke %d", code)
	}
	granted, err = h.st.ConnectionEnabled(context.Background(), owner.ID, agent.ID, connect.GitHub)
	if err != nil || !granted {
		t.Fatal("guest revoke changed the grant")
	}
	_ = other
}

func TestNamedToolPolicySkipsOrBlocksTheHostCard(t *testing.T) {
	ip := "203.0.113.75:9"
	h := newHarness(t)
	user, secret := h.account("conn-policy@example.com", "Ada", "correct-horse-1")
	h.login("conn-policy@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.GitHub, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	var dials int
	var mu sync.Mutex
	ts, dial := connectUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dials++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	token := guestToken(t, h, agent)
	mcp := `{"server":"github","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_issue","arguments":{"owner":"octo","repo":"demo","title":"policy"}}}}`
	if err := h.st.SaveConnectionLevels(context.Background(), user.ID, connect.GitHub, map[string]string{"create_issue": "always-allow"}); err != nil {
		t.Fatal(err)
	}
	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/mcp", mcp)
	mu.Lock()
	gotDials := dials
	mu.Unlock()
	if code != 200 || gotDials != 1 || strings.Contains(body, connectCanary) {
		t.Fatalf("always allow %d dials %d %s", code, gotDials, body)
	}
	rows, err := h.st.ListConnectApprovals(context.Background(), user.ID, agent.ID, h.st.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Status == "pending" && strings.Contains(row.Action, "create_issue") {
			t.Fatal("always allow opened a card")
		}
	}
	raw := `{"provider":"github","method":"POST","path":"/repos/octo/demo/issues","body_b64":"` + base64.StdEncoding.EncodeToString([]byte(`{"title":"raw"}`)) + `"}`
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", raw)
	mu.Lock()
	gotDials = dials
	mu.Unlock()
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || gotDials != 1 {
		t.Fatalf("raw http %d dials %d %s", code, gotDials, body)
	}
	if err := h.st.SaveConnectionLevels(context.Background(), user.ID, connect.GitHub, map[string]string{"create_issue": "deny"}); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", mcp)
	mu.Lock()
	gotDials = dials
	mu.Unlock()
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"bad_request"`) || gotDials != 1 || strings.Contains(body, connectCanary) {
		t.Fatalf("deny %d dials %d %s", code, gotDials, body)
	}
	if err := h.st.SaveConnectionLevels(context.Background(), user.ID, connect.GitHub, map[string]string{"create_issue": "ask"}); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", mcp)
	mu.Lock()
	gotDials = dials
	mu.Unlock()
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || gotDials != 1 {
		t.Fatalf("ask %d dials %d %s", code, gotDials, body)
	}
	if waitConnectCardText(t, h, user.ID, agent.ID, "create_issue") == "" {
		t.Fatal("ask card")
	}
	audits, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	denied := false
	for _, row := range audits {
		absent(t, "policy audit", row.Detail)
		if strings.Contains(row.Detail, `"outcome":"denied"`) && strings.Contains(row.Detail, "create_issue") {
			denied = true
		}
	}
	if !denied {
		t.Fatal("deny was not audited")
	}
}

func postNewAgent(t *testing.T, h *harness, ip, userID, name, model string) store.Agent {
	t.Helper()
	sec, err := h.st.SystemSection(context.Background(), userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string][]string{
		"form": {"agent"}, "name": {name}, "title": {"Helper"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Be concise."},
	}
	code, body, _ := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("create %s %d %s", name, code, snippet(body))
	}
	agents, err := h.st.ListAgents(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range agents {
		if agent.Name == name {
			return agent
		}
	}
	t.Fatalf("missing %s", name)
	return store.Agent{}
}

func mustBundle(t *testing.T, h *harness, userID, agentID string) castcfg.Spec {
	t.Helper()
	bundle, err := h.st.LatestBundle(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := castcfg.ParseYAML(bundle.YAML)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bundle.YAML, connectCanary) {
		t.Fatal("bundle held the token")
	}
	return spec
}

func hasTool(spec castcfg.Spec, name string) bool {
	for _, tool := range spec.Tools {
		if tool == name {
			return true
		}
	}
	return false
}

func assertNoShell(t *testing.T, spec castcfg.Spec) {
	t.Helper()
	for _, tool := range castcfg.Tools() {
		if hasTool(spec, tool.Name) || spec.Permissions[tool.Name] != "" {
			t.Fatalf("shell tool %s stayed %#v", tool.Name, spec.Permissions[tool.Name])
		}
	}
}
