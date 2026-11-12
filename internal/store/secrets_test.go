package store

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/auth"
)

func vaultKey() []byte {
	return bytes.Repeat([]byte{0x3c}, 32)
}

func TestMigrateEncryptsFixtures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	ctx := context.Background()
	s, err := Open(path, Options{SecretsKey: vaultKey(), Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	user := makeUser(t, s, "ada@example.com", "Ada")
	sec, err := s.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := s.CreateAgent(ctx, user.ID, AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	const id = "sec-migrate"
	const plain = "fixture-plain-value"
	if _, err := s.db.Exec(`DELETE FROM schema_migrations WHERE version = 16`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO secret_fixtures(id, workspace_id, user_id, name, value, created_at) VALUES (?, ?, ?, 'SEARCH_API_KEY', ?, '2026-10-28T15:00:00Z')`, id, user.WorkspaceID, user.ID, plain); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO agent_secret_grants(agent_id, secret_id, workspace_id, user_id) VALUES (?, ?, ?, ?)`, agent.ID, id, user.WorkspaceID, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, Options{SecretsKey: vaultKey(), Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	has, err := s.SchemaHas(ctx, 16)
	if err != nil || !has {
		t.Fatalf("version 16 recorded: %v %v", has, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 16`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("version rows %d %v", n, err)
	}
	var left int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM secret_fixtures`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("plaintext rows %d %v", left, err)
	}
	got, err := s.DecryptSecret(ctx, user.ID, id)
	if err != nil || got != plain {
		t.Fatal("migrated value did not decrypt")
	}
	grants, err := s.GrantedSecrets(ctx, user.ID, agent.ID)
	if err != nil || len(grants) != 1 || grants[0].ID != id || grants[0].Value != plain {
		t.Fatal("grant did not survive migration")
	}
	var raw string
	if err := s.db.QueryRow(`SELECT CAST(ciphertext AS TEXT) FROM secrets WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, plain) {
		t.Fatal("ciphertext contains the value")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	checkpoint(t, path)
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wal, _ := os.ReadFile(path + "-wal")
	if bytes.Contains(disk, []byte(plain)) || bytes.Contains(wal, []byte(plain)) {
		t.Fatal("plaintext remains on disk")
	}
	s, err = Open(path, Options{SecretsKey: vaultKey(), Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 16`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("reopen version rows %d %v", n, err)
	}
	got, err = s.DecryptSecret(ctx, user.ID, id)
	if err != nil || got != plain {
		t.Fatal("reopen did not decrypt")
	}
}

func checkpoint(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`VACUUM`); err != nil {
		t.Fatal(err)
	}
}

func TestSecretNameRuleAndCap(t *testing.T) {
	s := testStore(t)
	if err := s.SetSecretsKey(vaultKey()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := makeUser(t, s, "ada@example.com", "Ada")
	other := makeUser(t, s, "bea@example.com", "Bea")
	ok, err := s.CreateSecret(ctx, user.ID, SecretInput{Name: "SEARCH_API_KEY", Value: "short"})
	if err != nil || ok.Name != "SEARCH_API_KEY" {
		t.Fatal(err)
	}
	for _, name := range []string{"search-api key", "PATH", "HOME", "CASTBOT_FOO", "CASTLET_FOO", "HOSTNAME", "1SECRET"} {
		if _, err := s.CreateSecret(ctx, user.ID, SecretInput{Name: name, Value: "short"}); err != ErrInvalid {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := s.CreateSecret(ctx, user.ID, SecretInput{Name: "SEARCH_API_KEY", Value: "other"}); err != ErrConflict {
		t.Fatalf("unique: %v", err)
	}
	if _, err := s.CreateSecret(ctx, other.ID, SecretInput{Name: "SEARCH_API_KEY", Value: "theirs"}); err != nil {
		t.Fatalf("other user: %v", err)
	}
	big := strings.Repeat("a", SecretValueMax)
	if _, err := s.CreateSecret(ctx, user.ID, SecretInput{Name: "BIG_TOKEN", Value: big}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSecret(ctx, user.ID, SecretInput{Name: "TOO_BIG", Value: big + "a"}); err != ErrInvalid {
		t.Fatalf("cap: %v", err)
	}
	if _, err := s.CreateSecret(ctx, user.ID, SecretInput{Name: "EMPTY_VALUE", Value: ""}); err != ErrInvalid {
		t.Fatalf("empty: %v", err)
	}
	if _, err := s.CreateSecret(ctx, user.ID, SecretInput{Name: "LINE_VALUE", Value: "a\nb"}); err != ErrInvalid {
		t.Fatalf("newline: %v", err)
	}
	if _, err := s.SecretMeta(ctx, other.ID, ok.ID); err != ErrNotFound {
		t.Fatalf("other read: %v", err)
	}
}
