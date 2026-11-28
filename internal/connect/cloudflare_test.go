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

func TestCloudflareCatalog(t *testing.T) {
	entry, ok := Lookup(Cloudflare)
	if !ok || entry.SecretName != SecretNameCloudflare || entry.APIHost != cloudflareHost || entry.Auth != AuthBearer || entry.ExtraLabel != "" {
		t.Fatalf("%+v %v", entry, ok)
	}
	if !BlocksGuest(SecretNameCloudflare) || !BlocksGuest("CLOUDFLARE_API_TOKEN") || !BlocksGuest("CF_API_TOKEN") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("CLOUDFLARE_TOKEN"); !ok || id != Cloudflare {
		t.Fatal("alias")
	}
	writes := map[string]bool{}
	for _, tool := range Tools(Cloudflare) {
		writes[tool.Name] = tool.Write
		if tool.Build == nil {
			t.Fatal(tool.Name)
		}
	}
	if writes["cloudflare_list_zones"] || writes["cloudflare_list_dns"] || writes["cloudflare_zone_summary"] || !writes["cloudflare_upsert_dns"] || !writes["cloudflare_purge_cache"] || len(writes) != 5 {
		t.Fatal(writes)
	}
	yml, ok := ShimYAML(Cloudflare)
	if !ok || strings.Contains(yml, "CLOUDFLARE") || !strings.Contains(yml, "\n  - cloudflare\n") {
		t.Fatal(yml)
	}
}

func TestCloudflareUsesFixedMethods(t *testing.T) {
	zone := "0123456789abcdef0123456789abcdef"
	record := "fedcba9876543210fedcba9876543210"
	call, err := cloudflareZonesCall(context.Background(), map[string]any{"limit": "80", "account": "admin"})
	if err != nil || call.Method != "GET" || call.URL != "https://api.cloudflare.com/client/v4/zones?per_page=50" || strings.Contains(call.URL, "account") {
		t.Fatal(call.URL, err)
	}
	if _, err := CheckURL(Cloudflare, call.URL); err != nil {
		t.Fatal(err)
	}
	call, err = cloudflareDNSCall(context.Background(), map[string]any{"zone": zone})
	if err != nil || call.URL != "https://api.cloudflare.com/client/v4/zones/"+zone+"/dns_records?per_page=20" {
		t.Fatal(call.URL, err)
	}
	call, err = cloudflareSummaryCall(context.Background(), map[string]any{"zone": zone})
	if err != nil || call.URL != "https://api.cloudflare.com/client/v4/zones/"+zone {
		t.Fatal(call.URL, err)
	}
	call, err = cloudflareUpsertCall(context.Background(), map[string]any{
		"zone": zone, "type": "a", "name": "www.example.com", "content": "192.0.2.1",
		"proxied": true, "query": "{ delete }",
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if json.Unmarshal(call.Body, &body) != nil || call.Method != "POST" || call.URL != "https://api.cloudflare.com/client/v4/zones/"+zone+"/dns_records" {
		t.Fatal(call.Method, call.URL, string(call.Body))
	}
	if body["type"] != "A" || body["name"] != "www.example.com" || body["content"] != "192.0.2.1" || body["ttl"] != float64(1) || body["proxied"] != nil || strings.Contains(string(call.Body), "delete") {
		t.Fatal(string(call.Body))
	}
	call, err = cloudflareUpsertCall(context.Background(), map[string]any{
		"zone": zone, "record": record, "type": "CNAME", "name": "www.example.com", "content": "origin.example.com",
	})
	if err != nil || call.Method != "PUT" || call.URL != "https://api.cloudflare.com/client/v4/zones/"+zone+"/dns_records/"+record {
		t.Fatal(call.Method, call.URL, err)
	}
	page := "https://www.example.com/app.js"
	call, err = cloudflarePurgeCall(context.Background(), map[string]any{"zone": zone, "url": page, "purge_everything": true})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(call.Body)
	if call.Method != "POST" || call.URL != "https://api.cloudflare.com/client/v4/zones/"+zone+"/purge_cache" || strings.Contains(raw, "purge_everything") || !strings.Contains(raw, page) {
		t.Fatal(call.URL, raw)
	}
	for _, zone := range []string{"ABCDEF", zone + "/workers", "https://evil.example"} {
		if _, err := cloudflareSummaryCall(context.Background(), map[string]any{"zone": zone}); err == nil {
			t.Fatal(zone)
		}
	}
	for _, kind := range []string{"MX", "NS", "SRV"} {
		if _, err := cloudflareUpsertCall(context.Background(), map[string]any{"zone": zone, "type": kind, "name": "www.example.com", "content": "192.0.2.1"}); err == nil {
			t.Fatal(kind)
		}
	}
	for _, page := range []string{"http://www.example.com/a", "https://192.0.2.1/a", "https://user:pass@example.com/a", "https://www.example.com/a#frag"} {
		if _, err := cloudflarePurgeCall(context.Background(), map[string]any{"zone": zone, "url": page}); err == nil {
			t.Fatal(page)
		}
	}
}

func TestCloudflareWriteDoesNotReachUpstream(t *testing.T) {
	var hits int
	up := func(context.Context, Call) (Result, error) {
		hits++
		return Result{Status: 200, Body: []byte(`{}`)}, nil
	}
	zone := "0123456789abcdef0123456789abcdef"
	args := map[string]any{"zone": zone, "type": "A", "name": "www.example.com", "content": "192.0.2.1", "url": "https://www.example.com/app.js"}
	for _, name := range []string{"cloudflare_upsert_dns", "cloudflare_purge_cache"} {
		_, err := HandleRPC(context.Background(), Cloudflare, rpcCall(name, args), false, up)
		if !errors.Is(err, ErrDenied) || hits != 0 {
			t.Fatalf("%s %v hits %d", name, err, hits)
		}
	}
	out, err := HandleRPC(context.Background(), Cloudflare, rpcCall("cloudflare_delete", args), true, up)
	if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
		t.Fatalf("%s %v hits %d", out, err, hits)
	}
}

func TestCloudflareHidesTheKey(t *testing.T) {
	const owner = "cfut_owner_api_token_0001"
	const other = "cfut_other_api_token_0002"
	zone := "0123456789abcdef0123456789abcdef"
	var mu sync.Mutex
	var auth string
	var hits int
	ts, dial := connectServer(t, []string{cloudflareHost}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Method != "GET" || r.URL.Path != "/client/v4/zones/"+zone {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"` + zone + `","name":"` + other + `","status":"active","paused":false,"secret":"` + owner + `"}}`))
	})
	defer ts.Close()
	out, err := HandleRPC(context.Background(), Cloudflare, rpcCall("cloudflare_zone_summary", map[string]any{"zone": zone}), true, func(ctx context.Context, call Call) (Result, error) {
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

func TestCloudflareHostOutsideTheList(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{cloudflareHost, "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "cfut_cloudflare_key_0003", Call{
		Connection: Cloudflare, Method: "GET", URL: "https://evil.example/client/v4/zones",
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), "cfut_cloudflare_key_0003") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}
