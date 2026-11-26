package connect

import (
	"context"
	"testing"

	"github.com/scott/castbot/internal/egresscontract"
)

func TestCatalogOrderAndAliases(t *testing.T) {
	got := Catalog()
	if len(got) < 2 || got[0].ID != GitHub || got[1].ID != DigitalOcean {
		t.Fatalf("%v", got)
	}
	if got[0].APIHost != "api.github.com" || got[0].GitHost != "github.com" || got[0].Auth != AuthGitHub {
		t.Fatalf("github %+v", got[0])
	}
	if got[1].APIHost != "api.digitalocean.com" || got[1].GitHost != "" || got[1].RemoteMCP == "" || got[1].Auth != AuthBearer {
		t.Fatalf("digitalocean %+v", got[1])
	}
	if id, ok := ProviderEnv("GH_TOKEN"); !ok || id != GitHub {
		t.Fatalf("alias %s %v", id, ok)
	}
	if id, ok := ProviderEnv(SecretNameDigitalOcean); !ok || id != DigitalOcean {
		t.Fatalf("vault %s %v", id, ok)
	}
	if !VaultName(SecretNameGitHub) || VaultName("GITHUB_TOKEN") || VaultName("SAFE_TOKEN") {
		t.Fatal("vault names")
	}
	if !BlocksGuest("DIGITALOCEAN_ACCESS_TOKEN") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if _, err := APIURL("widget", "/v1", ""); err == nil {
		t.Fatal("unknown provider built a URL")
	}
	if _, err := GitURL(DigitalOcean, "/v2/account", ""); err == nil {
		t.Fatal("digitalocean git")
	}
	if !egresscontract.KnownProvider("linear") || !egresscontract.KnownProvider(" GitHub ") || egresscontract.KnownProvider("not-a-name") || egresscontract.KnownProvider("request_secret") || egresscontract.KnownProvider("agents") {
		t.Fatal("provider id shape")
	}
	if _, ok := ShimYAML("widget"); ok {
		t.Fatal("unknown id wrote a shim")
	}
	if yml, ok := ShimYAML(GitHub); !ok || yml == "" {
		t.Fatal("github shim")
	}
}

func TestVaultPacking(t *testing.T) {
	packed, ok := JoinVault("token-value", "chat-1")
	if !ok || packed != "token-value\tchat-1" {
		t.Fatalf("%q %v", packed, ok)
	}
	secret, extra, ok := SplitVault(packed)
	if !ok || secret != "token-value" || extra != "chat-1" || VaultSecret(packed) != "token-value" {
		t.Fatalf("%s %s %v", secret, extra, ok)
	}
	plain, ok := JoinVault("only-token", "")
	if !ok || plain != "only-token" {
		t.Fatal(plain)
	}
	if _, _, ok := SplitVault("a\tb\tc"); ok {
		t.Fatal("two tabs")
	}
	ctx := WithVault(context.Background(), "token-value", "chat-1")
	gotS, gotE := VaultFrom(ctx)
	if gotS != "token-value" || gotE != "chat-1" {
		t.Fatalf("%s %s", gotS, gotE)
	}
}

func TestRegisterRejectsABadEntry(t *testing.T) {
	before := len(Catalog())
	mustPanic(t, func() {
		register(Entry{ID: "Widget", SecretName: "WIDGET_CONNECTION", Hosts: []string{"api.example.com"}, APIHost: "api.example.com"})
	})
	mustPanic(t, func() {
		register(Entry{ID: "widget", SecretName: "WIDGET_CONNECTION", Hosts: []string{"api.example.com"}, APIHost: "evil.example"})
	})
	mustPanic(t, func() {
		register(Entry{ID: "widget", SecretName: "widget_connection", Hosts: []string{"api.example.com"}, APIHost: "api.example.com"})
	})
	if _, ok := Lookup("widget"); ok || len(Catalog()) != before {
		t.Fatal("bad entry registered")
	}
}

func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	fn()
}
