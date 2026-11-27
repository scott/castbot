package connect

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestSentryCatalog(t *testing.T) {
	entry, ok := Lookup(Sentry)
	if !ok || entry.SecretName != SecretNameSentry || entry.APIHost != sentryHost || entry.Auth != AuthBearer || entry.ExtraLabel != "" {
		t.Fatalf("%+v %v", entry, ok)
	}
	if !BlocksGuest(SecretNameSentry) || !BlocksGuest("SENTRY_AUTH_TOKEN") || !BlocksGuest("SENTRY_API_TOKEN") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("SENTRY_TOKEN"); !ok || id != Sentry {
		t.Fatal("alias")
	}
	writes := map[string]bool{}
	for _, tool := range Tools(Sentry) {
		writes[tool.Name] = tool.Write
		if tool.Build == nil {
			t.Fatal(tool.Name)
		}
	}
	if writes["sentry_list_orgs"] || writes["sentry_list_projects"] || writes["sentry_list_issues"] || writes["sentry_get_issue"] || !writes["sentry_resolve"] || len(writes) != 5 {
		t.Fatal(writes)
	}
	yml, ok := ShimYAML(Sentry)
	if !ok || strings.Contains(yml, "SENTRY") || !strings.Contains(yml, "\n  - sentry\n") {
		t.Fatal(yml)
	}
}

func TestSentryUsesFixedMethods(t *testing.T) {
	call, err := sentryOrgsCall(context.Background(), map[string]any{"limit": "80", "query": "is:unresolved"})
	if err != nil {
		t.Fatal(err)
	}
	if call.Method != "GET" || call.URL != "https://sentry.io/api/0/organizations/?limit=25" || strings.Contains(call.URL, "query") || strings.Contains(call.URL, "unresolved") {
		t.Fatal(call.URL)
	}
	if _, err := CheckURL(Sentry, call.URL); err != nil {
		t.Fatal(err)
	}
	call, err = sentryProjectsCall(context.Background(), map[string]any{"org": "acme", "query": "admin"})
	if err != nil || call.URL != "https://sentry.io/api/0/organizations/acme/projects/?limit=10" {
		t.Fatal(call.URL, err)
	}
	call, err = sentryIssuesCall(context.Background(), map[string]any{"org": "acme", "project": "web", "query": "is:unresolved", "limit": "3"})
	if err != nil || call.Method != "GET" || call.URL != "https://sentry.io/api/0/projects/acme/web/issues/?limit=3" || strings.Contains(call.URL, "unresolved") {
		t.Fatal(call.URL, err)
	}
	call, err = sentryIssueCall(context.Background(), map[string]any{"issue": "12345"})
	if err != nil || call.URL != "https://sentry.io/api/0/issues/12345/" {
		t.Fatal(call.URL, err)
	}
	call, err = sentryResolveCall(context.Background(), map[string]any{"issue": "12345", "status": "ignored", "query": "{ delete }"})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(call.Body)
	if call.Method != "PUT" || call.URL != "https://sentry.io/api/0/issues/12345/" || raw != `{"status":"resolved"}` || strings.Contains(raw, "ignored") || strings.Contains(raw, "delete") {
		t.Fatal(call.Method, call.URL, raw)
	}
	for _, org := range []string{"Acme", "acme/admin", "https://evil.example", "../acme"} {
		if _, err := sentryProjectsCall(context.Background(), map[string]any{"org": org}); err == nil {
			t.Fatal(org)
		}
	}
	for _, id := range []string{"0", "123/delete", "https://evil.example"} {
		if _, err := sentryIssueCall(context.Background(), map[string]any{"issue": id}); err == nil {
			t.Fatal(id)
		}
	}
}

func TestSentryWriteDoesNotReachUpstream(t *testing.T) {
	var hits int
	up := func(context.Context, Call) (Result, error) {
		hits++
		return Result{Status: 200, Body: []byte(`{}`)}, nil
	}
	_, err := HandleRPC(context.Background(), Sentry, rpcCall("sentry_resolve", map[string]any{"issue": "12345", "status": "ignored"}), false, up)
	if !errors.Is(err, ErrDenied) || hits != 0 {
		t.Fatalf("%v hits %d", err, hits)
	}
	out, err := HandleRPC(context.Background(), Sentry, rpcCall("sentry_delete", map[string]any{"issue": "12345"}), true, up)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
}

func TestSentryHidesTheKey(t *testing.T) {
	const owner = "sntry_owner_auth_token_0001"
	const other = "sntry_other_auth_token_0002"
	var mu sync.Mutex
	var auth string
	var hits int
	ts, dial := connectServer(t, []string{sentryHost}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Method != "GET" || r.URL.Path != "/api/0/issues/12345/" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"12345","shortId":"WEB-1","title":"` + other + `","status":"unresolved","secret":"` + owner + `"}`))
	})
	defer ts.Close()
	out, err := HandleRPC(context.Background(), Sentry, rpcCall("sentry_get_issue", map[string]any{"issue": "12345"}), true, func(ctx context.Context, call Call) (Result, error) {
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

func TestSentryHostOutsideTheList(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{sentryHost, "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "sntry_sentry_key_0003", Call{
		Connection: Sentry, Method: "GET", URL: "https://evil.example/api/0/organizations/",
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), "sntry_sentry_key_0003") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}
