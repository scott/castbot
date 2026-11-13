// Package castlet is the supervisor baked into the shared runtime image.
// It listens on the only reachable port, checks the per-agent token, pulls
// the bundle and granted secrets from the control plane, and proxies the
// Cast runtime bound to 127.0.0.1.
package castlet

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/egresscontract"
)

const (
	logCap  = 64 << 10
	bodyCap = 8 << 20
	fileCap = 32 << 20
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Config is the process configuration. The bootstrap token is read from
// the environment by the command and is not written to disk.
type Config struct {
	Addr       string
	ControlURL string
	AgentID    string
	Bootstrap  string
	CAFile     string
	AgentDir   string
	RuntimeURL string
	RuntimeCmd []string
	Token      string
	// AllowHTTP keeps the local test control plane on http.
	// The castlet command leaves this false, so a real boot requires https.
	AllowHTTP bool
}

// Supervisor is one castlet process.
type Supervisor struct {
	cfg         Config
	proxyClient *http.Client
	logs        *ring

	mu           sync.Mutex
	reloadMu     sync.Mutex
	journalMu    sync.Mutex
	cliMu        sync.Mutex
	cliTokens    []cliToken
	cliLoaded    bool
	cliWall      time.Time
	cliMono      time.Duration
	cliMonoSet   bool
	wallNow      func() time.Time
	monoNow      func() time.Duration
	clockWall    time.Time
	clockMono    time.Duration
	clockInit    bool
	journalReady bool
	journalNext  int
	journalEnd   int64
	journalOff   map[int]int64
	permSession  map[string]string
	token        string
	runtimeToken string
	ready        bool
	version      string
	sha          string
	secrets      []secret
	egressAt     time.Time
	egressView   egresscontract.Status
	proc         *exec.Cmd
	procDone     chan struct{}
	bootErr      string
}

var errReloadBusy = errors.New("reload in progress")

type secret struct {
	Name  string
	Value string
}

// RequireHTTPS rejects a control plane URL that is not https.
func RequireHTTPS(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "https") {
		return errors.New("control plane URL must use https")
	}
	return nil
}

// New builds a supervisor. Boot talks to the control plane.
// The control plane URL must be https unless Config.AllowHTTP is set.
func New(cfg Config) (*Supervisor, error) {
	if cfg.ControlURL != "" && !cfg.AllowHTTP {
		if err := RequireHTTPS(cfg.ControlURL); err != nil {
			return nil, err
		}
	}
	if cfg.AgentDir == "" {
		cfg.AgentDir = "/app/agent"
	}
	if cfg.RuntimeURL == "" {
		cfg.RuntimeURL = "http://127.0.0.1:8081"
	}
	cfg.RuntimeURL = strings.TrimRight(cfg.RuntimeURL, "/")
	s := &Supervisor{
		cfg:   cfg,
		token: cfg.Token,
		logs:  &ring{max: logCap},
		proxyClient: &http.Client{Transport: &http.Transport{
			DisableCompression:    true,
			DisableKeepAlives:     true,
			Proxy:                 nil,
			ResponseHeaderTimeout: 30 * time.Second,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}},
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	s.runtimeToken = hex.EncodeToString(buf)
	return s, nil
}

// Boot exchanges the bootstrap token, writes /app/agent, and starts the runtime.
func (s *Supervisor) Boot(ctx context.Context) error {
	if !s.reloadMu.TryLock() {
		return errReloadBusy
	}
	defer s.reloadMu.Unlock()
	token := s.cfg.Token
	if token == "" {
		var err error
		token, err = s.exchange(ctx)
		if err != nil {
			s.setBootErr("bootstrap")
			return err
		}
	}
	s.mu.Lock()
	s.token = token
	s.mu.Unlock()
	raw, version, sum, err := s.pullBundle(ctx, token)
	if err != nil {
		s.setBootErr("bundle")
		return err
	}
	if err := bundle.Install(s.cfg.AgentDir, raw); err != nil {
		s.setBootErr("bundle")
		return err
	}
	secrets, err := s.pullSecrets(ctx, token)
	if err != nil {
		s.setBootErr("secrets")
		return err
	}
	s.mu.Lock()
	s.secrets = secrets
	s.version = version
	s.sha = sum
	s.mu.Unlock()
	if err := s.startRuntime(ctx); err != nil {
		s.setBootErr("runtime")
		return err
	}
	s.refreshCLITokens(ctx)
	return nil
}

// MarkReady lets a test proxy without pulling a bundle.
func (s *Supervisor) MarkReady() {
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
}

// Close stops the runtime process.
func (s *Supervisor) Close() { s.stopRuntime() }

func (s *Supervisor) setBootErr(msg string) {
	s.mu.Lock()
	s.bootErr = msg
	s.ready = false
	s.mu.Unlock()
}

func (s *Supervisor) exchange(ctx context.Context) (string, error) {
	payload, _ := json.Marshal(map[string]string{"token": s.cfg.Bootstrap})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.api("/bootstrap"), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.Bootstrap)
	res, err := s.do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New("bootstrap rejected")
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body); err != nil {
		return "", err
	}
	if strings.TrimSpace(body.Token) == "" {
		return "", errors.New("bootstrap rejected")
	}
	return body.Token, nil
}

func (s *Supervisor) pullBundle(ctx context.Context, token string) ([]byte, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api("/bundle"), nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := s.do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", "", errors.New("bundle rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, bodyCap))
	if err != nil {
		return nil, "", "", err
	}
	sum := bundle.Sum(raw)
	if !constEq(sum, res.Header.Get("X-Castbot-Bundle-SHA256")) {
		return nil, "", "", errors.New("bundle hash mismatch")
	}
	version := res.Header.Get("X-Castbot-Bundle-Version")
	if version == "" {
		return nil, "", "", errors.New("bundle version missing")
	}
	return raw, version, sum, nil
}

func (s *Supervisor) pullSecrets(ctx context.Context, token string) ([]secret, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api("/secrets"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := s.do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("secrets rejected")
	}
	var body struct {
		Secrets []secret `json:"secrets"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, bodyCap)).Decode(&body); err != nil {
		return nil, err
	}
	var out []secret
	seen := map[string]bool{}
	for _, sec := range body.Secrets {
		name := strings.TrimSpace(sec.Name)
		if !envName.MatchString(name) || blockedEnv(name) || seen[name] {
			return nil, errors.New("secret name rejected")
		}
		if sec.Value == "" || strings.ContainsAny(sec.Value, "\x00\n\r") {
			return nil, errors.New("secret value rejected")
		}
		seen[name] = true
		out = append(out, secret{Name: name, Value: sec.Value})
	}
	return out, nil
}

func blockedEnv(name string) bool {
	switch strings.ToUpper(name) {
	case "PATH", "HOME", "HOSTNAME", "CASTBOT_BOOTSTRAP_TOKEN", "CASTBOT_CA_FILE", "CASTLET_SECRET_COUNT":
		return true
	default:
		return strings.HasPrefix(strings.ToUpper(name), "CASTLET_") || strings.HasPrefix(strings.ToUpper(name), "CASTBOT_")
	}
}

func (s *Supervisor) api(suffix string) string {
	base := strings.TrimRight(s.cfg.ControlURL, "/")
	return base + "/api/castlet/agents/" + url.PathEscape(s.cfg.AgentID) + suffix
}

func (s *Supervisor) do(req *http.Request) (*http.Response, error) {
	client, err := s.controlClient()
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (s *Supervisor) controlClient() (*http.Client, error) {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
	}
	if s.cfg.CAFile != "" {
		raw, err := os.ReadFile(s.cfg.CAFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("control plane certificate")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

func (s *Supervisor) startRuntime(ctx context.Context) error {
	s.stopRuntime()
	if len(s.cfg.RuntimeCmd) == 0 {
		if err := s.waitReady(ctx); err != nil {
			return err
		}
		s.mu.Lock()
		s.ready = true
		s.bootErr = ""
		s.mu.Unlock()
		return nil
	}
	cmd := exec.Command(s.cfg.RuntimeCmd[0], s.cfg.RuntimeCmd[1:]...)
	cmd.Env = s.childEnv()
	cmd.Stdout = s.logs
	cmd.Stderr = s.logs
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
		s.mu.Lock()
		if s.procDone == done {
			s.ready = false
			if s.bootErr == "" {
				s.bootErr = "runtime exited"
			}
		}
		s.mu.Unlock()
	}()
	s.mu.Lock()
	s.proc = cmd
	s.procDone = done
	s.mu.Unlock()
	if err := s.waitReady(ctx); err != nil {
		s.stopRuntime()
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.procDone != done {
		return errors.New("runtime exited")
	}
	select {
	case <-done:
		s.ready = false
		s.bootErr = "runtime exited"
		return errors.New("runtime exited")
	default:
	}
	s.ready = true
	s.bootErr = ""
	return nil
}

func (s *Supervisor) stopRuntime() {
	s.mu.Lock()
	cmd := s.proc
	done := s.procDone
	s.proc = nil
	s.procDone = nil
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func (s *Supervisor) waitReady(ctx context.Context) error {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := s.exited(); err != nil {
			return err
		}
		if s.probeRuntime() {
			if err := s.exited(); err != nil {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("runtime did not start")
		case <-deadline.C:
			return errors.New("runtime did not start")
		case <-s.doneChan():
			return errors.New("runtime exited")
		case <-tick.C:
		}
	}
}

func (s *Supervisor) doneChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.procDone != nil {
		return s.procDone
	}
	return nil
}

func (s *Supervisor) exited() error {
	ch := s.doneChan()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return errors.New("runtime exited")
	default:
		return nil
	}
}

func (s *Supervisor) probeRuntime() bool {
	req, err := http.NewRequest(http.MethodGet, s.cfg.RuntimeURL+"/.well-known/agent.json", nil)
	if err != nil {
		return false
	}
	res, err := s.proxyClient.Do(req)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func (s *Supervisor) childEnv() []string {
	skip := map[string]bool{
		"CASTBOT_BOOTSTRAP_TOKEN": true,
		"CASTBOT_CA_FILE":         true,
		"AUTH_TOKEN":              true,
	}
	var out []string
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if skip[key] {
			continue
		}
		out = append(out, env)
	}
	s.mu.Lock()
	secrets := append([]secret(nil), s.secrets...)
	runtimeToken := s.runtimeToken
	s.mu.Unlock()
	for _, sec := range secrets {
		out = append(out, sec.Name+"="+sec.Value)
	}
	out = append(out, "CASTLET_SECRET_COUNT="+strconv.Itoa(len(secrets)))
	if runtimeToken != "" {
		out = append(out, "AUTH_TOKEN="+runtimeToken)
	}
	return out
}

func (s *Supervisor) reload(ctx context.Context) error {
	if !s.reloadMu.TryLock() {
		return errReloadBusy
	}
	defer s.reloadMu.Unlock()
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token == "" {
		return errors.New("not ready")
	}
	raw, version, sum, err := s.pullBundle(ctx, token)
	if err != nil {
		return err
	}
	secrets, err := s.pullSecrets(ctx, token)
	if err != nil {
		return err
	}
	if err := bundle.Install(s.cfg.AgentDir, raw); err != nil {
		return err
	}
	s.mu.Lock()
	s.secrets = secrets
	s.version = version
	s.sha = sum
	s.ready = false
	s.mu.Unlock()
	// Always restart. A rotated secret has to reach the runtime env even
	// when the bundle bytes did not change.
	if err := s.startRuntime(ctx); err != nil {
		s.setBootErr("runtime")
		return err
	}
	s.refreshCLITokens(ctx)
	return nil
}

func constEq(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}

type ring struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = append([]byte(nil), r.buf[len(r.buf)-r.max:]...)
	}
	return len(p), nil
}

func (r *ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]byte(nil), r.buf...)
}
