package connect

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestStripeCatalog(t *testing.T) {
	entry, ok := Lookup(Stripe)
	if !ok || entry.SecretName != SecretNameStripe || entry.APIHost != stripeHost || entry.Auth != AuthBasic || entry.ExtraLabel != "" || entry.TokenLabel != "Restricted key" {
		t.Fatalf("%+v %v", entry, ok)
	}
	if !BlocksGuest(SecretNameStripe) || !BlocksGuest("STRIPE_SECRET_KEY") || !BlocksGuest("STRIPE_API_KEY") || !BlocksGuest("STRIPE_RESTRICTED_KEY") || BlocksGuest("SAFE_TOKEN") {
		t.Fatal("guest block")
	}
	if id, ok := ProviderEnv("STRIPE_SECRET_KEY"); !ok || id != Stripe {
		t.Fatal("alias")
	}
	for _, tool := range Tools(Stripe) {
		if tool.Write || tool.Build == nil {
			t.Fatal(tool.Name)
		}
	}
	if len(Tools(Stripe)) != 4 {
		t.Fatal(len(Tools(Stripe)))
	}
	yml, ok := ShimYAML(Stripe)
	if !ok || strings.Contains(yml, "STRIPE") || !strings.Contains(yml, "\n  - stripe\n") {
		t.Fatal(yml)
	}
}

func TestStripeUsesFixedMethods(t *testing.T) {
	call, err := stripeBalanceCall(context.Background(), map[string]any{"expand": "refunds"})
	if err != nil || call.Method != "GET" || call.URL != "https://api.stripe.com/v1/balance" || strings.Contains(call.URL, "refund") || len(call.Body) != 0 {
		t.Fatal(call.URL, err)
	}
	if _, err := CheckURL(Stripe, call.URL); err != nil {
		t.Fatal(err)
	}
	call, err = stripeChargesCall(context.Background(), map[string]any{"limit": "80", "customer": "cus_Abcdefghijklmnop", "query": "refund"})
	if err != nil || call.URL != "https://api.stripe.com/v1/charges?limit=20" || strings.Contains(call.URL, "cus_") || strings.Contains(call.URL, "refund") {
		t.Fatal(call.URL, err)
	}
	call, err = stripePaymentsCall(context.Background(), map[string]any{"limit": "3"})
	if err != nil || call.URL != "https://api.stripe.com/v1/payment_intents?limit=3" {
		t.Fatal(call.URL, err)
	}
	call, err = stripeCustomerCall(context.Background(), map[string]any{"customer": "cus_Abcdefghijklmnop"})
	if err != nil || call.Method != "GET" || call.URL != "https://api.stripe.com/v1/customers/cus_Abcdefghijklmnop" {
		t.Fatal(call.URL, err)
	}
	for _, id := range []string{"cus_short", "cus_Abc/defghij", "https://evil.example", "ch_Abcdefghijklmnop"} {
		if _, err := stripeCustomerCall(context.Background(), map[string]any{"customer": id}); err == nil {
			t.Fatal(id)
		}
	}
}

func TestStripeWriteDoesNotReachUpstream(t *testing.T) {
	var hits int
	up := func(context.Context, Call) (Result, error) {
		hits++
		return Result{Status: 200, Body: []byte(`{}`)}, nil
	}
	args := map[string]any{"customer": "cus_Abcdefghijklmnop", "amount": "100"}
	for _, name := range []string{"stripe_refund", "stripe_create", "stripe_payout", "stripe_transfer"} {
		for _, approved := range []bool{false, true} {
			out, err := HandleRPC(context.Background(), Stripe, rpcCall(name, args), approved, up)
			if err != nil || hits != 0 || !strings.Contains(string(out), "unknown tool") {
				t.Fatalf("%s approved %v %s %v hits %d", name, approved, out, err, hits)
			}
		}
	}
}

func TestStripeHidesTheKey(t *testing.T) {
	const owner = "rk_owner_restricted_key_0001"
	const other = "rk_other_restricted_key_0002"
	const secret = "pi_secret_sample_value"
	customer := "cus_Abcdefghijklmnop"
	var mu sync.Mutex
	var auth string
	var hits int
	ts, dial := connectServer(t, []string{stripeHost}, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		if r.Method != "GET" || r.URL.Path != "/v1/customers/"+customer || r.URL.User != nil {
			t.Errorf("%s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"` + customer + `","name":"` + other + `","email":"ada@example.com","client_secret":"` + secret + `","secret":"` + owner + `"}`))
	})
	defer ts.Close()
	out, err := HandleRPC(context.Background(), Stripe, rpcCall("stripe_get_customer", map[string]any{"customer": customer}), true, func(ctx context.Context, call Call) (Result, error) {
		if strings.Contains(call.URL, other) || strings.Contains(call.URL, owner) || call.URL != "https://api.stripe.com/v1/customers/"+customer {
			t.Fatal(call.URL)
		}
		return Do(ctx, other, call, dial)
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	if strings.Contains(text, other) || strings.Contains(text, owner) || strings.Contains(text, secret) || strings.Contains(text, "client_secret") || !strings.Contains(text, "[redacted]") || strings.Contains(text, `"secret"`) {
		t.Fatal(text)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(other+":"))
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 || auth != want || strings.Contains(auth, owner) || strings.Contains(auth, other) {
		t.Fatalf("hits %d", hits)
	}
}

func TestStripeHostOutsideTheList(t *testing.T) {
	var hits int
	ts, dial := connectServer(t, []string{stripeHost, "evil.example"}, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	res, err := Do(context.Background(), "rk_stripe_key_0003", Call{
		Connection: Stripe, Method: "GET", URL: "https://evil.example/v1/balance",
	}, dial)
	if err == nil || res.Outcome != OutcomeAllow || hits != 0 || strings.Contains(string(res.Body), "rk_stripe_key_0003") {
		t.Fatalf("%+v %v hits %d", res, err, hits)
	}
}
