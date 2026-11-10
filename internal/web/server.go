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
	"net"
	"net/http"
	"strings"

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
	store *store.Store
	key   []byte
}

// New builds a server. key is the 32-byte session key.
func New(st *store.Store, key []byte) *Server {
	return &Server{store: st, key: key}
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
	mux.HandleFunc("GET /approvals", s.workspacePage)
	mux.HandleFunc("GET /routines", s.workspacePage)
	mux.HandleFunc("GET /files", s.workspacePage)
	mux.HandleFunc("GET /usage", s.workspacePage)

	mux.HandleFunc("GET /agents/{id}", s.chat)
	mux.HandleFunc("GET /agents/{id}/machine", s.agentTab)
	mux.HandleFunc("GET /agents/{id}/routines", s.agentTab)
	mux.HandleFunc("GET /agents/{id}/files", s.agentTab)
	mux.HandleFunc("POST /agents/{id}", s.updateAgent)
	mux.HandleFunc("POST /agents/{id}/delete", s.deleteAgent)
	mux.HandleFunc("POST /agents/{id}/messages", s.postMessage)
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

func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		st := s.loadState(w, r)
		r = r.WithContext(context.WithValue(r.Context(), stateKey, st))
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Bad request", http.StatusBadRequest)
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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		if r.RemoteAddr == "" {
			return "unknown"
		}
		return r.RemoteAddr
	}
	return host
}

func (s *Server) render(w http.ResponseWriter, status int, name string, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := views.ExecuteTemplate(w, name, p); err != nil {
		log.Printf("render %s: %v", name, err)
	}
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
