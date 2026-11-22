package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/castcfg/modellist"
	"github.com/scott/castbot/internal/store"
)

func TestWriteModelListUsesTheKey(t *testing.T) {
	const token = "fixture-model-list-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("auth")
		}
		if strings.Contains(r.URL.String(), token) {
			t.Fatal("key in url")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4.7"},{"id":"grok-2-image"}]}`))
	}))
	defer srv.Close()
	cat := modellist.New()
	cat.SetBase("xai", srv.URL)
	var buf bytes.Buffer
	if err := writeModelList(context.Background(), cat, "xai", token, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "xai/grok-4.7\tgrok-4.7\n" {
		t.Fatalf("list %q", buf.String())
	}
	if strings.Contains(buf.String(), token) {
		t.Fatal("key in list")
	}
	buf.Reset()
	if err := writeModelList(context.Background(), cat, "bedrock", token, &buf); err == nil {
		t.Fatal("reserved provider")
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	cat = modellist.New()
	cat.SetBase("xai", srv.URL)
	if err := writeModelList(context.Background(), cat, "xai", token, &buf); err == nil {
		t.Fatal("rejected key returned a list")
	}
	buf.Reset()
	if err := writeModelList(context.Background(), cat, "xai", "bad key", &buf); err == nil {
		t.Fatal("key with a space")
	}
}

func TestApplyProviderKeyIsScoped(t *testing.T) {
	secretKey := bytes.Repeat([]byte{7}, 32)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), store.Options{
		SecretsKey: secretKey,
		Argon:      auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	owner := modelUser(t, st, "owner@example.com")
	other := modelUser(t, st, "other@example.com")
	const token = "fixture-vault-model-key"
	if err := applyProviderKey(ctx, st, owner.Email, "xai", token); err != nil {
		t.Fatal(err)
	}
	if err := applyProviderKey(ctx, st, owner.Email, "ollama", token); err != store.ErrInvalid {
		t.Fatalf("ollama %v", err)
	}
	rows, err := st.ProviderSecrets(ctx, owner.ID, []string{"xai", "f"})
	if err != nil || len(rows) != 1 || rows[0].Value != token || rows[0].Provider != "xai" || rows[0].Name != "MODEL_XAI" {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if rows[0].Label != "xAI" || strings.Contains(rows[0].Label, token) {
		t.Fatalf("label %q", rows[0].Label)
	}
	otherRows, err := st.ProviderSecrets(ctx, other.ID, []string{"xai"})
	if err != nil || len(otherRows) != 0 {
		t.Fatalf("other %+v %v", otherRows, err)
	}
	if _, err := st.DecryptSecret(ctx, other.ID, rows[0].ID); err != store.ErrNotFound {
		t.Fatalf("other decrypt %v", err)
	}
	if err := applyProviderKey(ctx, st, owner.Email, "xai", "fixture-vault-model-next"); err != nil {
		t.Fatal(err)
	}
	rows, err = st.ProviderSecrets(ctx, owner.ID, []string{"xai"})
	if err != nil || len(rows) != 1 || rows[0].Value != "fixture-vault-model-next" {
		t.Fatalf("replaced %+v %v", rows, err)
	}
}

func modelUser(t *testing.T, st *store.Store, email string) store.User {
	t.Helper()
	hash, err := auth.Hash("right-password-1", st.Argon())
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(context.Background(), store.NewUser{
		Email: email, DisplayName: "Owner", Workspace: "Personal", PasswordHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return user
}
