package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
	_ "modernc.org/sqlite"
)

func TestVaultPageHidesValues(t *testing.T) {
	ip := "203.0.113.80:9"
	h := newHarness(t)
	user, secret := h.account("ada-vault@example.com", "Ada", "correct-horse-1")
	h.login("ada-vault@example.com", "correct-horse-1", secret, ip, false)
	const value = "vault-sample-value-9k"
	sec, err := h.st.PutSecret(context.Background(), user.ID, "GITHUB_TOKEN", value)
	if err != nil {
		t.Fatal(err)
	}
	h.srv.noteReload(user.ID, "none", sec.ID, "reloaded")
	code, body, _ := h.do(http.MethodGet, "/settings/secrets", "", ip, nil)
	if code != http.StatusOK {
		t.Fatalf("list %d", code)
	}
	if strings.Contains(body, value) || strings.Contains(body, `type="password" name="value" value=`) {
		t.Fatal("list rendered a value")
	}
	if !strings.Contains(body, `aria-label="Value hidden"`) || !strings.Contains(body, "••••••••") || strings.Contains(body, "data-show-password") || !strings.Contains(body, "GITHUB_TOKEN") {
		t.Fatal("mask missing")
	}
	code, body, _ = h.do(http.MethodGet, "/settings/secrets/item/"+sec.ID, "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, value) || strings.Contains(body, `type="password" name="value" value=`) {
		t.Fatal("detail rendered a value")
	}
	if !strings.Contains(body, "Hidden after save") || !strings.Contains(body, "Only names and times are logged, never values.") {
		t.Fatal("detail copy")
	}
	rows, err := h.st.SecretAudit(context.Background(), user.ID, sec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if strings.Contains(row.Detail, value) {
			t.Fatal("audit stored a value")
		}
	}
	code, body, _ = h.do(http.MethodPost, "/settings/secrets", h.form(url.Values{
		"name": {"search-api key"}, "value": {value},
	}), ip, nil)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "Use capital letters, digits and underscores, starting with a letter. For example SEARCH_API_KEY.") || strings.Contains(body, value) {
		t.Fatalf("invalid name %d", code)
	}
	code, body, _ = h.do(http.MethodPost, "/settings/secrets", h.form(url.Values{
		"name": {"GITHUB_TOKEN"}, "value": {value},
	}), ip, nil)
	if code != http.StatusConflict || !strings.Contains(body, "You already have a secret named GITHUB_TOKEN. Pick another name, or") || !strings.Contains(body, "replace that one") || strings.Contains(body, value) {
		t.Fatalf("conflict %d", code)
	}
	code, body, _ = h.do(http.MethodPost, "/settings/secrets", h.form(url.Values{
		"name": {"TOO_BIG"}, "value": {strings.Repeat("b", store.SecretValueMax+1)},
	}), ip, nil)
	if code != http.StatusUnprocessableEntity || strings.Contains(body, strings.Repeat("b", 40)) {
		t.Fatalf("cap %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/settings/secrets", "name=OK_NAME&value=abc&csrf=nope", ip, nil)
	if code != http.StatusForbidden {
		t.Fatalf("csrf %d", code)
	}
}

func TestVaultAuthzIsNotFound(t *testing.T) {
	ip := "203.0.113.81:9"
	h := newHarness(t)
	owner, secret := h.account("ada-authz@example.com", "Ada", "correct-horse-1")
	h.login("ada-authz@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.PutSecret(context.Background(), owner.ID, "GITHUB_TOKEN", "vault-sample-value")
	if err != nil {
		t.Fatal(err)
	}
	agent := vaultAgent(t, h.st, owner.ID, "Atlas")
	if err := h.st.GrantSecret(context.Background(), owner.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	other, otherSecret := h.account("bea-authz@example.com", "Bea", "correct-horse-1")
	h.login("bea-authz@example.com", "correct-horse-1", otherSecret, ip, false)
	paths := []struct {
		method, path string
		form         url.Values
	}{
		{http.MethodGet, "/settings/secrets/item/" + sec.ID, nil},
		{http.MethodPost, "/settings/secrets/item/" + sec.ID + "/replace", url.Values{"value": {"next"}}},
		{http.MethodPost, "/settings/secrets/item/" + sec.ID + "/delete", url.Values{}},
		{http.MethodPost, "/settings/secrets/item/" + sec.ID + "/grant", url.Values{"agent": {agent.ID}}},
		{http.MethodPost, "/agents/" + agent.ID + "/secrets/revoke", url.Values{"secret": {sec.ID}}},
		{http.MethodPost, "/agents/" + agent.ID + "/secrets/grant", url.Values{"secret": {sec.ID}}},
		{http.MethodPost, "/settings/secrets/item/" + sec.ID + "/reload/" + agent.ID, url.Values{}},
	}
	for _, path := range paths {
		form := ""
		if path.form != nil {
			form = h.form(path.form)
		}
		code, body, _ := h.do(path.method, path.path, form, ip, nil)
		if code != http.StatusNotFound {
			t.Fatalf("%s %s -> %d", path.method, path.path, code)
		}
		if strings.Contains(body, "vault-sample-value") {
			t.Fatal("authz body leaked a value")
		}
	}
	_ = other
	if _, err := h.st.SecretMeta(context.Background(), owner.ID, sec.ID); err != nil {
		t.Fatal("owner secret was changed")
	}
}

func TestPausedReplaceDoesNotWake(t *testing.T) {
	ip := "203.0.113.82:9"
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s08", "https://castbot.example")
	user, secret := h.account("ada-pause@example.com", "Ada", "correct-horse-1")
	h.login("ada-pause@example.com", "correct-horse-1", secret, ip, false)
	secRow, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: secRow.ID, Name: "Wren", Title: "Release notes", Status: "paused"})
	if err != nil {
		t.Fatal(err)
	}
	token := readyMachine(t, h, fake, agent, "vm-wren", "paused")
	sec, err := h.st.PutSecret(context.Background(), user.ID, "GITHUB_TOKEN", "old-vault-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	before, edges := fake.Snapshot()
	code, _, hdr := h.do(http.MethodPost, "/settings/secrets/item/"+sec.ID+"/replace", h.form(url.Values{"value": {"new-vault-value"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("replace %d", code)
	}
	after, edgesAfter := fake.Snapshot()
	if edgesAfter != edges || strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatal("paused replace called the host")
	}
	for _, call := range after[len(before):] {
		if call == "reload" || call == "wake" {
			t.Fatal("paused replace woke or reloaded")
		}
	}
	code, body, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK || !strings.Contains(body, "GITHUB_TOKEN") || !strings.Contains(body, "new-vault-value") {
		t.Fatal("next pull did not return the new value")
	}
	if !strings.Contains(hdr.Get("Location"), "watch=1") {
		t.Fatal("replace did not return to the secret")
	}
	got, err := h.st.DecryptSecret(context.Background(), user.ID, sec.ID)
	if err != nil || got != "new-vault-value" {
		t.Fatal("saved value mismatch")
	}
}

func TestReloadWaitsForReplyThenRetries(t *testing.T) {
	ip := "203.0.113.83:9"
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s08", "https://castbot.example")
	user, secret := h.account("ada-defer@example.com", "Ada", "correct-horse-1")
	h.login("ada-defer@example.com", "correct-horse-1", secret, ip, false)
	secRow, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: secRow.ID, Name: "Juniper", Title: "Repo maintainer", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	readyMachine(t, h, fake, agent, "vm-juniper", "running")
	sec, err := h.st.PutSecret(context.Background(), user.ID, "GITHUB_TOKEN", "old-vault-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), user.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), user.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	_, edges := fake.Snapshot()
	code, _, hdr := h.do(http.MethodPost, "/settings/secrets/item/"+sec.ID+"/replace", h.form(url.Values{"value": {"new-vault-value"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("replace %d", code)
	}
	_, edgesNow := fake.Snapshot()
	if edgesNow != edges {
		t.Fatal("reload ran during a reply")
	}
	code, body, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Reloads after its reply") || !strings.Contains(body, "New value saved.") {
		t.Fatal("deferred copy missing")
	}
	if err := h.srv.runs.Finish(run.Job{UserID: user.ID, AgentID: agent.ID, RunID: runRow.ID}, "done", 1); err != nil {
		t.Fatal(err)
	}
	_, edgesDone := fake.Snapshot()
	if edgesDone == edges {
		t.Fatal("reload did not run after the reply")
	}
	fake.ReloadErr = errors.New("down")
	code, _, hdr = h.do(http.MethodPost, "/settings/secrets/item/"+sec.ID+"/replace", h.form(url.Values{"value": {"retry-vault-value"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("second replace %d", code)
	}
	got, err := h.st.DecryptSecret(context.Background(), user.ID, sec.ID)
	if err != nil || got != "retry-vault-value" {
		t.Fatal("failed reload rolled the value back")
	}
	code, body, _ = h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "The new value is saved, but Juniper did not reload.") || !strings.Contains(body, "Try again") {
		t.Fatal("failure copy missing")
	}
	if strings.Contains(body, "retry-vault-value") {
		t.Fatal("failure page showed the value")
	}
	fake.ReloadErr = nil
	code, _, hdr = h.do(http.MethodPost, "/settings/secrets/item/"+sec.ID+"/reload/"+agent.ID, h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("retry %d", code)
	}
	code, body, _ = h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Reloaded") {
		t.Fatal("retry did not reload")
	}
}

func TestModelKeyDeleteAndEnvFallback(t *testing.T) {
	ip := "203.0.113.84:9"
	h := newHarness(t)
	user, secret := h.account("ada-key@example.com", "Ada", "correct-horse-1")
	h.login("ada-key@example.com", "correct-horse-1", secret, ip, false)
	t.Setenv(castcfg.ProviderKeyEnv("openrouter"), "env-provider-value")
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if err := h.st.SetAgentModel(context.Background(), user.ID, agent.ID, "Provider A / model-large"); err != nil {
		t.Fatal(err)
	}
	_, value, missing := h.srv.effectiveModelSecret(context.Background(), user.ID, "Provider A / model-large")
	if missing != "" || value != "env-provider-value" {
		t.Fatal("env fallback did not win")
	}
	code, _, hdr := h.do(http.MethodPost, "/settings/secrets/keys/a", h.form(url.Values{
		"label": {"provider-a-main"}, "value": {"vault-provider-value"},
	}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("add key %d", code)
	}
	_, value, missing = h.srv.effectiveModelSecret(context.Background(), user.ID, "Provider A / model-large")
	if missing != "" || value != "vault-provider-value" {
		t.Fatal("vault key did not win")
	}
	id := strings.TrimPrefix(hdr.Get("Location"), "/settings/secrets/item/")
	id = strings.TrimSuffix(id, "?watch=1")
	code, body, _ := h.do(http.MethodPost, "/settings/secrets/item/"+id+"/delete", h.form(url.Values{}), ip, nil)
	shown := html.UnescapeString(body)
	if code != http.StatusConflict || !strings.Contains(shown, "This key is in use") || !strings.Contains(shown, "provider-a-main is the Provider A key for Atlas.") || !strings.Contains(shown, "Replace this key, or change those agents' model first.") || !strings.Contains(body, "/replace") || strings.Contains(shown, "Add another") || !strings.Contains(shown, "Agent secrets") {
		t.Fatalf("blocked delete %d", code)
	}
	if strings.Contains(body, "vault-provider-value") || strings.Contains(body, "env-provider-value") {
		t.Fatal("blocked page showed a key")
	}
	if err := h.st.SetAgentModel(context.Background(), user.ID, agent.ID, "ollama/local"); err != nil {
		t.Fatal(err)
	}
	code, _, hdr = h.do(http.MethodPost, "/settings/secrets/item/"+id+"/delete", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/settings/secrets" {
		t.Fatalf("delete after switch %d %s", code, hdr.Get("Location"))
	}
	if _, err := h.st.SecretMeta(context.Background(), user.ID, id); err != store.ErrNotFound {
		t.Fatal("key remained")
	}
}

func TestSecretFingerprintIsVaultHMAC(t *testing.T) {
	h := newHarness(t)
	user, _ := h.account("ada-fp@example.com", "Ada", "correct-horse-1")
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	const value = "demo-secret-value"
	sec, err := h.st.PutSecret(context.Background(), user.ID, "DEMO_TOKEN", value)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	agent, err = h.st.GetAgent(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := h.srv.secretFingerprint(context.Background(), user.ID, agent)
	if err != nil {
		t.Fatal(err)
	}
	want := hashSecretPairs(hmac.New(sha256.New, h.srv.secretFPKey()), []secretPair{{name: "DEMO_TOKEN", value: value}})
	plain := hashSecretPairs(sha256.New(), []secretPair{{name: "DEMO_TOKEN", value: value}})
	if fp != want || fp == plain {
		t.Fatal("fingerprint is not the HMAC of the decrypted value")
	}
	if err := h.st.SetSecretFP(context.Background(), user.ID, agent.ID, fp); err != nil {
		t.Fatal(err)
	}
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s08", "https://castbot.example")
	if err := h.srv.refreshSecrets(context.Background(), user.ID, agent, "https://vm.example", "token"); err != nil {
		t.Fatal(err)
	}
	_, edges := fake.Snapshot()
	if edges != 0 {
		t.Fatal("unchanged fingerprint reloaded")
	}
}

func vaultAgent(t *testing.T, st *store.Store, userID, name string) store.Agent {
	t.Helper()
	sec, err := st.SystemSection(context.Background(), userID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(context.Background(), userID, store.AgentInput{
		SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func TestReplaceLockRefusesVaultReload(t *testing.T) {
	ip := "203.0.113.85:9"
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s08", "https://castbot.example")
	user, secret := h.account("ada-lock@example.com", "Ada", "correct-horse-1")
	h.login("ada-lock@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	readyMachine(t, h, fake, agent, "vm-atlas", "running")
	if _, _, err := h.st.BeginReplacement(context.Background(), user.ID, agent.ID, "ada@example.com", "example.invalid/castbot-runtime:s08", false); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.PutSecret(context.Background(), user.ID, "GITHUB_TOKEN", "old-vault-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodPost, "/settings/secrets/item/"+sec.ID+"/replace", h.form(url.Values{"value": {"new-vault-value"}}), ip, nil)
	if code != http.StatusConflict || !strings.Contains(body, "This machine is being replaced.") {
		t.Fatalf("lock %d", code)
	}
	got, err := h.st.DecryptSecret(context.Background(), user.ID, sec.ID)
	if err != nil || got != "old-vault-value" {
		t.Fatal("replace lock still wrote a value")
	}
}

func TestMachinePullIsGrantPlusModelKey(t *testing.T) {
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s08", "https://castbot.example")
	user, _ := h.account("pull@example.com", "Ada", "correct-horse-1")
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if err := h.st.SetAgentModel(context.Background(), user.ID, agent.ID, "Provider A / model-large"); err != nil {
		t.Fatal(err)
	}
	agent.Model = "Provider A / model-large"
	const granted = "grant-sample-value"
	const other = "other-sample-value"
	const modelKey = "model-key-sample"
	g, err := h.st.PutSecret(context.Background(), user.ID, "GRANTED_TOKEN", granted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutSecret(context.Background(), user.ID, "OTHER_TOKEN", other); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{Name: "PROVIDER_A_MAIN", Label: "provider-a-main", Provider: "a", Value: modelKey}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	yaml := "name: Atlas\nbrain:\n  model: Provider A / model-large\n"
	if _, err := h.st.PutBundle(context.Background(), user.ID, agent.ID, "1", yaml, "Stay brief.\n"); err != nil {
		t.Fatal(err)
	}
	token := readyMachine(t, h, fake, agent, "vm-pull", "running")
	ip := "203.0.113.86:9"
	code, body, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK || !strings.Contains(body, "GRANTED_TOKEN") || !strings.Contains(body, granted) || !strings.Contains(body, modelKey) {
		t.Fatal("pull missing a granted secret or the model key")
	}
	if strings.Contains(body, "OTHER_TOKEN") || strings.Contains(body, other) {
		t.Fatal("pull included a secret that was not granted")
	}
	bundle, err := h.st.LatestBundle(context.Background(), user.ID, agent.ID)
	if err != nil || strings.Contains(bundle.YAML, granted) || strings.Contains(bundle.YAML, other) || strings.Contains(bundle.YAML, modelKey) {
		t.Fatal("bundle stored a secret")
	}
	code, packed, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK || strings.Contains(packed, granted) || strings.Contains(packed, other) || strings.Contains(packed, modelKey) {
		t.Fatal("bundle response carried a secret")
	}
}

func TestNewestVaultKeyWins(t *testing.T) {
	ip := "203.0.113.87:9"
	h := newHarness(t)
	user, secret := h.account("ada-newest@example.com", "Ada", "correct-horse-1")
	h.login("ada-newest@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if err := h.st.SetAgentModel(context.Background(), user.ID, agent.ID, "Provider A / model-large"); err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC) })
	old, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{
		Name: "PROVIDER_A_OLD", Label: "old-key", Provider: "a", Value: "old-vault-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	h.st.SetNow(func() time.Time { return time.Date(2026, 11, 3, 15, 0, 0, 0, time.UTC) })
	newer, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{
		Name: "PROVIDER_A_NEW", Label: "new-key", Provider: "a", Value: "new-vault-value",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, value, missing := h.srv.effectiveModelSecret(context.Background(), user.ID, "Provider A / model-large")
	if missing != "" || value != "new-vault-value" {
		t.Fatal("newest vault key did not win")
	}
	code, body, _ := h.do(http.MethodGet, "/settings/secrets", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Not in use") || !strings.Contains(body, "old-key") || strings.Contains(body, "old-vault-value") || strings.Contains(body, "new-vault-value") {
		t.Fatal("older key was not marked unused")
	}
	slots := modelSlots()
	if len(slots) < 3 {
		t.Fatal("expected every keyed provider")
	}
	for _, slot := range slots {
		if !strings.Contains(body, slot.Title) {
			t.Fatalf("missing %s", slot.Title)
		}
	}
	code, _, _ = h.do(http.MethodPost, "/settings/secrets/item/"+old.ID+"/delete", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("unused delete %d", code)
	}
	if _, err := h.st.SecretMeta(context.Background(), user.ID, old.ID); err != store.ErrNotFound {
		t.Fatal("unused key remained")
	}
	code, body, _ = h.do(http.MethodPost, "/settings/secrets/item/"+newer.ID+"/delete", h.form(url.Values{}), ip, nil)
	if code != http.StatusConflict || !strings.Contains(body, "This key is in use") {
		t.Fatalf("effective delete %d", code)
	}
	third := slots[2]
	model := third.Manifest + "/demo-model"
	if inf, ok := castcfg.InferenceFor(model); !ok || inf.Key != third.Manifest {
		t.Fatal("third provider model did not match")
	}
	t.Setenv(castcfg.ProviderKeyEnv(third.Manifest), "env-third-value")
	_, value, missing = h.srv.effectiveModelSecret(context.Background(), user.ID, model)
	if missing != "" || value != "env-third-value" {
		t.Fatal("env fallback missed a later provider")
	}
	if _, err := h.st.CreateSecret(context.Background(), user.ID, store.SecretInput{
		Name: "PROVIDER_C_MAIN", Label: "later-key", Provider: third.ID, Value: "vault-third-value",
	}); err != nil {
		t.Fatal(err)
	}
	_, value, missing = h.srv.effectiveModelSecret(context.Background(), user.ID, model)
	if missing != "" || value != "vault-third-value" {
		t.Fatal("vault key missed a later provider")
	}
}

func TestMigrateKeepsHMACAndSkipsReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	opt := store.Options{SecretsKey: bytes.Repeat([]byte{0x3c}, 32), Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}}
	st, err := store.Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, err := auth.Hash("correct-horse-1", st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	totp, err := auth.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, store.NewUser{Email: "ada-fp2@example.com", DisplayName: "Ada", Workspace: "Personal", PasswordHash: hash, TOTPSecret: totp})
	if err != nil {
		t.Fatal(err)
	}
	sec, err := st.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := st.CreateAgent(ctx, user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	const id = "sec-fp"
	const plain = "fixture-plain-value"
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = 16`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO secret_fixtures(id, workspace_id, user_id, name, value, created_at) VALUES (?, ?, ?, 'SEARCH_API_KEY', ?, '2026-10-28T15:00:00Z')`, id, user.WorkspaceID, user.ID, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_secret_grants(agent_id, secret_id, workspace_id, user_id) VALUES (?, ?, ?, ?)`, agent.ID, id, user.WorkspaceID, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	session := bytes.Repeat([]byte{0x11}, 32)
	mac := hmac.New(sha256.New, session)
	mac.Write([]byte("castbot-secret-fp-v1"))
	before := hashSecretPairs(hmac.New(sha256.New, mac.Sum(nil)), []secretPair{{name: "SEARCH_API_KEY", value: plain}})
	st, err = store.Open(path, opt)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := New(st, session, nil)
	t.Cleanup(srv.Close)
	agent, err = st.GetAgent(ctx, user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := srv.secretFingerprint(ctx, user.ID, agent)
	if err != nil || after != before {
		t.Fatal("fingerprint changed across migration")
	}
	if err := st.SetSecretFP(ctx, user.ID, agent.ID, before); err != nil {
		t.Fatal(err)
	}
	fake := host.NewFake()
	srv.SetHost(fake, "example.invalid/castbot-runtime:s08", "https://castbot.example")
	if err := srv.refreshSecrets(ctx, user.ID, agent, "https://vm.example", "token"); err != nil {
		t.Fatal(err)
	}
	if _, edges := fake.Snapshot(); edges != 0 {
		t.Fatal("migration reloaded a matching set")
	}
}

func TestReloadWaitsForApproval(t *testing.T) {
	ip := "203.0.113.88:9"
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s08", "https://castbot.example")
	user, secret := h.account("ada-approve@example.com", "Ada", "correct-horse-1")
	h.login("ada-approve@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Juniper")
	readyMachine(t, h, fake, agent, "vm-approve", "running")
	sec, err := h.st.PutSecret(context.Background(), user.ID, "GITHUB_TOKEN", "old-vault-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	thread := agentThread(t, h, user.ID, agent.ID)
	runRow, err := h.st.CreateRun(context.Background(), user.ID, thread, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), user.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	ap, err := h.st.RecordPermission(context.Background(), user.ID, runRow.ID, json.RawMessage(`{"id":"req-1","tool":"bash"}`), time.Now().Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	gotRun, err := h.st.GetRun(context.Background(), user.ID, runRow.ID)
	if err != nil || gotRun.Status != "waiting_approval" {
		t.Fatal("approval did not wait")
	}
	_, edges := fake.Snapshot()
	code, _, hdr := h.do(http.MethodPost, "/settings/secrets/item/"+sec.ID+"/replace", h.form(url.Values{"value": {"new-vault-value"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("replace %d", code)
	}
	if _, now := fake.Snapshot(); now != edges {
		t.Fatal("reload ran during an approval")
	}
	code, body, _ := h.do(http.MethodGet, hdr.Get("Location"), "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "Reloads after its reply") {
		t.Fatal("approval wait was not shown")
	}
	if _, claimed, err := h.st.ClaimDecision(context.Background(), user.ID, ap.ID, runRow.ID, "denied", user.ID); err != nil || !claimed {
		t.Fatal("approval was not decided")
	}
	if err := h.srv.runs.Finish(run.Job{UserID: user.ID, AgentID: agent.ID, RunID: runRow.ID}, "done", 2); err != nil {
		t.Fatal(err)
	}
	if _, now := fake.Snapshot(); now == edges {
		t.Fatal("reload did not run after the approval")
	}
}

func TestVaultMutationsNeedCSRFAndReplaceLock(t *testing.T) {
	ip := "203.0.113.89:9"
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s08", "https://castbot.example")
	user, secret := h.account("ada-lock2@example.com", "Ada", "correct-horse-1")
	h.login("ada-lock2@example.com", "correct-horse-1", secret, ip, false)
	agent := vaultAgent(t, h.st, user.ID, "Atlas")
	if err := h.st.SetAgentModel(context.Background(), user.ID, agent.ID, "Provider A / model-large"); err != nil {
		t.Fatal(err)
	}
	readyMachine(t, h, fake, agent, "vm-lock", "running")
	sec, err := h.st.PutSecret(context.Background(), user.ID, "GITHUB_TOKEN", "old-vault-value")
	if err != nil {
		t.Fatal(err)
	}
	spare, err := h.st.PutSecret(context.Background(), user.ID, "OTHER_TOKEN", "spare-vault-value")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(context.Background(), user.ID, agent.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	posts := []struct {
		path string
		form url.Values
	}{
		{"/settings/secrets", url.Values{"name": {"NEW_TOKEN"}, "value": {"fresh-vault-value"}, "agent": {agent.ID}}},
		{"/settings/secrets/keys/a", url.Values{"label": {"provider-a-main"}, "value": {"vault-provider-value"}}},
		{"/settings/secrets/item/" + sec.ID + "/replace", url.Values{"value": {"new-vault-value"}}},
		{"/settings/secrets/item/" + sec.ID + "/delete", url.Values{}},
		{"/settings/secrets/item/" + sec.ID + "/reload/" + agent.ID, url.Values{}},
		{"/settings/secrets/item/" + sec.ID + "/grant", url.Values{"agent": {agent.ID}}},
		{"/agents/" + agent.ID + "/secrets/grant", url.Values{"secret": {spare.ID}}},
		{"/agents/" + agent.ID + "/secrets/revoke", url.Values{"secret": {sec.ID}}},
	}
	for _, post := range posts {
		vals := url.Values{}
		for k, vs := range post.form {
			vals[k] = append([]string{}, vs...)
		}
		vals.Set("csrf", "nope")
		code, body, _ := h.do(http.MethodPost, post.path, vals.Encode(), ip, nil)
		if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
			t.Fatalf("csrf %s %d", post.path, code)
		}
	}
	if _, err := h.st.SecretByName(context.Background(), user.ID, "NEW_TOKEN"); err != store.ErrNotFound {
		t.Fatal("csrf add wrote a secret")
	}
	got, err := h.st.DecryptSecret(context.Background(), user.ID, sec.ID)
	if err != nil || got != "old-vault-value" {
		t.Fatal("csrf changed a value")
	}
	if _, _, err := h.st.BeginReplacement(context.Background(), user.ID, agent.ID, "ada@example.com", "example.invalid/castbot-runtime:s08", false); err != nil {
		t.Fatal(err)
	}
	for _, post := range posts {
		code, body, _ := h.do(http.MethodPost, post.path, h.form(post.form), ip, nil)
		if code != http.StatusConflict || !strings.Contains(body, "This machine is being replaced.") {
			t.Fatalf("lock %s %d", post.path, code)
		}
	}
	got, err = h.st.DecryptSecret(context.Background(), user.ID, sec.ID)
	if err != nil || got != "old-vault-value" {
		t.Fatal("lock changed a value")
	}
	if _, err := h.st.SecretByName(context.Background(), user.ID, "NEW_TOKEN"); err != store.ErrNotFound {
		t.Fatal("lock wrote a secret")
	}
	ids, err := h.st.GrantAgentIDs(context.Background(), user.ID, sec.ID)
	if err != nil || len(ids) != 1 || ids[0] != agent.ID {
		t.Fatal("lock changed a grant")
	}
	ids, err = h.st.GrantAgentIDs(context.Background(), user.ID, spare.ID)
	if err != nil || len(ids) != 0 {
		t.Fatal("lock granted a spare secret")
	}
}
