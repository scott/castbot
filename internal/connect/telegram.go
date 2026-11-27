package connect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	Telegram           = "telegram"
	SecretNameTelegram = "TELEGRAM_CONNECTION"
	telegramHost       = "api.telegram.org"
)

// telegramDial replaces the public dial in tests. Production leaves it nil.
// telegramRoots is the test certificate pool.
var (
	telegramDial  func(ctx context.Context, host, port string) (net.Conn, error)
	telegramRoots *x509.CertPool
)

func init() {
	register(telegramEntry())
}

func telegramEntry() Entry {
	return Entry{
		ID: Telegram, Title: "Telegram", SecretName: SecretNameTelegram,
		Detail:     "Bot token and one chat id. That chat is the only destination. Stored in your vault.",
		TokenLabel: "Bot token",
		ExtraLabel: "Chat id",
		Hosts:      []string{telegramHost},
		APIHost:    telegramHost,
		Auth:       AuthBearer,
		Aliases:    []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_TOKEN", "TELEGRAM_API_TOKEN"},
		Tools:      telegramTools,
	}
}

var telegramTools = []Tool{
	{
		Name: "telegram_read", Title: "Telegram: read", Method: "GET",
		Args: []Arg{{Name: "limit", Description: "How many messages, at most 20."}},
		Run:  telegramRead,
	},
	{
		Name: "telegram_send", Title: "Telegram: send", Write: true, Method: "POST",
		Args: []Arg{{Name: "text", Description: "Message text.", Required: true}},
		Run:  telegramSend,
	},
}

func telegramRead(ctx context.Context, args map[string]any) ([]byte, error) {
	token, chat, err := telegramCreds(ctx)
	if err != nil {
		return nil, err
	}
	limit, err := telegramLimit(args)
	if err != nil {
		return nil, err
	}
	// No offset. Passing one would acknowledge updates and drop them.
	raw, err := telegramCall(ctx, token, "getUpdates", map[string]any{
		"limit":           limit,
		"timeout":         0,
		"allowed_updates": []string{"message", "channel_post"},
	})
	if err != nil {
		return nil, err
	}
	return telegramReadBody(raw, chat), nil
}

func telegramSend(ctx context.Context, args map[string]any) ([]byte, error) {
	token, chat, err := telegramCreds(ctx)
	if err != nil {
		return nil, err
	}
	text, err := telegramText(args)
	if err != nil {
		return nil, err
	}
	raw, err := telegramCall(ctx, token, "sendMessage", map[string]any{
		"chat_id": chat,
		"text":    text,
	})
	if err != nil {
		return nil, err
	}
	return telegramSendBody(raw), nil
}

func telegramCreds(ctx context.Context) (token, chat string, err error) {
	token, chat = VaultFrom(ctx)
	token = strings.TrimSpace(token)
	chat = strings.TrimSpace(chat)
	if !telegramToken(token) || !telegramChat(chat) {
		return "", "", ErrBad
	}
	return token, chat, nil
}

func telegramToken(s string) bool {
	id, secret, ok := strings.Cut(s, ":")
	if !ok || strings.Contains(secret, ":") {
		return false
	}
	if len(id) < 6 || len(id) > 16 || len(secret) < 20 || len(secret) > 64 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	for _, r := range secret {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func telegramChat(s string) bool {
	if len(s) < 1 || len(s) > 20 {
		return false
	}
	i := 0
	if s[0] == '-' {
		if len(s) == 1 {
			return false
		}
		i = 1
	}
	if s[i] == '0' {
		return false
	}
	for _, r := range s[i:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func telegramText(args map[string]any) (string, error) {
	val, ok := argString(args, "text")
	if !ok || val == "" || len(val) > 4000 || strings.ContainsAny(val, "\r\x00") {
		return "", ErrBad
	}
	return val, nil
}

func telegramLimit(args map[string]any) (int, error) {
	const max = 20
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
	return max, nil
}

// telegramCall posts one fixed method. The Bot API takes the token in the
// path. The shared proxy refuses a token in a URL, so this dials the host
// itself and the caller returns a projected body, never this URL.
func telegramCall(ctx context.Context, token, method string, payload any) ([]byte, error) {
	if method != "getUpdates" && method != "sendMessage" {
		return nil, ErrBad
	}
	if _, err := CheckURL(Telegram, "https://"+telegramHost+"/"+method); err != nil {
		return nil, ErrAllow
	}
	body, err := json.Marshal(payload)
	if err != nil || tokenIn(token, string(body)) {
		return nil, ErrBad
	}
	page := &url.URL{Scheme: "https", Host: telegramHost, Path: "/bot" + token + "/" + method}
	if page.User != nil || page.Hostname() != telegramHost || strings.Contains(page.Hostname(), token) {
		return nil, ErrAllow
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+telegramHost+"/", strings.NewReader(string(body)))
	if err != nil {
		return nil, ErrBad
	}
	req.URL = page
	req.Host = telegramHost
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
		Transport: &http.Transport{
			Proxy:             nil,
			ForceAttemptHTTP2: false,
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: telegramRoots, ServerName: telegramHost},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, splitErr := net.SplitHostPort(addr)
				if splitErr != nil {
					return nil, ErrAllow
				}
				return telegramTCP(ctx, host, port)
			},
		},
	}
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, ErrRedirect) {
			return nil, ErrRedirect
		}
		return nil, ErrUpstream
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrUpstream
	}
	raw = []byte(Redact(token, string(raw)))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if msg := telegramDescription(raw); msg != "" {
			return telegramErr(msg), nil
		}
		return nil, ErrUpstream
	}
	return raw, nil
}

func telegramTCP(ctx context.Context, host, port string) (net.Conn, error) {
	if host != telegramHost || port != "443" {
		return nil, ErrAllow
	}
	if telegramDial != nil {
		return telegramDial(ctx, host, port)
	}
	return dialPublic(ctx, host, port)
}

func telegramReadBody(body []byte, chat string) []byte {
	var raw struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      []struct {
			Message     *telegramMessage `json:"message"`
			ChannelPost *telegramMessage `json:"channel_post"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	if !raw.OK {
		return telegramErr(raw.Description)
	}
	type item struct {
		ID   int64  `json:"id"`
		Text string `json:"text"`
		Date int64  `json:"date"`
	}
	out := []item{}
	for _, update := range raw.Result {
		msg := update.Message
		if msg == nil {
			msg = update.ChannelPost
		}
		if msg == nil || msg.Chat.ID.String() != chat {
			continue
		}
		if len(out) >= 20 {
			break
		}
		out = append(out, item{ID: msg.MessageID, Text: clipText(msg.Text, 2000), Date: msg.Date})
	}
	buf, err := json.Marshal(map[string]any{"messages": out})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

func telegramSendBody(body []byte) []byte {
	var raw struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return []byte(`{"error":"bad response"}`)
	}
	if !raw.OK {
		return telegramErr(raw.Description)
	}
	buf, err := json.Marshal(map[string]any{"sent": true, "message_id": raw.Result.MessageID})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}

type telegramMessage struct {
	MessageID int64  `json:"message_id"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
	Chat      struct {
		ID json.Number `json:"id"`
	} `json:"chat"`
}

func telegramDescription(body []byte) string {
	var raw struct {
		Description string `json:"description"`
	}
	if json.Unmarshal(body, &raw) != nil {
		return ""
	}
	return raw.Description
}

func telegramErr(message string) []byte {
	if strings.TrimSpace(message) == "" {
		message = "request failed"
	}
	buf, err := json.Marshal(map[string]string{"error": clipText(message, 200)})
	if err != nil {
		return []byte(`{"error":"bad response"}`)
	}
	return buf
}
