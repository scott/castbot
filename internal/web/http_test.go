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
	"testing"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

type harness struct {
	t    *testing.T
	srv  *Server
	st   *store.Store
	jar  map[string]*http.Cookie
	csrf string
}

func newHarness(t *testing.T) *harness {
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
	return &harness{t: t, srv: New(st, key), st: st, jar: map[string]*http.Cookie{}}
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
	for _, c := range h.jar {
		if c.Path != "" && !pathMatch(c.Path, req.URL.Path) {
			continue
		}
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			delete(h.jar, c.Name)
			continue
		}
		h.jar[c.Name] = c
	}
	raw, _ := io.ReadAll(res.Body)
	text := string(raw)
	if tok := tokenFrom(text); tok != "" {
		h.csrf = tok
	}
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
	if h.csrf == "" {
		h.do(http.MethodGet, "/login", "", "203.0.113.10:4000", nil)
	}
	vals.Set("csrf", h.csrf)
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
	if code != 200 || strings.Count(body, `class="cell`) != 6 || !strings.Contains(body, `id="verify"`) || !strings.Contains(body, "disabled") {
		t.Fatalf("code page cells")
	}
	code, body, _ = h.do(http.MethodPost, "/login/code", h.form(url.Values{"code": {"000000"}}), ip, nil)
	if code != 200 || !strings.Contains(body, "That code did not work.") || !strings.Contains(body, "4 tries left") {
		t.Fatalf("code error %s", body)
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
	if code != 200 || !strings.Contains(body, "Echo: hello there") || !strings.Contains(body, "hello there") {
		t.Fatalf("echo %d %s", code, body)
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
