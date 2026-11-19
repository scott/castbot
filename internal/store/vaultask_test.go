package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNamedSecretCardsReuseAndIsolate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	owner := makeUser(t, s, "ada-vaultask@example.com", "Ada")
	other := makeUser(t, s, "bea-vaultask@example.com", "Bea")
	agent := vaultAskAgent(t, s, owner, "Atlas")
	otherAgent := vaultAskAgent(t, s, other, "Wren")
	exp := s.Now().Add(time.Hour)

	first, created, err := s.CreateNamedSecretRequest(ctx, owner.ID, agent.ID, "thread", "run", "x_api_key", "Monitor posts", "Watch posts", exp)
	if err != nil || !created || first.Connection != "X_API_KEY" || first.Label != "Monitor posts" || first.Status != "pending" {
		t.Fatalf("create %+v %v %v", first, created, err)
	}
	again, created, err := s.CreateNamedSecretRequest(ctx, owner.ID, agent.ID, "thread", "run", "X_API_KEY", "Other", "Again", exp)
	if err != nil || created || again.ID != first.ID || again.Label != "Monitor posts" {
		t.Fatalf("reuse %+v %v %v", again, created, err)
	}
	if _, err := s.GetSecretRequest(ctx, other.ID, first.ID); err != ErrNotFound {
		t.Fatalf("other user read the card %v", err)
	}
	if _, err := s.ResolveSecretRequest(ctx, other.ID, first.ID, "declined"); err != ErrNotFound {
		t.Fatalf("other user resolved the card %v", err)
	}
	if _, _, err := s.CreateNamedSecretRequest(ctx, other.ID, agent.ID, "", "", "X_API_KEY", "", "no", exp); err != ErrNotFound {
		t.Fatalf("other user opened a card on this agent %v", err)
	}
	saved, err := s.ResolveSecretRequest(ctx, owner.ID, first.ID, "saved")
	if err != nil || saved.Status != "saved" || saved.ResolvedAt == "" {
		t.Fatalf("resolve %+v %v", saved, err)
	}
	if _, _, err := s.CreateNamedSecretRequest(ctx, owner.ID, agent.ID, "", "", "GITHUB_CONNECTION", "", "no", exp); err != ErrInvalid {
		t.Fatalf("connection name %v", err)
	}

	base := s.Now()
	for i := 0; i < SecretRequestLimit; i++ {
		row, _, err := s.CreateNamedSecretRequest(ctx, owner.ID, agent.ID, "", "", "LIMIT_KEY", "", "n", exp)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveSecretRequest(ctx, owner.ID, row.ID, "declined"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateNamedSecretRequest(ctx, owner.ID, agent.ID, "", "", "LIMIT_KEY", "", "n", exp); err != ErrConflict {
		t.Fatalf("limit %v", err)
	}
	s.SetNow(func() time.Time { return base.Add(2 * time.Hour) })
	if _, created, err := s.CreateNamedSecretRequest(ctx, owner.ID, agent.ID, "", "", "LIMIT_KEY", "", "later", s.Now().Add(time.Hour)); err != nil || !created {
		t.Fatalf("after the hour %v %v", created, err)
	}

	del, created, err := s.CreateSecretDelete(ctx, owner.ID, agent.ID, "thread", "run", "x_api_key", "Stop", s.Now().Add(10*time.Minute))
	if err != nil || !created || del.Name != "X_API_KEY" || del.UserID != owner.ID || del.WorkspaceID == "" {
		t.Fatalf("delete %+v %v %v", del, created, err)
	}
	reused, created, err := s.CreateSecretDelete(ctx, owner.ID, agent.ID, "", "", "X_API_KEY", "Again", s.Now().Add(time.Hour))
	if err != nil || created || reused.ID != del.ID {
		t.Fatalf("delete reuse %+v %v %v", reused, created, err)
	}
	if _, err := s.GetSecretDelete(ctx, other.ID, del.ID); err != ErrNotFound {
		t.Fatalf("other user read the delete %v", err)
	}
	if _, err := s.ListSecretDeletes(ctx, other.ID, agent.ID, time.Time{}); err != ErrNotFound {
		t.Fatalf("other user listed deletes %v", err)
	}
	if _, err := s.ResolveSecretDelete(ctx, other.ID, del.ID, "declined"); err != ErrNotFound {
		t.Fatalf("other user denied the delete %v", err)
	}
	if _, _, err := s.CreateSecretDelete(ctx, other.ID, agent.ID, "", "", "X_API_KEY", "no", s.Now().Add(time.Hour)); err != ErrNotFound {
		t.Fatalf("other user opened a delete %v", err)
	}
	listed, err := s.ListSecretDeletes(ctx, owner.ID, agent.ID, s.Now().Add(-time.Hour))
	if err != nil || len(listed) != 1 || listed[0].ID != del.ID {
		t.Fatalf("list %+v %v", listed, err)
	}
	denied, err := s.ResolveSecretDelete(ctx, owner.ID, del.ID, "declined")
	if err != nil || denied.Status != "declined" {
		t.Fatalf("deny %+v %v", denied, err)
	}
	for i := 0; i < SecretRequestLimit; i++ {
		row, _, err := s.CreateSecretDelete(ctx, owner.ID, agent.ID, "", "", "LIMIT_KEY", "n", s.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResolveSecretDelete(ctx, owner.ID, row.ID, "deleted"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateSecretDelete(ctx, owner.ID, agent.ID, "", "", "LIMIT_KEY", "n", s.Now().Add(time.Hour)); err != ErrConflict {
		t.Fatalf("delete limit %v", err)
	}
	fresh, _, err := s.CreateSecretDelete(ctx, other.ID, otherAgent.ID, "", "", "X_API_KEY", "own", s.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	later := s.Now().Add(2 * time.Minute)
	s.SetNow(func() time.Time { return later })
	expired, err := s.ExpireSecretDelete(ctx, other.ID, fresh.ID)
	if err != nil || expired.Status != "expired" {
		t.Fatalf("expire %+v %v", expired, err)
	}
	cols := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(secret_deletes)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	rows.Close()
	if cols["value"] || !cols["user_id"] || !cols["workspace_id"] || !cols["name"] {
		t.Fatalf("columns %#v", cols)
	}
	var audits string
	auditRows, err := s.db.Query(`SELECT detail_json FROM audit_log WHERE user_id = ?`, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	for auditRows.Next() {
		var detail string
		if err := auditRows.Scan(&detail); err != nil {
			t.Fatal(err)
		}
		audits += detail
	}
	auditRows.Close()
	if strings.Contains(audits, "test-named") || strings.Contains(audits, "value") {
		t.Fatal("audit held a value")
	}
}

func vaultAskAgent(t *testing.T, s *Store, user User, name string) Agent {
	t.Helper()
	sec, err := s.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(context.Background(), user.ID, AgentInput{SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	return agent
}
