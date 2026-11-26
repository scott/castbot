package connect

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

const (
	Notion           = "notion"
	SecretNameNotion = "NOTION_CONNECTION"
	notionVersion    = "2022-06-28"
)

func init() {
	register(notionEntry())
}

func notionEntry() Entry {
	return Entry{
		ID: Notion, Title: "Notion", SecretName: SecretNameNotion,
		Detail:     "Integration token for pages shared with it. Stored in your vault.",
		TokenLabel: "Integration token",
		Hosts:      []string{"api.notion.com"},
		APIHost:    "api.notion.com",
		Auth:       AuthBearer,
		Aliases:    []string{"NOTION_API_KEY", "NOTION_TOKEN", "NOTION_INTEGRATION_TOKEN"},
		Tools:      notionTools,
	}
}

var notionTools = []Tool{
	{
		Name: "notion_search", Title: "Notion: search", Method: "POST",
		Args: []Arg{
			{Name: "query", Description: "Text to find. Optional."},
			{Name: "limit", Description: "How many pages, at most 20."},
		},
		Build: notionSearchCall, ReadBody: notionSearchBody,
	},
	{
		Name: "notion_get_page", Title: "Notion: read a page", Method: "GET",
		Args:  []Arg{{Name: "page", Description: "Page id.", Required: true}},
		Build: notionPageCall, ReadBody: notionPageBody,
	},
	{
		Name: "notion_get_blocks", Title: "Notion: read blocks", Method: "GET",
		Args: []Arg{
			{Name: "block", Description: "Page or block id.", Required: true},
			{Name: "limit", Description: "How many blocks, at most 20."},
		},
		Build: notionBlocksCall, ReadBody: notionBlocksBody,
	},
	{
		Name: "notion_create_page", Title: "Notion: create a page", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "page", Description: "Parent page id.", Required: true},
			{Name: "title", Description: "Page title.", Required: true},
		},
		Build: notionCreateCall, ReadBody: notionPageBody,
	},
	{
		Name: "notion_append", Title: "Notion: append", Write: true, Method: "PATCH",
		Args: []Arg{
			{Name: "block", Description: "Page or block id.", Required: true},
			{Name: "text", Description: "Paragraph to add.", Required: true},
		},
		Build: notionAppendCall, ReadBody: notionAppendBody,
	},
}

func notionSearchCall(_ context.Context, args map[string]any) (Call, error) {
	limit, err := notionLimit(args, 10, 20)
	if err != nil {
		return Call{}, err
	}
	query, err := notionText(args, "query", false, 200)
	if err != nil {
		return Call{}, err
	}
	payload := map[string]any{
		"page_size": limit,
		"filter":    map[string]any{"value": "page", "property": "object"},
	}
	if query != "" {
		payload["query"] = query
	}
	return notionCall("POST", "https://api.notion.com/v1/search", payload)
}

func notionPageCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := notionID(args, "page")
	if err != nil {
		return Call{}, err
	}
	return notionCall("GET", "https://api.notion.com/v1/pages/"+url.PathEscape(id), nil)
}

func notionBlocksCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := notionID(args, "block")
	if err != nil {
		return Call{}, err
	}
	limit, err := notionLimit(args, 20, 20)
	if err != nil {
		return Call{}, err
	}
	return notionCall("GET", "https://api.notion.com/v1/blocks/"+url.PathEscape(id)+"/children?page_size="+strconv.Itoa(limit), nil)
}

func notionCreateCall(_ context.Context, args map[string]any) (Call, error) {
	parent, err := notionID(args, "page")
	if err != nil {
		return Call{}, err
	}
	title, err := notionText(args, "title", true, 200)
	if err != nil {
		return Call{}, err
	}
	return notionCall("POST", "https://api.notion.com/v1/pages", map[string]any{
		"parent": map[string]any{"page_id": parent},
		"properties": map[string]any{
			"title": map[string]any{
				"title": []any{map[string]any{"text": map[string]any{"content": title}}},
			},
		},
	})
}

func notionAppendCall(_ context.Context, args map[string]any) (Call, error) {
	id, err := notionID(args, "block")
	if err != nil {
		return Call{}, err
	}
	text, err := notionText(args, "text", true, 2000)
	if err != nil {
		return Call{}, err
	}
	return notionCall("PATCH", "https://api.notion.com/v1/blocks/"+url.PathEscape(id)+"/children", map[string]any{
		"children": []any{map[string]any{
			"object": "block",
			"type":   "paragraph",
			"paragraph": map[string]any{
				"rich_text": []any{map[string]any{"type": "text", "text": map[string]any{"content": text}}},
			},
		}},
	})
}

func notionCall(method, rawURL string, payload any) (Call, error) {
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
			"Accept":         "application/json",
			"Content-Type":   "application/json",
			"Notion-Version": notionVersion,
		},
		Body: body,
	}, nil
}

func notionID(args map[string]any, key string) (string, error) {
	val, ok := argString(args, key)
	if !ok || val == "" {
		return "", ErrBad
	}
	if len(val) == 36 {
		for i, r := range val {
			switch i {
			case 8, 13, 18, 23:
				if r != '-' {
					return "", ErrBad
				}
			default:
				if !notionHex(r) {
					return "", ErrBad
				}
			}
		}
		return val, nil
	}
	if len(val) != 32 {
		return "", ErrBad
	}
	for _, r := range val {
		if !notionHex(r) {
			return "", ErrBad
		}
	}
	return val, nil
}

func notionHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func notionText(args map[string]any, key string, required bool, max int) (string, error) {
	val, ok := argString(args, key)
	if !ok || val == "" {
		if required {
			return "", ErrBad
		}
		return "", nil
	}
	if len(val) > max || strings.ContainsAny(val, "\r\x00") {
		return "", ErrBad
	}
	return val, nil
}

func notionLimit(args map[string]any, fallback, max int) (int, error) {
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

func notionClip(body []byte) []byte {
	if len(body) > 256<<10 {
		return body[:256<<10]
	}
	return body
}

func notionSearchBody(body []byte) []byte {
	var raw struct {
		Results []notionPageJSON `json:"results"`
		Message string           `json:"message"`
	}
	if json.Unmarshal(notionClip(body), &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	if raw.Message != "" && len(raw.Results) == 0 {
		return notionErr(raw.Message)
	}
	out := []map[string]string{}
	for i, page := range raw.Results {
		if i >= 20 {
			break
		}
		out = append(out, map[string]string{"id": page.ID, "url": page.URL, "title": notionPageTitle(page.Properties)})
	}
	buf, err := json.Marshal(map[string]any{"pages": out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func notionPageBody(body []byte) []byte {
	var raw notionPageJSON
	if json.Unmarshal(notionClip(body), &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	if raw.Message != "" && raw.ID == "" {
		return notionErr(raw.Message)
	}
	buf, err := json.Marshal(map[string]string{"id": raw.ID, "url": raw.URL, "title": notionPageTitle(raw.Properties)})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func notionBlocksBody(body []byte) []byte {
	var raw struct {
		Results []map[string]any `json:"results"`
		Message string           `json:"message"`
	}
	if json.Unmarshal(notionClip(body), &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	if raw.Message != "" && len(raw.Results) == 0 {
		return notionErr(raw.Message)
	}
	type block struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Text string `json:"text"`
	}
	out := []block{}
	for i, item := range raw.Results {
		if i >= 20 {
			break
		}
		id, _ := item["id"].(string)
		kind, _ := item["type"].(string)
		out = append(out, block{ID: id, Type: kind, Text: clipText(notionBlockText(item), 2000)})
	}
	buf, err := json.Marshal(map[string]any{"blocks": out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func notionAppendBody(body []byte) []byte {
	var raw struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
		Message string `json:"message"`
	}
	if json.Unmarshal(notionClip(body), &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	if raw.Message != "" && len(raw.Results) == 0 {
		return notionErr(raw.Message)
	}
	ids := []string{}
	for i, item := range raw.Results {
		if i >= 20 {
			break
		}
		ids = append(ids, item.ID)
	}
	buf, err := json.Marshal(map[string]any{"appended": true, "blocks": ids})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

type notionPageJSON struct {
	ID         string         `json:"id"`
	URL        string         `json:"url"`
	Message    string         `json:"message"`
	Properties map[string]any `json:"properties"`
}

func notionPageTitle(props map[string]any) string {
	for _, raw := range props {
		obj, ok := raw.(map[string]any)
		if !ok || obj["type"] != "title" {
			continue
		}
		return clipText(notionRich(obj["title"]), 200)
	}
	return ""
}

func notionBlockText(block map[string]any) string {
	kind, _ := block["type"].(string)
	inner, _ := block[kind].(map[string]any)
	if inner == nil {
		return ""
	}
	if text := notionRich(inner["rich_text"]); text != "" {
		return text
	}
	return notionRich(inner["text"])
}

func notionRich(raw any) string {
	items, ok := raw.([]any)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if plain, ok := obj["plain_text"].(string); ok {
			b.WriteString(plain)
			continue
		}
		text, _ := obj["text"].(map[string]any)
		if text == nil {
			continue
		}
		if content, ok := text["content"].(string); ok {
			b.WriteString(content)
		}
	}
	return b.String()
}

func notionErr(message string) []byte {
	buf, err := json.Marshal(map[string]string{"error": clipText(message, 200)})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}
