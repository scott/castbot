package web

import (
	"errors"
	"net/http"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/store"
)

func (s *Server) twoFactor(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	p, err := s.twoFactorView(r, user, "")
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) twoFactorStart(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	on, err := s.store.HasTOTP(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if !on {
		if _, err := s.store.PendingTOTP(r.Context(), user.ID); errors.Is(err, store.ErrNotFound) {
			secret, gerr := auth.NewTOTPSecret()
			if gerr != nil {
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			if err := s.store.BeginTOTP(r.Context(), user.ID, secret); err != nil && !errors.Is(err, store.ErrConflict) {
				if s.bad(w, err) {
					return
				}
			}
		} else if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
	}
	p, err := s.twoFactorView(r, user, "")
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) twoFactorConfirm(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.ConfirmTOTP(r.Context(), user.ID, digits(r.FormValue("code")))
	if errors.Is(err, store.ErrInvalid) {
		p, verr := s.twoFactorView(r, user, "That code did not work. It may be mistyped or expired. Codes change every 30 seconds.")
		if verr != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		s.render(w, http.StatusBadRequest, "app", finish(p))
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		p, verr := s.twoFactorView(r, user, "Set up an authenticator first.")
		if verr != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		s.render(w, http.StatusBadRequest, "app", finish(p))
		return
	}
	if err != nil && !errors.Is(err, store.ErrConflict) {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?notice=two-factor", http.StatusSeeOther)
}

func (s *Server) twoFactorView(r *http.Request, user store.User, formError string) (page, error) {
	p, err := s.shell(r, user)
	if err != nil {
		return page{}, err
	}
	p.Page = "two-factor"
	p.Title = "Two-factor"
	p.FormError = formError
	if p.TwoFactorOn {
		return p, nil
	}
	secret, err := s.store.PendingTOTP(r.Context(), user.ID)
	if errors.Is(err, store.ErrNotFound) {
		return p, nil
	}
	if err != nil {
		return page{}, err
	}
	p.TOTPSecret = secret
	p.TOTPURL = auth.OTPAuthURL(user.Email, secret)
	return p, nil
}
