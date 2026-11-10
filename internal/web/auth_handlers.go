package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/scott/castbot/internal/store"
)

func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	if from(r).User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.URL.Query().Get("switch") == "1" {
		if id, ok := s.pendingID(r); ok {
			_ = s.store.DeleteChallenge(r.Context(), id)
		}
		clearCookie(w, cookiePending, "/login")
		s.renderLogin(w, r, page{Email: "", Remember: true})
		return
	}
	email := ""
	if id, ok := s.pendingID(r); ok {
		if ch, err := s.store.GetChallenge(r.Context(), id); err == nil {
			email = ch.Email
		}
	}
	s.renderLogin(w, r, page{Email: email, Remember: true})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.FormValue("email"))
	remember := r.FormValue("remember") == "1"
	att, err := s.store.CheckPassword(r.Context(), email, r.FormValue("password"), clientIP(r))
	if err != nil {
		s.render(w, http.StatusInternalServerError, "auth", authPage(from(r), page{Email: email, Remember: remember, Banner: true, BannerLead: "Something went wrong.", BannerRest: "Try again in a moment.", BannerIcon: "warn"}))
		return
	}
	if att.Status == "locked" {
		s.renderLocked(w, r, email, att.LockedUntil)
		return
	}
	if att.Status != "ok" {
		p := page{Email: email, Remember: remember, Banner: true, BannerIcon: "warn", BannerLead: "That email and password do not match.", BannerRest: "Check both and try again."}
		if att.TriesLeft > 0 && att.TriesLeft <= 2 {
			p.Hint = triesHint(att.TriesLeft)
		}
		s.renderLogin(w, r, p)
		return
	}
	if ok, err := s.store.TrustedDevice(r.Context(), deviceCookie(r), att.User.ID); err == nil && ok {
		s.startSession(w, r, att.User, remember)
		return
	}
	ch, err := s.store.BeginChallenge(r.Context(), att.User, remember)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.setCookie(w, cookiePending, seal("pending", ch.ID, s.key), "/login", 600)
	http.Redirect(w, r, "/login/code", http.StatusSeeOther)
}

func (s *Server) codeGet(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.challenge(w, r)
	if !ok {
		return
	}
	user, err := s.store.GetUser(r.Context(), ch.UserID)
	if err != nil {
		clearCookie(w, cookiePending, "/login")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.renderCode(w, r, user, page{})
}

func (s *Server) codePost(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.challenge(w, r)
	if !ok {
		return
	}
	user, err := s.store.GetUser(r.Context(), ch.UserID)
	if err != nil {
		clearCookie(w, cookiePending, "/login")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	code := digits(r.FormValue("code"))
	recovery := strings.TrimSpace(r.FormValue("recovery"))
	att, err := s.store.CheckCode(r.Context(), ch.ID, code, recovery, clientIP(r))
	if err != nil {
		clearCookie(w, cookiePending, "/login")
		s.renderLogin(w, r, page{Email: ch.Email, Remember: ch.Remember, Banner: true, BannerIcon: "warn", BannerLead: "That sign-in step expired.", BannerRest: "Enter your password again."})
		return
	}
	if att.Status == "locked" {
		clearCookie(w, cookiePending, "/login")
		s.renderLocked(w, r, ch.Email, att.LockedUntil)
		return
	}
	if att.Status == "recovery" {
		s.renderCode(w, r, user, page{
			Banner: true, BannerIcon: "warn",
			BannerLead: "Recovery codes are not available yet.",
			BannerRest: "Enter the 6-digit code from your authenticator app.",
		})
		return
	}
	if att.Status != "ok" {
		p := page{
			Code: code, OTPBad: true, Banner: true, BannerIcon: "warn",
			BannerLead: "That code did not work.",
			BannerRest: "It may be mistyped or expired. Codes change every 30 seconds, so enter the newest one.",
		}
		if att.TriesLeft > 0 {
			p.Hint = triesHint(att.TriesLeft)
		}
		s.renderCode(w, r, user, p)
		return
	}
	if r.FormValue("trust") == "1" {
		tok, err := s.store.TrustDevice(r.Context(), att.User.ID, userAgent(r))
		if err == nil {
			s.setCookie(w, cookieDevice, tok, "/", 30*24*3600)
		}
	}
	clearCookie(w, cookiePending, "/login")
	s.startSession(w, r, att.User, ch.Remember)
}

func (s *Server) forgotGet(w http.ResponseWriter, r *http.Request) {
	if from(r).User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p := authPage(from(r), page{Title: "Reset password", StubTitle: "Reset password", StubBody: "Enter the email on the account. If it exists, the next screen says a reset link was sent."})
	s.render(w, http.StatusOK, "forgot", p)
}

func (s *Server) forgotPost(w http.ResponseWriter, r *http.Request) {
	p := authPage(from(r), page{
		Title: "Reset password", Banner: true, BannerIcon: "info",
		BannerLead: "If an account exists for that email, we sent a reset link.",
		BannerRest: "It expires in 30 minutes.",
	})
	s.render(w, http.StatusOK, "forgot", p)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if st := from(r); st.Session != nil {
		_ = s.store.DeleteSession(r.Context(), st.Session.UserID, st.Session.ID)
	}
	clearCookie(w, cookieSession, "/")
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user store.User, remember bool) {
	sess, err := s.store.CreateSession(r.Context(), user.ID, userAgent(r), remember)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	maxAge := 0
	if remember {
		maxAge = 30 * 24 * 3600
	}
	s.setCookie(w, cookieSession, seal("session", sess.ID, s.key), "/", maxAge)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) pendingID(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookiePending)
	if err != nil {
		return "", false
	}
	return openSeal("pending", c.Value, s.key)
}

func (s *Server) challenge(w http.ResponseWriter, r *http.Request) (store.Challenge, bool) {
	id, ok := s.pendingID(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return store.Challenge{}, false
	}
	ch, err := s.store.GetChallenge(r.Context(), id)
	if err != nil {
		clearCookie(w, cookiePending, "/login")
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return store.Challenge{}, false
	}
	return ch, true
}

func deviceCookie(r *http.Request) string {
	c, err := r.Cookie(cookieDevice)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, p page) {
	s.render(w, http.StatusOK, "auth", authPage(from(r), p))
}

func (s *Server) renderLocked(w http.ResponseWriter, r *http.Request, email string, until time.Time) {
	now := s.store.Now()
	p := page{
		Email: email, Remember: true, Locked: true,
		Banner: true, BannerIcon: "clock",
		BannerLead: "Sign-in is paused for this account.",
		BannerRest: "Too many tries. You can try again in",
		LockRFC:    until.UTC().Format(time.RFC3339),
		LockLabel:  lockLabel(until, now),
	}
	s.render(w, http.StatusOK, "auth", authPage(from(r), p))
}

func (s *Server) renderCode(w http.ResponseWriter, r *http.Request, user store.User, p page) {
	p.Email = user.Email
	p.Initials = initials(user.DisplayName)
	p.Cells = otpCells(p.Code)
	p.Title = "Enter your code"
	base := authPage(from(r), p)
	base.Email = user.Email
	base.Initials = p.Initials
	base.Cells = p.Cells
	base.Code = p.Code
	s.render(w, http.StatusOK, "code", base)
}

func authPage(st requestState, p page) page {
	p.Page = "auth"
	p.CSRF = st.CSRF
	if p.Title == "" {
		p.Title = "Sign in"
	}
	if p.Theme == "" {
		p.Theme = "dark"
	}
	return finish(p)
}

func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
		if b.Len() == 6 {
			break
		}
	}
	return b.String()
}
