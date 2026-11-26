package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestSlackCatalog(t *testing.T) {
	entry, ok := Lookup(Slack)
	if !ok || entry.SecretName != SecretNameSlack || entry.APIHost != "slack.com" || entry.Auth != AuthBearer {
		t.Fatalf("%+v %v", entry, ok)
	}
	if !BlocksGuest(SecretNameSlack) || !BlocksGuest("SLACK_BOT_TOKEN") || !BlocksGuest("SLACK_API_TOKEN") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("SLACK_TOKEN"); !ok || id != Slack {
		t.Fatal("alias")
	}
	writes := map[string]bool{}
	for _, tool := range Tools(Slack) {
		writes[tool.Name] = tool.Write
		if tool.Build == nil {
			t.Fatal(tool.Name)
		}
	}
	if writes["slack_list_channels"] || writes["slack_read_channel"] || writes["slack_read_thread"] {
		t.Fatal("read marked write")
	}
	if !writes["slack_post"] || !writes["slack_reply"] || len(writes) != 5 {
		t.Fatal(writes)
	}
	for _, name := range []string{"chat.delete", "files.upload", "admin.users", "conversations.open"} {
		for _, tool := range Tools(Slack) {
			if strings.Contains(tool.Name, name) {
				t.Fatal(name)
			}
		}
	}
	yml, ok := ShimYAML(Slack)
	if !ok || strings.Contains(yml, "SLACK") || !strings.Contains(yml, "\n  - slack\n") {
		t.Fatal(yml)
	}
}

func TestSlackUsesFixedMethods(t *testing.T) {
	call, err := slackListCall(context.Background(), map[string]any{"limit": "500", "types": "im,mpim"})
	if err != nil {
		t.Fatal(err)
	}
	u := slackURL(t, call)
	if call.Method != "GET" || u.Path != "/api/conversations.list" || u.Query().Get("types") != "public_channel,private_channel" || u.Query().Get("limit") != "100" || u.Query().Get("exclude_archived") != "true" {
		t.Fatalf("%s %s", call.Method, call.URL)
	}
	if _, err := CheckURL(Slack, call.URL); err != nil {
		t.Fatal(err)
	}
	call, err = slackHistoryCall(context.Background(), map[string]any{"channel": "C012ABCDEF", "limit": "50", "user": "U12345678"})
	if err != nil {
		t.Fatal(err)
	}
	u = slackURL(t, call)
	if u.Path != "/api/conversations.history" || u.Query().Get("channel") != "C012ABCDEF" || u.Query().Get("limit") != "20" || strings.Contains(call.URL, "U12345678") {
		t.Fatal(call.URL)
	}
	call, err = slackThreadCall(context.Background(), map[string]any{"channel": "G012ABCDEF", "ts": "1700000000.000100"})
	if err != nil {
		t.Fatal(err)
	}
	u = slackURL(t, call)
	if u.Path != "/api/conversations.replies" || u.Query().Get("ts") != "1700000000.000100" {
		t.Fatal(call.URL)
	}
	call, err = slackPostCall(context.Background(), map[string]any{"channel": "C012ABCDEF", "text": "Hello", "user": "U12345678", "token": "xoxb-secret"})
	if err != nil {
		t.Fatal(err)
	}
	body := slackJSON(t, call)
	if call.URL != "https://slack.com/api/chat.postMessage" || len(body) != 2 || body["channel"] != "C012ABCDEF" || body["text"] != "Hello" {
		t.Fatal(body)
	}
	call, err = slackReplyCall(context.Background(), map[string]any{"channel": "C012ABCDEF", "text": "Reply", "ts": "1700000000.000100"})
	if err != nil {
		t.Fatal(err)
	}
	body = slackJSON(t, call)
	if len(body) != 3 || body["thread_ts"] != "1700000000.000100" || body["text"] != "Reply" {
		t.Fatal(body)
	}
	for _, channel := range []string{"U12345678", "D012ABCDEF", "W012ABCDEF", "@ada", "C012/delete"} {
		if _, err := slackPostCall(context.Background(), map[string]any{"channel": channel, "text": "Hi"}); err == nil {
			t.Fatal(channel)
		}
	}
	if _, err := slackReplyCall(context.Background(), map[string]any{"channel": "C012ABCDEF", "text": "Hi"}); err == nil {
		t.Fatal("missing thread")
	}
}

func TestSlackWriteDoesNotReachUpstream(t *testing.T) {
	var hits int
	up := func(context.Context, Call) (Result, error) {
		hits++
		return Result{Status: 200, Body: []byte(`{}`)}, nil
	}
	args := map[string]any{"channel": "C012ABCDEF", "text": "Hello", "ts": "1700000000.000100"}
	for _, name := range []string{"slack_post", "slack_reply"} {
		_, err := HandleRPC(context.Background(), Slack, rpcCall(name, args), false, up)
		if !errors.Is(err, ErrDenied) || hits != 0 {
			t.Fatalf("%s %v hits %d", name, err, hits)
		}
	}
	out, err := HandleRPC(context.Background(), Slack, rpcCall("slack_delete", args), true, up)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
	out, err = HandleRPC(context.Background(), Slack, rpcCall("slack_post", map[string]any{"channel": "U12345678", "text": "Hi"}), true, up)
	if err != nil || hits != 0 || !strings.Contains(string(out), "bad arguments") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
}

func TestSlackHidesTheKey(t *testing.T) {
	const owner = "xoxb-owner-token-0001"
	const other = "xoxb-other-token-0002"
	var mu sync.Mutex
	var auth string
	var hits int
	ts, dial := connectServer(t, []string{"slack.com"}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Method != "GET" || r.URL.Path != "/api/conversations.history" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"messages":[{"user":"U1","text":"` + other + `","ts":"1.2","files":[{"url_private":"` + owner + `"}]}]}`))
	})
	defer ts.Close()
	out, err := HandleRPC(context.Background(), Slack, rpcCall("slack_read_channel", map[string]any{"channel": "C012ABCDEF"}), true, func(ctx context.Context, call Call) (Result, error) {
		if strings.Contains(call.URL, other) || strings.Contains(call.URL, owner) || strings.Contains(string(call.Body), owner) {
			t.Fatal("key in request")
		}
		return Do(ctx, other, call, dial)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), other) || strings.Contains(string(out), owner) || !strings.Contains(string(out), "[redacted]") || strings.Contains(string(out), "url_private") {
		t.Fatal(string(out))
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 || auth != "Bearer "+other || strings.Contains(auth, owner) {
		t.Fatalf("hits %d auth %q", hits, auth)
	}
}

func TestSlackHostOutsideTheList(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{"slack.com", "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "xoxb-slack-key-0003", Call{
		Connection: Slack, Method: "GET", URL: "https://evil.example/api/conversations.list",
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), "xoxb-slack-key-0003") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}

func slackURL(t *testing.T, call Call) *url.URL {
	t.Helper()
	u, err := url.Parse(call.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func slackJSON(t *testing.T, call Call) map[string]any {
	t.Helper()
	var raw map[string]any
	if json.Unmarshal(call.Body, &raw) != nil {
		t.Fatal(string(call.Body))
	}
	return raw
}
