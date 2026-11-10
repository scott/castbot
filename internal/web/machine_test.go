package web

import (
	"context"
	"net/http"
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
