package store

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/connect"
)

func connectStore(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	if err := s.SetSecretsKey(bytes.Repeat([]byte{0x3c}, 32)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConnectionGrantIsNotAnEnvSecret(t *testing.T) {
	s := connectStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "ada@example.com", "Ada")
	other := makeUser(t, s, "bea@example.com", "Bea")
	secRow, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: secRow.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	const canary = "test-connection-value"
	saved, err := s.SaveConnectionSecret(ctx, owner.ID, connect.GitHub, canary)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantSecret(ctx, owner.ID, agent.ID, saved.ID); err == nil {
		t.Fatal("connection secret was granted as an env secret")
	}
	got, err := s.ConnectionValue(ctx, owner.ID, connect.GitHub)
	if err != nil || got != canary {
		t.Fatal("vault read")
	}
	if _, err := s.SaveConnectionSecret(ctx, owner.ID, connect.GitHub, canary+"-2"); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableConnection(ctx, owner.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	on, err := s.ConnectionEnabled(ctx, owner.ID, agent.ID, connect.GitHub)
	if err != nil || !on {
		t.Fatal("grant")
	}
	if _, err := s.db.Exec(`INSERT INTO agent_secret_grants(agent_id, secret_id, workspace_id, user_id) VALUES (?, ?, ?, ?)`, agent.ID, saved.ID, owner.WorkspaceID, owner.ID); err != nil {
		t.Fatal(err)
	}
	pulled, err := s.GrantedSecrets(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pulled {
		if item.Name == connect.SecretNameGitHub || item.Value == canary || strings.Contains(item.Value, canary) {
			t.Fatal("pull returned a connection secret")
		}
	}
	if _, err := s.db.Exec(`UPDATE secrets SET kind = 'env' WHERE id = ? AND user_id = ?`, saved.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	pulled, err = s.GrantedSecrets(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range pulled {
		if connect.BlocksGuest(item.Name) || strings.Contains(item.Value, canary) {
			t.Fatal("forced env kind still left the vault")
		}
	}
	if _, err := s.db.Exec(`UPDATE secrets SET kind = 'connection' WHERE id = ? AND user_id = ?`, saved.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeConnection(ctx, owner.ID, agent.ID, connect.GitHub); err != nil {
		t.Fatal(err)
	}
	on, err = s.ConnectionEnabled(ctx, owner.ID, agent.ID, connect.GitHub)
	if err != nil || on {
		t.Fatal("revoke")
	}
	if err := s.EnableConnection(ctx, other.ID, agent.ID, connect.GitHub); err == nil {
		t.Fatal("other user enabled the grant")
	}
	rows, err := s.ListAudit(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sawEnable, sawRevoke bool
	for _, row := range rows {
		if strings.Contains(row.Detail, canary) {
			t.Fatal("audit held the value")
		}
		if row.Action == "connection.enabled" {
			sawEnable = true
		}
		if row.Action == "connection.revoked" {
			sawRevoke = true
		}
	}
	if !sawEnable || !sawRevoke {
		t.Fatal("audit actions")
	}
	has, err := s.SchemaHas(ctx, 22)
	if err != nil || !has {
		t.Fatal("schema 22")
	}
}

func TestSecretCardExpiryReuseAndLimit(t *testing.T) {
	s := connectStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "ada2@example.com", "Ada")
	other := makeUser(t, s, "bea2@example.com", "Bea")
	secRow, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: secRow.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	s.SetNow(func() time.Time { return now })
	first, created, err := s.CreateSecretRequest(ctx, owner.ID, agent.ID, "", "", connect.GitHub, "ship the notes", now.Add(time.Hour))
	if err != nil || !created {
		t.Fatal(err)
	}
	again, created, err := s.CreateSecretRequest(ctx, owner.ID, agent.ID, "", "", connect.GitHub, "again", now.Add(time.Hour))
	if err != nil || created || again.ID != first.ID {
		t.Fatal("pending was not reused")
	}
	if _, err := s.GetSecretRequest(ctx, other.ID, first.ID); err != ErrNotFound {
		t.Fatal("other user read the card")
	}
	s.SetNow(func() time.Time { return now.Add(61 * time.Minute) })
	expired, err := s.ExpireSecretRequest(ctx, owner.ID, first.ID)
	if err != nil || expired.Status != "expired" {
		t.Fatalf("%+v %v", expired, err)
	}
	later := now.Add(3 * time.Hour)
	s.SetNow(func() time.Time { return later })
	for i := 0; i < SecretRequestLimit; i++ {
		row, _, err := s.CreateSecretRequest(ctx, owner.ID, agent.ID, "", "", connect.GitHub, "n", later.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveSecretRequest(ctx, owner.ID, row.ID, "declined"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateSecretRequest(ctx, owner.ID, agent.ID, "", "", connect.GitHub, "n", later.Add(time.Hour)); err != ErrConflict {
		t.Fatal("rate limit")
	}
	rows, err := s.ListAudit(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var limited bool
	for _, row := range rows {
		if row.Action == "secret_request.limited" {
			limited = true
		}
		if strings.Contains(row.Detail, "test-connection-value") {
			t.Fatal("audit value")
		}
	}
	if !limited {
		t.Fatal("limited audit")
	}
}

func TestApprovedConnectCardExpiresUnused(t *testing.T) {
	s := connectStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "ada4@example.com", "Ada")
	secRow, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: secRow.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	row, err := s.CreateConnectApproval(ctx, owner.ID, agent.ID, "", connect.GitHub, "POST /repos/octo/demo/issues", "POST", "/repos/octo/demo/issues", hash, s.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.DecideConnectApproval(ctx, owner.ID, row.ID, "approved"); err != nil || !ok {
		t.Fatal("approve")
	}
	s.SetNow(func() time.Time { return time.Now().Add(2 * time.Minute) })
	got, err := s.ExpireConnectApproval(ctx, owner.ID, row.ID)
	if err != nil || got.Status != "expired" {
		t.Fatalf("status %s %v", got.Status, err)
	}
	ok, err := s.ConsumeConnectApproval(ctx, owner.ID, row.ID)
	if err != nil || ok {
		t.Fatal("expired approval was consumed")
	}
}

func TestConsumeCastApprovalOnce(t *testing.T) {
	s := connectStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "ada3@example.com", "Ada")
	secRow, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: secRow.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateRun(ctx, owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	ap, err := s.RecordPermission(ctx, owner.ID, run.ID, []byte(`{"id":"req-1","tool":"create_issue","arguments":{}}`), s.Now().Add(time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimDecision(ctx, owner.ID, ap.ID, run.ID, "approved", owner.ID); err != nil || !ok {
		t.Fatal(err)
	}
	ok, err := s.ConsumeToolApproval(ctx, owner.ID, agent.ID, "create_issue")
	if err != nil || !ok {
		t.Fatal("first consume")
	}
	ok, err = s.ConsumeToolApproval(ctx, owner.ID, agent.ID, "create_issue")
	if err != nil || ok {
		t.Fatal("second consume")
	}
}

func TestLegacyProviderTokenGrantIsNotPulled(t *testing.T) {
	s := connectStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "ada-legacy@example.com", "Ada")
	secRow, err := s.SystemSection(ctx, owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, owner.ID, AgentInput{SectionID: secRow.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	const canary = "vault-sample-token"
	sec, err := s.PutSecret(ctx, owner.ID, "DIGITALOCEAN_API_TOKEN", canary)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.GrantSecret(ctx, owner.ID, agent.ID, sec.ID); err == nil {
		t.Fatal("grant of a provider token name")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO agent_secret_grants(agent_id, secret_id, workspace_id, user_id) VALUES (?, ?, ?, ?)`,
		agent.ID, sec.ID, owner.WorkspaceID, owner.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GrantedSecrets(ctx, owner.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got {
		if item.Name == "DIGITALOCEAN_API_TOKEN" || strings.Contains(item.Value, canary) {
			t.Fatal("guest pull included a provider token")
		}
	}
	on, err := s.ConnectionEnabled(ctx, owner.ID, agent.ID, connect.DigitalOcean)
	if err != nil || on {
		t.Fatal("env row enabled a connection")
	}
}
