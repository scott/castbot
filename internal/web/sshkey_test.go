package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
	"github.com/scott/castbot/internal/store"
)

func TestAttachDropletSSHKey(t *testing.T) {
	const fp = "aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"
	create := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-create","arguments":{"Name":"tiny"}}}}`
	snake := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualifiedName":"digitalocean/droplet_create","Name":"tiny"}}}`
	for _, raw := range []string{create, snake} {
		if !dropletCreateInvoke(json.RawMessage(raw)) || dropletCreateHasKeys(json.RawMessage(raw)) {
			t.Fatalf("classifier %s", raw)
		}
		out, err := injectDropletSSHKey(json.RawMessage(raw), fp)
		if err != nil || !strings.Contains(string(out), `"SSHKeys":["`+fp+`"]`) {
			t.Fatalf("inject %v %s", err, out)
		}
	}
	kept := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-create","arguments":{"ssh_keys":["11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff:00"]}}}}`
	if !dropletCreateHasKeys(json.RawMessage(kept)) {
		t.Fatal("caller key was empty")
	}
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/create"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-list"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-delete"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"find_tools","arguments":{"query":"droplets"}}}`,
	} {
		if dropletCreateInvoke(json.RawMessage(raw)) {
			t.Fatalf("classified as create %s", raw)
		}
	}
	if keysNextOK("https://evil.example/v2/account/keys") || keysNextOK("https://api.digitalocean.com/v2/droplets") || keysNextOK("http://api.digitalocean.com/v2/account/keys") {
		t.Fatal("page link")
	}
	if !keysNextOK("https://api.digitalocean.com/v2/account/keys?page=2&per_page=200") {
		t.Fatal("account keys page")
	}
}

func TestDropletCreateRegistersAndAttachesTheSSHKey(t *testing.T) {
	ip := "203.0.113.93:9"
	h := newHarness(t)
	user, secret := h.account("ssh-create@example.com", "Ada", "correct-horse-1")
	h.login("ssh-create@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.DigitalOcean, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.DigitalOcean); err != nil {
		t.Fatal(err)
	}
	up := newKeyUpstream(t)
	ts, dial := connectUpstream(t, up.serve)
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	token := guestToken(t, h, agent)
	write := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-create","arguments":{"Name":"tiny"}}}}}`
	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/mcp", write)
	seed := sshSeed(t, h.st, user.ID)
	if code != http.StatusOK || !mcpSentence(body, "This action needs approval.") || up.mcpCalls() != 0 || up.posts() != 1 || strings.Contains(body, seed) || strings.Contains(body, connectCanary) {
		t.Fatalf("first %d mcp %d posts %d", code, up.mcpCalls(), up.posts())
	}
	fp := sshFingerprint(t, h.st, user.ID)
	if fp == "" || !strings.Contains(up.postBody(), "ssh-ed25519") || !strings.Contains(up.postBody(), `"name":"castbot"`) || !up.sawPage2() || strings.Contains(up.postBody(), seed) || strings.Contains(up.postBody(), connectCanary) {
		t.Fatal("registration body")
	}
	card := waitConnectCardText(t, h, user.ID, agent.ID, "invoke_tool")
	rows, err := h.st.ListConnectApprovals(context.Background(), user.ID, agent.ID, h.st.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var hash, action string
	for _, row := range rows {
		if row.ID == card {
			hash, action = row.RequestHash, row.Action
		}
	}
	want, err := connect.MCPHash("invoke_tool", json.RawMessage(`{"qualified_name":"droplets/droplet-create","arguments":{"Name":"tiny","SSHKeys":["`+fp+`"]}}`))
	if err != nil || hash != want || !strings.Contains(action, fp) || strings.Contains(action, seed) {
		t.Fatal("card did not bind the fingerprint")
	}
	gets, posts := up.gets(), up.posts()
	code, _, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/connect-approvals/"+card+"/approve", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK {
		t.Fatalf("approve %d", code)
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", write)
	if code != http.StatusOK || up.posts() != posts || up.gets() != gets || !up.sawFingerprint(fp) || strings.Contains(body, seed) || strings.Contains(body, connectCanary) {
		t.Fatalf("forward %d posts %d gets %d", code, up.posts(), up.gets())
	}
	code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", write)
	if code != http.StatusOK || up.posts() != posts || strings.Contains(body, seed) {
		t.Fatalf("reuse %d posts %d", code, up.posts())
	}
	audits, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range audits {
		if strings.Contains(row.Detail, seed) || strings.Contains(row.Detail, connectCanary) {
			t.Fatal("audit held a secret")
		}
	}
}

func TestDropletCreateKeepsCallerKeysAndSkipsOtherTools(t *testing.T) {
	ip := "203.0.113.94:9"
	h := newHarness(t)
	user, secret := h.account("ssh-keep@example.com", "Ada", "correct-horse-1")
	h.login("ssh-keep@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.DigitalOcean, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.DigitalOcean); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveConnectionLevels(context.Background(), user.ID, connect.DigitalOcean, map[string]string{"invoke_tool": "always-allow"}); err != nil {
		t.Fatal(err)
	}
	up := newKeyUpstream(t)
	ts, dial := connectUpstream(t, up.serve)
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	token := guestToken(t, h, agent)
	const caller = "11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff:00"
	kept := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-create","arguments":{"Name":"tiny","ssh_keys":["` + caller + `"]}}}}}`
	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/mcp", kept)
	if code != http.StatusOK || up.posts() != 0 || up.gets() != 0 || !up.sawFingerprint(caller) || strings.Contains(body, connectCanary) {
		t.Fatalf("caller keys %d posts %d", code, up.posts())
	}
	for _, raw := range []string{
		`{"server":"digitalocean","message":{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/create"}}}}`,
		`{"server":"digitalocean","message":{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-list"}}}}`,
		`{"server":"digitalocean","message":{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"find_tools","arguments":{"query":"droplets"}}}}`,
	} {
		code, body = machinePost(t, h, ip, token, agent.ID, "/egress/mcp", raw)
		if up.posts() != 0 || up.gets() != 0 || strings.Contains(body, connectCanary) {
			t.Fatalf("other tool %d %s", code, raw)
		}
	}
}

func TestDropletCreateReportsAMissingSSHKey(t *testing.T) {
	ip := "203.0.113.95:9"
	h := newHarness(t)
	user, secret := h.account("ssh-miss@example.com", "Ada", "correct-horse-1")
	h.login("ssh-miss@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.DigitalOcean, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.DigitalOcean); err != nil {
		t.Fatal(err)
	}
	token := guestToken(t, h, agent)
	write := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-create","arguments":{"Name":"tiny"}}}}}`
	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/mcp", write)
	seed := sshSeed(t, h.st, user.ID)
	row, err := h.st.ConnectionSSHKey(context.Background(), user.ID, connect.DigitalOcean)
	if err != nil || row.Fingerprint != "" {
		t.Fatal("unregistered key")
	}
	if code != http.StatusOK || !mcpSentence(body, "The SSH key is not registered yet.") || strings.Contains(body, seed) || strings.Contains(body, connectCanary) || strings.Contains(body, row.PublicKey) {
		t.Fatalf("missing %d", code)
	}
	audits, err := h.st.ListAudit(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, row := range audits {
		if strings.Contains(row.Detail, seed) || strings.Contains(row.Detail, connectCanary) || strings.Contains(row.Detail, "ssh-ed25519") {
			t.Fatal("audit held key material")
		}
		if strings.Contains(row.Detail, `"outcome":"ssh_key"`) {
			saw = true
		}
	}
	if !saw {
		t.Fatal("ssh failure was not audited")
	}
}

func TestConnectionPageShowsTheSSHFingerprint(t *testing.T) {
	ip := "203.0.113.96:9"
	h := newHarness(t)
	user, secret := h.account("ssh-page@example.com", "Ada", "correct-horse-1")
	h.login("ssh-page@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	up := newKeyUpstream(t)
	ts, dial := connectUpstream(t, up.serve)
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	code, _, hdr := h.do(http.MethodPost, "/settings/connections/digitalocean", h.form(url.Values{
		"token": {connectCanary},
	}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=connected") {
		t.Fatalf("connect %d %s", code, hdr.Get("Location"))
	}
	fp := sshFingerprint(t, h.st, user.ID)
	seed := sshSeed(t, h.st, user.ID)
	code, page, _ := h.do(http.MethodGet, "/settings/connections/digitalocean?notice=connected", "", ip, nil)
	if code != 200 || !strings.Contains(page, `id="conn-ssh"`) || !strings.Contains(page, fp) || !strings.Contains(page, "The private key stays in your vault.") || strings.Contains(page, seed) || strings.Contains(page, connectCanary) || strings.Contains(page, "ssh-ed25519") {
		t.Fatalf("detail %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/settings/connections/digitalocean/disconnect", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("disconnect %d", code)
	}
	row, err := h.st.ConnectionSSHKey(context.Background(), user.ID, connect.DigitalOcean)
	if err != nil || row.Fingerprint != fp {
		t.Fatal("disconnect removed the key")
	}
	guest := guestToken(t, h, agent)
	code, pull, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + guest})
	if code != 200 || strings.Contains(pull, seed) || strings.Contains(pull, connectCanary) || strings.Contains(pull, "DIGITALOCEAN_SSH_KEY") || strings.Contains(pull, fp) {
		t.Fatalf("pull %d", code)
	}
	otherUser, otherSecret := h.account("ssh-page-other@example.com", "Bea", "correct-horse-1")
	h.login("ssh-page-other@example.com", "correct-horse-1", otherSecret, ip, false)
	code, otherPage, _ := h.do(http.MethodGet, "/settings/connections/digitalocean", "", ip, nil)
	if code != 200 || strings.Contains(otherPage, fp) || strings.Contains(otherPage, seed) || strings.Contains(otherPage, `id="conn-ssh"`) {
		t.Fatalf("other page %d", code)
	}
	if _, err := h.st.ConnectionSSHKey(context.Background(), otherUser.ID, connect.DigitalOcean); err != store.ErrNotFound {
		t.Fatal("other user read the key")
	}
}

func TestSSHKeyNameTakenUsesASuffix(t *testing.T) {
	ip := "203.0.113.97:9"
	h := newHarness(t)
	user, secret := h.account("ssh-name@example.com", "Ada", "correct-horse-1")
	h.login("ssh-name@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if _, err := h.st.SaveConnectionSecret(context.Background(), user.ID, connect.DigitalOcean, connectCanary); err != nil {
		t.Fatal(err)
	}
	if err := h.st.EnableConnection(context.Background(), user.ID, agent.ID, connect.DigitalOcean); err != nil {
		t.Fatal(err)
	}
	otherPub, _, err := newAccountSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	up := newKeyUpstream(t)
	up.seed(otherPub, "castbot")
	ts, dial := connectUpstream(t, up.serve)
	defer ts.Close()
	h.srv.SetConnectDial(dial)
	token := guestToken(t, h, agent)
	write := `{"server":"digitalocean","message":{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"invoke_tool","arguments":{"qualified_name":"droplets/droplet-create","arguments":{"Name":"tiny"}}}}}`
	code, body := machinePost(t, h, ip, token, agent.ID, "/egress/mcp", write)
	if code != http.StatusOK || !mcpSentence(body, "This action needs approval.") || up.posts() != 2 || !strings.HasPrefix(up.createdName(), "castbot-") {
		t.Fatalf("suffix %d posts %d name %s", code, up.posts(), up.createdName())
	}
	if sshFingerprint(t, h.st, user.ID) == "" || strings.Contains(body, connectCanary) {
		t.Fatal("suffix fingerprint")
	}
}

func TestConnectionPageReportsAFailedSSHKey(t *testing.T) {
	ip := "203.0.113.98:9"
	h := newHarness(t)
	_, secret := h.account("ssh-fail@example.com", "Ada", "correct-horse-1")
	h.login("ssh-fail@example.com", "correct-horse-1", secret, ip, false)
	code, _, hdr := h.do(http.MethodPost, "/settings/connections/digitalocean", h.form(url.Values{
		"token": {connectCanary},
	}), ip, nil)
	if code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "notice=ssh") {
		t.Fatalf("connect %d %s", code, hdr.Get("Location"))
	}
	code, page, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != 200 || !strings.Contains(page, "The SSH key could not be registered yet.") || strings.Contains(page, `id="conn-ssh"`) || strings.Contains(page, connectCanary) || strings.Contains(page, "ssh-ed25519") {
		t.Fatalf("notice %d", code)
	}
}

type keyUpstream struct {
	mu       sync.Mutex
	keys     []accountKey
	nextPage bool
	postN    int
	getN     int
	page2    int
	mcpN     int
	postRaw  string
	created  string
	calls    []string
}

func newKeyUpstream(t *testing.T) *keyUpstream {
	t.Helper()
	return &keyUpstream{nextPage: true}
}

func (u *keyUpstream) seed(publicLine, name string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.keys = append(u.keys, accountKey{ID: 7, Name: name, PublicKey: publicLine, Fingerprint: mustFingerprint(publicLine)})
}

func (u *keyUpstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	u.mu.Lock()
	u.calls = append(u.calls, r.Method+" "+r.Host+r.URL.RequestURI())
	u.mu.Unlock()
	if r.URL.Path == "/v2/account/keys" && r.Method == http.MethodGet {
		u.mu.Lock()
		u.getN++
		page := r.URL.Query().Get("page")
		if page == "2" {
			u.page2++
		}
		keys := append([]accountKey(nil), u.keys...)
		follow := ""
		if page == "" && u.nextPage {
			follow = "https://api.digitalocean.com/v2/account/keys?page=2&per_page=200"
		}
		u.mu.Unlock()
		writeKeyPage(w, keys, follow)
		return
	}
	if r.URL.Path == "/v2/account/keys" && r.Method == http.MethodPost {
		var req struct {
			Name      string `json:"name"`
			PublicKey string `json:"public_key"`
		}
		_ = json.Unmarshal(body, &req)
		u.mu.Lock()
		u.postN++
		u.postRaw = string(body)
		if req.Name == "castbot" && u.nameTakenLocked("castbot") {
			u.mu.Unlock()
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"id":"unprocessable_entity","message":"name in use"}`))
			return
		}
		fp := mustFingerprint(req.PublicKey)
		u.keys = append(u.keys, accountKey{ID: int64(1000 + len(u.keys)), Name: req.Name, PublicKey: req.PublicKey, Fingerprint: fp})
		u.created = req.Name
		u.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"ssh_key": map[string]any{"id": 1000, "name": req.Name, "fingerprint": fp, "public_key": req.PublicKey}})
		return
	}
	var msg struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &msg)
	if msg.Method == "initialize" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess-ssh")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{}}}`))
		return
	}
	if msg.Method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	u.mu.Lock()
	u.mcpN++
	u.calls = append(u.calls, "mcp "+string(body))
	u.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"ok":true}}`))
}

func writeKeyPage(w http.ResponseWriter, keys []accountKey, next string) {
	type item struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		Fingerprint string `json:"fingerprint"`
		PublicKey   string `json:"public_key"`
	}
	out := make([]item, 0, len(keys))
	for _, key := range keys {
		out = append(out, item{ID: key.ID, Name: key.Name, Fingerprint: key.Fingerprint, PublicKey: key.PublicKey})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ssh_keys": out,
		"links":    map[string]any{"pages": map[string]string{"next": next}},
	})
}

func (u *keyUpstream) nameTakenLocked(name string) bool {
	for _, key := range u.keys {
		if key.Name == name {
			return true
		}
	}
	return false
}

func (u *keyUpstream) posts() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.postN
}

func (u *keyUpstream) gets() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.getN
}

func (u *keyUpstream) mcpCalls() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.mcpN
}

func (u *keyUpstream) postBody() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.postRaw
}

func (u *keyUpstream) createdName() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.created
}

func (u *keyUpstream) sawPage2() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.page2 > 0
}

func (u *keyUpstream) sawFingerprint(fp string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, call := range u.calls {
		if strings.Contains(call, fp) {
			return true
		}
	}
	return false
}

func mustFingerprint(line string) string {
	fp, err := sshLineFingerprint(line)
	if err != nil {
		return ""
	}
	return fp
}

func sshSeed(t *testing.T, st *store.Store, userID string) string {
	t.Helper()
	seed, err := st.SSHKeySeed(context.Background(), userID, connect.DigitalOcean)
	if err != nil || seed == "" {
		t.Fatal("seed")
	}
	return seed
}

func sshFingerprint(t *testing.T, st *store.Store, userID string) string {
	t.Helper()
	row, err := st.ConnectionSSHKey(context.Background(), userID, connect.DigitalOcean)
	if err != nil || !store.SSHFingerprintOK(row.Fingerprint) {
		t.Fatal("fingerprint")
	}
	return row.Fingerprint
}
