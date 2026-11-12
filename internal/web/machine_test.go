package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
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
	const fileMarker = "owner-file-marker"
	dir := t.TempDir()
	h.srv.SetDataDir(dir)
	sum := sha256.Sum256([]byte(fileMarker))
	digest := hex.EncodeToString(sum[:])
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", digest), []byte(fileMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	upload, err := h.st.InsertUpload(ctx, owner.ID, agent.ID, "", fileMarker+".txt", "text/plain", digest, "workspace/uploads/"+fileMarker+".txt", "copied", int64(len(fileMarker)))
	if err != nil {
		t.Fatal(err)
	}
	cacheID, err := h.st.PlantCached(ctx, owner.ID, agent.ID, "docs/"+fileMarker+".md", int64(len(fileMarker)))
	if err != nil {
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
		"/agents/" + agent.ID + "?panel=files",
		"/agents/" + agent.ID + "/files/" + upload.ID + "/download",
		"/agents/" + agent.ID + "/files/" + upload.ID + "/preview",
		"/agents/" + agent.ID + "/files/" + cacheID + "/download",
		"/agents/" + agent.ID + "/files/" + cacheID + "/preview",
	} {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusNotFound || strings.Contains(body, marker) || strings.Contains(body, "Bundle marker") || strings.Contains(body, fileMarker) {
			t.Fatalf("%s -> %d", path, code)
		}
	}
	code, body, _ := h.do(http.MethodGet, "/search?q="+fileMarker, "", ip, nil)
	if code != http.StatusOK || strings.Contains(body, fileMarker) {
		t.Fatalf("search %d", code)
	}
	h.mu.Lock()
	csrf := h.csrf
	h.mu.Unlock()
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/uploads", "", ip, map[string]string{
		"X-CSRF-Token": csrf,
		"X-File-Name":  "notes.txt",
		"Content-Type": "application/octet-stream",
	})
	if code != http.StatusNotFound || strings.Contains(body, fileMarker) {
		t.Fatalf("upload %d %s", code, body)
	}
	boot, err := h.st.IssueBootstrap(ctx, other.ID, otherAgent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(ctx, otherAgent.ID, boot)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/bundle", "", ip, map[string]string{"Authorization": "Bearer " + token})
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
	ip := "203.0.113.90:9"
	h.login("page@example.com", "correct-horse-1", secret, ip, false)
	for _, path := range []string{
		"/agents/" + agent.ID + "/workspace/x.html",
		"/agents/" + agent.ID + "/workspace/x.js",
	} {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusNotFound || strings.Contains(body, secretValue) {
			t.Fatalf("%s -> %d", path, code)
		}
	}
	var code int
	var body string
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
	if code != http.StatusNotFound {
		t.Fatalf("removed workspace route %d", code)
	}
}
