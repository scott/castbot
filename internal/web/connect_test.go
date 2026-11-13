package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

const connectCanary = "test-connection-value"

func TestConnectionTokenStaysOutOfGuest(t *testing.T) {
	ip := "203.0.113.48:9"
	h := newHarness(t)
	user, secret := h.account("connect-guest@example.com", "Ada", "correct-horse-1")
	h.login("connect-guest@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	model := firstModel(t)
	fields := map[string][]string{
		"form": {"agent"}, "name": {"Atlas"}, "title": {"Helper"}, "section": {sec.ID},
		"model": {model}, "instructions": {"Be concise."},
		"tool": {"bash"}, "perm_bash": {"ask"},
	}
	code, _, _ := h.send(http.MethodPost, "/new-agent", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("create %d", code)
	}
	agent := onlyAgent(t, h, user.ID)
	envSecret, err := h.st.PutSecret(context.Background(), user.ID, "NOTES_TOKEN", "env-sample-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, envSecret.ID); err != nil {
		t.Fatal(err)
	}
	fields["connection"] = []string{connect.GitHub}
	fields["token_github"] = []string{connectCanary}
	code, _, _ = h.send(http.MethodPost, "/agents/"+agent.ID+"/edit", ip, fields, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("save %d", code)
	}
	on, err := h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, connect.GitHub)
	if err != nil || !on {
		t.Fatal("grant")
	}
	saved, err := h.st.ConnectionValue(context.Background(), user.ID, connect.GitHub)
	if err != nil || saved != connectCanary {
		t.Fatal("vault")
	}
	token := guestToken(t, h, agent)
	code, secretsBody, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || !strings.Contains(secretsBody, "env-sample-value") {
		t.Fatalf("pull %d", code)
	}
	absent(t, "pull", secretsBody)
	if strings.Contains(secretsBody, "GITHUB_CONNECTION") || strings.Contains(secretsBody, "DIGITALOCEAN_CONNECTION") {
		t.Fatal("pull named a connection secret")
	}
	code, packed, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 {
		t.Fatalf("bundle %d", code)
	}
	absent(t, "bundle", packed)
	dir := t.TempDir()
	if err := bundle.Install(dir, []byte(packed)); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		absent(t, "guest file", string(raw))
		return nil
	})
	b, err := h.st.LatestBundle(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	absent(t, "yaml", b.YAML+b.Instructions)
	for name, extra := range b.Extras {
		if strings.Contains(extra, "env:") || strings.Contains(extra, "TOKEN") {
			t.Fatalf("shim %s", name)
		}
		absent(t, "shim", extra)
	}
	if !strings.Contains(b.YAML, "request_secret: always-allow") || !strings.Contains(b.YAML, "mcp: ask") {
		t.Fatal("permissions")
	}
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID+"/edit", "", ip, nil)
	if code != 200 || !strings.Contains(page, "Connections") || !strings.Contains(page, `type="password"`) {
		t.Fatalf("edit %d", code)
	}
	absent(t, "edit", page)
	if strings.Contains(page, `value="`+connectCanary+`"`) {
		t.Fatal("edit redisplays the token")
	}
	row, _, err := h.st.CreateSecretRequest(context.Background(), user.ID, agent.ID, "", "", connect.GitHub, "Read issues", h.st.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_ = row
	code, chat, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(chat, `type="password"`) || !strings.Contains(chat, "Connect GitHub") {
		t.Fatalf("card %d", code)
	}
	absent(t, "chat", chat)
	audits, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range audits {
		absent(t, "audit", row.Detail+row.Action)
	}
	code, proxyBody, _ := h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/egress/http", `{"provider":"github","method":"GET","path":"/user","query":"q=`+connectCanary+`"}`, ip, map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"})
	if code == 200 || strings.Contains(proxyBody, connectCanary) {
		t.Fatalf("proxy error leaked")
	}
	audits, err = h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range audits {
		absent(t, "proxy audit", row.Detail)
	}
}

func TestConnectProxyAllowlistApprovalAndRevoke(t *testing.T) {
	ip := "203.0.113.49:9"
	h := newHarness(t)
	user, secret := h.account("connect-proxy@example.com", "Ada", "correct-horse-1")
	h.login("connect-proxy@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	model := firstModel(t)
	if err := h.st.SaveAgentFields(context.Background(), user.ID, agent.ID, store.AgentInput{SectionID: agent.SectionID, Name: agent.Name, Title: agent.Title, Model: model}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.GitHub, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var dials int
	var auth string
	var sawHost string
	ts, dial := connectUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dials++
		auth = r.Header.Get("Authorization")
		sawHost = r.Host
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"echo":"` + connectCanary + `"}`))
	})
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	token := guestToken(t, h, agent)
	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/http", `{"provider":"github","method":"GET","path":"https://evil.example/user"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"bad_request"`) || strings.Contains(body, connectCanary) {
		t.Fatalf("allow %d", code)
	}
	mu.Lock()
	if dials != 0 || auth != "" {
		t.Fatal("dialed off the allowlist")
	}
	mu.Unlock()
	if err := h.st.RevokeConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", `{"provider":"github","method":"GET","path":"/user"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"not_connected"`) {
		t.Fatalf("revoked %d %s", code, body)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	code, statusBody, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/egress", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || !strings.Contains(statusBody, `"enabled":true`) || !strings.Contains(statusBody, `"providers":["github"]`) || strings.Contains(statusBody, connectCanary) {
		t.Fatalf("status %d", code)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", `{"provider":"github","method":"GET","path":"/user","headers":{"Authorization":"Bearer guest-marker"}}`)
	if code != 200 || strings.Contains(body, connectCanary) {
		t.Fatalf("read %d", code)
	}
	raw := decodeProxy(t, body)
	if strings.Contains(string(raw), connectCanary) || !strings.Contains(string(raw), "[redacted]") {
		t.Fatal("decoded body")
	}
	mu.Lock()
	if auth != "Bearer "+connectCanary || strings.Contains(auth, "guest-marker") {
		t.Fatal("upstream auth")
	}
	before := dials
	mu.Unlock()

	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/git", `{"provider":"github","method":"GET","path":"/octo/demo.git/info/refs","query":"service=git-upload-pack"}`)
	if code != 200 || strings.Contains(body, connectCanary) {
		t.Fatalf("git %d", code)
	}
	mu.Lock()
	if sawHost != "github.com" || dials != before+1 {
		t.Fatalf("git host %s dials %d", sawHost, dials)
	}
	before = dials
	mu.Unlock()
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/git", `{"provider":"digitalocean","method":"GET","path":"/v2/account"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"bad_request"`) {
		t.Fatalf("do git %d", code)
	}
	mu.Lock()
	if dials != before {
		t.Fatal("digitalocean git dialed")
	}
	mu.Unlock()

	writeBody := `{"provider":"github","method":"POST","path":"/repos/octo/demo/issues","body_b64":"` + base64.StdEncoding.EncodeToString([]byte(`{"title":"hi"}`)) + `"}`
	writeCode, writeResp := machinePost(t, h, ip, token, agent.ID, "/egress/http", writeBody)
	if writeCode != http.StatusForbidden || !strings.Contains(writeResp, `"error":"approval_required"`) || strings.Contains(writeResp, connectCanary) {
		t.Fatalf("needs approval %d", writeCode)
	}
	approval := waitConnectCard(t, h, user.ID, agent.ID)
	code, card, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/connect-approvals/"+approval+"/deny", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || strings.Contains(card, connectCanary) || !strings.Contains(card, "Denied") {
		t.Fatalf("deny card %d", code)
	}
	writeCode, writeResp = machinePost(t, h, ip, token, agent.ID, "/egress/http", strings.TrimSuffix(writeBody, "}")+`,"approval_id":"`+approval+`"}`)
	if writeCode != http.StatusForbidden || !strings.Contains(writeResp, `"error":"bad_request"`) || strings.Contains(writeResp, connectCanary) {
		t.Fatalf("denied %d", writeCode)
	}
	mu.Lock()
	if dials != before {
		t.Fatal("denied call was forwarded")
	}
	mu.Unlock()

	writeCode, writeResp = machinePost(t, h, ip, token, agent.ID, "/egress/http", writeBody)
	if writeCode != http.StatusForbidden || !strings.Contains(writeResp, `"error":"approval_required"`) {
		t.Fatalf("second card %d", writeCode)
	}
	approval = waitConnectCard(t, h, user.ID, agent.ID)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/connect-approvals/"+approval+"/approve", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || strings.Contains(card, connectCanary) {
		t.Fatalf("approve card %d", code)
	}
	writeCode, writeResp = machinePost(t, h, ip, token, agent.ID, "/egress/http", strings.TrimSuffix(writeBody, "}")+`,"approval_id":"`+approval+`"}`)
	if writeCode != 200 || strings.Contains(writeResp, connectCanary) {
		t.Fatalf("approved %d", writeCode)
	}
	mu.Lock()
	if dials != before+1 {
		t.Fatalf("dials %d", dials)
	}
	mu.Unlock()

	code, _, hdr := h.do(http.MethodPost, "/agents/"+agent.ID+"/connections/github/revoke", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "/edit") {
		t.Fatalf("revoke %d %s", code, hdr.Get("Location"))
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", `{"provider":"github","method":"GET","path":"/user"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"not_connected"`) {
		t.Fatalf("after revoke %d", code)
	}
	code, statusBody, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/egress", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || !strings.Contains(statusBody, `"enabled":true`) || !strings.Contains(statusBody, `"providers":[]`) {
		t.Fatalf("empty providers %d", code)
	}

	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", `{"server":"github","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}`)
	if code != 200 || strings.Contains(body, connectCanary) || !strings.Contains(body, "get_me") || !strings.Contains(body, `"message"`) {
		t.Fatalf("mcp list %d", code)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", `{"server":"github","message":{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_me"}}}`)
	if code != 200 || strings.Contains(body, connectCanary) {
		t.Fatalf("mcp read %d", code)
	}
}

func TestSecretRequestCard(t *testing.T) {
	ip := "203.0.113.50:9"
	h := newHarness(t)
	user, secret := h.account("connect-card@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("connect-other@example.com", "Bea", "correct-horse-1")
	h.login("connect-card@example.com", "correct-horse-1", secret, ip, false)
	base := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return base })
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	model := firstModel(t)
	if err := h.st.SaveAgentFields(context.Background(), user.ID, agent.ID, store.AgentInput{SectionID: agent.SectionID, Name: agent.Name, Title: agent.Title, Model: model}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", "name: Atlas\nbrain:\n  model: "+model+"\n  instructions_file: instructions.md\ntools:\n  builtin: []\n", "Be concise."); err != nil {
		t.Fatal(err)
	}
	token := guestToken(t, h, agent)
	code, body, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/egress", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != 200 || !strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"providers":[]`) || strings.Contains(body, connectCanary) {
		t.Fatalf("status before connect %d", code)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", `{"provider":"github","method":"GET","path":"/user"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"not_connected"`) {
		t.Fatalf("http before connect %d", code)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"connection":"custom","reason":"no"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"bad_request"`) {
		t.Fatalf("unknown %d %s", code, body)
	}

	var status string
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, status = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"connection":"github","reason":"Read issues"}`)
	}()
	row := waitSecretCard(t, h, user.ID, agent.ID)
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(page, `type="password"`) || !strings.Contains(page, `autocomplete="off"`) || strings.Contains(page, connectCanary) {
		t.Fatalf("masked %d", code)
	}
	if strings.Contains(page, "show") && strings.Contains(page, "eye") {
		t.Fatal("reveal control")
	}
	h.st.SetNow(func() time.Time { return base.Add(61 * time.Minute) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("expiry wait")
	}
	if !strings.Contains(status, "expired") || strings.Contains(status, connectCanary) {
		t.Fatal("expired status")
	}
	_ = row

	h.st.SetNow(func() time.Time { return base.Add(2 * time.Hour) })
	done = make(chan struct{})
	go func() {
		defer close(done)
		_, status = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"connection":"github","reason":"Read issues"}`)
	}()
	row = waitSecretCard(t, h, user.ID, agent.ID)
	useSession(t, h, other.ID, ip)
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{"value": {connectCanary}}), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusNotFound {
		t.Fatalf("other user %d", code)
	}
	useSession(t, h, user.ID, ip)
	code, card, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/decline", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || strings.Contains(card, connectCanary) || !strings.Contains(card, "Declined") {
		t.Fatalf("decline %d", code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("decline wait")
	}
	if !strings.Contains(status, "declined") {
		t.Fatal(status)
	}

	done = make(chan struct{})
	go func() {
		defer close(done)
		_, status = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"connection":"github","reason":"Read issues"}`)
	}()
	row = waitSecretCard(t, h, user.ID, agent.ID)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{"value": {connectCanary}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || strings.Contains(card, connectCanary) || !strings.Contains(card, "Saved") {
		t.Fatalf("save %d", code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("save wait")
	}
	if !strings.Contains(status, `"saved"`) || strings.Contains(status, connectCanary) {
		t.Fatal(status)
	}
	got, err := h.st.ConnectionValue(context.Background(), user.ID, connect.GitHub)
	if err != nil || got != connectCanary {
		t.Fatal("saved value")
	}
	on, err := h.st.ConnectionEnabled(context.Background(), user.ID, agent.ID, connect.GitHub)
	if err != nil || !on {
		t.Fatal("enabled")
	}
	b, err := h.st.LatestBundle(context.Background(), user.ID, agent.ID)
	if err != nil || strings.Contains(b.YAML, connectCanary) || !strings.Contains(b.Extras["mcp/github.yaml"], connect.ShimCommand) {
		t.Fatal("bundle after card")
	}

	code, status, _ = h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/egress/secret-request", `{"connection":"github","reason":"again"}`, ip, map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"})
	if code != 200 || !strings.Contains(status, "already_configured") {
		t.Fatalf("already %d %s", code, status)
	}
	if err := h.st.RevokeConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	done = make(chan struct{})
	go func() {
		defer close(done)
		_, status = machinePost(t, h, ip, token, agent.ID, "/egress/secret-request", `{"connection":"github","reason":"use the saved token"}`)
	}()
	row = waitSecretCard(t, h, user.ID, agent.ID)
	code, card, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/secret-requests/"+row.ID+"/submit", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || strings.Contains(card, connectCanary) || !strings.Contains(card, "Saved") {
		t.Fatalf("confirm %d", code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirm wait")
	}
	if !strings.Contains(status, `"saved"`) {
		t.Fatal(status)
	}

	if err := h.st.RevokeConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return base.Add(4 * time.Hour) })
	for i := 0; i < store.SecretRequestLimit; i++ {
		opened, _, err := h.st.CreateSecretRequest(context.Background(), user.ID, agent.ID, "", "", connect.GitHub, "n", h.st.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.ResolveSecretRequest(context.Background(), user.ID, opened.ID, "declined"); err != nil {
			t.Fatal(err)
		}
	}
	code, status, _ = h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/egress/secret-request", `{"connection":"github","reason":"too many"}`, ip, map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"})
	if code != 200 || !strings.Contains(status, "declined") || strings.Contains(status, connectCanary) {
		t.Fatalf("limit %d %s", code, status)
	}
}

func TestApprovalBindsTheExactRequest(t *testing.T) {
	ip := "203.0.113.51:9"
	h := newHarness(t)
	user, secret := h.account("connect-bind@example.com", "Ada", "correct-horse-1")
	h.login("connect-bind@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	model := firstModel(t)
	if err := h.st.SaveAgentFields(context.Background(), user.ID, agent.ID, store.AgentInput{SectionID: agent.SectionID, Name: agent.Name, Title: agent.Title, Model: model}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.GitHub, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.DigitalOcean, "do-sample-value"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.DigitalOcean); err != nil {
		t.Fatal(err)
	}
	var dials int
	ts, dial := connectUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		dials++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	token := guestToken(t, h, agent)

	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/git", `{"provider":"github","method":"POST","path":"/octo/demo.git/git-upload-pack","body_b64":"`+base64.StdEncoding.EncodeToString([]byte("pack"))+`"}`)
	if code != 200 || dials != 1 {
		t.Fatalf("clean fetch %d dials %d", code, dials)
	}
	before := dials
	trick := `{"provider":"github","method":"POST","path":"/repos/octo/demo/issues","query":"x=git-upload-pack","body_b64":"` + base64.StdEncoding.EncodeToString([]byte(`{"title":"unapproved write"}`)) + `"}`
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", trick)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || dials != before {
		t.Fatalf("query trick %d dials %d", code, dials)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", `{"provider":"digitalocean","method":"POST","path":"/v2/droplets","query":"note=git-upload-pack","body_b64":"`+base64.StdEncoding.EncodeToString([]byte(`{"name":"drop"}`))+`"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || dials != before {
		t.Fatalf("do query trick %d dials %d", code, dials)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", `{"provider":"github","method":"POST","path":"/repos/octo/git-upload-pack","body_b64":"`+base64.StdEncoding.EncodeToString([]byte(`{"title":"path trick"}`))+`"}`)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || dials != before {
		t.Fatalf("path trick %d dials %d", code, dials)
	}

	harmless := `{"provider":"github","method":"POST","path":"/repos/octo/demo/issues","body_b64":"` + base64.StdEncoding.EncodeToString([]byte(`{"title":"harmless"}`)) + `"}`
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", harmless)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || dials != before {
		t.Fatalf("harmless card %d", code)
	}
	approval := waitConnectCardText(t, h, user.ID, agent.ID, "harmless")
	code, page, _ := h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
	if code != 200 || !strings.Contains(page, "harmless") || strings.Contains(page, "swapped") || strings.Contains(page, connectCanary) {
		t.Fatalf("card summary %d", code)
	}
	code, card, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/connect-approvals/"+approval+"/approve", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || strings.Contains(card, connectCanary) {
		t.Fatalf("approve %d", code)
	}
	swapped := `{"provider":"github","method":"POST","path":"/repos/octo/demo/issues","body_b64":"` + base64.StdEncoding.EncodeToString([]byte(`{"title":"swapped"}`)) + `","approval_id":"` + approval + `"}`
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", swapped)
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"bad_request"`) || strings.Contains(body, "approval_required") || dials != before || strings.Contains(body, connectCanary) {
		t.Fatalf("swapped body %d dials %d", code, dials)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/http", strings.TrimSuffix(harmless, "}")+`,"approval_id":"`+approval+`"}`)
	if code != 200 || dials != before+1 || strings.Contains(body, connectCanary) {
		t.Fatalf("matching body %d dials %d", code, dials)
	}
	before = dials
	code, _ = machinePost(t, h, ip, token, agent.ID, "/egress/http", strings.TrimSuffix(harmless, "}")+`,"approval_id":"`+approval+`"}`)
	if code == 200 || dials != before {
		t.Fatalf("reuse %d dials %d", code, dials)
	}

	mcp := func(title string) string {
		return `{"server":"github","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"create_issue","arguments":{"owner":"octo","repo":"demo","title":"` + title + `"}}}}`
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", mcp("harmless"))
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || dials != before {
		t.Fatalf("mcp card %d dials %d", code, dials)
	}
	mcpID := waitConnectCardText(t, h, user.ID, agent.ID, "create_issue")
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/connect-approvals/"+mcpID+"/approve", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatalf("mcp approve %d", code)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", mcp("swapped"))
	if code != http.StatusForbidden || !strings.Contains(body, `"error":"approval_required"`) || dials != before {
		t.Fatalf("swapped args %d dials %d", code, dials)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", mcp("harmless"))
	if code != 200 || dials != before+1 || strings.Contains(body, connectCanary) {
		t.Fatalf("matching args %d dials %d", code, dials)
	}
}

func waitConnectCardText(t *testing.T, h *harness, userID, agentID, needle string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := h.st.ListConnectApprovals(context.Background(), userID, agentID, h.st.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Status == "pending" && strings.Contains(row.Action, needle) {
				return row.ID
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("no approval card containing %s", needle)
	return ""
}

func useSession(t *testing.T, h *harness, userID, ip string) {
	t.Helper()
	sess, err := h.st.CreateSession(context.Background(), userID, "Mozilla/5.0", false)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.jar = map[string]*http.Cookie{
		cookieSession: {Name: cookieSession, Value: seal("session", sess.ID, h.srv.key), Path: "/"},
	}
	h.csrf = ""
	h.mu.Unlock()
	if code, _, _ := h.do(http.MethodGet, "/", "", ip, nil); code != http.StatusOK {
		t.Fatalf("session %d", code)
	}
}

func absent(t *testing.T, where, text string) {
	t.Helper()
	needles := []string{
		connectCanary,
		"GITHUB_CONNECTION",
		"DIGITALOCEAN_CONNECTION",
		"GH_TOKEN",
		"GITHUB_PERSONAL_ACCESS_TOKEN",
		"DIGITALOCEAN_API_TOKEN",
	}
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			t.Fatalf("%s held %s", where, needle)
		}
	}
}

func guestToken(t *testing.T, h *harness, agent store.Agent) string {
	t.Helper()
	plain, err := h.st.IssueBootstrap(context.Background(), agent.UserID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func machinePost(t *testing.T, h *harness, ip, token, agentID, suffix, jsonBody string) (int, string) {
	t.Helper()
	code, body, _ := h.do(http.MethodPost, "/api/castlet/agents/"+agentID+suffix, jsonBody, ip, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	return code, body
}

func decodeProxy(t *testing.T, body string) []byte {
	t.Helper()
	var payload struct {
		Body string `json:"body_b64"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(payload.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func waitConnectCard(t *testing.T, h *harness, userID, agentID string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := h.st.ListConnectApprovals(context.Background(), userID, agentID, h.st.Now().Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Status == "pending" {
				return row.ID
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("no approval card")
	return ""
}

func waitSecretCard(t *testing.T, h *harness, userID, agentID string) store.SecretRequest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := h.st.ListSecretRequests(context.Background(), userID, agentID, h.st.Now().Add(-24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Status == "pending" {
				return row
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("no secret card")
	return store.SecretRequest{}
}

func connectUpstream(t *testing.T, h http.HandlerFunc) (*httptest.Server, *connect.Dial) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "api.github.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"api.github.com", "github.com"},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}}
	ts.StartTLS()
	return ts, &connect.Dial{
		Roots: pool,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ts.Listener.Addr().String())
		},
	}
}
