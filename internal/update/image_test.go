package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImagePolicy(t *testing.T) {
	release := "0123456789abcdef0123456789abcdef01234567"
	imageCommit := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	official := RuntimeImagePrefix + imageCommit
	same := RuntimeImagePrefix + release
	calls := 0
	visible := func(context.Context, string) (bool, error) {
		calls++
		return true, nil
	}
	hidden := func(context.Context, string) (bool, error) {
		return false, nil
	}
	boom := func(context.Context, string) (bool, error) {
		return false, errLookup
	}
	cases := []struct {
		name      string
		installed string
		visible   func(context.Context, string) (bool, error)
		move      bool
		block     bool
		next      string
		wantErr   bool
	}{
		{name: "unset", installed: ""},
		{name: "official matches", installed: official},
		{name: "official moves", installed: same, visible: visible, move: true, next: official},
		{name: "official hidden", installed: same, visible: hidden, block: true},
		{name: "official lookup error", installed: same, visible: boom, wantErr: true},
		{name: "other registry matches release", installed: "registry.example/castbot:" + release},
		{name: "other registry matches image", installed: "registry.example/castbot:" + imageCommit},
		{name: "other registry differs", installed: "registry.example/castbot:" + "ffffffffffffffffffffffffffffffffffffffff", block: true},
		{name: "short sha", installed: "registry.example/castbot:abc123", block: true},
		{name: "official short", installed: RuntimeImagePrefix + "abc123", block: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := DecideImage(context.Background(), tc.installed, release, official, imageCommit, tc.visible)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.Move != tc.move || plan.Block != tc.block || plan.Next != tc.next {
				t.Fatalf("plan %+v", plan)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("visible calls %d", calls)
	}
}

var errLookup = errString("lookup failed")

type errString string

func (e errString) Error() string { return string(e) }

func TestGHCRVisible(t *testing.T) {
	tag := "0123456789abcdef0123456789abcdef01234567"
	image := RuntimeImagePrefix + tag
	var tokenCode, manifestCode int
	tokenCode = http.StatusOK
	manifestCode = http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != UserAgent {
			t.Errorf("user agent %s", r.UserAgent())
		}
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("scope") != "repository:scott/castbot-runtime:pull" {
				t.Errorf("scope %s", r.URL.RawQuery)
			}
			w.WriteHeader(tokenCode)
			if tokenCode == http.StatusOK {
				w.Write([]byte(`{"token":"registry-token"}`))
			}
		case "/v2/scott/castbot-runtime/manifests/" + tag:
			if r.Header.Get("Authorization") != "Bearer registry-token" {
				t.Error("missing registry token")
			}
			w.WriteHeader(manifestCode)
			w.Write([]byte(`{}`))
		default:
			t.Errorf("path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Checker{Client: srv.Client(), RegistryBase: srv.URL}
	ok, err := c.ghcrVisible(context.Background(), image)
	if err != nil || !ok {
		t.Fatalf("visible %v %v", ok, err)
	}
	manifestCode = http.StatusNotFound
	ok, err = c.ghcrVisible(context.Background(), image)
	if err != nil || ok {
		t.Fatalf("missing %v %v", ok, err)
	}
	tokenCode = http.StatusUnauthorized
	ok, err = c.ghcrVisible(context.Background(), image)
	if err != nil || ok {
		t.Fatalf("denied %v %v", ok, err)
	}
	tokenCode = http.StatusBadGateway
	if _, err := c.ghcrVisible(context.Background(), image); err == nil {
		t.Fatal("expected lookup error")
	}
}
