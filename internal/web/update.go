package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/scott/castbot/internal/store"
	"github.com/scott/castbot/internal/update"
)

// updatePanel is the owner Update section and the home banner.
type updatePanel struct {
	On         bool
	Dev        bool
	Pinned     bool
	Ready      bool
	Blocked    bool
	Moves      bool
	Busy       bool
	Banner     bool
	Mode       string
	Running    string
	Commit     string
	Offer      string
	Notes      string
	CheckError string
}

func (s *Server) SetUpdate(version, commit, envMode, cachePath string) {
	s.mu.Lock()
	s.updateSet = true
	s.updateVersion = strings.TrimSpace(version)
	s.updateCommit = strings.TrimSpace(commit)
	s.updateEnv = strings.TrimSpace(envMode)
	s.updateCache = strings.TrimSpace(cachePath)
	s.mu.Unlock()
}

// SetUpdateApply installs a release after the owner confirms it.
// A nil function records the choice and does not install anything.
func (s *Server) SetUpdateApply(fn func(context.Context) error) {
	s.mu.Lock()
	s.updateApply = fn
	s.mu.Unlock()
}

// SetUpdateCheck runs one release check for Check now.
// It records the result and does not install. A nil function refuses the button.
func (s *Server) SetUpdateCheck(fn func(context.Context) error) {
	s.mu.Lock()
	s.updateCheck = fn
	s.mu.Unlock()
}

func (s *Server) updateConfig() (version, commit, envMode, cachePath string, set bool, apply func(context.Context) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateVersion, s.updateCommit, s.updateEnv, s.updateCache, s.updateSet, s.updateApply
}

func (s *Server) fillUpdate(ctx context.Context, user store.User, p *page) {
	panel, _ := s.loadUpdate(ctx, user)
	p.Update = panel
}

func (s *Server) loadUpdate(ctx context.Context, user store.User) (updatePanel, func(context.Context) error) {
	version, commit, envMode, cachePath, set, apply := s.updateConfig()
	if !set || user.Role != "owner" {
		return updatePanel{}, nil
	}
	stored, err := s.store.InstanceSetting(ctx, update.ModeKey)
	if err != nil {
		stored = ""
	}
	panel := updatePanel{On: true, Mode: update.ResolveMode(envMode, stored)}
	if _, ok := update.ParseMode(envMode); ok {
		panel.Pinned = true
	}
	if version == "" || version == "dev" {
		panel.Dev = true
		panel.Running = "dev"
		return panel, nil
	}
	panel.Running = showVersion(version)
	panel.Commit = shortCommit(commit)
	if panel.Mode == update.ModeOff {
		return panel, nil
	}
	if cachePath == "" {
		cachePath = update.DefaultCachePath
	}
	cache, err := update.ReadCache(cachePath)
	if err != nil || (!cache.Available && !cache.ImageBlocked) || !update.Newer(version, cache.Version) {
		return panel, nil
	}
	panel.Offer = showVersion(cache.Version)
	if cache.ImageBlocked {
		panel.Blocked = true
		return panel, nil
	}
	if !cache.Available {
		return panel, nil
	}
	panel.Ready = true
	panel.Notes = cache.Notes
	panel.Moves = cache.ImageMoves
	panel.Banner = panel.Mode == update.ModePrompt
	n, err := s.store.InflightRuns(ctx)
	if err == nil && n > 0 {
		panel.Busy = true
	}
	return panel, apply
}

func (s *Server) saveUpdateMode(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if user.Role != "owner" {
		http.NotFound(w, r)
		return
	}
	panel, _ := s.loadUpdate(r.Context(), user)
	if panel.Pinned {
		http.Redirect(w, r, "/settings#update", http.StatusSeeOther)
		return
	}
	mode, good := update.ParseMode(r.FormValue("mode"))
	if !good {
		p, err := s.settingsView(r, user, store.Session{})
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		p.FormError = "Choose ask me, install updates, or don't check."
		s.render(w, http.StatusBadRequest, "app", finish(p))
		return
	}
	if err := s.store.SetInstanceSetting(r.Context(), update.ModeKey, mode); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings?notice=update#update", http.StatusSeeOther)
}

func (s *Server) checkUpdate(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if user.Role != "owner" {
		http.NotFound(w, r)
		return
	}
	panel, _ := s.loadUpdate(r.Context(), user)
	if !panel.On || panel.Dev || panel.Mode == update.ModeOff {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	check := s.updateCheck
	s.mu.Unlock()
	if check == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := check(ctx); err != nil {
		p, viewErr := s.settingsView(r, user, sess)
		if viewErr != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		p.Update.CheckError = "The check did not finish. This page still shows the last result."
		s.render(w, http.StatusBadGateway, "app", finish(p))
		return
	}
	http.Redirect(w, r, "/settings#update", http.StatusSeeOther)
}

func (s *Server) updatePage(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if user.Role != "owner" {
		http.NotFound(w, r)
		return
	}
	p, err := s.settingsView(r, user, sess)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) applyUpdate(w http.ResponseWriter, r *http.Request) {
	user, sess, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if user.Role != "owner" {
		http.NotFound(w, r)
		return
	}
	panel, apply := s.loadUpdate(r.Context(), user)
	if panel.Dev || !panel.Ready || panel.Mode == update.ModeOff {
		http.NotFound(w, r)
		return
	}
	if panel.Busy {
		p, err := s.settingsView(r, user, sess)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		s.render(w, http.StatusConflict, "app", finish(p))
		return
	}
	if apply != nil {
		// The helper must outlive this request. A canceled request must not stop it.
		if err := apply(context.Background()); err != nil {
			p, viewErr := s.settingsView(r, user, sess)
			if viewErr != nil {
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			p.FormError = "The update did not start."
			s.render(w, http.StatusBadGateway, "app", finish(p))
			return
		}
		p := page{
			Title:      "Updating",
			Theme:      user.Theme,
			CSRF:       from(r).CSRF,
			Refresh:    "3;url=/settings",
			BannerLead: "castbot is restarting.",
			BannerRest: "This page comes back at Settings.",
		}
		s.render(w, http.StatusOK, "updateWait", finish(p))
		return
	}
	p, err := s.settingsView(r, user, sess)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func showVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return "dev"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

func shortCommit(commit string) string {
	if len(commit) >= 7 {
		return commit[:7]
	}
	return commit
}
