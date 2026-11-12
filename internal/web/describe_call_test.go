package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castcfg"
)

func clearDescribeKeys(t *testing.T) {
	t.Helper()
	for _, slot := range modelSlots() {
		if slot.Env != "" {
			t.Setenv(slot.Env, "")
		}
		if name := castcfg.ProviderKeyEnv(slot.Manifest); name != "" {
			t.Setenv(name, "")
		}
	}
	if serverProviderKeySet() {
		t.Fatal("server key still set")
	}
}

func TestDescribeDoesNotDial(t *testing.T) {
	clearDescribeKeys(t)
	prev := http.DefaultTransport
	http.DefaultTransport = roundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("describe dialed")
		return nil, errors.New("dialed")
	})
	t.Cleanup(func() { http.DefaultTransport = prev })
	if _, err := (providerDescribe{}).Describe(context.Background(), "Check example-org/docs for broken links every morning."); err == nil {
		t.Fatal("missing server key still described")
	}
}

func TestDescribeCallsProvider(t *testing.T) {
	token := strings.Join([]string{"sk", "demo", "EXAMPLE", "0000", "notlive"}, "-")
	model := defaultModelID()
	inf, ok := castcfg.InferenceFor(model)
	if !ok || inf.EnvVar == "" {
		t.Fatal("default model")
	}
	_, apiModel, ok := castcfg.ChatCompletions(model)
	if !ok || apiModel == "" {
		t.Fatal("chat endpoint")
	}
	key := "server-side-demo"
	reply := `{"choices":[{"message":{"content":"{\"name\":\"Scout\",\"title\":\"Link checker\",\"job\":\"Check docs\",\"instructions\":\"Ask before you push.\",\"schedule\":\"every morning\",\"unclear\":false}"}}]}`

	t.Run("key only in the auth header", func(t *testing.T) {
		clearDescribeKeys(t)
		t.Setenv(castcfg.ProviderKeyEnv(inf.Key), key)
		assertDescribeCall(t, token, apiModel, key, reply)
	})
	t.Run("manifest env is the key", func(t *testing.T) {
		clearDescribeKeys(t)
		t.Setenv(inf.EnvVar, key)
		assertDescribeCall(t, token, apiModel, key, reply)
	})
	t.Run("missing key does not call", func(t *testing.T) {
		clearDescribeKeys(t)
		hits := 0
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			hits++
		}))
		defer srv.Close()
		_, err := (providerDescribe{client: srv.Client(), base: srv.URL}).Describe(context.Background(), "Check docs. Token "+token)
		if err == nil || hits != 0 {
			t.Fatalf("err %v hits %d", err, hits)
		}
	})
	t.Run("http error", func(t *testing.T) {
		clearDescribeKeys(t)
		t.Setenv(castcfg.ProviderKeyEnv(inf.Key), key)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, token, http.StatusBadGateway)
		}))
		defer srv.Close()
		_, err := (providerDescribe{client: srv.Client(), base: srv.URL}).Describe(context.Background(), "Check docs")
		if !errors.Is(err, errDescribeDown) || strings.Contains(err.Error(), token) {
			t.Fatal(err)
		}
	})
	t.Run("bad json asks again", func(t *testing.T) {
		clearDescribeKeys(t)
		t.Setenv(castcfg.ProviderKeyEnv(inf.Key), key)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"choices":[{"message":{"content":"not json"}}]}`)
		}))
		defer srv.Close()
		prop, err := (providerDescribe{client: srv.Client(), base: srv.URL}).Describe(context.Background(), "Check docs")
		if err != nil || !prop.Unclear {
			t.Fatalf("%+v %v", prop, err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		clearDescribeKeys(t)
		t.Setenv(castcfg.ProviderKeyEnv(inf.Key), key)
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
			w.WriteHeader(http.StatusNoContent)
		}))
		defer func() {
			close(release)
			srv.Close()
		}()
		client := &http.Client{Transport: srv.Client().Transport, Timeout: 40 * time.Millisecond}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		_, err := (providerDescribe{client: client, base: srv.URL}).Describe(ctx, "Check docs. Token "+token)
		if !errors.Is(err, errDescribeDown) || strings.Contains(err.Error(), token) {
			t.Fatal(err)
		}
	})
}

func assertDescribeCall(t *testing.T, token, apiModel, key, reply string) {
	t.Helper()
	var got struct {
		method string
		path   string
		header http.Header
		raw    []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.header = r.Header.Clone()
		got.raw, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}))
	defer srv.Close()
	prop, err := (providerDescribe{client: srv.Client(), base: srv.URL}).Describe(context.Background(), "Check docs. Token "+token)
	if err != nil {
		t.Fatal(err)
	}
	if prop.Name != "Scout" || prop.Job != "Check docs" || prop.Instructions != "Ask before you push." || prop.Title != "Link checker" {
		t.Fatalf("%+v", prop)
	}
	if got.method != http.MethodPost || got.path != "/chat/completions" {
		t.Fatalf("%s %s", got.method, got.path)
	}
	if got.header.Get("Authorization") != "Bearer "+key {
		t.Fatal("auth header")
	}
	if len(got.header.Values("Authorization")) != 1 {
		t.Fatal("auth repeated")
	}
	for name, vals := range got.header {
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		for _, val := range vals {
			if strings.Contains(val, key) || strings.Contains(val, token) {
				t.Fatalf("header %s leaked", name)
			}
		}
	}
	body := string(got.raw)
	if strings.Contains(body, key) || strings.Contains(body, token) {
		t.Fatal("body leaked")
	}
	if !strings.Contains(body, "********") {
		t.Fatal("token was not stripped")
	}
	var parsed struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got.raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Model != apiModel {
		t.Fatalf("model %q", parsed.Model)
	}
	var sawUser bool
	for _, msg := range parsed.Messages {
		if msg.Role == "user" {
			sawUser = true
			if strings.Contains(msg.Content, token) || strings.Contains(msg.Content, key) {
				t.Fatal("user message leaked")
			}
		}
	}
	if !sawUser {
		t.Fatal("user message missing")
	}
}

func TestGreetWaitingWording(t *testing.T) {
	got := greetCopy("Alex", "Scout", "Check links.", "", []string{"Please start with docs/guides."})
	if !strings.Contains(got, "You asked me to start with docs/guides, so I am starting there now.") || strings.Contains(got, "Please start with") {
		t.Fatal(got)
	}
	plain := greetCopy("Alex", "Scout", "Check links.", "", []string{"Start with the pricing page."})
	if !strings.Contains(plain, "pricing page") || strings.Contains(strings.ToLower(plain), "start with start with") {
		t.Fatal(plain)
	}
}

func TestBareCreateRefused(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("bare@example.com", "Alex Rivera", "correct-horse-1")
	ip := "203.0.113.230:9"
	h.login("bare@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodPost, "/new-agent", h.form(url.Values{"name": {"Echo"}, "title": {"Helper"}}), ip, nil)
	if code != http.StatusBadRequest || !strings.Contains(body, "setup card") {
		t.Fatalf("bare %d %s", code, body)
	}
	if n := len(agentRows(t, h, user.ID)); n != 0 {
		t.Fatalf("agents %d", n)
	}
}
