package connect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestNotionCatalog(t *testing.T) {
	entry, ok := Lookup(Notion)
	if !ok || entry.SecretName != SecretNameNotion || entry.APIHost != "api.notion.com" || entry.Auth != AuthBearer {
		t.Fatalf("%+v %v", entry, ok)
	}
	if !BlocksGuest(SecretNameNotion) || !BlocksGuest("NOTION_API_KEY") || !BlocksGuest("NOTION_INTEGRATION_TOKEN") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("NOTION_TOKEN"); !ok || id != Notion {
		t.Fatal("alias")
	}
	writes := map[string]bool{}
	for _, tool := range Tools(Notion) {
		writes[tool.Name] = tool.Write
		if tool.Build == nil {
			t.Fatal(tool.Name)
		}
	}
	if writes["notion_search"] || writes["notion_get_page"] || writes["notion_get_blocks"] {
		t.Fatal("read marked write")
	}
	if !writes["notion_create_page"] || !writes["notion_append"] || len(writes) != 5 {
		t.Fatal(writes)
	}
	yml, ok := ShimYAML(Notion)
	if !ok || strings.Contains(yml, "NOTION") || !strings.Contains(yml, "\n  - notion\n") {
		t.Fatal(yml)
	}
}

func TestNotionUsesFixedMethods(t *testing.T) {
	page := "9cfb482a-81e3-4154-b5b9-2c805e70a02d"
	call, err := notionSearchCall(context.Background(), map[string]any{"query": "notes", "limit": "80", "filter": "database"})
	if err != nil {
		t.Fatal(err)
	}
	body := notionJSON(t, call)
	if call.Method != "POST" || call.URL != "https://api.notion.com/v1/search" || call.Headers["Notion-Version"] != notionVersion || body["query"] != "notes" || body["page_size"] != float64(20) {
		t.Fatal(call.URL, body)
	}
	filter := body["filter"].(map[string]any)
	if filter["value"] != "page" || strings.Contains(string(call.Body), "database") {
		t.Fatal(string(call.Body))
	}
	if _, err := CheckURL(Notion, call.URL); err != nil {
		t.Fatal(err)
	}
	call, err = notionPageCall(context.Background(), map[string]any{"page": page})
	if err != nil || call.Method != "GET" || call.URL != "https://api.notion.com/v1/pages/"+page || call.Headers["Notion-Version"] != notionVersion {
		t.Fatal(call.URL, err)
	}
	call, err = notionCreateCall(context.Background(), map[string]any{"page": page, "title": "Notes", "database": page, "query": "{ delete }"})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(call.Body)
	if call.Method != "POST" || call.URL != "https://api.notion.com/v1/pages" || strings.Contains(raw, "database") || strings.Contains(raw, "delete") || !strings.Contains(raw, `"page_id":"`+page+`"`) || !strings.Contains(raw, "Notes") {
		t.Fatal(raw)
	}
	call, err = notionAppendCall(context.Background(), map[string]any{"block": page, "text": "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	raw = string(call.Body)
	if call.Method != "PATCH" || !strings.HasSuffix(call.URL, "/children") || !strings.Contains(raw, `"type":"paragraph"`) || strings.Contains(raw, "archived") || strings.Contains(raw, "delete") {
		t.Fatal(call.Method, call.URL, raw)
	}
	for _, id := range []string{"not-an-id", page + "/trash", "https://evil.example"} {
		if _, err := notionPageCall(context.Background(), map[string]any{"page": id}); err == nil {
			t.Fatal(id)
		}
	}
}

func TestNotionWriteDoesNotReachUpstream(t *testing.T) {
	var hits int
	up := func(context.Context, Call) (Result, error) {
		hits++
		return Result{Status: 200, Body: []byte(`{}`)}, nil
	}
	page := "9cfb482a-81e3-4154-b5b9-2c805e70a02d"
	args := map[string]any{"page": page, "block": page, "title": "Notes", "text": "Hello"}
	for _, name := range []string{"notion_create_page", "notion_append"} {
		_, err := HandleRPC(context.Background(), Notion, rpcCall(name, args), false, up)
		if !errors.Is(err, ErrDenied) || hits != 0 {
			t.Fatalf("%s %v hits %d", name, err, hits)
		}
	}
	out, err := HandleRPC(context.Background(), Notion, rpcCall("notion_delete", args), true, up)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
}

func TestNotionHidesTheKey(t *testing.T) {
	const owner = "ntn_owner_integration_key"
	const other = "ntn_other_integration_key"
	page := "9cfb482a81e34154b5b92c805e70a02d"
	var mu sync.Mutex
	var auth, version string
	var hits int
	ts, dial := connectServer(t, []string{"api.notion.com"}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		auth = r.Header.Get("Authorization")
		version = r.Header.Get("Notion-Version")
		mu.Unlock()
		if r.Method != "GET" || r.URL.Path != "/v1/pages/"+page {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + page + `","url":"https://www.notion.so/x","properties":{"title":{"type":"title","title":[{"plain_text":"` + other + `"}]}},"secret":"` + owner + `"}`))
	})
	defer ts.Close()
	out, err := HandleRPC(context.Background(), Notion, rpcCall("notion_get_page", map[string]any{"page": page}), true, func(ctx context.Context, call Call) (Result, error) {
		if strings.Contains(call.URL, other) || strings.Contains(call.URL, owner) || call.Headers["Notion-Version"] != notionVersion {
			t.Fatal(call.URL)
		}
		return Do(ctx, other, call, dial)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), other) || strings.Contains(string(out), owner) || !strings.Contains(string(out), "[redacted]") || strings.Contains(string(out), `"secret"`) {
		t.Fatal(string(out))
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 || auth != "Bearer "+other || version != notionVersion || strings.Contains(auth, owner) {
		t.Fatalf("hits %d auth %q version %q", hits, auth, version)
	}
}

func TestNotionHostOutsideTheList(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{"api.notion.com", "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "ntn_notion_key_0003", Call{
		Connection: Notion, Method: "GET", URL: "https://evil.example/v1/pages/abc",
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), "ntn_notion_key_0003") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}

func notionJSON(t *testing.T, call Call) map[string]any {
	t.Helper()
	var raw map[string]any
	if json.Unmarshal(call.Body, &raw) != nil {
		t.Fatal(string(call.Body))
	}
	return raw
}
