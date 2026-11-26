package connect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	Google           = "google"
	SecretNameGoogle = "GOOGLE_CONNECTION"

	googleIMAP = "imap.gmail.com"
	googleSMTP = "smtp.gmail.com"
	googleCal  = "apidata.googleusercontent.com"

	googleTextCap = 64 << 10
)

// googleDial replaces the public dial in tests. Production leaves it nil.
// googleRoots is the test certificate pool for the calendar host.
var (
	googleDial  func(ctx context.Context, host, port string) (net.Conn, error)
	googleRoots *x509.CertPool
)

func init() {
	register(googleEntry())
}

func googleEntry() Entry {
	return Entry{
		ID: Google, Title: "Google", SecretName: SecretNameGoogle,
		Detail:     "Gmail address and an app password. Mail and calendar for that account. Stored in your vault.",
		TokenLabel: "App password",
		ExtraLabel: "Gmail address",
		Hosts:      []string{googleIMAP, googleSMTP, googleCal},
		APIHost:    googleCal,
		Auth:       AuthBasicPair,
		Aliases:    []string{"GMAIL_APP_PASSWORD", "GOOGLE_APP_PASSWORD"},
		Tools:      googleTools,
	}
}

var googleTools = []Tool{
	{
		Name: "google_list_messages", Title: "Google: list messages", Method: "GET",
		Args: []Arg{{Name: "limit", Description: "How many messages, at most 20."}},
		Run:  googleListMessages,
	},
	{
		Name: "google_get_message", Title: "Google: read a message", Method: "GET",
		Args: []Arg{{Name: "uid", Description: "Message id from the list.", Required: true}},
		Run:  googleGetMessage,
	},
	{
		Name: "google_create_draft", Title: "Google: create a draft", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "to", Description: "One recipient address.", Required: true},
			{Name: "subject", Description: "Subject.", Required: true},
			{Name: "body", Description: "Message text.", Required: true},
		},
		Run: googleCreateDraft,
	},
	{
		Name: "google_send", Title: "Google: send", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "to", Description: "One recipient address.", Required: true},
			{Name: "subject", Description: "Subject.", Required: true},
			{Name: "body", Description: "Message text.", Required: true},
		},
		Run: googleSend,
	},
	{
		Name: "google_list_events", Title: "Google: list events", Method: "GET",
		Args: []Arg{
			{Name: "start", Description: "Range start, as a time stamp. Optional."},
			{Name: "end", Description: "Range end, as a time stamp. Optional."},
		},
		Run: googleListEvents,
	},
	{
		Name: "google_create_event", Title: "Google: create an event", Write: true, Method: "POST",
		Args: []Arg{
			{Name: "title", Description: "Event title.", Required: true},
			{Name: "start", Description: "Start time.", Required: true},
			{Name: "end", Description: "End time.", Required: true},
			{Name: "description", Description: "Event text. Optional."},
		},
		Run: googleCreateEvent,
	},
}

func googleCreds(ctx context.Context) (email, secret string, err error) {
	secret, email = VaultFrom(ctx)
	email = strings.TrimSpace(email)
	secret = strings.ReplaceAll(strings.TrimSpace(secret), " ", "")
	if !googleEmail(email) || secret == "" || len(secret) > 200 || strings.ContainsAny(secret, "\"\\\r\n\x00") {
		return "", "", ErrBad
	}
	return email, secret, nil
}

func googleEmail(s string) bool {
	if len(s) < 6 || len(s) > 254 || strings.ContainsAny(s, " \t\r\n\"\\<>") {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 || at >= len(s)-4 {
		return false
	}
	domain := s[at+1:]
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || !strings.Contains(domain, ".") {
		return false
	}
	return true
}

func googleEndpoint(host, port string) bool {
	switch host {
	case googleIMAP:
		return port == "993"
	case googleSMTP:
		return port == "465"
	case googleCal:
		return port == "443"
	default:
		return false
	}
}

func googleTCP(ctx context.Context, host, port string) (net.Conn, error) {
	if !googleEndpoint(host, port) {
		return nil, ErrAllow
	}
	if googleDial != nil {
		return googleDial(ctx, host, port)
	}
	return dialPublic(ctx, host, port)
}

func googleTLS(ctx context.Context, host, port string) (net.Conn, error) {
	raw, err := googleTCP(ctx, host, port)
	if err != nil {
		return nil, err
	}
	if googleDial != nil {
		return raw, nil
	}
	secure := tls.Client(raw, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: googleRoots})
	if err := secure.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, ErrUpstream
	}
	return secure, nil
}

func googleDeadline(ctx context.Context, conn net.Conn) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(20 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
}

func googleHTTPS(ctx context.Context, method, rawURL, contentType string, body []byte) ([]byte, error) {
	switch method {
	case http.MethodGet, http.MethodPut, "REPORT":
	default:
		return nil, ErrBad
	}
	page, err := CheckURL(Google, rawURL)
	if err != nil {
		return nil, ErrAllow
	}
	email, secret, err := googleCreds(ctx)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = strings.NewReader(string(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, page.String(), reader)
	if err != nil {
		return nil, ErrBad
	}
	req.SetBasicAuth(email, secret)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if method == "REPORT" {
		req.Header.Set("Depth", "1")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
		Transport: &http.Transport{
			Proxy:             nil,
			ForceAttemptHTTP2: false,
			TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: googleRoots},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, splitErr := net.SplitHostPort(addr)
				if splitErr != nil {
					return nil, ErrAllow
				}
				return googleTCP(ctx, host, port)
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
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, ErrUpstream
	}
	raw = []byte(Redact(secret, string(raw)))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, ErrUpstream
	}
	return raw, nil
}

func googleLimit(args map[string]any, key string, fallback, max int) (int, error) {
	if args == nil {
		return fallback, nil
	}
	v, exists := args[key]
	if !exists || v == nil {
		return fallback, nil
	}
	raw, ok := argString(args, key)
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
