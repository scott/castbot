package connect

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestWebCatalog(t *testing.T) {
	entry, ok := Lookup(Web)
	if !ok || entry.SecretName != SecretNameWeb || entry.APIHost != "api.search.brave.com" || entry.Auth != AuthHeader || entry.HeaderName != "X-Subscription-Token" {
		t.Fatalf("%+v %v", entry, ok)
	}
	if entry.GitHost != "" || entry.RemoteMCP != "" || entry.ExtraLabel != "" {
		t.Fatal("web shape")
	}
	if !BlocksGuest(SecretNameWeb) || !BlocksGuest("BRAVE_API_KEY") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("BRAVE_SEARCH_API_KEY"); !ok || id != Web {
		t.Fatal("alias")
	}
	names := map[string]Tool{}
	for _, tool := range Tools(Web) {
		names[tool.Name] = tool
		if tool.Write || tool.Method != "GET" {
			t.Fatal(tool.Name)
		}
	}
	if len(names) != 2 || !names["web_search"].Args[0].Required || names["web_read"].PublicURL != true {
		t.Fatal("tools")
	}
	var urlArg bool
	for _, tool := range Tools(Web) {
		for _, arg := range tool.Args {
			if strings.EqualFold(arg.Name, "url") {
				if tool.Name != "web_read" {
					t.Fatal("url arg on " + tool.Name)
				}
				urlArg = true
			}
		}
	}
	if !urlArg {
		t.Fatal("read url")
	}
	yml, ok := ShimYAML(Web)
	if !ok || strings.Contains(yml, "BRAVE") || !strings.Contains(yml, "\n  - web\n") {
		t.Fatal(yml)
	}
	got := Catalog()
	if len(got) < 3 || got[0].ID != GitHub || got[1].ID != DigitalOcean {
		t.Fatal("order")
	}
	var saw bool
	for _, entry := range got {
		if entry.ID == Web {
			saw = true
		}
	}
	if !saw {
		t.Fatal("missing web")
	}
}

func TestWebSearchStaysOnTheHostAndCapsCount(t *testing.T) {
	call, err := webSearchCall(context.Background(), map[string]any{"query": "cast bot", "count": "50", "token": "ignored-key"})
	if err != nil || call.Method != "GET" || call.PublicURL {
		t.Fatal(err)
	}
	u, err := CheckURL(Web, call.URL)
	if err != nil || u.Hostname() != "api.search.brave.com" || u.Path != "/res/v1/web/search" || u.Query().Get("count") != "10" || u.Query().Get("q") != "cast bot" {
		t.Fatalf("%v %v", u, err)
	}
	if strings.Contains(call.URL, "ignored-key") {
		t.Fatal("arg token")
	}
	if _, err := webSearchCall(context.Background(), map[string]any{"query": ""}); err == nil {
		t.Fatal("empty query")
	}
	if _, err := webSearchCall(context.Background(), map[string]any{"query": "x", "count": "0"}); err == nil {
		t.Fatal("zero count")
	}
	if _, err := APIURL(Web, "/other", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := APIURL(Web, "https://evil.example/", ""); err == nil {
		t.Fatal("guest host")
	}
}

func TestWebSearchHidesTheKeyAndIgnoresAnother(t *testing.T) {
	const owner = "owner-web-key-0001"
	const other = "other-web-key-0002"
	var mu sync.Mutex
	var sawHeader, sawAuth, sawQuery string
	var hits int
	ts, dial := connectServer(t, []string{"api.search.brave.com", "example.com"}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		sawHeader = r.Header.Get("X-Subscription-Token")
		sawAuth = r.Header.Get("Authorization")
		sawQuery = r.URL.RawQuery
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"One","url":"https://example.com/a","description":"snip ` + other + `"}]}}`))
	})
	defer ts.Close()
	msg := rpcCall("web_search", map[string]any{"query": "castbot", "count": 2})
	out, err := HandleRPC(context.Background(), Web, msg, false, func(ctx context.Context, call Call) (Result, error) {
		if call.Method != "GET" || call.PublicURL {
			t.Fatal("search call")
		}
		return Do(ctx, other, call, dial)
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, other) || strings.Contains(text, owner) || !strings.Contains(text, "[redacted]") || !strings.Contains(text, "https://example.com/a") {
		t.Fatal(text)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 || sawHeader != other || strings.Contains(sawHeader, owner) || sawAuth != "" || strings.Contains(sawQuery, other) {
		t.Fatalf("hits %d header %q auth %q query %q", hits, sawHeader, sawAuth, sawQuery)
	}
}

func TestWebReadSendsNoKey(t *testing.T) {
	const key = "page-web-key-0003"
	var mu sync.Mutex
	var header, auth, cookie string
	var paths []string
	ts, dial := connectServer(t, []string{"api.search.brave.com", "example.com"}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		header = r.Header.Get("X-Subscription-Token")
		auth = r.Header.Get("Authorization")
		cookie = r.Header.Get("Cookie")
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "https://example.com/done", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><script>var leak="` + key + `";</script><style>.x{}</style></head><body><h1>Hello</h1><p>page text</p></body></html>`))
	})
	defer ts.Close()
	msg := rpcCall("web_read", map[string]any{"url": "https://example.com/start"})
	out, err := HandleRPC(WithVault(context.Background(), key, ""), Web, msg, false, func(ctx context.Context, call Call) (Result, error) {
		if !call.PublicURL || call.Method != "GET" {
			t.Fatal("read call")
		}
		return Do(ctx, key, call, dial)
	})
	if err != nil {
		t.Fatal(err)
	}
	text := rpcText(t, out)
	if strings.Contains(text, key) || strings.Contains(text, "<script") || strings.Contains(text, "leak") || !strings.Contains(text, "Hello") || !strings.Contains(text, "page text") {
		t.Fatal(text)
	}
	mu.Lock()
	defer mu.Unlock()
	if header != "" || auth != "" || cookie != "" || len(paths) != 2 || paths[1] != "/done" {
		t.Fatalf("header %q auth %q cookie %q paths %v", header, auth, cookie, paths)
	}
}

func TestWebReadRefusesUnsafeURLs(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{"example.com"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	bad := []string{
		"http://example.com/a",
		"https://user:pw@example.com/a",
		"https://203.0.113.10/a",
		"https://example.com:8443/a",
		"https://127.0.0.1/",
		"file:///etc/passwd",
		"",
	}
	for _, raw := range bad {
		hits = 0
		msg := rpcCall("web_read", map[string]any{"url": raw})
		out, err := HandleRPC(context.Background(), Web, msg, true, func(ctx context.Context, call Call) (Result, error) {
			hits++
			return Do(ctx, "web-key-00000004", call, dial)
		})
		if err != nil || hits != 0 || !strings.Contains(string(out), "bad arguments") {
			t.Fatalf("%s err %v hits %d %s", raw, err, hits, out)
		}
	}
}

func TestWebReadRefusesABadRedirect(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{"example.com"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "https://203.0.113.10/secret", http.StatusFound)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "web-key-00000005", Call{
		Connection: Web, Method: "GET", URL: "https://example.com/start", PublicURL: true,
	}, dial)
	if err == nil || res.Outcome != OutcomeRedirect || hits != 1 || strings.Contains(string(res.Body), "web-key-00000005") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}

func TestWebUnknownToolAndNoWrite(t *testing.T) {
	var hits int
	up := func(ctx context.Context, call Call) (Result, error) {
		hits++
		if call.Method != "GET" {
			t.Fatal(call.Method)
		}
		return Result{Status: 200, Body: []byte(`{}`)}, nil
	}
	out, err := HandleRPC(context.Background(), Web, rpcCall("web_post", map[string]any{"query": "x"}), false, up)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
	for _, tool := range Tools(Web) {
		if !tool.Write {
			continue
		}
		_, err := HandleRPC(context.Background(), Web, rpcCall(tool.Name, map[string]any{"query": "x", "url": "https://example.com/"}), false, up)
		if !errors.Is(err, ErrDenied) || hits != 0 {
			t.Fatalf("%s %v hits %d", tool.Name, err, hits)
		}
	}
	if len(Tools(Web)) != 2 {
		t.Fatal("tool count")
	}
}

func TestWebHostOutsideTheList(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{"api.search.brave.com", "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "web-key-00000006", Call{
		Connection: Web, Method: "GET", URL: "https://evil.example/res/v1/web/search?q=x",
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), "web-key-00000006") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
	if _, err := CheckURL(Web, "https://evil.example/"); err == nil {
		t.Fatal("allowlist")
	}
}

func TestWebPageTextDropsMarkupAndCaps(t *testing.T) {
	got := string(webReadBody([]byte(`<html><script>token</script><p>Hello&amp;co</p></html>`)))
	if !strings.Contains(got, "Hello&co") || strings.Contains(got, "token") || strings.Contains(got, "<") {
		t.Fatal(got)
	}
	huge := "<p>" + strings.Repeat("a", webReadCap+5000) + "</p>"
	text := pageText([]byte(huge))
	if len(text) > webReadCap || len(text) < webReadCap-4 {
		t.Fatalf("len %d", len(text))
	}
	raw := webSearchBody([]byte(`{"web":{"results":[{"title":"T","url":"https://example.com","description":"S"}]}}`))
	var parsed struct {
		Results []webHit `json:"results"`
	}
	if json.Unmarshal(raw, &parsed) != nil || len(parsed.Results) != 1 || parsed.Results[0].Snippet != "S" {
		t.Fatal(string(raw))
	}
}

func rpcCall(name string, args map[string]any) RPC {
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	return RPC{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: params}
}

func rpcText(t *testing.T, out []byte) string {
	t.Helper()
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &env); err != nil || len(env.Result.Content) == 0 {
		t.Fatal(string(out))
	}
	return env.Result.Content[0].Text
}

func connectServer(t *testing.T, hosts []string, h http.HandlerFunc) (*httptest.Server, *Dial) {
	t.Helper()
	cert, pool := testCert(t, hosts...)
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	dial := &Dial{
		Roots: pool,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ts.Listener.Addr().String())
		},
	}
	return ts, dial
}
