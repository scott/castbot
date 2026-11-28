package connect

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTelegramCatalog(t *testing.T) {
	entry, ok := Lookup(Telegram)
	if !ok || entry.SecretName != SecretNameTelegram || entry.APIHost != telegramHost || entry.Auth != AuthBearer || entry.ExtraLabel != "Chat id" || entry.TokenLabel != "Bot token" {
		t.Fatalf("%+v %v", entry, ok)
	}
	if !BlocksGuest(SecretNameTelegram) || !BlocksGuest("TELEGRAM_BOT_TOKEN") || !BlocksGuest("TELEGRAM_API_TOKEN") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("TELEGRAM_TOKEN"); !ok || id != Telegram {
		t.Fatal("alias")
	}
	writes := map[string]bool{}
	for _, tool := range Tools(Telegram) {
		writes[tool.Name] = tool.Write
		if tool.Run == nil || tool.Build != nil {
			t.Fatal(tool.Name)
		}
	}
	if writes["telegram_read"] || !writes["telegram_send"] || len(writes) != 2 {
		t.Fatal(writes)
	}
	yml, ok := ShimYAML(Telegram)
	if !ok || strings.Contains(yml, "TELEGRAM") || !strings.Contains(yml, "\n  - telegram\n") {
		t.Fatal(yml)
	}
}

func TestTelegramUsesTheSavedChat(t *testing.T) {
	const token = "100000001:AAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const chat = "-1001234567890"
	var mu sync.Mutex
	var hits int
	var paths []string
	var bodies []string
	var hosts []string
	srv := telegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits++
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, string(buf))
		hosts = append(hosts, r.Host)
		mu.Unlock()
		if r.Host != telegramHost || r.Header.Get("Authorization") != "" {
			t.Errorf("host %s auth %q", r.Host, r.Header.Get("Authorization"))
		}
		if strings.Contains(r.RequestURI, "%3A") || !strings.Contains(r.URL.Path, token) {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			_, _ = io.WriteString(w, `{"ok":true,"result":[`+
				`{"update_id":1,"message":{"message_id":10,"date":1700000000,"text":"hello","chat":{"id":-1001234567890},"secret":"drop-me"}},`+
				`{"update_id":2,"message":{"message_id":11,"date":1700000001,"text":"leak-other-chat","chat":{"id":999}}},`+
				`{"update_id":3,"channel_post":{"message_id":12,"date":1700000002,"text":"posted","chat":{"id":-1001234567890}}}`+
				`]}`)
		case strings.Contains(string(buf), `"text":"missing"`):
			_, _ = io.WriteString(w, `{"ok":false,"description":"no such chat `+token+`"}`)
		default:
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":15,"text":"Hello","chat":{"id":-1001234567890},"secret":"drop-me"}}`)
		}
	}, func(host, port string) {
		mu.Lock()
		hosts = append(hosts, host+":"+port)
		mu.Unlock()
	})
	_ = srv
	ctx, cancel := telegramCtx(token, chat)
	defer cancel()
	if _, err := telegramRead(telegramCtxNow(t, token, "https://evil.example"), nil); !errors.Is(err, ErrBad) || hits != 0 {
		t.Fatalf("bad chat %v hits %d", err, hits)
	}
	if _, err := telegramRead(telegramCtxNow(t, "100000001:bad/token-value-here", chat), nil); !errors.Is(err, ErrBad) || hits != 0 {
		t.Fatalf("bad token %v hits %d", err, hits)
	}
	if _, err := telegramRead(ctx, map[string]any{"limit": "nope", "chat_id": "999", "offset": "4"}); !errors.Is(err, ErrBad) || hits != 0 {
		t.Fatalf("bad limit %v hits %d", err, hits)
	}
	out, err := HandleRPC(ctx, Telegram, rpcCall("telegram_read", map[string]any{"limit": "80", "chat_id": "999", "offset": "4"}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rpcText(t, out)
	if strings.Contains(text, token) || strings.Contains(text, "leak-other-chat") || strings.Contains(text, "drop-me") || strings.Contains(text, "999") || !strings.Contains(text, "hello") || !strings.Contains(text, "posted") {
		t.Fatal(text)
	}
	mu.Lock()
	gotHits, gotPath, gotBody := hits, paths[0], bodies[0]
	mu.Unlock()
	if gotHits != 1 || gotPath != "/bot"+token+"/getUpdates" || !strings.Contains(gotBody, `"limit":20`) || strings.Contains(gotBody, "offset") || strings.Contains(gotBody, "999") || strings.Contains(gotBody, "chat_id") {
		t.Fatalf("hits %d path %s body %s", hits, gotPath, gotBody)
	}
	var readBody map[string]any
	if json.Unmarshal([]byte(gotBody), &readBody) != nil {
		t.Fatal(gotBody)
	}
	allowed, _ := readBody["allowed_updates"].([]any)
	if len(allowed) != 2 || allowed[0] != "message" || allowed[1] != "channel_post" || readBody["timeout"] != float64(0) {
		t.Fatal(gotBody)
	}
	out, err = HandleRPC(ctx, Telegram, rpcCall("telegram_send", map[string]any{"text": "Hello", "chat_id": "999", "parse_mode": "HTML"}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text = rpcText(t, out)
	if strings.Contains(text, token) || strings.Contains(text, "drop-me") || !strings.Contains(text, `"sent":true`) || !strings.Contains(text, `"message_id":15`) {
		t.Fatal(text)
	}
	mu.Lock()
	gotHits, gotPath, gotBody = hits, paths[1], bodies[1]
	mu.Unlock()
	if gotHits != 2 || gotPath != "/bot"+token+"/sendMessage" || strings.Contains(gotBody, "999") || strings.Contains(gotBody, "parse_mode") || strings.Contains(gotBody, "reply_markup") {
		t.Fatalf("hits %d path %s body %s", hits, gotPath, gotBody)
	}
	var sendBody map[string]any
	if json.Unmarshal([]byte(gotBody), &sendBody) != nil || sendBody["chat_id"] != chat || sendBody["text"] != "Hello" || len(sendBody) != 2 {
		t.Fatal(gotBody)
	}
	if _, err := telegramSend(ctx, map[string]any{"text": "bad\rline"}); !errors.Is(err, ErrBad) {
		t.Fatal(err)
	}
	mu.Lock()
	gotHits = hits
	mu.Unlock()
	if gotHits != 2 {
		t.Fatalf("cr hits %d", gotHits)
	}
	out, err = HandleRPC(ctx, Telegram, rpcCall("telegram_send", map[string]any{"text": "missing"}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text = rpcText(t, out)
	if strings.Contains(text, token) || !strings.Contains(text, "[redacted]") || strings.Contains(text, `"sent"`) {
		t.Fatal(text)
	}
	if _, err := telegramTCP(context.Background(), "evil.example", "443"); !errors.Is(err, ErrAllow) {
		t.Fatal(err)
	}
}

func TestTelegramWriteDoesNotReachUpstream(t *testing.T) {
	const token = "100000001:AAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	var hits int
	telegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}, nil)
	ctx, cancel := telegramCtx(token, "424242")
	defer cancel()
	_, err := HandleRPC(ctx, Telegram, rpcCall("telegram_send", map[string]any{"text": "Hello", "chat_id": "1"}), false, nil)
	if !errors.Is(err, ErrDenied) || hits != 0 {
		t.Fatalf("%v hits %d", err, hits)
	}
	out, err := HandleRPC(ctx, Telegram, rpcCall("telegram_delete", map[string]any{"text": "Hello"}), true, nil)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
}

func TestTelegramHidesTheKey(t *testing.T) {
	const owner = "100000001:AAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const other = "100000002:BBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	const chat = "424242"
	var mu sync.Mutex
	var path string
	var hits int
	telegramServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		path = r.URL.Path
		mu.Unlock()
		if strings.Contains(r.URL.Path, owner) || !strings.Contains(r.URL.Path, other) || r.Header.Get("Authorization") != "" {
			t.Errorf("path leaked")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":1,"message":{"message_id":3,"date":1,"text":"`+other+`","chat":{"id":424242},"secret":"`+owner+`"}}]}`)
	}, nil)
	ctx, cancel := telegramCtx(other, chat)
	defer cancel()
	out, err := HandleRPC(ctx, Telegram, rpcCall("telegram_read", map[string]any{}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := rpcText(t, out)
	if strings.Contains(text, other) || strings.Contains(text, owner) || !strings.Contains(text, "[redacted]") || strings.Contains(text, `"secret"`) {
		t.Fatal(text)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 || path != "/bot"+other+"/getUpdates" || strings.Contains(path, owner) {
		t.Fatalf("hits %d path %s", hits, path)
	}
}

func TestTelegramHostOutsideTheList(t *testing.T) {
	const token = "100000003:CCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	var hits int
	_, dial := connectServer(t, []string{telegramHost, "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	res, err := Do(context.Background(), token, Call{
		Connection: Telegram, Method: "GET", URL: "https://evil.example/bot" + token + "/getUpdates",
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), token) {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
	res, err = Do(context.Background(), token, Call{
		Connection: Telegram, Method: "POST", URL: "https://" + telegramHost + "/bot" + token + "/sendMessage",
		Body: []byte(`{"chat_id":"1","text":"hi"}`),
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), token) {
		t.Fatalf("url %+v %v hits %d", res, err, hits)
	}
}

func telegramServer(t *testing.T, h http.HandlerFunc, note func(host, port string)) *httptest.Server {
	t.Helper()
	cert, pool := testCert(t, telegramHost)
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	prevDial, prevRoots := telegramDial, telegramRoots
	telegramRoots = pool
	telegramDial = func(ctx context.Context, host, port string) (net.Conn, error) {
		if note != nil {
			note(host, port)
		}
		if host != telegramHost || port != "443" {
			return nil, ErrAllow
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ts.Listener.Addr().String())
	}
	t.Cleanup(func() {
		telegramDial = prevDial
		telegramRoots = prevRoots
		ts.Close()
	})
	return ts
}

func telegramCtx(token, chat string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	return WithVault(ctx, token, chat), cancel
}

func telegramCtxNow(t *testing.T, token, chat string) context.Context {
	t.Helper()
	ctx, cancel := telegramCtx(token, chat)
	t.Cleanup(cancel)
	return ctx
}
