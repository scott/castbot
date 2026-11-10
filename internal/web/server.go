package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

const (
	csp = "default-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; script-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"

	cookieSession = "castbot_session"
	cookieCSRF    = "castbot_csrf"
	cookiePending = "castbot_pending"
	cookieDevice  = "castbot_device"
)

type ctxKey int

const stateKey ctxKey = 1

type requestState struct {
	Session *store.Session
	User    *store.User
	CSRF    string
}

// Server is the HTTP application. It never queries SQLite itself.
type Server struct {
	store  *store.Store
	key    []byte
	client runtime.Client
	runs   *run.Runner
	cancel context.CancelFunc
	ka     time.Duration

	mu        sync.Mutex
	guests    map[string]guest
	machine   host.AgentHost
	image     string
	publicURL string
	trusted   []*net.IPNet
	base      context.Context
	prov      sync.WaitGroup
	gitBase   string
	gitHTTP   *http.Client

	reloadMu sync.Mutex
	reloads  map[string]*reloadGate
	reloadWG sync.WaitGroup

	devEcho         bool
	explicitRuntime bool
	log             *slog.Logger
	recMu           sync.Mutex
	recKind         string
	recDelay        time.Duration
	recEvery        time.Duration

	approvalFor time.Duration
	expireMu    sync.Mutex
}

// guest is one local castlet the control plane can proxy files through.
type guest struct {
	Base  string
	Token string
}

// New builds a server. key is the 32-byte session key.
// client talks to the agent runtime. S03 and S04 swap that client.
func New(st *store.Store, key []byte, client runtime.Client) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{store: st, key: key, client: client, runs: run.New(ctx, st, client), cancel: cancel, base: ctx, approvalFor: approvalWaitFromEnv()}
	s.runs.SetOpen(s.openRun)
	s.runs.SetApprovalTimeout(s.approvalFor)
	go s.expireLoop()
	return s
}

// SetHost attaches a machine host. image is the one shared runtime image.
// publicURL is the https control plane address written into the machine.
// Call this before serving.
func (s *Server) SetHost(h host.AgentHost, image, publicURL string) {
	s.mu.Lock()
	s.machine = h
	s.image = strings.TrimSpace(image)
	s.publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	s.mu.Unlock()
}

// SetTrustedProxies uses X-Forwarded-For only when the connection comes from one of these networks.
// An empty list keeps the connection address, which is the default.
func (s *Server) SetTrustedProxies(nets []*net.IPNet) {
	s.mu.Lock()
	s.trusted = nets
	s.mu.Unlock()
}

// SetDevEcho allows the local echo runtime. It is off unless the process
// was started with CASTBOT_DEV_ECHO=1.
func (s *Server) SetDevEcho(on bool) {
	s.mu.Lock()
	s.devEcho = on
	s.mu.Unlock()
}

func (s *Server) devEchoOn() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.devEcho
}

// SetExplicitRuntime marks the attached client as CASTBOT_RUNTIME_URL.
// That client is used when an agent has no machine. The echo flag is separate.
func (s *Server) SetExplicitRuntime(on bool) {
	s.mu.Lock()
	s.explicitRuntime = on
	s.mu.Unlock()
}

func (s *Server) hostConfig() (host.AgentHost, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.machine, s.image, s.publicURL
}

// Close cancels run goroutines and waits until they record an outcome.
func (s *Server) Close() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.runs != nil {
		s.runs.Wait(3 * time.Second)
	}
	done := make(chan struct{})
	go func() {
		s.prov.Wait()
		s.reloadWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// SetRetryBudget shortens how long a run waits on a refused runtime.
func (s *Server) SetRetryBudget(d time.Duration) {
	if s.runs != nil {
		s.runs.SetBudget(d)
	}
}

// SetRunLimit caps how long one reply may run.
func (s *Server) SetRunLimit(d time.Duration) {
	if s.runs != nil {
		s.runs.SetRunLimit(d)
	}
}

// SetClient points later runs at a different runtime.
func (s *Server) SetClient(c runtime.Client) {
	s.mu.Lock()
	s.client = c
	s.mu.Unlock()
	if s.runs != nil {
		s.runs.SetClient(c)
	}
}

// SetGuest records the castlet address and agent token for one agent.
// File reads for that agent are proxied there. The token is not logged.
func (s *Server) SetGuest(agentID, base, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.guests == nil {
		s.guests = map[string]guest{}
	}
	s.guests[agentID] = guest{Base: strings.TrimRight(base, "/"), Token: token}
}

func (s *Server) lookupGuest(agentID string) (guest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.guests[agentID]
	return g, ok && g.Base != "" && g.Token != ""
}

// SetKeepalive changes how often the event stream writes a comment.
func (s *Server) SetKeepalive(d time.Duration) {
	s.ka = d
}

func (s *Server) keepaliveEvery() time.Duration {
	if s.ka > 0 {
		return s.ka
	}
	return 15 * time.Second
}

// Handler is the application router with security headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.loginGet)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("GET /login/code", s.codeGet)
	mux.HandleFunc("POST /login/code", s.codePost)
	mux.HandleFunc("GET /login/forgot", s.forgotGet)
	mux.HandleFunc("POST /login/forgot", s.forgotPost)
	mux.HandleFunc("POST /logout", s.logout)

	mux.HandleFunc("GET /", s.home)
	mux.HandleFunc("GET /settings", s.settings)
	mux.HandleFunc("POST /settings/account", s.saveName)
	mux.HandleFunc("POST /settings/appearance", s.saveTheme)
	mux.HandleFunc("GET /settings/password", s.passwordForm)
	mux.HandleFunc("POST /settings/password", s.savePassword)
	mux.HandleFunc("GET /settings/email", s.stubEmail)
	mux.HandleFunc("GET /settings/two-factor", s.stubTwoFactor)
	mux.HandleFunc("GET /settings/keys", s.stubKeys)
	mux.HandleFunc("GET /settings/sessions", s.sessionsConfirm)
	mux.HandleFunc("POST /settings/sessions", s.sessionsEnd)

	mux.HandleFunc("GET /new-agent", s.newAgent)
	mux.HandleFunc("POST /new-agent", s.createAgent)
	mux.HandleFunc("POST /new-agent/import", s.importNew)
	mux.HandleFunc("GET /approvals", s.workspacePage)
	mux.HandleFunc("GET /routines", s.workspacePage)
	mux.HandleFunc("GET /files", s.workspacePage)
	mux.HandleFunc("GET /usage", s.workspacePage)

	mux.HandleFunc("GET /agents/{id}", s.chat)
	mux.HandleFunc("GET /agents/{id}/edit", s.editAgent)
	mux.HandleFunc("POST /agents/{id}/edit", s.saveAgent)
	mux.HandleFunc("POST /agents/{id}/import", s.importAgent)
	mux.HandleFunc("GET /agents/{id}/avatar", s.agentAvatar)
	mux.HandleFunc("GET /agents/{id}/machine", s.agentTab)
	mux.HandleFunc("GET /agents/{id}/routines", s.agentTab)
	mux.HandleFunc("GET /agents/{id}/files", s.agentTab)
	mux.HandleFunc("POST /agents/{id}", s.updateAgent)
	mux.HandleFunc("POST /agents/{id}/machine", s.provisionMachine)
	mux.HandleFunc("POST /agents/{id}/machine/pause", s.pauseMachine)
	mux.HandleFunc("POST /agents/{id}/machine/wake", s.wakeMachine)
	mux.HandleFunc("POST /agents/{id}/machine/delete", s.deleteMachine)
	mux.HandleFunc("POST /agents/{id}/delete", s.deleteAgent)
	mux.HandleFunc("POST /agents/{id}/messages", s.postMessage)
	mux.HandleFunc("POST /agents/{id}/cli", s.exportCLI)
	mux.HandleFunc("POST /agents/{id}/cli/{token}/revoke", s.revokeCLI)
	mux.HandleFunc("GET /api/castlet/agents/{id}/cli-tokens", s.machineCLITokens)
	mux.HandleFunc("POST /api/castlet/agents/{id}/cli-used", s.machineCLIUsed)
	mux.HandleFunc("POST /api/castlet/agents/{id}/journal", s.machineJournal)
	mux.HandleFunc("GET /agents/{id}/bundle", s.ownerBundle)
	mux.HandleFunc("GET /agents/{id}/secrets", s.ownerSecrets)
	mux.HandleFunc("GET /agents/{id}/workspace", s.ownerFiles)
	mux.HandleFunc("GET /agents/{id}/workspace/{path...}", s.ownerFile)
	mux.HandleFunc("POST /api/castlet/agents/{id}/bootstrap", s.bootstrap)
	mux.HandleFunc("GET /api/castlet/agents/{id}/bundle", s.machineBundle)
	mux.HandleFunc("GET /api/castlet/agents/{id}/secrets", s.machineSecrets)
	mux.HandleFunc("GET /runs/{id}/events", s.runEvents)
	mux.HandleFunc("GET /runs/{id}", s.getRun)
	mux.HandleFunc("POST /runs/{id}/retry", s.retryRun)
	mux.HandleFunc("POST /runs/{id}/approvals/{approval}/approve", s.approveRun)
	mux.HandleFunc("POST /runs/{id}/approvals/{approval}/deny", s.denyRun)
	mux.HandleFunc("GET /approvals/{id}", s.getApproval)
	mux.HandleFunc("GET /agents", s.listAgents)

	mux.HandleFunc("GET /sections", s.listSections)
	mux.HandleFunc("POST /sections", s.createSection)
	mux.HandleFunc("GET /sections/{id}", s.getSection)
	mux.HandleFunc("POST /sections/{id}", s.updateSection)
	mux.HandleFunc("POST /sections/{id}/delete", s.deleteSection)

	mux.HandleFunc("GET /threads", s.listThreads)
	mux.HandleFunc("POST /threads", s.createThread)
	mux.HandleFunc("GET /threads/{id}", s.getThread)
	mux.HandleFunc("POST /threads/{id}", s.updateThread)
	mux.HandleFunc("POST /threads/{id}/delete", s.deleteThread)
	mux.HandleFunc("GET /threads/{id}/messages", s.listMessages)

	mux.HandleFunc("GET /messages/{id}", s.getMessage)
	mux.HandleFunc("POST /messages/{id}", s.updateMessage)
	mux.HandleFunc("POST /messages/{id}/delete", s.deleteMessage)

	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))
	return s.wrap(mux)
}

func postLimit(path string) int64 {
	switch {
	case path == "/new-agent/import" || strings.HasSuffix(path, "/import"):
		return 5 << 20
	case path == "/new-agent" || strings.HasSuffix(path, "/edit"):
		return 2 << 20
	default:
		return 1 << 20
	}
}

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		st := s.loadState(w, r)
		r = r.WithContext(context.WithValue(r.Context(), stateKey, st))
		// castlet calls these with a bearer token, not a browser form.
		if r.Method == http.MethodPost && !strings.HasPrefix(r.URL.Path, "/api/castlet/") {
			r.Body = http.MaxBytesReader(w, r.Body, postLimit(r.URL.Path))
			var err error
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				err = r.ParseMultipartForm(8 << 20)
			} else {
				err = r.ParseForm()
			}
			if err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
				return
			}
			if !sameOrigin(r) {
				s.render(w, http.StatusForbidden, "auth", finish(page{
					Page: "auth", Title: "Request blocked", Theme: "dark",
					Banner: true, BannerLead: "That request was blocked.", BannerRest: "Reload the page and try again.",
					BannerIcon: "warn",
				}))
				return
			}
			if !csrfOK(r, st.CSRF) {
				s.render(w, http.StatusForbidden, "auth", finish(page{
					Page: "auth", Title: "Form expired", Theme: "dark",
					Banner: true, BannerLead: "The form expired.", BannerRest: "Reload the page and try again.",
					BannerIcon: "warn",
				}))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) loadState(w http.ResponseWriter, r *http.Request) requestState {
	st := requestState{}
	if c, err := r.Cookie(cookieSession); err == nil {
		if id, ok := openSeal("session", c.Value, s.key); ok {
			sess, err := s.store.GetSession(r.Context(), id)
			if err == nil {
				user, err := s.store.GetUser(r.Context(), sess.UserID)
				if err == nil && !user.Disabled {
					st.Session = &sess
					st.User = &user
					st.CSRF = sess.CSRF
				}
			}
		}
	}
	if st.User == nil {
		st.CSRF = s.ensureCSRF(w, r)
	}
	return st
}

func from(r *http.Request) requestState {
	st, _ := r.Context().Value(stateKey).(requestState)
	return st
}

func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (store.User, store.Session, bool) {
	st := from(r)
	if st.User == nil || st.Session == nil {
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/login")
			w.WriteHeader(http.StatusUnauthorized)
			return store.User{}, store.Session{}, false
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return store.User{}, store.Session{}, false
	}
	return *st.User, *st.Session, true
}

func sameOrigin(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	if site == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" || origin == "null" {
		if origin == "null" {
			return site == "same-origin" || site == "none"
		}
		return site == "" || site == "same-origin" || site == "none"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func csrfOK(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	form := r.FormValue("csrf")
	hdr := r.Header.Get("X-CSRF-Token")
	ok := false
	if form != "" && subtle.ConstantTimeCompare([]byte(form), []byte(want)) == 1 {
		ok = true
	}
	if hdr != "" && subtle.ConstantTimeCompare([]byte(hdr), []byte(want)) == 1 {
		ok = true
	}
	return ok
}

func (s *Server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(cookieCSRF); err == nil && len(c.Value) >= 32 {
		return c.Value
	}
	tok, err := randomToken()
	if err != nil {
		return ""
	}
	s.setCookie(w, cookieCSRF, tok, "/", 24*3600)
	return tok
}

func (s *Server) setCookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	c := &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	if maxAge != 0 {
		c.MaxAge = maxAge
	}
	http.SetCookie(w, c)
}

func clearCookie(w http.ResponseWriter, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: path, MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

func seal(kind, id string, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(kind))
	mac.Write([]byte{0})
	mac.Write([]byte(id))
	return id + "." + hex.EncodeToString(mac.Sum(nil))
}

func openSeal(kind, token string, key []byte) (string, bool) {
	id, mac, ok := strings.Cut(token, ".")
	if !ok || id == "" || len(id) > 80 {
		return "", false
	}
	got, err := hex.DecodeString(mac)
	if err != nil {
		return "", false
	}
	expect := hmac.New(sha256.New, key)
	expect.Write([]byte(kind))
	expect.Write([]byte{0})
	expect.Write([]byte(id))
	if !hmac.Equal(got, expect.Sum(nil)) {
		return "", false
	}
	return id, true
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (s *Server) clientIP(r *http.Request) string {
	s.mu.Lock()
	trusted := s.trusted
	s.mu.Unlock()
	return clientAddr(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), trusted)
}

func remoteHost(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil || host == "" {
		if remote == "" {
			return "unknown"
		}
		return remote
	}
	return host
}

// clientAddr returns the connection address unless that hop is trusted.
// A trusted hop walks X-Forwarded-For from the right and skips other trusted hops.
// An entry that is not an IP, including a blank, "unknown", or host:port, stops
// the walk. The address is then the last trusted hop, not a value further left
// that the client chose.
func clientAddr(remote, forwarded string, trusted []*net.IPNet) string {
	host := remoteHost(remote)
	if len(trusted) == 0 || !trustedIP(host, trusted) {
		return host
	}
	last := host
	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		item := strings.TrimSpace(parts[i])
		if net.ParseIP(item) == nil {
			return last
		}
		if trustedIP(item, trusted) {
			last = item
			continue
		}
		return item
	}
	return last
}

func trustedIP(raw string, nets []*net.IPNet) bool {
	ip := net.ParseIP(raw)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseTrustedProxies parses a comma-separated list of IPs and CIDRs.
// A prefix broader than /8 for IPv4 or /32 for IPv6 is refused.
func ParseTrustedProxies(raw string) ([]*net.IPNet, error) {
	return parseTrustedProxies(raw, false)
}

// ParseTrustedProxiesAllowWide is ParseTrustedProxies with the broad-prefix
// refusal turned off. Call it only when the operator set the override flag.
func ParseTrustedProxiesAllowWide(raw string) ([]*net.IPNet, error) {
	return parseTrustedProxies(raw, true)
}

func parseTrustedProxies(raw string, allowWide bool) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			_, n, err := net.ParseCIDR(part)
			if err != nil {
				return nil, err
			}
			var ones, bits int
			n, ones, bits = normalizeProxy(n)
			if ones == 0 {
				return nil, errors.New("trusted proxy must not match every address")
			}
			if !allowWide && bits == 32 && ones < 8 {
				return nil, errors.New("trusted proxy is broader than /8")
			}
			if !allowWide && bits == 128 && ones < 32 {
				return nil, errors.New("trusted proxy is broader than /32")
			}
			out = append(out, n)
			continue
		}
		ip := net.ParseIP(part)
		if ip == nil {
			return nil, errors.New("trusted proxy is not an address")
		}
		out = append(out, oneIP(ip))
	}
	return out, nil
}

// normalizeProxy turns an IPv4-mapped prefix into an IPv4 prefix so
// ::ffff:0.0.0.0/96 is judged as /0, not as an IPv6 /96.
func normalizeProxy(n *net.IPNet) (*net.IPNet, int, int) {
	ones, bits := n.Mask.Size()
	if bits == 128 && n.IP.To4() != nil {
		if ones < 96 {
			ones = 0
		} else {
			ones -= 96
		}
		bits = 32
		ip := n.IP.To4().Mask(net.CIDRMask(ones, 32))
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(ones, 32)}, ones, bits
	}
	return n, ones, bits
}

func oneIP(ip net.IP) *net.IPNet {
	if v4 := ip.To4(); v4 != nil {
		return &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)}
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}
}

func (s *Server) render(w http.ResponseWriter, status int, name string, p page) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := views.ExecuteTemplate(w, name, p); err != nil {
		log.Printf("render %s failed", name)
		return err
	}
	return nil
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeText(w, http.StatusNotFound, "Not found\n")
}

func mapStoreErr(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeText(w, http.StatusNotFound, "Not found\n")
	case errors.Is(err, store.ErrProtected), errors.Is(err, store.ErrInvalid):
		writeText(w, http.StatusBadRequest, "Bad request\n")
	case errors.Is(err, store.ErrConflict):
		writeText(w, http.StatusConflict, "Conflict\n")
	case errors.Is(err, store.ErrQuota):
		writeText(w, http.StatusConflict, "Agent limit reached\n")
	default:
		log.Printf("store error on %s", r.URL.Path)
		writeText(w, http.StatusInternalServerError, "Error\n")
	}
	return true
}

func userAgent(r *http.Request) string {
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	return ua
}
