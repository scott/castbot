package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

func TestBackupRestoresDatabaseAndBlobs(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "castbot.db")
	st := openBackupStore(t, dbPath)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.NewUser{
		Email: "ada@example.com", DisplayName: "Ada", Workspace: "Personal",
		PasswordHash: "hash", TOTPSecret: "totp-secret-not-real",
	}); err != nil {
		t.Fatal(err)
	}
	hello := writeBlob(t, filepath.Join(dir, "files"), []byte("hello"))
	secret := []byte("secret-marker-not-a-token")
	outside := filepath.Join(dir, "outside")
	if err := os.WriteFile(outside, secret, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "files", strings.Repeat("ab", 32))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", ".part-upload"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", strings.Repeat("cd", 32)), []byte("wrong-name"), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 15, 20, 5, 0, 0, time.UTC)
	got, err := Create(ctx, st, dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "2026-11-15_1305" || got.Files != 1 || got.Schema < 28 {
		t.Fatalf("result %+v", got)
	}
	info, err := os.Stat(got.Dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v %v", info, err)
	}
	raw, err := os.ReadFile(filepath.Join(got.Dir, "files", hello))
	if err != nil || string(raw) != "hello" {
		t.Fatalf("blob %q %v", raw, err)
	}
	tree, err := os.ReadFile(filepath.Join(got.Dir, "RECORD"))
	if err != nil || !strings.Contains(string(tree), "integrity ok") || !strings.Contains(string(tree), "files 1\n") {
		t.Fatalf("record %s %v", tree, err)
	}
	backed, err := os.ReadFile(filepath.Join(got.Dir, "castbot.db"))
	if err != nil || strings.Contains(string(backed), "secret-marker") {
		t.Fatal("backup kept a file it should have skipped")
	}
	walk := filepath.Join(got.Dir, "files")
	entries, err := os.ReadDir(walk)
	if err != nil || len(entries) != 1 {
		t.Fatalf("files dir %#v %v", entries, err)
	}

	if _, err := Create(ctx, st, dir, now); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second backup %v", err)
	}
	if _, err := st.CreateUser(ctx, store.NewUser{
		Email: "bea@example.com", DisplayName: "Bea", Workspace: "Other",
		PasswordHash: "hash", TOTPSecret: "totp-secret-not-real",
	}); err != nil {
		t.Fatal(err)
	}
	writeBlob(t, filepath.Join(dir, "files"), []byte("later"))
	if err := os.WriteFile(filepath.Join(dir, "files", hello), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Restore(dir, got.ID); err != nil {
		t.Fatal(err)
	}
	again := openBackupStore(t, dbPath)
	defer again.Close()
	if _, err := again.FindUserByEmail(ctx, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := again.FindUserByEmail(ctx, "bea@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("later user %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "files", hello))
	if err != nil || string(body) != "hello" {
		t.Fatalf("restored blob %q %v", body, err)
	}
	rows, err := List(dir)
	if err != nil || len(rows) != 1 || !rows[0].OK || rows[0].Files != 1 || rows[0].ID != got.ID {
		t.Fatalf("list %#v %v", rows, err)
	}

	db, err := os.ReadFile(filepath.Join(got.Dir, "castbot.db"))
	if err != nil {
		t.Fatal(err)
	}
	db[len(db)/2] ^= 0xff
	if err := os.WriteFile(filepath.Join(got.Dir, "castbot.db"), db, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(dir, got.ID); err == nil {
		t.Fatal("tampered backup restored")
	}
	if _, err := again.FindUserByEmail(ctx, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
}

func openBackupStore(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path, store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func writeBlob(t *testing.T, dir string, body []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	name := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}
