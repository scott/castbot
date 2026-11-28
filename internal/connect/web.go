package connect

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const (
	Web           = "web"
	SecretNameWeb = "WEB_CONNECTION"

	// webReadCap is the most text a page read returns.
	webReadCap = 200 << 10
	// webParseCap bounds the HTML the extractor walks.
	webParseCap = 2 << 20
)

func init() {
	register(webEntry())
}

func webEntry() Entry {
	return Entry{
		ID: Web, Title: "Web", SecretName: SecretNameWeb,
		Detail:     "Search key. Search uses it. Reading a page does not. Stored in your vault.",
		TokenLabel: "Search key",
		Hosts:      []string{"api.search.brave.com"},
		APIHost:    "api.search.brave.com",
		Auth:       AuthHeader,
		HeaderName: "X-Subscription-Token",
		Aliases:    []string{"BRAVE_API_KEY", "BRAVE_SEARCH_API_KEY", "BRAVE_SUBSCRIPTION_TOKEN"},
		Tools:      webTools,
	}
}

var webTools = []Tool{
	{
		Name: "web_search", Title: "Web: search", Method: "GET",
		Args: []Arg{
			{Name: "query", Description: "Search words.", Required: true},
			{Name: "count", Description: "How many results, at most 10."},
		},
		Build:    webSearchCall,
		ReadBody: webSearchBody,
	},
	{
		Name: "web_read", Title: "Web: read a page", Method: "GET", PublicURL: true,
		Args: []Arg{
			{Name: "url", Description: "One public https page.", Required: true},
		},
		Build:    webReadCall,
		ReadBody: webReadBody,
	},
}

func webSearchCall(_ context.Context, args map[string]any) (Call, error) {
	query, ok := argString(args, "query")
	if !ok || query == "" || len(query) > 400 || strings.ContainsAny(query, "\r\n\x00") {
		return Call{}, ErrBad
	}
	count := 10
	if raw, ok := argString(args, "count"); ok && raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Call{}, ErrBad
		}
		if n > 10 {
			n = 10
		}
		count = n
	}
	raw := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query) + "&count=" + strconv.Itoa(count)
	return Call{
		Method:  "GET",
		URL:     raw,
		Headers: map[string]string{"Accept": "application/json"},
	}, nil
}

func webReadCall(_ context.Context, args map[string]any) (Call, error) {
	raw, ok := argString(args, "url")
	if !ok {
		return Call{}, ErrBad
	}
	page, err := CheckPublicURL(raw)
	if err != nil {
		return Call{}, ErrBad
	}
	return Call{
		Method:  "GET",
		URL:     page.String(),
		Headers: map[string]string{"Accept": "text/html, text/plain;q=0.9"},
	}, nil
}

func webSearchBody(body []byte) []byte {
	var parsed struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	out := struct {
		Results []webHit `json:"results"`
	}{Results: []webHit{}}
	if json.Unmarshal(body, &parsed) == nil {
		for _, item := range parsed.Web.Results {
			if len(out.Results) >= 10 {
				break
			}
			out.Results = append(out.Results, webHit{
				Title:   clipText(item.Title, 300),
				URL:     clipText(item.URL, 2000),
				Snippet: clipText(item.Description, 500),
			})
		}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return []byte(`{"results":[]}`)
	}
	return raw
}

type webHit struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

func webReadBody(body []byte) []byte {
	text := pageText(body)
	if text == "" {
		return []byte("No text.")
	}
	return []byte(text)
}

func pageText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if len(body) > webParseCap {
		body = body[:webParseCap]
	}
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return clipText(collapseSpace(string(body)), webReadCap)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil || b.Len() > webReadCap {
			return
		}
		if n.Type == html.ElementNode && skipPageTag(n.Data) {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return clipText(collapseSpace(b.String()), webReadCap)
}

func skipPageTag(name string) bool {
	switch strings.ToLower(name) {
	case "script", "style", "noscript", "template", "svg":
		return true
	default:
		return false
	}
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
