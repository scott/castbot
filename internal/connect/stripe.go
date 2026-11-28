package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

const (
	Stripe           = "stripe"
	SecretNameStripe = "STRIPE_CONNECTION"
	stripeHost       = "api.stripe.com"
)

func init() {
	register(stripeEntry())
}

func stripeEntry() Entry {
	return Entry{
		ID: Stripe, Title: "Stripe", SecretName: SecretNameStripe,
		Detail:     "Paste a restricted read key. Stored in your vault.",
		TokenLabel: "Restricted key",
		Hosts:      []string{stripeHost},
		APIHost:    stripeHost,
		Auth:       AuthBasic,
		Aliases:    []string{"STRIPE_SECRET_KEY", "STRIPE_API_KEY", "STRIPE_RESTRICTED_KEY"},
		Tools:      stripeTools,
	}
}

var stripeTools = []Tool{
	{
		Name: "stripe_balance", Title: "Stripe: balance", Method: "GET",
		Build: stripeBalanceCall, ReadBody: stripeBalanceBody,
	},
	{
		Name: "stripe_list_charges", Title: "Stripe: list charges", Method: "GET",
		Args:  []Arg{{Name: "limit", Description: "How many charges, at most 20."}},
		Build: stripeChargesCall, ReadBody: stripeChargesBody,
	},
	{
		Name: "stripe_get_customer", Title: "Stripe: read a customer", Method: "GET",
		Args:  []Arg{{Name: "customer", Description: "Customer id.", Required: true}},
		Build: stripeCustomerCall, ReadBody: stripeCustomerBody,
	},
	{
		Name: "stripe_list_payment_intents", Title: "Stripe: list payments", Method: "GET",
		Args:  []Arg{{Name: "limit", Description: "How many payments, at most 20."}},
		Build: stripePaymentsCall, ReadBody: stripePaymentsBody,
	},
}

func stripeBalanceCall(_ context.Context, _ map[string]any) (Call, error) {
	return stripeCall("https://api.stripe.com/v1/balance")
}

func stripeChargesCall(_ context.Context, args map[string]any) (Call, error) {
	limit, err := stripeLimit(args)
	if err != nil {
		return Call{}, err
	}
	return stripeCall("https://api.stripe.com/v1/charges?limit=" + strconv.Itoa(limit))
}

func stripeCustomerCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := stripeCustomer(args)
	if err != nil {
		return Call{}, err
	}
	return stripeCall("https://api.stripe.com/v1/customers/" + url.PathEscape(id))
}

func stripePaymentsCall(_ context.Context, args map[string]any) (Call, error) {
	limit, err := stripeLimit(args)
	if err != nil {
		return Call{}, err
	}
	return stripeCall("https://api.stripe.com/v1/payment_intents?limit=" + strconv.Itoa(limit))
}

func stripeCall(rawURL string) (Call, error) {
	return Call{
		Method: "GET",
		URL:    rawURL,
		Headers: map[string]string{
			"Accept": "application/json",
		},
	}, nil
}

func stripeCustomer(args map[string]any) (string, error) {
	val, ok := argString(args, "customer")
	if !ok || !strings.HasPrefix(val, "cus_") || len(val) < 12 || len(val) > 68 {
		return "", ErrBad
	}
	for _, r := range val[4:] {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			continue
		}
		return "", ErrBad
	}
	return val, nil
}

func stripeLimit(args map[string]any) (int, error) {
	const fallback, max = 10, 20
	if args != nil {
		if v, exists := args["limit"]; exists && v != nil {
			raw, ok := argString(args, "limit")
			if !ok || raw == "" {
				return 0, ErrBad
			}
			n := 0
			for _, r := range raw {
				if r < '0' || r > '9' || n > 1000 {
					return 0, ErrBad
				}
				n = n*10 + int(r-'0')
			}
			if n < 1 {
				return 0, ErrBad
			}
			if n > max {
				n = max
			}
			return n, nil
		}
	}
	return fallback, nil
}

func stripeClip(body []byte) []byte {
	if len(body) > 256<<10 {
		return body[:256<<10]
	}
	return body
}

func stripeBalanceBody(body []byte) []byte {
	if msg, bad := stripeProblem(body); bad {
		return stripeFail(msg)
	}
	var raw map[string]any
	if stripeDecode(body, &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	buf, err := json.Marshal(map[string]any{
		"available": stripeMoney(raw["available"]),
		"pending":   stripeMoney(raw["pending"]),
	})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func stripeChargesBody(body []byte) []byte {
	return stripeListBody(body, "charges")
}

func stripePaymentsBody(body []byte) []byte {
	return stripeListBody(body, "payments")
}

func stripeListBody(body []byte, key string) []byte {
	if msg, bad := stripeProblem(body); bad {
		return stripeFail(msg)
	}
	var raw map[string]any
	if stripeDecode(body, &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	rows, _ := raw["data"].([]any)
	out := []map[string]any{}
	for i, item := range rows {
		if i >= 20 {
			break
		}
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, stripePayment(row))
	}
	buf, err := json.Marshal(map[string]any{key: out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func stripeCustomerBody(body []byte) []byte {
	if msg, bad := stripeProblem(body); bad {
		return stripeFail(msg)
	}
	var raw map[string]any
	if stripeDecode(body, &raw) != nil || stripeScalar(raw["id"]) == "" {
		return []byte(`{"error":"bad response"}`)
	}
	buf, err := json.Marshal(map[string]string{
		"id":    stripeScalar(raw["id"]),
		"name":  clipText(stripeScalar(raw["name"]), 200),
		"email": clipText(stripeScalar(raw["email"]), 200),
	})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func stripePayment(row map[string]any) map[string]any {
	item := map[string]any{
		"id":       clipText(stripeScalar(row["id"]), 64),
		"amount":   stripeAmount(row["amount"]),
		"currency": clipText(stripeScalar(row["currency"]), 8),
		"status":   clipText(stripeScalar(row["status"]), 32),
	}
	if paid, ok := row["paid"].(bool); ok {
		item["paid"] = paid
	}
	return item
}

func stripeMoney(v any) []map[string]any {
	rows, _ := v.([]any)
	out := []map[string]any{}
	for i, item := range rows {
		if i >= 8 {
			break
		}
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, map[string]any{
			"amount":   stripeAmount(row["amount"]),
			"currency": clipText(stripeScalar(row["currency"]), 8),
		})
	}
	return out
}

func stripeAmount(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		n, err := t.Int64()
		if err == nil {
			return n
		}
	case float64:
		return int64(t)
	}
	return 0
}

func stripeProblem(body []byte) (string, bool) {
	var raw map[string]any
	if stripeDecode(body, &raw) != nil {
		return "", false
	}
	errObj, ok := raw["error"].(map[string]any)
	if !ok {
		return "", false
	}
	msg := stripeScalar(errObj["message"])
	if msg == "" {
		msg = "request failed"
	}
	return msg, true
}

func stripeDecode(body []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(stripeClip(body)))
	dec.UseNumber()
	return dec.Decode(dest)
}

func stripeScalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func stripeFail(message string) []byte {
	buf, err := json.Marshal(map[string]string{"error": clipText(message, 200)})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}
