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

func TestLinearCatalog(t *testing.T) {
	entry, ok := Lookup(Linear)
	if !ok || entry.SecretName != SecretNameLinear || entry.APIHost != "api.linear.app" || entry.Auth != AuthBearer || entry.GitHost != "" {
		t.Fatalf("%+v %v", entry, ok)
	}
	if !BlocksGuest(SecretNameLinear) || !BlocksGuest("LINEAR_API_KEY") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("LINEAR_TOKEN"); !ok || id != Linear {
		t.Fatal("alias")
	}
	writes := map[string]bool{}
	for _, tool := range Tools(Linear) {
		writes[tool.Name] = tool.Write
		if tool.Method != "POST" || tool.Build == nil {
			t.Fatal(tool.Name)
		}
	}
	if writes["linear_list_teams"] || writes["linear_list_issues"] || writes["linear_get_issue"] || writes["linear_list_comments"] {
		t.Fatal("read marked write")
	}
	if !writes["linear_create_issue"] || !writes["linear_comment"] || !writes["linear_update_status"] || len(writes) != 7 {
		t.Fatal(writes)
	}
	for _, query := range []string{linearQueryTeams, linearQueryIssues, linearQueryIssue, linearQueryComments, linearQueryCreate, linearQueryComment, linearQueryStatus} {
		for _, banned := range []string{"issueDelete", "issueArchive", "projectCreate", "invite"} {
			if strings.Contains(query, banned) {
				t.Fatal(banned)
			}
		}
	}
	yml, ok := ShimYAML(Linear)
	if !ok || strings.Contains(yml, "LINEAR") || !strings.Contains(yml, "\n  - linear\n") {
		t.Fatal(yml)
	}
}

func TestLinearUsesFixedDocuments(t *testing.T) {
	team := "9cfb482a-81e3-4154-b5b9-2c805e70a02d"
	call, err := linearIssuesCall(context.Background(), map[string]any{"team": "ENG", "count": "80", "query": "{ users { nodes { email } } }"})
	if err != nil {
		t.Fatal(err)
	}
	doc := linearDoc(t, call)
	if doc.Query != linearQueryIssues || call.URL != "https://api.linear.app/graphql" || call.Method != "POST" {
		t.Fatal(doc.Query)
	}
	if _, err := CheckURL(Linear, call.URL); err != nil {
		t.Fatal(err)
	}
	if doc.Vars["first"] != float64(25) {
		t.Fatal(doc.Vars["first"])
	}
	filter := doc.Vars["filter"].(map[string]any)
	teamFilter := filter["team"].(map[string]any)
	if _, ok := teamFilter["key"]; !ok {
		t.Fatal(filter)
	}
	call, err = linearIssuesCall(context.Background(), map[string]any{"team": team})
	if err != nil {
		t.Fatal(err)
	}
	doc = linearDoc(t, call)
	filter = doc.Vars["filter"].(map[string]any)
	if _, ok := filter["team"].(map[string]any)["id"]; !ok {
		t.Fatal(filter)
	}
	call, err = linearCreateCall(context.Background(), map[string]any{
		"team": team, "title": "Fix login", "description": "steps",
		"query": "mutation { issueDelete(id: \"x\") { success } }",
	})
	if err != nil {
		t.Fatal(err)
	}
	doc = linearDoc(t, call)
	if doc.Query != linearQueryCreate || strings.Contains(doc.Query, "issueDelete") {
		t.Fatal(doc.Query)
	}
	input := doc.Vars["input"].(map[string]any)
	if input["teamId"] != team || input["title"] != "Fix login" || input["description"] != "steps" {
		t.Fatal(input)
	}
	call, err = linearStatusCall(context.Background(), map[string]any{
		"issue": "ENG-1", "status": team, "title": "should not send", "query": "{ delete }",
	})
	if err != nil {
		t.Fatal(err)
	}
	doc = linearDoc(t, call)
	if doc.Query != linearQueryStatus {
		t.Fatal(doc.Query)
	}
	input = doc.Vars["input"].(map[string]any)
	if len(input) != 1 || input["stateId"] != team {
		t.Fatal(input)
	}
	if _, err := linearCreateCall(context.Background(), map[string]any{"team": team}); err == nil {
		t.Fatal("missing title")
	}
}

func TestLinearWriteDoesNotReachUpstream(t *testing.T) {
	var hits int
	up := func(context.Context, Call) (Result, error) {
		hits++
		return Result{Status: 200, Body: []byte(`{}`)}, nil
	}
	args := map[string]any{"team": "team-1", "title": "T", "issue": "ENG-1", "body": "note", "status": "state-1"}
	for _, name := range []string{"linear_create_issue", "linear_comment", "linear_update_status"} {
		_, err := HandleRPC(context.Background(), Linear, rpcCall(name, args), false, up)
		if !errors.Is(err, ErrDenied) || hits != 0 {
			t.Fatalf("%s %v hits %d", name, err, hits)
		}
	}
	out, err := HandleRPC(context.Background(), Linear, rpcCall("linear_delete", args), true, up)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
}

func TestLinearHidesTheKey(t *testing.T) {
	const owner = "owner-linear-key-001"
	const other = "other-linear-key-002"
	var mu sync.Mutex
	var auth string
	var hits int
	ts, dial := connectServer(t, []string{"api.linear.app"}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Method != "POST" || r.URL.Path != "/graphql" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"teams":{"nodes":[]}},"echo":"` + other + `"}`))
	})
	defer ts.Close()
	out, err := HandleRPC(context.Background(), Linear, rpcCall("linear_list_teams", map[string]any{}), true, func(ctx context.Context, call Call) (Result, error) {
		if strings.Contains(string(call.Body), other) || strings.Contains(string(call.Body), owner) {
			t.Fatal("key in body")
		}
		return Do(ctx, other, call, dial)
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), other) || strings.Contains(string(out), owner) || !strings.Contains(string(out), "[redacted]") {
		t.Fatal(string(out))
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 || auth != "Bearer "+other || strings.Contains(auth, owner) {
		t.Fatalf("hits %d auth %q", hits, auth)
	}
}

func TestLinearHostOutsideTheList(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{"api.linear.app", "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "linear-key-0003", Call{
		Connection: Linear, Method: "POST", URL: "https://evil.example/graphql", Body: []byte(`{"query":"query { teams { nodes { id } } }"}`),
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), "linear-key-0003") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}

type linearPayload struct {
	Query string
	Vars  map[string]any
}

func linearDoc(t *testing.T, call Call) linearPayload {
	t.Helper()
	var raw struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(call.Body, &raw); err != nil {
		t.Fatal(err)
	}
	return linearPayload{Query: raw.Query, Vars: raw.Variables}
}
