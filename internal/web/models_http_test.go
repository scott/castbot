package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

func TestProviderModelsAreScopedToTheCaller(t *testing.T) {
	const keyA = "fixture-key-a"
	const keyB = "fixture-key-b"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.String(), keyA) || strings.Contains(r.URL.String(), keyB) {
			t.Fatal("key in the upstream URL")
		}
		switch r.Header.Get("Authorization") {
		case "Bearer " + keyA:
			_, _ = io.WriteString(w, `{"data":[{"id":"only-a"}]}`)
		case "Bearer " + keyB:
			_, _ = io.WriteString(w, `{"data":[{"id":"only-b"}]}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(upstream.Close)

	h := newHarness(t)
	h.srv.ModelCatalog().SetBase("openrouter", upstream.URL)
	ip := "203.0.113.51:9"
	code, _, hdr := h.do(http.MethodGet, "/models/openrouter", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" || calls.Load() != 0 {
		t.Fatalf("anon %d %s calls %d", code, hdr.Get("Location"), calls.Load())
	}

	userC, secretC := h.account("none@example.com", "Casey", "correct-horse-1")
	h.login("none@example.com", "correct-horse-1", secretC, ip, false)
	_ = userC
	code, body, _ := h.do(http.MethodGet, "/models/openrouter", "", ip, nil)
	if code != http.StatusNotFound || strings.Contains(body, keyA) || !strings.Contains(body, "not configured") || calls.Load() != 0 {
		t.Fatalf("no key %d %s", code, body)
	}

	userA, secretA := h.account("a@example.com", "Ada", "correct-horse-1")
	userB, secretB := h.account("b@example.com", "Bea", "correct-horse-1")
	addProviderKey(t, h, userA.ID, "OPENROUTER_A", keyA)
	addProviderKey(t, h, userB.ID, "OPENROUTER_B", keyB)

	h.login("a@example.com", "correct-horse-1", secretA, ip, false)
	code, body, _ = h.do(http.MethodGet, "/models/openrouter", "", ip, nil)
	assertModelJSON(t, code, body, "openrouter/only-a", keyA, keyB)
	if strings.Contains(body, "only-b") {
		t.Fatal("caller A saw B")
	}

	h.login("b@example.com", "correct-horse-1", secretB, ip, false)
	code, body, _ = h.do(http.MethodGet, "/models/openrouter?user="+userA.ID, "", ip, nil)
	assertModelJSON(t, code, body, "openrouter/only-b", keyA, keyB)
	if strings.Contains(body, "only-a") {
		t.Fatal("caller B saw A")
	}
	if calls.Load() != 2 {
		t.Fatalf("calls %d", calls.Load())
	}
}

func TestServerKeyListsModels(t *testing.T) {
	const key = "fixture-server-key"
	t.Setenv(castcfg.ProviderKeyEnv("digitalocean"), key)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key || strings.Contains(r.URL.String(), key) {
			t.Fatal("key was not kept in the header")
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-v4.1-flash"},{"id":"flux-image"}]}`)
	}))
	t.Cleanup(upstream.Close)
	h := newHarness(t)
	h.srv.ModelCatalog().SetBase("digitalocean", upstream.URL)
	_, secret := h.account("do@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.52:9"
	h.login("do@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodGet, "/models/digitalocean", "", ip, nil)
	assertModelJSON(t, code, body, "digitalocean/deepseek-v4.1-flash", key)
	if strings.Contains(body, "flux-image") {
		t.Fatal("image model was listed")
	}
}

func TestHTMLPagesDoNotFetchModels(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	h.srv.ModelCatalog().SetClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(2 * time.Second)
		return nil, errors.New("pages must not dial")
	})})
	user, secret := h.account("page@example.com", "Ada", "correct-horse-1")
	ip := "203.0.113.53:9"
	h.login("page@example.com", "correct-horse-1", secret, ip, false)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Scout", Title: "Helper", Model: "openrouter/minimax/minimax-m2.5", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for _, path := range []string{"/new-agent", "/new-agent?advanced=1", "/agents/" + agent.ID + "/edit"} {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusOK {
			t.Fatalf("%s %d", path, code)
		}
		if strings.Contains(body, "fixture-server-key") {
			t.Fatal("page contained a key")
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("html page waited on a model fetch")
	}
	if calls.Load() != 0 {
		t.Fatalf("page dialed %d", calls.Load())
	}
}

func addProviderKey(t *testing.T, h *harness, userID, name, value string) {
	t.Helper()
	if _, err := h.st.CreateSecret(context.Background(), userID, store.SecretInput{
		Name: name, Label: "main", Provider: "a", Value: value,
	}); err != nil {
		t.Fatal(err)
	}
}

func assertModelJSON(t *testing.T, code int, body, id string, secrets ...string) {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var payload struct {
		Models []map[string]string `json:"models"`
		Error  string              `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error != "" || len(payload.Models) != 1 || payload.Models[0]["id"] != id || payload.Models[0]["label"] == "" {
		t.Fatalf("%s", body)
	}
	if len(payload.Models[0]) != 2 {
		t.Fatalf("extra fields %v", payload.Models[0])
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(body, secret) {
			t.Fatal("response contained a key")
		}
		sum := sha256.Sum256([]byte(secret))
		if strings.Contains(body, hex.EncodeToString(sum[:])) {
			t.Fatal("response contained a token hash")
		}
	}
	if strings.Contains(body, `"data"`) || strings.Contains(body, "Bearer") {
		t.Fatal("response contained an upstream body")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
