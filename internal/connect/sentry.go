package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

const (
	Sentry           = "sentry"
	SecretNameSentry = "SENTRY_CONNECTION"
	sentryHost       = "sentry.io"
)

func init() {
	register(sentryEntry())
}

func sentryEntry() Entry {
	return Entry{
		ID: Sentry, Title: "Sentry", SecretName: SecretNameSentry,
		Detail:     "Auth token for the orgs it can see. Stored in your vault.",
		TokenLabel: "Auth token",
		Hosts:      []string{sentryHost},
		APIHost:    sentryHost,
		Auth:       AuthBearer,
		Aliases:    []string{"SENTRY_AUTH_TOKEN", "SENTRY_TOKEN", "SENTRY_API_TOKEN"},
		Tools:      sentryTools,
	}
}

var sentryTools = []Tool{
	{
		Name: "sentry_list_orgs", Title: "Sentry: list orgs", Method: "GET",
		Args:  []Arg{{Name: "limit", Description: "How many orgs, at most 25."}},
		Build: sentryOrgsCall, ReadBody: sentryOrgsBody,
	},
	{
		Name: "sentry_list_projects", Title: "Sentry: list projects", Method: "GET",
		Args: []Arg{
			{Name: "org", Description: "Org slug from the list.", Required: true},
			{Name: "limit", Description: "How many projects, at most 25."},
		},
		Build: sentryProjectsCall, ReadBody: sentryProjectsBody,
	},
	{
		Name: "sentry_list_issues", Title: "Sentry: list issues", Method: "GET",
		Args: []Arg{
			{Name: "org", Description: "Org slug from the list.", Required: true},
			{Name: "project", Description: "Project slug from the list.", Required: true},
			{Name: "limit", Description: "How many issues, at most 25."},
		},
		Build: sentryIssuesCall, ReadBody: sentryIssuesBody,
	},
	{
		Name: "sentry_get_issue", Title: "Sentry: read an issue", Method: "GET",
		Args:  []Arg{{Name: "issue", Description: "Issue id.", Required: true}},
		Build: sentryIssueCall, ReadBody: sentryIssueBody,
	},
	{
		Name: "sentry_resolve", Title: "Sentry: resolve", Write: true, Method: "PUT",
		Args:  []Arg{{Name: "issue", Description: "Issue id.", Required: true}},
		Build: sentryResolveCall, ReadBody: sentryResolveBody,
	},
}

func sentryOrgsCall(_ context.Context, args map[string]any) (Call, error) {
	limit, err := sentryLimit(args, 10, 25)
	if err != nil {
		return Call{}, err
	}
	return sentryCall("GET", "https://sentry.io/api/0/organizations/?limit="+strconv.Itoa(limit), nil)
}

func sentryProjectsCall(_ context.Context, args map[string]any) (Call, error) {
	org, err := sentrySlug(args, "org")
	if err != nil {
		return Call{}, err
	}
	limit, err := sentryLimit(args, 10, 25)
	if err != nil {
		return Call{}, err
	}
	return sentryCall("GET", "https://sentry.io/api/0/organizations/"+url.PathEscape(org)+"/projects/?limit="+strconv.Itoa(limit), nil)
}

func sentryIssuesCall(_ context.Context, args map[string]any) (Call, error) {
	org, err := sentrySlug(args, "org")
	if err != nil {
		return Call{}, err
	}
	project, err := sentrySlug(args, "project")
	if err != nil {
		return Call{}, err
	}
	limit, err := sentryLimit(args, 10, 25)
	if err != nil {
		return Call{}, err
	}
	return sentryCall("GET", "https://sentry.io/api/0/projects/"+url.PathEscape(org)+"/"+url.PathEscape(project)+"/issues/?limit="+strconv.Itoa(limit), nil)
}

func sentryIssueCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := sentryID(args, "issue")
	if err != nil {
		return Call{}, err
	}
	return sentryCall("GET", "https://sentry.io/api/0/issues/"+url.PathEscape(id)+"/", nil)
}

func sentryResolveCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := sentryID(args, "issue")
	if err != nil {
		return Call{}, err
	}
	return sentryCall("PUT", "https://sentry.io/api/0/issues/"+url.PathEscape(id)+"/", map[string]any{"status": "resolved"})
}

func sentryCall(method, rawURL string, payload any) (Call, error) {
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

func sentrySlug(args map[string]any, key string) (string, error) {
	val, ok := argString(args, key)
	if !ok || len(val) < 1 || len(val) > 64 {
		return "", ErrBad
	}
	for i, r := range val {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if i > 0 && (r == '-' || r == '_') {
			ok = true
		}
		if !ok {
			return "", ErrBad
		}
	}
	return val, nil
}

func sentryID(args map[string]any, key string) (string, error) {
	val, ok := argString(args, key)
	if !ok || len(val) < 1 || len(val) > 20 || val[0] == '0' {
		return "", ErrBad
	}
	for _, r := range val {
		if r < '0' || r > '9' {
			return "", ErrBad
		}
	}
	return val, nil
}

func sentryLimit(args map[string]any, fallback, max int) (int, error) {
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

func sentryClip(body []byte) []byte {
	if len(body) > 256<<10 {
		return body[:256<<10]
	}
	return body
}

func sentryOrgsBody(body []byte) []byte {
	return sentryNamedBody(body, "orgs", false)
}

func sentryProjectsBody(body []byte) []byte {
	return sentryNamedBody(body, "projects", false)
}

func sentryIssuesBody(body []byte) []byte {
	return sentryNamedBody(body, "issues", true)
}

func sentryNamedBody(body []byte, key string, issue bool) []byte {
	if msg, bad := sentryDetail(body); bad {
		return sentryErr(msg)
	}
	var rows []map[string]any
	if sentryDecode(body, &rows) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	out := []map[string]string{}
	for i, row := range rows {
		if i >= 25 {
			break
		}
		item := map[string]string{
			"id":   sentryScalar(row["id"]),
			"slug": clipText(sentryScalar(row["slug"]), 64),
			"name": clipText(sentryScalar(row["name"]), 200),
		}
		if issue {
			item = map[string]string{
				"id":      sentryScalar(row["id"]),
				"shortId": clipText(sentryScalar(row["shortId"]), 40),
				"title":   clipText(sentryScalar(row["title"]), 200),
				"status":  clipText(sentryScalar(row["status"]), 32),
				"culprit": clipText(sentryScalar(row["culprit"]), 200),
			}
		}
		out = append(out, item)
	}
	buf, err := json.Marshal(map[string]any{key: out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func sentryIssueBody(body []byte) []byte {
	if msg, bad := sentryDetail(body); bad {
		return sentryErr(msg)
	}
	var row map[string]any
	if sentryDecode(body, &row) != nil || sentryScalar(row["id"]) == "" {
		return []byte(`{"error":"bad response"}`)
	}
	buf, err := json.Marshal(map[string]string{
		"id":      sentryScalar(row["id"]),
		"shortId": clipText(sentryScalar(row["shortId"]), 40),
		"title":   clipText(sentryScalar(row["title"]), 200),
		"status":  clipText(sentryScalar(row["status"]), 32),
		"culprit": clipText(sentryScalar(row["culprit"]), 200),
	})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func sentryResolveBody(body []byte) []byte {
	if msg, bad := sentryDetail(body); bad {
		return sentryErr(msg)
	}
	var row map[string]any
	if sentryDecode(body, &row) != nil || sentryScalar(row["id"]) == "" {
		return []byte(`{"error":"bad response"}`)
	}
	buf, err := json.Marshal(map[string]string{
		"id":     sentryScalar(row["id"]),
		"status": clipText(sentryScalar(row["status"]), 32),
	})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func sentryDetail(body []byte) (string, bool) {
	clip := sentryClip(body)
	if len(bytes.TrimSpace(clip)) == 0 || clip[0] == '[' {
		return "", false
	}
	var raw map[string]any
	if sentryDecode(clip, &raw) != nil {
		return "", false
	}
	if _, ok := raw["id"]; ok {
		return "", false
	}
	detail := sentryScalar(raw["detail"])
	if detail == "" {
		return "", false
	}
	return detail, true
}

func sentryDecode(body []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(sentryClip(body)))
	dec.UseNumber()
	return dec.Decode(dest)
}

func sentryScalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func sentryErr(message string) []byte {
	buf, err := json.Marshal(map[string]string{"error": clipText(message, 200)})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}
