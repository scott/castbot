package connect

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	Cloudflare           = "cloudflare"
	SecretNameCloudflare = "CLOUDFLARE_CONNECTION"
	cloudflareHost       = "api.cloudflare.com"
)

func init() {
	register(cloudflareEntry())
}

func cloudflareEntry() Entry {
	return Entry{
		ID: Cloudflare, Title: "Cloudflare", SecretName: SecretNameCloudflare,
		Detail:     "API token for the zones it can see. Stored in your vault.",
		TokenLabel: "API token",
		Hosts:      []string{cloudflareHost},
		APIHost:    cloudflareHost,
		Auth:       AuthBearer,
		Aliases:    []string{"CLOUDFLARE_API_TOKEN", "CLOUDFLARE_TOKEN", "CF_API_TOKEN"},
		Tools:      cloudflareTools,
	}
}

var cloudflareTools = []Tool{
	{
		Name: "cloudflare_list_zones", Title: "Cloudflare: list zones", Method: "GET",
		Args:  []Arg{{Name: "limit", Description: "How many zones, at most 50."}},
		Build: cloudflareZonesCall, ReadBody: cloudflareZonesBody,
	},
	{
		Name: "cloudflare_list_dns", Title: "Cloudflare: list DNS", Method: "GET",
		Args: []Arg{
			{Name: "zone", Description: "Zone id from the list.", Required: true},
			{Name: "limit", Description: "How many records, at most 50."},
		},
		Build: cloudflareDNSCall, ReadBody: cloudflareDNSBody,
	},
	{
		Name: "cloudflare_zone_summary", Title: "Cloudflare: zone summary", Method: "GET",
		Args:  []Arg{{Name: "zone", Description: "Zone id from the list.", Required: true}},
		Build: cloudflareSummaryCall, ReadBody: cloudflareSummaryBody,
	},
	{
		Name: "cloudflare_upsert_dns", Title: "Cloudflare: upsert DNS", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "zone", Description: "Zone id.", Required: true},
			{Name: "type", Description: "A, AAAA, CNAME, or TXT.", Required: true},
			{Name: "name", Description: "Record name.", Required: true},
			{Name: "content", Description: "Record value.", Required: true},
			{Name: "record", Description: "Record id to update. Omit to create."},
		},
		Build: cloudflareUpsertCall, ReadBody: cloudflareUpsertBody,
	},
	{
		Name: "cloudflare_purge_cache", Title: "Cloudflare: purge cache", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "zone", Description: "Zone id.", Required: true},
			{Name: "url", Description: "One https URL in the zone.", Required: true},
		},
		Build: cloudflarePurgeCall, ReadBody: cloudflarePurgeBody,
	},
}

func cloudflareZonesCall(_ context.Context, args map[string]any) (Call, error) {
	limit, err := cloudflareLimit(args)
	if err != nil {
		return Call{}, err
	}
	return cloudflareCall("GET", "https://api.cloudflare.com/client/v4/zones?per_page="+strconv.Itoa(limit), nil)
}

func cloudflareDNSCall(_ context.Context, args map[string]any) (Call, error) {
	zone, err := cloudflareZone(args)
	if err != nil {
		return Call{}, err
	}
	limit, err := cloudflareLimit(args)
	if err != nil {
		return Call{}, err
	}
	return cloudflareCall("GET", "https://api.cloudflare.com/client/v4/zones/"+url.PathEscape(zone)+"/dns_records?per_page="+strconv.Itoa(limit), nil)
}

func cloudflareSummaryCall(_ context.Context, args map[string]any) (Call, error) {
	zone, err := cloudflareZone(args)
	if err != nil {
		return Call{}, err
	}
	return cloudflareCall("GET", "https://api.cloudflare.com/client/v4/zones/"+url.PathEscape(zone), nil)
}

func cloudflareUpsertCall(_ context.Context, args map[string]any) (Call, error) {
	zone, err := cloudflareZone(args)
	if err != nil {
		return Call{}, err
	}
	kind, name, content, err := cloudflareRecord(args)
	if err != nil {
		return Call{}, err
	}
	payload := map[string]any{"type": kind, "name": name, "content": content, "ttl": 1}
	raw := "https://api.cloudflare.com/client/v4/zones/" + url.PathEscape(zone) + "/dns_records"
	method := "POST"
	if rec, ok := argString(args, "record"); ok && rec != "" {
		if !cloudflareID(rec) {
			return Call{}, ErrBad
		}
		method = "PUT"
		raw += "/" + url.PathEscape(rec)
	}
	return cloudflareCall(method, raw, payload)
}

func cloudflarePurgeCall(_ context.Context, args map[string]any) (Call, error) {
	zone, err := cloudflareZone(args)
	if err != nil {
		return Call{}, err
	}
	page, err := cloudflareFile(args)
	if err != nil {
		return Call{}, err
	}
	return cloudflareCall("POST", "https://api.cloudflare.com/client/v4/zones/"+url.PathEscape(zone)+"/purge_cache", map[string]any{
		"files": []string{page},
	})
}

func cloudflareCall(method, rawURL string, payload any) (Call, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return Call{}, ErrBad
		}
	}
	return Call{
		Method: method,
		URL:    rawURL,
		Headers: map[string]string{
			"Accept":       "application/json",
			"Content-Type": "application/json",
		},
		Body: body,
	}, nil
}

func cloudflareZone(args map[string]any) (string, error) {
	val, ok := argString(args, "zone")
	if !ok || !cloudflareID(val) {
		return "", ErrBad
	}
	return val, nil
}

func cloudflareID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return false
	}
	return true
}

func cloudflareRecord(args map[string]any) (kind, name, content string, err error) {
	kind, ok := argString(args, "type")
	if !ok {
		return "", "", "", ErrBad
	}
	kind = strings.ToUpper(kind)
	switch kind {
	case "A", "AAAA", "CNAME", "TXT":
	default:
		return "", "", "", ErrBad
	}
	name, ok = argString(args, "name")
	if !ok || !cloudflareName(name) {
		return "", "", "", ErrBad
	}
	content, ok = argString(args, "content")
	if !ok || content == "" || len(content) > 512 || strings.ContainsAny(content, "\r\n\x00") {
		return "", "", "", ErrBad
	}
	switch kind {
	case "A":
		ip := net.ParseIP(content)
		if ip == nil || ip.To4() == nil {
			return "", "", "", ErrBad
		}
		content = ip.To4().String()
	case "AAAA":
		ip := net.ParseIP(content)
		if ip == nil || ip.To4() != nil {
			return "", "", "", ErrBad
		}
		content = ip.String()
	case "CNAME":
		if !cloudflareName(content) {
			return "", "", "", ErrBad
		}
	}
	return kind, name, content, nil
}

func cloudflareName(s string) bool {
	if len(s) < 1 || len(s) > 253 || strings.Contains(s, "..") || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func cloudflareFile(args map[string]any) (string, error) {
	raw, ok := argString(args, "url")
	if !ok || len(raw) < 12 || len(raw) > 500 {
		return "", ErrBad
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || net.ParseIP(u.Hostname()) != nil {
		return "", ErrBad
	}
	if port := u.Port(); port != "" && port != "443" {
		return "", ErrBad
	}
	if u.Fragment != "" {
		return "", ErrBad
	}
	return u.String(), nil
}

func cloudflareLimit(args map[string]any) (int, error) {
	const fallback, max = 20, 50
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

func cloudflareClip(body []byte) []byte {
	if len(body) > 256<<10 {
		return body[:256<<10]
	}
	return body
}

type cloudflareEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func cloudflareOpen(body []byte) (cloudflareEnvelope, bool) {
	var env cloudflareEnvelope
	if json.Unmarshal(cloudflareClip(body), &env) != nil {
		return env, false
	}
	return env, true
}

func cloudflareFail(env cloudflareEnvelope) []byte {
	msg := "request failed"
	if len(env.Errors) > 0 && strings.TrimSpace(env.Errors[0].Message) != "" {
		msg = env.Errors[0].Message
	}
	buf, err := json.Marshal(map[string]string{"error": clipText(msg, 200)})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func cloudflareZonesBody(body []byte) []byte {
	env, ok := cloudflareOpen(body)
	if !ok {
		return []byte(`{"error":"bad response"}`)
	}
	if !env.Success {
		return cloudflareFail(env)
	}
	var rows []map[string]any
	if json.Unmarshal(env.Result, &rows) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	out := []map[string]string{}
	for i, row := range rows {
		if i >= 50 {
			break
		}
		out = append(out, map[string]string{
			"id":     cloudflareScalar(row["id"]),
			"name":   clipText(cloudflareScalar(row["name"]), 253),
			"status": clipText(cloudflareScalar(row["status"]), 32),
		})
	}
	buf, err := json.Marshal(map[string]any{"zones": out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func cloudflareDNSBody(body []byte) []byte {
	env, ok := cloudflareOpen(body)
	if !ok {
		return []byte(`{"error":"bad response"}`)
	}
	if !env.Success {
		return cloudflareFail(env)
	}
	var rows []map[string]any
	if json.Unmarshal(env.Result, &rows) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	out := []map[string]string{}
	for i, row := range rows {
		if i >= 50 {
			break
		}
		out = append(out, map[string]string{
			"id":      cloudflareScalar(row["id"]),
			"type":    clipText(cloudflareScalar(row["type"]), 16),
			"name":    clipText(cloudflareScalar(row["name"]), 253),
			"content": clipText(cloudflareScalar(row["content"]), 512),
		})
	}
	buf, err := json.Marshal(map[string]any{"records": out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func cloudflareSummaryBody(body []byte) []byte {
	env, ok := cloudflareOpen(body)
	if !ok {
		return []byte(`{"error":"bad response"}`)
	}
	if !env.Success {
		return cloudflareFail(env)
	}
	var row map[string]any
	if json.Unmarshal(env.Result, &row) != nil || cloudflareScalar(row["id"]) == "" {
		return []byte(`{"error":"bad response"}`)
	}
	paused, _ := row["paused"].(bool)
	buf, err := json.Marshal(map[string]any{
		"id":     cloudflareScalar(row["id"]),
		"name":   clipText(cloudflareScalar(row["name"]), 253),
		"status": clipText(cloudflareScalar(row["status"]), 32),
		"paused": paused,
	})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func cloudflareUpsertBody(body []byte) []byte {
	env, ok := cloudflareOpen(body)
	if !ok {
		return []byte(`{"error":"bad response"}`)
	}
	if !env.Success {
		return cloudflareFail(env)
	}
	var row map[string]any
	if json.Unmarshal(env.Result, &row) != nil || cloudflareScalar(row["id"]) == "" {
		return []byte(`{"error":"bad response"}`)
	}
	buf, err := json.Marshal(map[string]any{"upserted": true, "id": cloudflareScalar(row["id"])})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func cloudflarePurgeBody(body []byte) []byte {
	env, ok := cloudflareOpen(body)
	if !ok {
		return []byte(`{"error":"bad response"}`)
	}
	if !env.Success {
		return cloudflareFail(env)
	}
	return []byte(`{"purged":true}`)
}

func cloudflareScalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return ""
	}
}
