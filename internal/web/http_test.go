package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

type harness struct {
	t    *testing.T
	srv  *Server
	st   *store.Store
	mu   sync.Mutex
	jar  map[string]*http.Cookie
	csrf string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo"})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	return newHarnessWith(t, runtime.NewHTTP(up.URL))
}

func newHarnessWith(t *testing.T, client runtime.Client) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), store.Options{Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 32}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0x11
	}
	srv := New(st, key, client)
	srv.SetDevEcho(true)
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv, st: st, jar: map[string]*http.Cookie{}}
}

func (h *harness) account(email, name, password string) (store.User, string) {
	h.t.Helper()
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		h.t.Fatal(err)
	}
	hash, err := auth.Hash(password, h.st.Argon())
	if err != nil {
		h.t.Fatal(err)
	}
	user, err := h.st.CreateUser(context.Background(), store.NewUser{
		Email: email, DisplayName: name, Workspace: "Personal",
		PasswordHash: hash, TOTPSecret: secret, Theme: "dark",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return user, secret
}

func (h *harness) do(method, path, form, ip string, header map[string]string) (int, string, http.Header) {
	h.t.Helper()
	var body io.Reader
	if form != "" {
		body = strings.NewReader(form)
	}
	req := httptest.NewRequest(method, path, body)
	if ip == "" {
		ip = "203.0.113.10:4000"
	}
	req.RemoteAddr = ip
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	h.mu.Lock()
	for _, c := range h.jar {
		if c.Path != "" && !pathMatch(c.Path, req.URL.Path) {
			continue
		}
		req.AddCookie(c)
	}
	h.mu.Unlock()
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	text := string(raw)
	h.mu.Lock()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			delete(h.jar, c.Name)
			continue
		}
		h.jar[c.Name] = c
	}
	if tok := tokenFrom(text); tok != "" {
		h.csrf = tok
	}
	h.mu.Unlock()
	return res.StatusCode, text, res.Header
}

func pathMatch(cookiePath, reqPath string) bool {
	if cookiePath == "/" {
		return true
	}
	if reqPath == cookiePath {
		return true
	}
	return strings.HasPrefix(reqPath, cookiePath+"/") || strings.HasPrefix(reqPath, cookiePath)
}

func tokenFrom(body string) string {
	const key = `name="csrf-token" content="`
	i := strings.Index(body, key)
	if i < 0 {
		return ""
	}
	rest := body[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func (h *harness) form(vals url.Values) string {
	if vals == nil {
		vals = url.Values{}
	}
	h.mu.Lock()
	empty := h.csrf == ""
	h.mu.Unlock()
	if empty {
		h.do(http.MethodGet, "/login", "", "203.0.113.10:4000", nil)
	}
	h.mu.Lock()
	csrf := h.csrf
	h.mu.Unlock()
	vals.Set("csrf", csrf)
	return vals.Encode()
}

func (h *harness) login(email, password, secret, ip string, trust bool) {
	h.t.Helper()
	h.do(http.MethodGet, "/login", "", ip, nil)
	code, _, hdr := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {email}, "password": {password}, "remember": {"1"}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		h.t.Fatalf("password step %d %s", code, hdr.Get("Location"))
	}
	totp, err := auth.TOTPCode(secret, h.st.Now())
	if err != nil {
		h.t.Fatal(err)
	}
	vals := url.Values{"code": {totp}}
	if trust {
		vals.Set("trust", "1")
	}
	code, body, hdr := h.do(http.MethodPost, "/login/code", h.form(vals), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
		h.t.Fatalf("code step %d %s %s", code, hdr.Get("Location"), body)
	}
	h.do(http.MethodGet, "/", "", ip, nil)
}

func cookieLine(hdr http.Header, name string) string {
	for _, line := range hdr.Values("Set-Cookie") {
		if strings.HasPrefix(line, name+"=") {
			return line
		}
	}
	return ""
}

func TestLoginAndSessionCookie(t *testing.T) {
	h := newHarness(t)
	_, secret := h.account("alex@example.com", "Alex Rivera", "correct-horse-1")
	ip := "203.0.113.20:4000"
	code, body, _ := h.do(http.MethodGet, "/login", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Keep me signed in on this device") || !strings.Contains(body, "checked") {
		t.Fatalf("login page %d", code)
	}
	code, body, hdr := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"correct-horse-1"}, "remember": {"1"}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("step %d %s", code, hdr.Get("Location"))
	}
	line := cookieLine(hdr, "castbot_pending")
	for _, bit := range []string{"HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(line, bit) {
			t.Fatalf("pending cookie %q missing %s", line, bit)
		}
	}
	code, body, _ = h.do(http.MethodGet, "/login/code", "", ip, nil)
	if code != 200 || strings.Count(body, `class="cell`) != 6 || !strings.Contains(body, `id="verify"`) || !strings.Contains(body, "disabled") || !strings.Contains(body, "autofocus") {
		t.Fatalf("code page cells")
	}
	code, body, _ = h.do(http.MethodPost, "/login/code", h.form(url.Values{"code": {"000000"}}), ip, nil)
	if code != 200 || !strings.Contains(body, "That code did not work.") || !strings.Contains(body, "4 tries left") {
		t.Fatalf("code error %s", body)
	}
	if strings.Contains(body, `value="000000"`) || !strings.Contains(body, "autofocus") || !strings.Contains(body, "otp bad") {
		t.Fatal("wrong code stayed in the field")
	}
	totp, err := auth.TOTPCode(secret, h.st.Now())
	if err != nil {
		t.Fatal(err)
	}
	code, _, hdr = h.do(http.MethodPost, "/login/code", h.form(url.Values{"code": {totp}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	line = cookieLine(hdr, "castbot_session")
	for _, bit := range []string{"HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(line, bit) {
			t.Fatalf("session cookie %q missing %s", line, bit)
		}
	}
	code, body, _ = h.do(http.MethodGet, "/", "", ip, nil)
	if code != 200 || !strings.Contains(body, "Alex Rivera") {
		t.Fatalf("home %d", code)
	}
	code, _, hdr = h.do(http.MethodPost, "/logout", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" {
		t.Fatalf("logout %d %s", code, hdr.Get("Location"))
	}
	code, _, hdr = h.do(http.MethodGet, "/", "", ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login" {
		t.Fatalf("after logout %d %s", code, hdr.Get("Location"))
	}
}

func TestWrongPasswordDoesNotRevealAccount(t *testing.T) {
	h := newHarness(t)
	h.account("alex@example.com", "Alex Rivera", "correct-horse-1")
	h.do(http.MethodGet, "/login", "", "203.0.113.21:9", nil)
	_, known, _ := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"wrong-password-1"}}), "203.0.113.21:9", nil)
	_, unknown, _ := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"nobody@example.com"}, "password": {"wrong-password-1"}}), "203.0.113.22:9", nil)
	for _, body := range []string{known, unknown} {
		if !strings.Contains(body, "That email and password do not match.") || !strings.Contains(body, "Check both and try again.") {
			t.Fatalf("banner missing: %s", body)
		}
		for _, leak := range []string{"does not exist", "no account", "unknown", "not found"} {
			if strings.Contains(strings.ToLower(body), leak) {
				t.Fatalf("leaked %q", leak)
			}
		}
		if strings.Contains(body, "tries left") {
			t.Fatal("hint shown too early")
		}
	}
}

func TestLockoutHTTP(t *testing.T) {
	h := newHarness(t)
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return start })
	_, secret := h.account("alex@example.com", "Alex Rivera", "correct-horse-1")
	h.account("other@example.com", "Other Person", "correct-horse-1")

	ip := "203.0.113.30:9"
	var body string
	for i := 0; i < 3; i++ {
		_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"wrong-password-1"}}), ip, nil)
	}
	if !strings.Contains(body, "2 tries left, then sign-in pauses for 15 minutes.") {
		t.Fatalf("hint: %s", body)
	}
	for i := 0; i < 2; i++ {
		_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"wrong-password-1"}}), ip, nil)
	}
	if !strings.Contains(body, "Sign-in is paused for this account.") || !strings.Contains(body, "Sign in again in") {
		t.Fatalf("locked: %s", body)
	}
	_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"correct-horse-1"}}), ip, nil)
	if !strings.Contains(body, "Sign-in is paused for this account.") {
		t.Fatal("correct password bypassed the lock")
	}

	later := start.Add(15*time.Minute + time.Second)
	h.st.SetNow(func() time.Time { return later })
	code, _, hdr := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"correct-horse-1"}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("after window %d %s", code, hdr.Get("Location"))
	}

	// Unknown email locks on that email and on the IP.
	for i := 0; i < 5; i++ {
		_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"ghost@example.com"}, "password": {"wrong-password-1"}}), "203.0.113.40:9", nil)
	}
	if !strings.Contains(body, "Sign-in is paused for this account.") {
		t.Fatal("unknown email did not lock")
	}
	_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"ghost@example.com"}, "password": {"wrong-password-1"}}), "203.0.113.41:9", nil)
	if !strings.Contains(body, "Sign-in is paused for this account.") {
		t.Fatal("email lock did not follow the address")
	}
	_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"other@example.com"}, "password": {"correct-horse-1"}}), "203.0.113.40:9", nil)
	if !strings.Contains(body, "Sign-in is paused for this account.") {
		t.Fatal("ip lock did not cover a different email")
	}
	code, _, hdr = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"other@example.com"}, "password": {"correct-horse-1"}}), "203.0.113.42:9", nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("clean ip %d %s", code, hdr.Get("Location"))
	}

	// Password and code failures share one budget. A recovery submit does not.
	share := "203.0.113.50:9"
	for i := 0; i < 4; i++ {
		h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"wrong-password-1"}}), share, nil)
	}
	code, _, hdr = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"correct-horse-1"}}), share, nil)
	if hdr.Get("Location") != "/login/code" {
		t.Fatalf("shared budget password %d %s", code, hdr.Get("Location"))
	}
	_, body, _ = h.do(http.MethodPost, "/login/code", h.form(url.Values{"recovery": {"not-a-real-code"}}), share, nil)
	if strings.Contains(body, "Sign-in is paused") || !strings.Contains(body, "Recovery codes are not available yet.") {
		t.Fatalf("recovery: %s", body)
	}
	_, body, _ = h.do(http.MethodPost, "/login/code", h.form(url.Values{"code": {"000000"}}), share, nil)
	if !strings.Contains(body, "Sign-in is paused for this account.") {
		t.Fatal("code failure did not finish the shared budget")
	}

	// Forgot-password copy does not change the counter.
	h.account("dana@example.com", "Dana", "correct-horse-1")
	for i := 0; i < 4; i++ {
		h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"dana@example.com"}, "password": {"wrong-password-1"}}), "203.0.113.60:9", nil)
	}
	_, body, _ = h.do(http.MethodPost, "/login/forgot", h.form(url.Values{"email": {"dana@example.com"}}), "203.0.113.60:9", nil)
	if !strings.Contains(body, "If an account exists for that email, we sent a reset link.") || !strings.Contains(body, "It expires in 30 minutes.") {
		t.Fatalf("forgot: %s", body)
	}
	_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"dana@example.com"}, "password": {"wrong-password-1"}}), "203.0.113.60:9", nil)
	if !strings.Contains(body, "Sign-in is paused for this account.") {
		t.Fatal("forgot changed the counter")
	}

	// A forwarded-for header is not the client address.
	h.account("fresh@example.com", "Fresh", "correct-horse-1")
	for i := 0; i < 5; i++ {
		h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"ghost2@example.com"}, "password": {"wrong-password-1"}}), "203.0.113.70:9", map[string]string{"X-Forwarded-For": "203.0.113.71"})
	}
	code, _, hdr = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"other@example.com"}, "password": {"correct-horse-1"}}), "203.0.113.71:9", nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("forwarded header was trusted: %d %s", code, hdr.Get("Location"))
	}
	_, body, _ = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"fresh@example.com"}, "password": {"correct-horse-1"}}), "203.0.113.70:9", nil)
	if !strings.Contains(body, "Sign-in is paused") {
		t.Fatal("real address was not locked")
	}
	_ = secret
}

func TestCSRFThemeEchoAndSessions(t *testing.T) {
	h := newHarness(t)
	user, secret := h.account("alex@example.com", "Alex Rivera", "correct-horse-1")
	other, _ := h.account("blair@example.com", "Blair", "correct-horse-1")
	ip := "203.0.113.80:9"
	code, body, _ := h.do(http.MethodPost, "/login", "email=alex@example.com", ip, nil)
	if code != http.StatusForbidden || !strings.Contains(body, "The form expired.") {
		t.Fatalf("csrf %d %s", code, body)
	}
	h.login("alex@example.com", "correct-horse-1", secret, ip, true)
	if h.jar["castbot_device"] == nil {
		t.Fatal("device cookie missing")
	}
	h.do(http.MethodPost, "/logout", h.form(url.Values{}), ip, nil)
	h.do(http.MethodGet, "/login", "", ip, nil)
	code, _, hdr := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"correct-horse-1"}, "remember": {"1"}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("trusted device still asked for a code: %d %s", code, hdr.Get("Location"))
	}
	code, body, _ = h.do(http.MethodGet, "/", "", ip, nil)
	if code != 200 {
		t.Fatal(code)
	}
	code, _, _ = h.do(http.MethodPost, "/settings/appearance", h.form(url.Values{"theme": {"light"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	_, body, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if !strings.Contains(body, `class="light"`) || !strings.Contains(body, `value="light" checked`) || !strings.Contains(body, "Yours only") {
		t.Fatal("theme or settings copy")
	}
	if !strings.Contains(body, `class="acct on"`) {
		t.Fatal("account button")
	}
	code, _, _ = h.do(http.MethodPost, "/new-agent", h.form(url.Values{"name": {"Echo"}, "title": {"Helper"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	agents, err := h.st.ListAgents(context.Background(), user.ID)
	if err != nil || len(agents) != 1 {
		t.Fatal(err, len(agents))
	}
	code, body, _ = h.do(http.MethodPost, "/agents/"+agents[0].ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || !strings.Contains(body, `sse-connect="/runs/`) || !strings.Contains(body, "hello there") || !strings.Contains(body, "Replying") {
		t.Fatalf("echo %d %s", code, body)
	}
	body = h.poll(ip, "/agents/"+agents[0].ID, "You said: hello there")
	if !strings.Contains(body, "You said: hello there") {
		t.Fatalf("stored reply missing: %s", body)
	}
	if _, err := h.st.CreateSession(context.Background(), user.ID, "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Safari/604.1", true); err != nil {
		t.Fatal(err)
	}
	kept, err := h.st.CreateSession(context.Background(), other.ID, "Mozilla/5.0 Chrome/120.0", true)
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ = h.do(http.MethodGet, "/settings/sessions", "", ip, nil)
	if !strings.Contains(body, "Sign out 1 other session?") {
		t.Fatalf("confirm: %s", body)
	}
	code, _, _ = h.do(http.MethodPost, "/settings/sessions", h.form(url.Values{}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	left, err := h.st.ListSessions(context.Background(), user.ID)
	if err != nil || len(left) != 1 {
		t.Fatalf("sessions left %d %v", len(left), err)
	}
	if _, err := h.st.GetSession(context.Background(), kept.ID); err != nil {
		t.Fatal("other user session was removed")
	}
	code, _, _ = h.do(http.MethodGet, "/settings", "", ip, nil)
	if code != 200 {
		t.Fatal("current session was removed")
	}
}

func TestAuthzMatrix(t *testing.T) {
	h := newHarness(t)
	a, aSecret := h.account("a@example.com", "User A", "correct-horse-1")
	b, bSecret := h.account("b@example.com", "User B", "correct-horse-1")
	ctx := context.Background()
	sec, err := h.st.CreateSection(ctx, a.ID, "A-Private-Section")
	if err != nil {
		t.Fatal(err)
	}
	agent, thread, err := h.st.CreateAgent(ctx, a.ID, store.AgentInput{SectionID: sec.ID, Name: "A-Only-Agent", Title: "Secret", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := h.st.CreateMessage(ctx, a.ID, thread.ID, "user", a.ID, "secret body")
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(ctx, a.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(ctx, a.ID, msg.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	const marker = "run-secret-marker-s02"
	if _, err := h.st.FailRun(ctx, a.ID, runRow.ID, marker, 0); err != nil {
		t.Fatal(err)
	}
	bSec, err := h.st.CreateSection(ctx, b.ID, "B-Private-Section")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.CreateAgent(ctx, b.ID, store.AgentInput{SectionID: bSec.ID, Name: "B-Only-Agent", Title: "Other", Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	pinned, err := h.st.SystemSection(ctx, a.ID, "pinned")
	if err != nil {
		t.Fatal(err)
	}

	type step struct {
		method, path, form string
		want               int
	}
	changes := []step{
		{http.MethodGet, "/sections?id=" + sec.ID, "", http.StatusOK},
		{http.MethodGet, "/sections/" + sec.ID, "", http.StatusOK},
		{http.MethodPost, "/sections/" + sec.ID, "name=Renamed", http.StatusOK},
		{http.MethodGet, "/agents?section=" + sec.ID, "", http.StatusOK},
		{http.MethodGet, "/agents/" + agent.ID, "", http.StatusOK},
		{http.MethodPost, "/agents/" + agent.ID, "title=Renamed+title", http.StatusOK},
		{http.MethodGet, "/threads?agent=" + agent.ID, "", http.StatusOK},
		{http.MethodGet, "/threads/" + thread.ID, "", http.StatusOK},
		{http.MethodPost, "/threads/" + thread.ID, "title=Renamed+thread", http.StatusOK},
		{http.MethodGet, "/threads/" + thread.ID + "/messages", "", http.StatusOK},
		{http.MethodGet, "/messages/" + msg.ID, "", http.StatusOK},
		{http.MethodPost, "/messages/" + msg.ID, "body=updated+body", http.StatusOK},
		{http.MethodGet, "/runs/" + runRow.ID, "", http.StatusOK},
		{http.MethodGet, "/runs/" + runRow.ID + "/events", "", http.StatusOK},
		{http.MethodGet, "/sections/" + pinned.ID, "", http.StatusOK},
	}
	removes := []step{
		{http.MethodPost, "/messages/" + msg.ID + "/delete", "", http.StatusOK},
		{http.MethodPost, "/threads/" + thread.ID + "/delete", "", http.StatusOK},
		{http.MethodPost, "/agents/" + agent.ID + "/delete", "", http.StatusOK},
		{http.MethodPost, "/sections/" + sec.ID + "/delete", "", http.StatusOK},
		{http.MethodPost, "/sections/" + pinned.ID + "/delete", "", http.StatusBadRequest},
	}
	post := func(s step, ip string) (int, string) {
		form := ""
		if s.method == http.MethodPost {
			form = h.form(url.Values{})
			if s.form != "" {
				form = s.form + "&" + form
			}
		}
		code, body, _ := h.do(s.method, s.path, form, ip, nil)
		return code, body
	}

	h.login("b@example.com", "correct-horse-1", bSecret, "203.0.113.90:9", false)
	h.assertRunHidden(runRow.ID, marker, "203.0.113.90:9")
	_, homeB, _ := h.do(http.MethodGet, "/", "", "203.0.113.90:9", nil)
	if strings.Contains(homeB, "A-Only-Agent") || strings.Contains(homeB, "A-Private-Section") {
		t.Fatal("B home leaked A")
	}
	for _, s := range append(changes, removes...) {
		code, body := post(s, "203.0.113.90:9")
		if code != http.StatusNotFound {
			t.Fatalf("B %s %s -> %d %s", s.method, s.path, code, body)
		}
	}

	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	h.login("a@example.com", "correct-horse-1", aSecret, "203.0.113.91:9", false)
	code, resumeBody, _ := h.do(http.MethodGet, "/runs/"+runRow.ID+"/events", "", "203.0.113.91:9", map[string]string{"Last-Event-ID": "1"})
	if code != http.StatusOK || !strings.Contains(resumeBody, "event: fail") || !strings.Contains(resumeBody, marker) {
		t.Fatalf("owner resume %d %s", code, resumeBody)
	}
	code, _, hdr := h.do(http.MethodPost, "/runs/"+runRow.ID+"/retry", h.form(url.Values{}), "203.0.113.91:9", nil)
	if code != http.StatusSeeOther {
		t.Fatalf("retry %d %s", code, hdr.Get("Location"))
	}
	h.waitRuns(a.ID, thread.ID)
	_, homeA, _ := h.do(http.MethodGet, "/", "", "203.0.113.91:9", nil)
	if strings.Contains(homeA, "B-Only-Agent") || strings.Contains(homeA, "B-Private-Section") {
		t.Fatal("A home leaked B")
	}
	for _, s := range append(changes, removes...) {
		code, body := post(s, "203.0.113.91:9")
		if code != s.want {
			t.Fatalf("A %s %s -> %d want %d %s", s.method, s.path, code, s.want, body)
		}
	}
}

func TestNoSQLInHandlers(t *testing.T) {
	dir := "."
	entries, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, needle := range []string{"SELECT ", "INSERT ", "UPDATE ", "DELETE FROM"} {
			if strings.Contains(text, needle) {
				t.Fatalf("%s contains %s", name, needle)
			}
		}
	}
}

func (h *harness) sessionID() string {
	c := h.jar["castbot_session"]
	if c == nil {
		return ""
	}
	id, ok := openSeal("session", c.Value, h.srv.key)
	if !ok {
		return ""
	}
	return id
}

func TestTOTPReplayTwoSessions(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 10, 1, 1, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return now })
	_, secret := h.account("replay@example.com", "Replay", "correct-horse-1")
	h.login("replay@example.com", "correct-horse-1", secret, "203.0.113.150:9", false)
	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	ip := "203.0.113.151:9"
	h.do(http.MethodGet, "/login", "", ip, nil)
	code, _, hdr := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"replay@example.com"}, "password": {"correct-horse-1"}}), ip, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("second session password %d %s", code, hdr.Get("Location"))
	}
	totp, err := auth.TOTPCode(secret, h.st.Now())
	if err != nil {
		t.Fatal(err)
	}
	code, body, hdr := h.do(http.MethodPost, "/login/code", h.form(url.Values{"code": {totp}}), ip, nil)
	if hdr.Get("Location") == "/" || code == http.StatusSeeOther || !strings.Contains(body, "That code did not work.") {
		t.Fatalf("replay accepted %d %s", code, hdr.Get("Location"))
	}
}

func TestTrustedDeviceAndPasswordRevocation(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 10, 1, 1, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return now })
	user, secret := h.account("alex@example.com", "Alex Rivera", "correct-horse-1")
	ipA := "203.0.113.180:9"
	ipB := "203.0.113.181:9"
	pass := "correct-horse-1"
	h.login("alex@example.com", pass, secret, ipA, true)
	deviceA := h.jar["castbot_device"]
	if deviceA == nil {
		t.Fatal("missing device")
	}
	now = now.Add(30 * time.Second)
	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	h.login("alex@example.com", pass, secret, ipB, true)
	deviceB := h.jar["castbot_device"]
	if deviceB == nil {
		t.Fatal("missing second device")
	}
	code, _, _ := h.do(http.MethodPost, "/settings/sessions", h.form(url.Values{}), ipB, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	h.jar = map[string]*http.Cookie{"castbot_device": deviceA}
	h.csrf = ""
	h.do(http.MethodGet, "/login", "", ipA, nil)
	code, _, hdr := h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {pass}}), ipA, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("trusted device survived sign-out of others: %d %s", code, hdr.Get("Location"))
	}
	h.jar = map[string]*http.Cookie{"castbot_device": deviceB}
	h.csrf = ""
	h.do(http.MethodGet, "/login", "", ipB, nil)
	code, _, hdr = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {pass}, "remember": {"1"}}), ipB, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("this browser lost its trusted device: %d %s", code, hdr.Get("Location"))
	}
	now = now.Add(30 * time.Second)
	h.jar = map[string]*http.Cookie{}
	h.csrf = ""
	h.login("alex@example.com", pass, secret, ipA, true)
	deviceA = h.jar["castbot_device"]
	h.jar = map[string]*http.Cookie{"castbot_device": deviceB}
	h.csrf = ""
	h.do(http.MethodGet, "/login", "", ipB, nil)
	code, _, hdr = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {pass}, "remember": {"1"}}), ipB, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/" {
		t.Fatalf("B device %d %s", code, hdr.Get("Location"))
	}
	h.do(http.MethodGet, "/settings", "", ipB, nil)
	before := h.sessionID()
	if before == "" {
		t.Fatal("session id")
	}
	third, err := h.st.CreateSession(context.Background(), user.ID, "Mozilla/5.0 Chrome/120.0", true)
	if err != nil {
		t.Fatal(err)
	}
	code, _, hdr = h.do(http.MethodPost, "/settings/password", h.form(url.Values{"current": {pass}, "next": {"correct-horse-2"}, "again": {"correct-horse-2"}}), ipB, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/settings?notice=password" {
		t.Fatalf("password change %d %s", code, hdr.Get("Location"))
	}
	after := h.sessionID()
	if after == "" || after == before {
		t.Fatalf("session was not rotated %q %q", before, after)
	}
	if _, err := h.st.GetSession(context.Background(), before); err != store.ErrNotFound {
		t.Fatalf("old session %v", err)
	}
	if _, err := h.st.GetSession(context.Background(), third.ID); err != store.ErrNotFound {
		t.Fatalf("third session %v", err)
	}
	code, _, _ = h.do(http.MethodGet, "/settings", "", ipB, nil)
	if code != 200 {
		t.Fatal("current browser was signed out")
	}
	h.jar = map[string]*http.Cookie{"castbot_device": deviceA}
	h.csrf = ""
	h.do(http.MethodGet, "/login", "", ipA, nil)
	code, _, hdr = h.do(http.MethodPost, "/login", h.form(url.Values{"email": {"alex@example.com"}, "password": {"correct-horse-2"}}), ipA, nil)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/login/code" {
		t.Fatalf("trusted device survived password change: %d %s", code, hdr.Get("Location"))
	}
}

func TestOriginCheck(t *testing.T) {
	h := newHarness(t)
	_, secret := h.account("origin@example.com", "Origin", "correct-horse-1")
	ip := "203.0.113.190:9"
	h.login("origin@example.com", "correct-horse-1", secret, ip, false)
	code, body, _ := h.do(http.MethodPost, "/settings/appearance", h.form(url.Values{"theme": {"light"}}), ip, map[string]string{"Origin": "https://evil.example"})
	if code != http.StatusForbidden || !strings.Contains(body, "That request was blocked.") {
		t.Fatalf("evil origin %d %s", code, body)
	}
	code, body, _ = h.do(http.MethodPost, "/settings/appearance", h.form(url.Values{"theme": {"light"}}), ip, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if code != http.StatusForbidden || !strings.Contains(body, "That request was blocked.") {
		t.Fatalf("cross-site %d %s", code, body)
	}
	code, _, hdr := h.do(http.MethodPost, "/settings/appearance", h.form(url.Values{"theme": {"light"}}), ip, map[string]string{"Origin": "https://example.com"})
	if code != http.StatusSeeOther {
		t.Fatalf("same origin %d %s", code, hdr.Get("Location"))
	}
	code, _, hdr = h.do(http.MethodPost, "/settings/appearance", h.form(url.Values{"theme": {"dark"}}), ip, map[string]string{"Origin": "null", "Sec-Fetch-Site": "same-origin"})
	if code != http.StatusSeeOther {
		t.Fatalf("null origin from this site %d %s", code, hdr.Get("Location"))
	}
}

func TestDenverClock(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 10, 1, 1, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return now })
	user, secret := h.account("clock@example.com", "Clock User", "correct-horse-1")
	ip := "203.0.113.200:9"
	h.login("clock@example.com", "correct-horse-1", secret, ip, false)
	_, body, _ := h.do(http.MethodGet, "/settings", "", ip, nil)
	if !strings.Contains(body, "Last changed Nov 9") || !strings.Contains(body, "Signed in since Nov 9") {
		t.Fatalf("settings clock: %s", body)
	}
	if strings.Contains(body, "Nov 10") {
		t.Fatal("settings still used UTC")
	}
	code, _, _ := h.do(http.MethodPost, "/new-agent", h.form(url.Values{"name": {"Echo"}, "title": {"Helper"}}), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatal(code)
	}
	agents, err := h.st.ListAgents(context.Background(), user.ID)
	if err != nil || len(agents) != 1 {
		t.Fatal(err)
	}
	_, body, _ = h.do(http.MethodPost, "/agents/"+agents[0].ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, nil)
	body = h.poll(ip, "/agents/"+agents[0].ID, "6:01 PM")
	if !strings.Contains(body, "6:01 PM") || strings.Contains(body, "1:01 AM") {
		t.Fatalf("message clock: %s", body)
	}
}

func (h *harness) assertRunHidden(runID, marker, ip string) {
	h.t.Helper()
	for _, path := range []string{"/runs/" + runID, "/runs/" + runID + "/events"} {
		code, body, _ := h.do(http.MethodGet, path, "", ip, nil)
		if code != http.StatusNotFound || strings.Contains(body, marker) {
			h.t.Fatalf("hidden %s %d %s", path, code, body)
		}
	}
	code, body, _ := h.do(http.MethodGet, "/runs/"+runID+"/events", "", ip, map[string]string{"Last-Event-ID": "1"})
	if code != http.StatusNotFound || strings.Contains(body, marker) {
		h.t.Fatalf("hidden resume %d %s", code, body)
	}
	code, body, _ = h.do(http.MethodPost, "/runs/"+runID+"/retry", h.form(url.Values{}), ip, nil)
	if code != http.StatusNotFound || strings.Contains(body, marker) {
		h.t.Fatalf("hidden retry %d %s", code, body)
	}
}

func (h *harness) waitRuns(userID, threadID string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, err := h.st.ListThreadRuns(context.Background(), userID, threadID)
		if err != nil {
			h.t.Fatal(err)
		}
		busy := false
		for _, row := range runs {
			if row.Status == "queued" || row.Status == "streaming" || row.Status == "waiting_approval" {
				busy = true
			}
		}
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatal("run did not finish")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) poll(ip, path, needle string) string {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var body string
	for {
		_, body, _ = h.do(http.MethodGet, path, "", ip, nil)
		if strings.Contains(body, needle) || time.Now().After(deadline) {
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestNoDashes(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".go", ".html", ".css", ".js", ".md", ".yml", ".yaml":
		default:
			if filepath.Base(path) != "castbot.env.example" {
				return nil
			}
		}
		if strings.Contains(path, "htmx.min.js") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.ContainsRune(string(raw), '\u2014') || strings.ContainsRune(string(raw), '\u2013') {
			t.Fatalf("dash in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
