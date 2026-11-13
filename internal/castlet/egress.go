package castlet

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/scott/castbot/internal/egresscontract"
)

const (
	egressCacheTTL   = 5 * time.Second
	egressStatusWait = 2 * time.Second
	egressCallWait   = 60 * time.Second
	egressSecretWait = 61 * time.Minute
)

var secretShape = regexp.MustCompile(`(?i)(?:ghp_|github_pat_|gho_|dop_v1_|doo_|sk-)[A-Za-z0-9_\-]{8,}`)

// LoopbackAddr accepts only 127.0.0.1 with a port. Any other host is rejected
// so the egress cannot be bound on a reachable interface.
func LoopbackAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "127.0.0.1:8090"
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil || host != "127.0.0.1" {
		return "", errors.New("egress must listen on 127.0.0.1")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("egress must listen on 127.0.0.1")
	}
	return "127.0.0.1:" + strconv.Itoa(n), nil
}

// ServeEgress is the loopback API the guest stubs call. It does not check
// the machine token. The host does, when a call is actually forwarded.
// A peer that is not loopback is not served.
func (s *Supervisor) ServeEgress(w http.ResponseWriter, r *http.Request) {
	if !requestFromLoopback(r) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case r.URL.Path == "/status" && r.Method == http.MethodGet:
		s.writeEgressStatus(w, r)
	case r.URL.Path == "/http" && r.Method == http.MethodPost:
		s.egressHTTP(w, r)
	case r.URL.Path == "/mcp" && r.Method == http.MethodPost:
		s.egressMCP(w, r)
	case r.URL.Path == "/secret-request" && r.Method == http.MethodPost:
		s.egressSecret(w, r)
	case strings.HasPrefix(r.URL.Path, "/git/"):
		s.egressRaw(w, r, "/git/")
	case strings.HasPrefix(r.URL.Path, "/upstream/"):
		s.egressRaw(w, r, "/upstream/")
	default:
		http.NotFound(w, r)
	}
}

func requestFromLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Supervisor) writeEgressStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.egressStatus(r.Context()))
}

func (s *Supervisor) egressStatus(ctx context.Context) egresscontract.Status {
	s.mu.Lock()
	if !s.egressAt.IsZero() && time.Since(s.egressAt) < egressCacheTTL {
		view := s.egressView
		s.mu.Unlock()
		return view
	}
	token := s.token
	s.mu.Unlock()
	if token == "" || strings.TrimSpace(s.cfg.ControlURL) == "" {
		return egresscontract.Status{Providers: []string{}}.Normalize()
	}
	view := s.fetchEgressStatus(ctx, token)
	s.mu.Lock()
	s.egressView = view
	s.egressAt = time.Now()
	s.mu.Unlock()
	return view
}

func (s *Supervisor) fetchEgressStatus(ctx context.Context, token string) egresscontract.Status {
	disabled := egresscontract.Status{Providers: []string{}}.Normalize()
	ctx, cancel := context.WithTimeout(ctx, egressStatusWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api("/egress"), nil)
	if err != nil {
		return disabled
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client, err := s.egressClient(egressStatusWait)
	if err != nil {
		return disabled
	}
	res, err := client.Do(req)
	if err != nil {
		return disabled
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil || res.StatusCode != http.StatusOK {
		return disabled
	}
	var st egresscontract.Status
	if err := json.Unmarshal(body, &st); err != nil {
		return disabled
	}
	return st.Normalize()
}

func (s *Supervisor) egressHTTP(w http.ResponseWriter, r *http.Request) {
	var fwd egresscontract.Forward
	if err := json.NewDecoder(io.LimitReader(r.Body, egresscontract.BodyCap+1)).Decode(&fwd); err != nil {
		writeFailure(w, http.StatusBadRequest, "bad_request")
		return
	}
	fwd, ok := validateForward(fwd)
	if !ok {
		writeFailure(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !s.egressStatus(r.Context()).Has(fwd.Provider) {
		writeFailure(w, http.StatusForbidden, "not_connected")
		return
	}
	code, body, err := s.postEgress(r.Context(), "/egress/http", fwd, egressCallWait)
	if err != nil {
		writeFailure(w, http.StatusBadGateway, "not_connected")
		return
	}
	s.writeForwardJSON(w, code, body)
}

func (s *Supervisor) egressRaw(w http.ResponseWriter, r *http.Request, prefix string) {
	provider, upPath, ok := splitRaw(prefix, r.URL.Path)
	if !ok || len(r.URL.RawQuery) > 4096 {
		writePlain(w, http.StatusBadRequest, egresscontract.Rejected)
		return
	}
	if !s.egressStatus(r.Context()).Has(provider) {
		writePlain(w, http.StatusForbidden, egresscontract.NotConnected)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, egresscontract.BodyCap+1))
	if err != nil || len(body) > egresscontract.BodyCap {
		writePlain(w, http.StatusRequestEntityTooLarge, egresscontract.Rejected)
		return
	}
	fwd := egresscontract.Forward{
		Provider:   provider,
		Method:     r.Method,
		Path:       upPath,
		Query:      r.URL.RawQuery,
		Headers:    filterHeaderMap(flattenHeaders(r.Header)),
		ApprovalID: cleanApproval(r.Header.Get("X-Castbot-Approval")),
	}
	fwd, ok = validateForward(fwd)
	if !ok {
		writePlain(w, http.StatusBadRequest, egresscontract.Rejected)
		return
	}
	if len(body) > 0 {
		fwd.BodyB64 = base64.StdEncoding.EncodeToString(body)
	}
	suffix := "/egress/http"
	if prefix == "/git/" {
		suffix = "/egress/git"
	}
	code, raw, err := s.postEgress(r.Context(), suffix, fwd, egressCallWait)
	if err != nil {
		writePlain(w, http.StatusBadGateway, egresscontract.NotConnected)
		return
	}
	if code != http.StatusOK {
		writePlain(w, http.StatusForbidden, failureMessage(raw))
		return
	}
	var result egresscontract.ForwardResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writePlain(w, http.StatusBadGateway, egresscontract.Rejected)
		return
	}
	writeRelay(w, result)
}

func (s *Supervisor) egressMCP(w http.ResponseWriter, r *http.Request) {
	var call egresscontract.MCPCall
	if err := json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&call); err != nil {
		writeFailure(w, http.StatusBadRequest, "bad_request")
		return
	}
	if call.Server != "github" && call.Server != "digitalocean" {
		writeFailure(w, http.StatusBadRequest, "bad_request")
		return
	}
	if len(call.Message) == 0 || call.Message[0] != '{' {
		writeFailure(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !s.egressStatus(r.Context()).Has(call.Server) {
		writeFailure(w, http.StatusForbidden, "not_connected")
		return
	}
	code, body, err := s.postEgress(r.Context(), "/egress/mcp", call, egressCallWait)
	if err != nil || code != http.StatusOK {
		errCode := "not_connected"
		if code == http.StatusForbidden {
			errCode = failureCode(body)
		}
		writeFailure(w, http.StatusForbidden, errCode)
		return
	}
	var result egresscontract.MCPResult
	if err := json.Unmarshal(body, &result); err != nil || len(result.Message) == 0 || result.Message[0] != '{' {
		writeFailure(w, http.StatusBadGateway, "bad_request")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(egresscontract.MCPResult{Message: result.Message})
}

func (s *Supervisor) egressSecret(w http.ResponseWriter, r *http.Request) {
	var call egresscontract.SecretCall
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&call); err != nil {
		writeFailure(w, http.StatusBadRequest, "bad_request")
		return
	}
	call.Connection = strings.ToLower(strings.TrimSpace(call.Connection))
	if !egresscontract.KnownProvider(call.Connection) {
		writeFailure(w, http.StatusBadRequest, "bad_request")
		return
	}
	call.Reason = scrubReason(call.Reason)
	if !validSecretName(call.Name) {
		call.Name = ""
	}
	if !s.egressStatus(r.Context()).Has(call.Connection) {
		writeFailure(w, http.StatusForbidden, "not_connected")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), egressSecretWait)
	defer cancel()
	code, body, err := s.postEgress(ctx, "/egress/secret-request", call, egressSecretWait)
	if err != nil || code != http.StatusOK {
		errCode := "not_connected"
		if code == http.StatusForbidden {
			errCode = failureCode(body)
		}
		writeFailure(w, http.StatusForbidden, errCode)
		return
	}
	var result egresscontract.SecretResult
	if err := json.Unmarshal(body, &result); err != nil || !egresscontract.KnownSecretResult(result.Result) {
		writeFailure(w, http.StatusBadGateway, "bad_request")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(egresscontract.SecretResult{Result: result.Result})
}

func (s *Supervisor) postEgress(ctx context.Context, suffix string, payload any, timeout time.Duration) (int, []byte, error) {
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token == "" {
		return 0, nil, errors.New("egress unavailable")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, errors.New("egress unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.api(suffix), bytes.NewReader(raw))
	if err != nil {
		return 0, nil, errors.New("egress unavailable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client, err := s.egressClient(timeout)
	if err != nil {
		return 0, nil, errors.New("egress unavailable")
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, errors.New("egress unavailable")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, egresscontract.BodyCap+1))
	if err != nil || len(body) > egresscontract.BodyCap {
		return 0, nil, errors.New("egress unavailable")
	}
	return res.StatusCode, body, nil
}

func (s *Supervisor) writeForwardJSON(w http.ResponseWriter, code int, body []byte) {
	if code != http.StatusOK {
		errCode := "not_connected"
		if code == http.StatusForbidden {
			errCode = failureCode(body)
		}
		writeFailure(w, http.StatusForbidden, errCode)
		return
	}
	var result egresscontract.ForwardResult
	if err := json.Unmarshal(body, &result); err != nil {
		writeFailure(w, http.StatusBadGateway, "bad_request")
		return
	}
	if result.Status >= 300 && result.Status < 400 {
		writeFailure(w, http.StatusBadGateway, "redirect_refused")
		return
	}
	if _, err := decodeBody(result.BodyB64); err != nil {
		writeFailure(w, http.StatusBadGateway, "bad_request")
		return
	}
	result.Headers = filterRelayHeaders(result.Headers)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Supervisor) egressClient(timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{
		Proxy:               nil,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
	if s.cfg.CAFile != "" {
		raw, err := os.ReadFile(s.cfg.CAFile)
		if err != nil {
			return nil, errors.New("egress unavailable")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("egress unavailable")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func validateForward(fwd egresscontract.Forward) (egresscontract.Forward, bool) {
	if !egresscontract.KnownProvider(fwd.Provider) {
		return fwd, false
	}
	fwd.Method = strings.ToUpper(strings.TrimSpace(fwd.Method))
	switch fwd.Method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return fwd, false
	}
	cleaned, ok := cleanForwardPath(fwd.Path)
	if !ok {
		return fwd, false
	}
	fwd.Path = cleaned
	if len(fwd.Query) > 4096 || strings.ContainsAny(fwd.Query, "\r\n") {
		return fwd, false
	}
	fwd.Headers = filterHeaderMap(fwd.Headers)
	fwd.ApprovalID = cleanApproval(fwd.ApprovalID)
	if fwd.BodyB64 != "" {
		if _, err := decodeBody(fwd.BodyB64); err != nil {
			return fwd, false
		}
	}
	return fwd, true
}

func cleanForwardPath(p string) (string, bool) {
	if p == "" || !strings.HasPrefix(p, "/") || len(p) > 2048 {
		return "", false
	}
	lower := strings.ToLower(p)
	if strings.Contains(p, "..") || strings.Contains(p, "://") || strings.ContainsAny(p, "\\\x00\r\n") ||
		strings.Contains(lower, "%2e") || strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%00") {
		return "", false
	}
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") || strings.Contains(cleaned, "..") {
		return "", false
	}
	return cleaned, true
}

func cleanApproval(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 80 {
		return ""
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return ""
	}
	return id
}

func flattenHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for key, vals := range h {
		out[key] = strings.Join(vals, ", ")
	}
	return out
}

func filterHeaderMap(in map[string]string) map[string]string {
	out := map[string]string{}
	if len(in) > 40 {
		return out
	}
	for key, val := range in {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || droppedHeader(key) || len(val) > 4096 || strings.ContainsAny(val, "\r\n") {
			continue
		}
		out[key] = val
	}
	return out
}

func droppedHeader(key string) bool {
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

func filterRelayHeaders(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, val := range in {
		if allowRelayHeader(key) && !strings.ContainsAny(val, "\r\n") && len(val) <= 4096 {
			out[strings.ToLower(key)] = val
		}
	}
	if out == nil {
		out = map[string]string{}
	}
	return out
}

func allowRelayHeader(key string) bool {
	key = strings.ToLower(key)
	switch key {
	case "content-type", "cache-control", "etag", "content-disposition":
		return true
	default:
		return strings.HasPrefix(key, "x-git-")
	}
}

func decodeBody(b64 string) ([]byte, error) {
	if b64 == "" {
		return nil, nil
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) > egresscontract.BodyCap {
		return nil, errors.New("egress body")
	}
	return raw, nil
}

func writeRelay(w http.ResponseWriter, result egresscontract.ForwardResult) {
	if result.Status < 100 || result.Status > 599 {
		writePlain(w, http.StatusBadGateway, egresscontract.Rejected)
		return
	}
	if result.Status >= 300 && result.Status < 400 {
		writePlain(w, http.StatusBadGateway, egresscontract.RedirectRefused)
		return
	}
	body, err := decodeBody(result.BodyB64)
	if err != nil {
		writePlain(w, http.StatusBadGateway, egresscontract.Rejected)
		return
	}
	for key, val := range filterRelayHeaders(result.Headers) {
		w.Header().Set(key, val)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(result.Status)
	_, _ = w.Write(body)
}

func writeFailure(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(egresscontract.Failure{
		Error:   code,
		Message: egresscontract.SafeMessage(code),
	})
}

func writePlain(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg+"\n")
}

func failureCode(body []byte) string {
	var fail egresscontract.Failure
	if err := json.Unmarshal(body, &fail); err != nil || fail.Error == "" {
		return "not_connected"
	}
	switch fail.Error {
	case "not_connected", "approval_required", "redirect_refused", "bad_request":
		return fail.Error
	default:
		return "not_connected"
	}
}

func failureMessage(body []byte) string {
	return egresscontract.SafeMessage(failureCode(body))
}

func splitRaw(prefix, full string) (string, string, bool) {
	if !strings.HasPrefix(full, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(full, prefix)
	provider, tail, found := strings.Cut(rest, "/")
	if !egresscontract.KnownProvider(provider) {
		return "", "", false
	}
	if !found || tail == "" {
		return provider, "/", true
	}
	return provider, "/" + tail, true
}

func scrubReason(reason string) string {
	reason = strings.TrimSpace(secretShape.ReplaceAllString(reason, "[redacted]"))
	runes := []rune(reason)
	if len(runes) > 500 {
		reason = string(runes[:500])
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == 0 {
			return ' '
		}
		return r
	}, reason)
}

func validSecretName(name string) bool {
	if len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
