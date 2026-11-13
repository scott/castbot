package modellist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestManifestFieldsDriveTheCatalog(t *testing.T) {
	ocean, ok := findEntry("digitalocean")
	if !ok || ocean.filter == nil || ocean.DefaultModel != "digitalocean/deepseek-v4.1-flash" {
		t.Fatalf("digitalocean %+v", ocean.DefaultModel)
	}
	if ocean.filter.MatchString("flux-image") || ocean.filter.MatchString("voice-tts") || !ocean.filter.MatchString("deepseek-3.2") {
		t.Fatal("digitalocean filter")
	}
	off := offlineList(ocean)
	if !off.Fallback || len(off.Models) != 2 || off.Models[0].ID != "digitalocean/deepseek-v4.1-flash" || off.Models[1].ID != "digitalocean/deepseek-3.2" {
		t.Fatalf("offline %+v", off.Models)
	}
	for _, model := range off.Models {
		if strings.Contains(model.ID, "llama3.3") || strings.Contains(model.ID, "deepseek-v3.2") {
			t.Fatal(model.ID)
		}
	}
	anth, ok := findEntry("anthropic-api")
	if !ok || anth.ModelsLifecycle != "active" || !anth.NormalizeV1Base || anth.ModelsHeaders["x-api-key"] != "${token}" || anth.ModelsHeaders["anthropic-version"] != "2023-06-01" {
		t.Fatalf("anthropic headers %+v", anth.ModelsHeaders)
	}
	gem, ok := findEntry("gemini")
	if !ok || gem.StripModelIdPrefix != "models/" || gem.filter == nil {
		t.Fatal("gemini strip")
	}
	router, ok := findEntry("openrouter")
	if !ok || router.AcceptsAnyModel || len(router.Prefixes) == 0 || router.Prefixes[0] != "openrouter/" || len(router.KnownModels) == 0 {
		t.Fatal("openrouter")
	}
	ollama, ok := findEntry("ollama")
	if !ok || !ollama.Keyless || !ollama.AcceptsAnyModel || ollama.ModelsPath != "/models" {
		t.Fatal("ollama")
	}
	bed, ok := findEntry("bedrock")
	if !ok || bed.Impl != "reserved" {
		t.Fatal("bedrock")
	}
	if _, err := New().List(context.Background(), "bedrock", "fixture-catalog-key"); err != ErrUnknown {
		t.Fatal(err)
	}
}

func TestFetchModels(t *testing.T) {
	const token = "fixture-catalog-key"
	tests := []struct {
		name     string
		provider string
		token    string
		timeout  time.Duration
		handle   func(t *testing.T, w http.ResponseWriter, r *http.Request, n int)
		want     []string
		fallback bool
		wantErr  error
		calls    int32
		cached   bool
	}{
		{
			name: "openrouter 403 falls back to the public list", provider: "openrouter", token: token,
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				assertNoToken(t, r, token)
				switch r.URL.Path {
				case "/models/user":
					if r.Header.Get("Authorization") != "Bearer "+token {
						t.Fatalf("user auth %q", r.Header.Get("Authorization"))
					}
					w.WriteHeader(http.StatusForbidden)
				case "/models":
					if r.Header.Get("Authorization") != "" {
						t.Fatal("public list received the key")
					}
					_, _ = io.WriteString(w, `{"data":[{"id":"acme/chat"}]}`)
				default:
					t.Fatalf("path %s", r.URL.Path)
				}
			},
			want: []string{"openrouter/acme/chat"}, calls: 2, cached: true,
		},
		{
			name: "openrouter timeout uses known models and is not cached", provider: "openrouter", token: token,
			timeout: 40 * time.Millisecond,
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				assertNoToken(t, r, token)
				<-r.Context().Done()
			},
			fallback: true, calls: 2,
		},
		{
			name: "empty list is not cached", provider: "openrouter", token: token,
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				assertNoToken(t, r, token)
				_, _ = io.WriteString(w, `{"data":[]}`)
			},
			fallback: true, calls: 2,
		},
		{
			name: "prefix and chat filter", provider: "digitalocean", token: token,
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				assertNoToken(t, r, token)
				if r.URL.Path != "/models" || r.URL.RawQuery != "" {
					t.Fatalf("url %s", r.URL.RequestURI())
				}
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatalf("auth %q", r.Header.Get("Authorization"))
				}
				_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-3.2"},{"id":"digitalocean/deepseek-v4.1-flash"},{"id":"flux-image"},{"id":"voice-tts"}]}`)
			},
			want:  []string{"digitalocean/deepseek-3.2", "digitalocean/deepseek-v4.1-flash"},
			calls: 1, cached: true,
		},
		{
			name: "openai drops non chat", provider: "openai-api", token: token,
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				assertNoToken(t, r, token)
				_, _ = io.WriteString(w, `{"data":[{"id":"gpt-4o"},{"id":"tts-1"},{"id":"text-embedding-3-small"},{"id":"gpt-4o-audio-preview"}]}`)
			},
			want: []string{"openai-api/gpt-4o"}, calls: 1, cached: true,
		},
		{
			name: "xai drops image and video", provider: "xai", token: token,
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				assertNoToken(t, r, token)
				_, _ = io.WriteString(w, `{"data":[{"id":"grok-4.7"},{"id":"grok-2-image"},{"id":"grok-2-video"}]}`)
			},
			want: []string{"xai/grok-4.7"}, calls: 1, cached: true,
		},
		{
			name: "gemini strips models prefix", provider: "gemini", token: token,
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				assertNoToken(t, r, token)
				_, _ = io.WriteString(w, `{"data":[{"id":"models/gemini-2.5-flash"},{"id":"models/gemini-embedding-001"},{"id":"models/gemini-2.5-tts"}]}`)
			},
			want: []string{"gemini/gemini-2.5-flash"}, calls: 1, cached: true,
		},
		{
			name: "anthropic sends key headers and keeps active rows", provider: "anthropic-api", token: token,
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				assertNoToken(t, r, token)
				if r.Header.Get("x-api-key") != token || r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Fatalf("headers %v", r.Header)
				}
				if r.Header.Get("Authorization") != "" {
					t.Fatal("bearer replaced the anthropic headers")
				}
				if !strings.Contains(r.URL.RawQuery, "limit=1000") || !strings.Contains(r.URL.RawQuery, "lifecycle=active") {
					t.Fatalf("query %s", r.URL.RawQuery)
				}
				_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-5-5","lifecycle":"active"},{"id":"claude-retired","lifecycle":"deprecated"},{"id":"claude-plain"}]}`)
			},
			want: []string{"anthropic-api/claude-sonnet-5-5", "anthropic-api/claude-plain"}, calls: 1, cached: true,
		},
		{
			name: "no key is not configured", provider: "openrouter", token: "",
			handle: func(t *testing.T, w http.ResponseWriter, r *http.Request, n int) {
				t.Fatal("fetched without a key")
			},
			wantErr: ErrNotConfigured, calls: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := n.Add(1)
				tt.handle(t, w, r, int(call))
			}))
			t.Cleanup(srv.Close)
			cat := New()
			cat.SetBase(tt.provider, srv.URL)
			if tt.timeout > 0 {
				cat.SetTimeout(tt.timeout)
			}
			ctx := context.Background()
			got, err := cat.List(ctx, tt.provider, tt.token)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("err %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			} else {
				assertIDs(t, got, tt.want, tt.fallback)
				if strings.Contains(jsonIDs(got), token) {
					t.Fatal("list contained the key")
				}
			}
			_, err = cat.List(ctx, tt.provider, tt.token)
			if tt.wantErr != nil && err != tt.wantErr {
				t.Fatal(err)
			}
			if n.Load() != tt.calls {
				t.Fatalf("upstream calls %d want %d", n.Load(), tt.calls)
			}
			if tt.cached && !cat.CachedID(tt.provider, tt.token, tt.want[0]) {
				t.Fatal("cache miss")
			}
		})
	}
}

func TestCacheIsolatesTokens(t *testing.T) {
	const a = "fixture-key-a"
	const b = "fixture-key-b"
	var mode atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertNoToken(t, r, a)
		assertNoToken(t, r, b)
		if mode.Load() == 1 {
			_, _ = io.WriteString(w, `{"data":[{"id":"replaced"}]}`)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer " + a:
			_, _ = io.WriteString(w, `{"data":[{"id":"only-a"}]}`)
		case "Bearer " + b:
			_, _ = io.WriteString(w, `{"data":[{"id":"only-b"}]}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	cat := New()
	cat.SetBase("digitalocean", srv.URL)
	ctx := context.Background()
	left, err := cat.List(ctx, "digitalocean", a)
	if err != nil || len(left.Models) != 1 || left.Models[0].ID != "digitalocean/only-a" {
		t.Fatalf("%+v %v", left.Models, err)
	}
	right, err := cat.List(ctx, "digitalocean", b)
	if err != nil || len(right.Models) != 1 || right.Models[0].ID != "digitalocean/only-b" {
		t.Fatalf("%+v %v", right.Models, err)
	}
	mode.Store(1)
	again, err := cat.List(ctx, "digitalocean", a)
	if err != nil || len(again.Models) != 1 || again.Models[0].ID != "digitalocean/only-a" {
		t.Fatalf("cache mixed keys %+v %v", again.Models, err)
	}
	sum := sha256.Sum256([]byte(a))
	if cat.CachedID("digitalocean", b, "digitalocean/only-a") || strings.Contains(hex.EncodeToString(sum[:]), "only-a") {
		t.Fatal("isolation")
	}
}

func TestSnapshotDoesNotDial(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		_, _ = io.WriteString(w, `{"data":[{"id":"acme/chat"}]}`)
	}))
	t.Cleanup(srv.Close)
	cat := New()
	cat.SetBase("openrouter", srv.URL)
	start := time.Now()
	list := cat.Snapshot("openrouter", "fixture-catalog-key")
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("snapshot waited on the network")
	}
	if n.Load() != 0 || !list.Fallback || len(list.Models) == 0 {
		t.Fatalf("calls %d models %d", n.Load(), len(list.Models))
	}
	live, err := cat.List(context.Background(), "openrouter", "fixture-catalog-key")
	if err != nil || live.Fallback || n.Load() != 1 {
		t.Fatalf("warm %+v calls %d %v", live.Fallback, n.Load(), err)
	}
	start = time.Now()
	snap := cat.Snapshot("openrouter", "fixture-catalog-key")
	if time.Since(start) > 200*time.Millisecond || snap.Fallback || snap.Models[0].ID != live.Models[0].ID {
		t.Fatal("warm snapshot missed the cache")
	}
}

func TestOllamaNeedsAReachableBase(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "")
	cat := New()
	if OllamaConfigured() {
		t.Fatal("keyless counted as configured")
	}
	if _, err := cat.List(context.Background(), "ollama", "fixture-catalog-key"); err != ErrNotConfigured {
		t.Fatal(err)
	}
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if r.URL.Path != "/models" {
			t.Fatalf("path %s", r.URL.Path)
		}
		if n.Load() == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"qwen3"},{"id":"bge-m3"}]}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_BASE_URL", srv.URL)
	if !OllamaConfigured() {
		t.Fatal("base URL was ignored")
	}
	if _, err := cat.List(context.Background(), "ollama", ""); err != ErrUnreachable || n.Load() != 1 {
		t.Fatalf("err calls %d", n.Load())
	}
	got, err := cat.List(context.Background(), "ollama", "")
	if err != nil || len(got.Models) != 1 || got.Models[0].ID != "ollama/qwen3" || got.Fallback {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := cat.List(context.Background(), "ollama", ""); err != nil || n.Load() != 2 {
		t.Fatalf("cache calls %d %v", n.Load(), err)
	}
}

func TestLifecycleQueryCanBeRetried(t *testing.T) {
	const token = "fixture-catalog-key"
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		assertNoToken(t, r, token)
		if strings.Contains(r.URL.RawQuery, "lifecycle=") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"claude-sonnet-5-5"}]}`)
	}))
	t.Cleanup(srv.Close)
	cat := New()
	cat.SetBase("anthropic-api", srv.URL)
	got, err := cat.List(context.Background(), "anthropic-api", token)
	if err != nil || len(got.Models) != 1 || got.Models[0].ID != "anthropic-api/claude-sonnet-5-5" || n.Load() != 2 {
		t.Fatalf("%+v calls %d %v", got.Models, n.Load(), err)
	}
}

func assertNoToken(t *testing.T, r *http.Request, token string) {
	t.Helper()
	if token != "" && strings.Contains(r.URL.String(), token) {
		t.Fatal("token was placed in the URL")
	}
}

func assertIDs(t *testing.T, got List, want []string, fallback bool) {
	t.Helper()
	if got.Fallback != fallback {
		t.Fatalf("fallback %v", got.Fallback)
	}
	if fallback {
		if len(got.Models) == 0 {
			t.Fatal("empty fallback")
		}
		return
	}
	if len(got.Models) != len(want) {
		t.Fatalf("ids %v", idsOf(got))
	}
	for i, id := range want {
		if got.Models[i].ID != id {
			t.Fatalf("id %s want %s", got.Models[i].ID, id)
		}
		if got.Models[i].Label == "" || strings.Contains(got.Models[i].Label, got.Models[i].ID) && strings.Count(got.Models[i].ID, "/") > strings.Count(got.Models[i].Label, "/") {
			t.Fatalf("label %q for %s", got.Models[i].Label, id)
		}
	}
}

func idsOf(list List) []string {
	var out []string
	for _, model := range list.Models {
		out = append(out, model.ID)
	}
	return out
}

func jsonIDs(list List) string {
	return strings.Join(idsOf(list), " ")
}
