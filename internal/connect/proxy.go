package connect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// MaxBody is the largest decoded request body. It matches the guest buffer.
	MaxBody = 32 << 20
	// RelayBody is the largest upstream body copied into a JSON result.
	// The guest reads at most MaxBody bytes of that JSON, and base64 grows by 4/3.
	RelayBody = (MaxBody - 8192) / 4 * 3

	// Outcome values are audit words. They are not secret values.
	OutcomeOK       = "ok"
	OutcomeDenied   = "denied"
	OutcomeExpired  = "expired"
	OutcomeNoGrant  = "no_grant"
	OutcomeAllow    = "off_allowlist"
	OutcomeApproval = "approval_required"
	OutcomeRedirect = "redirect_refused"
	OutcomeUpstream = "upstream"
	OutcomeBad      = "bad_request"
)

var (
	// ErrAllow means the destination is not on the connection allowlist.
	ErrAllow = errors.New("off_allowlist")
	// ErrNoGrant means this agent has no live connection grant.
	ErrNoGrant = errors.New("no_grant")
	// ErrDenied means the user denied the write, or the approval was missing.
	ErrDenied = errors.New("denied")
	// ErrExpired means the approval or secret card timed out.
	ErrExpired = errors.New("expired")
	// ErrBad means the guest request itself was refused.
	ErrBad = errors.New("bad_request")
	// ErrUpstream means the allowlisted host did not answer.
	ErrUpstream = errors.New("upstream")
	// ErrRedirect means a redirect was not followed.
	ErrRedirect = errors.New("redirect_refused")
	// ErrApproval means a write is waiting on a host card.
	ErrApproval = errors.New("approval_required")

	errAllow    = ErrAllow
	errPrivate  = ErrAllow
	errNoGrant  = ErrNoGrant
	errBad      = ErrBad
	errUpstream = ErrUpstream
	errDenied   = ErrDenied
	errExpired  = ErrExpired
	errRedirect = ErrRedirect
)

// Call is one guest request. URL is a public https URL. Headers are optional
// and untrusted. ApprovalID is set when the host already has an approval.
type Call struct {
	Connection string
	Method     string
	URL        string
	Headers    map[string]string
	Body       []byte
	Tool       string
	ApprovalID string
	// Hash binds an approval to this exact call. Summary is the card text.
	Hash    string
	Summary string
}

// Result is the upstream response after the token has been stripped.
type Result struct {
	Status  int
	Type    string
	Headers map[string]string
	Body    []byte
	Outcome string
}

// Dial overrides the production resolver. Tests use it to reach a local
// upstream. The allowlist check still runs before DialContext.
type Dial struct {
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
	Roots       *x509.CertPool
}

// Do forwards one allowlisted request. token is added to the upstream call
// and removed from anything returned. A nil dial uses the public resolver.
func Do(ctx context.Context, token string, call Call, tr *Dial) (Result, error) {
	if strings.TrimSpace(token) == "" {
		return Result{Outcome: OutcomeNoGrant}, errNoGrant
	}
	if !MethodOK(call.Method) {
		return Result{Outcome: OutcomeBad}, errBad
	}
	if tokenIn(token, call.URL) || tokenIn(token, string(call.Body)) {
		return Result{Outcome: OutcomeAllow}, errAllow
	}
	u, err := CheckURL(call.Connection, call.URL)
	if err != nil {
		return Result{Outcome: OutcomeAllow}, errAllow
	}
	method := strings.ToUpper(call.Method)
	var body io.Reader
	if len(call.Body) > 0 && method != "GET" && method != "HEAD" {
		if len(call.Body) > MaxBody {
			return Result{Outcome: OutcomeBad}, errBad
		}
		body = strings.NewReader(string(call.Body))
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return Result{Outcome: OutcomeBad}, errBad
	}
	applyGuestHeaders(req, call.Headers, token)
	applyAuth(req, call.Connection, token)
	client := clientFor(call.Connection, token, tr)
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, errRedirect) {
			return Result{Outcome: OutcomeRedirect}, errRedirect
		}
		if errors.Is(err, errAllow) {
			return Result{Outcome: OutcomeAllow}, errAllow
		}
		return Result{Outcome: OutcomeUpstream}, errUpstream
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return Result{Outcome: OutcomeRedirect}, errRedirect
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, RelayBody+1))
	if err != nil {
		return Result{Outcome: OutcomeUpstream}, errUpstream
	}
	if len(raw) > RelayBody {
		return Result{Outcome: OutcomeBad}, errBad
	}
	raw = []byte(Redact(token, string(raw)))
	headers := relayHeaders(res.Header, token)
	return Result{Status: res.StatusCode, Type: headers["content-type"], Headers: headers, Body: raw, Outcome: OutcomeOK}, nil
}

func clientFor(connection, token string, tr *Dial) *http.Client {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	if tr != nil && tr.Roots != nil {
		tlsConf.RootCAs = tr.Roots
	}
	transport := &http.Transport{
		Proxy:               nil,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   false,
		TLSClientConfig:     tlsConf,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, errAllow
			}
			if !HostAllowed(connection, host) {
				return nil, errAllow
			}
			if tr != nil && tr.DialContext != nil {
				return tr.DialContext(ctx, network, addr)
			}
			return dialPublic(ctx, host, port)
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errRedirect
			}
			if _, err := CheckURL(connection, req.URL.String()); err != nil {
				return errRedirect
			}
			applyAuth(req, connection, token)
			return nil
		},
	}
}

// publicResolvers are queried when the system answer has no public IPv4.
// The system resolver is still first. These are numeric addresses so the
// fallback does not depend on the same resolver.
var publicResolvers = []string{"1.1.1.1:53", "8.8.8.8:53"}

func dialPublic(ctx context.Context, host, port string) (net.Conn, error) {
	ips, err := resolvePublic(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errPrivate
	}
	if len(ips) > 3 {
		ips = ips[:3]
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	var last error
	for _, ip := range ips {
		conn, dialErr := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		last = dialErr
	}
	if last == nil {
		return nil, errPrivate
	}
	return nil, last
}

func resolvePublic(ctx context.Context, host string) ([]net.IP, error) {
	system, _ := lookupSystem(ctx, host)
	v4, _ := splitPublic(system)
	var fallback []net.IP
	if len(v4) == 0 {
		fallback, _ = lookupPublicDNS(ctx, host)
	}
	chosen := addressesFor(system, fallback)
	if len(chosen) == 0 {
		return nil, errPrivate
	}
	return chosen, nil
}

func lookupSystem(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	return ipAddrs(addrs), nil
}

func lookupPublicDNS(ctx context.Context, host string) ([]net.IP, error) {
	var last error
	qname := host
	if !strings.HasSuffix(qname, ".") {
		qname += "."
	}
	for _, server := range publicResolvers {
		lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		ips, err := lookupAt(lookupCtx, server, qname)
		cancel()
		if err != nil {
			last = err
			continue
		}
		v4, _ := splitPublic(ips)
		if len(v4) > 0 {
			return ips, nil
		}
		last = errPrivate
	}
	if last == nil {
		last = errPrivate
	}
	return nil, last
}

func lookupAt(ctx context.Context, server, host string) ([]net.IP, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if network != "udp" && network != "tcp" {
				network = "udp"
			}
			var d net.Dialer
			return d.DialContext(ctx, network, server)
		},
	}
	addrs, err := r.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	return ipAddrs(addrs), nil
}

func ipAddrs(addrs []net.IPAddr) []net.IP {
	out := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IP != nil {
			out = append(out, addr.IP)
		}
	}
	return out
}

func applyAuth(req *http.Request, connection, token string) {
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	req.Header.Del("X-Access-Token")
	entry, ok := Lookup(connection)
	mode := AuthBearer
	header := ""
	gitHost := ""
	if ok {
		if entry.Auth != "" {
			mode = entry.Auth
		}
		header = entry.HeaderName
		gitHost = entry.GitHost
	}
	switch mode {
	case AuthGitHub:
		if req.URL != nil && gitHost != "" && req.URL.Hostname() == gitHost {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	case AuthBasic:
		req.SetBasicAuth(token, "")
	case AuthHeader:
		if header != "" {
			req.Header.Set(header, token)
		}
	default:
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func applyGuestHeaders(req *http.Request, h map[string]string, token string) {
	if len(h) == 0 || len(h) > 40 {
		return
	}
	for key, val := range h {
		name := strings.ToLower(strings.TrimSpace(key))
		if name == "" || droppedReqHeader(name) || len(val) > 4096 || strings.ContainsAny(val, "\r\n") || tokenIn(token, val) {
			continue
		}
		req.Header.Set(name, val)
	}
}

func droppedReqHeader(key string) bool {
	switch key {
	case "authorization", "proxy-authorization", "cookie", "set-cookie",
		"x-cast-authorization", "x-castbot-approval", "host", "connection",
		"transfer-encoding", "content-length", "trailer", "upgrade",
		"keep-alive", "te", "proxy-connection", "accept-encoding":
		return true
	default:
		return false
	}
}

func relayHeaders(h http.Header, token string) map[string]string {
	out := map[string]string{}
	for key, vals := range h {
		if len(vals) == 0 || !allowRelayHeader(key) {
			continue
		}
		val := vals[0]
		if strings.ContainsAny(val, "\r\n") || len(val) > 4096 || tokenIn(token, val) {
			continue
		}
		out[strings.ToLower(key)] = Redact(token, val)
	}
	return out
}

func allowRelayHeader(key string) bool {
	key = strings.ToLower(key)
	switch key {
	case "content-type", "cache-control", "etag", "content-disposition", "mcp-session-id":
		return true
	default:
		return strings.HasPrefix(key, "x-git-")
	}
}

func tokenIn(token, text string) bool {
	if token == "" || text == "" {
		return false
	}
	return strings.Contains(text, token)
}

// Redact removes a token and its basic-auth encoding from text.
func Redact(token, text string) string {
	if token == "" || text == "" {
		return text
	}
	text = strings.ReplaceAll(text, token, "[redacted]")
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	text = strings.ReplaceAll(text, basic, "[redacted]")
	bearer := base64.StdEncoding.EncodeToString([]byte(token))
	if len(token) >= 8 {
		text = strings.ReplaceAll(text, bearer, "[redacted]")
	}
	return text
}

// Code maps an error from this package onto an outcome word.
func Code(err error) string {
	if err == nil {
		return OutcomeOK
	}
	switch {
	case errors.Is(err, errRedirect):
		return OutcomeRedirect
	case errors.Is(err, ErrApproval):
		return OutcomeApproval
	case errors.Is(err, errAllow), errors.Is(err, errPrivate):
		return OutcomeAllow
	case errors.Is(err, errNoGrant):
		return OutcomeNoGrant
	case errors.Is(err, errDenied):
		return OutcomeDenied
	case errors.Is(err, errExpired):
		return OutcomeExpired
	case errors.Is(err, errBad):
		return OutcomeBad
	default:
		return OutcomeUpstream
	}
}
