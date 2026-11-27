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

func TestTodoistCatalog(t *testing.T) {
	entry, ok := Lookup(Todoist)
	if !ok || entry.SecretName != SecretNameTodoist || entry.APIHost != todoistHost || entry.Auth != AuthBearer || entry.ExtraLabel != "" {
		t.Fatalf("%+v %v", entry, ok)
	}
	if !BlocksGuest(SecretNameTodoist) || !BlocksGuest("TODOIST_API_TOKEN") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("TODOIST_TOKEN"); !ok || id != Todoist {
		t.Fatal("alias")
	}
	writes := map[string]bool{}
	for _, tool := range Tools(Todoist) {
		writes[tool.Name] = tool.Write
		if tool.Build == nil {
			t.Fatal(tool.Name)
		}
	}
	if writes["todoist_list_tasks"] || writes["todoist_get_task"] || !writes["todoist_create_task"] || !writes["todoist_complete"] || !writes["todoist_comment"] || len(writes) != 5 {
		t.Fatal(writes)
	}
	yml, ok := ShimYAML(Todoist)
	if !ok || strings.Contains(yml, "TODOIST") || !strings.Contains(yml, "\n  - todoist\n") {
		t.Fatal(yml)
	}
}

func TestTodoistUsesFixedMethods(t *testing.T) {
	call, err := todoistListCall(context.Background(), map[string]any{"filter": "today | shared", "project_id": "1"})
	if err != nil || call.Method != "GET" || call.URL != todoistREST+"/tasks" || strings.Contains(call.URL, "filter") || strings.Contains(call.URL, "shared") || strings.Contains(call.URL, "project") {
		t.Fatal(call.URL, err)
	}
	if _, err := CheckURL(Todoist, call.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(call.URL, "/rest/v2/") {
		t.Fatal(call.URL)
	}
	call, err = todoistGetCall(context.Background(), map[string]any{"task": "1234567890"})
	if err != nil || call.URL != todoistREST+"/tasks/1234567890" {
		t.Fatal(call.URL, err)
	}
	call, err = todoistCreateCall(context.Background(), map[string]any{"content": "Buy milk", "project_id": "99", "query": "{ delete }"})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(call.Body)
	if call.Method != "POST" || call.URL != todoistREST+"/tasks" || raw != `{"content":"Buy milk"}` || strings.Contains(raw, "project") || strings.Contains(raw, "delete") {
		t.Fatal(call.URL, raw)
	}
	call, err = todoistCompleteCall(context.Background(), map[string]any{"task": "1234567890", "project_id": "99"})
	if err != nil || call.Method != "POST" || call.URL != todoistREST+"/tasks/1234567890/close" || len(call.Body) != 0 {
		t.Fatal(call.Method, call.URL, err)
	}
	call, err = todoistCommentCall(context.Background(), map[string]any{"task": "1234567890", "content": "Noted", "project_id": "99"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if json.Unmarshal(call.Body, &body) != nil || call.URL != todoistREST+"/comments" || body["task_id"] != "1234567890" || body["content"] != "Noted" || body["project_id"] != nil || len(body) != 2 {
		t.Fatal(call.URL, string(call.Body))
	}
	for _, id := range []string{"0", "12/close", "https://evil.example"} {
		if _, err := todoistGetCall(context.Background(), map[string]any{"task": id}); err == nil {
			t.Fatal(id)
		}
	}
}

func TestTodoistWriteDoesNotReachUpstream(t *testing.T) {
	var hits int
	up := func(context.Context, Call) (Result, error) {
		hits++
		return Result{Status: 200, Body: []byte(`{}`)}, nil
	}
	args := map[string]any{"task": "1234567890", "content": "Buy milk"}
	for _, name := range []string{"todoist_create_task", "todoist_complete", "todoist_comment"} {
		_, err := HandleRPC(context.Background(), Todoist, rpcCall(name, args), false, up)
		if !errors.Is(err, ErrDenied) || hits != 0 {
			t.Fatalf("%s %v hits %d", name, err, hits)
		}
	}
	out, err := HandleRPC(context.Background(), Todoist, rpcCall("todoist_delete_project", args), true, up)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
}

func TestTodoistHidesTheKey(t *testing.T) {
	const owner = "tdst_owner_api_token_0001"
	const other = "tdst_other_api_token_0002"
	var mu sync.Mutex
	var auth string
	var hits int
	ts, dial := connectServer(t, []string{todoistHost}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Method != "GET" || r.URL.Path != "/rest/v2/tasks/1234567890" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1234567890","content":"` + other + `","is_completed":false,"secret":"` + owner + `"}`))
	})
	defer ts.Close()
	out, err := HandleRPC(context.Background(), Todoist, rpcCall("todoist_get_task", map[string]any{"task": "1234567890"}), true, func(ctx context.Context, call Call) (Result, error) {
		if strings.Contains(call.URL, other) || strings.Contains(call.URL, owner) {
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
	if hits != 1 || auth != "Bearer "+other || strings.Contains(auth, owner) {
		t.Fatalf("hits %d auth %q", hits, auth)
	}
}

func TestTodoistHostOutsideTheList(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{todoistHost, "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "tdst_todoist_key_0003", Call{
		Connection: Todoist, Method: "GET", URL: "https://evil.example/rest/v2/tasks",
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), "tdst_todoist_key_0003") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}
