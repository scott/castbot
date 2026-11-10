package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott/castbot/internal/bundle"
)

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	plain := bearerToken(r.Header.Get("Authorization"))
	if plain == "" {
		var body struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
		plain = strings.TrimSpace(body.Token)
	}
	token, err := s.store.ExchangeBootstrap(r.Context(), r.PathValue("id"), plain)
	if err != nil {
		writeText(w, http.StatusUnauthorized, "unauthorized\n")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (s *Server) machineBundle(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	s.writeBundle(w, r, userID)
}

func (s *Server) machineSecrets(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	s.writeSecrets(w, r, userID)
}

func (s *Server) machineUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	plain := bearerToken(r.Header.Get("Authorization"))
	if plain == "" {
		plain = bearerToken(r.Header.Get("X-Cast-Authorization"))
	}
	userID, err := s.store.UserForAgentToken(r.Context(), r.PathValue("id"), plain)
	if err != nil {
		writeText(w, http.StatusNotFound, "Not found\n")
		return "", false
	}
	return userID, true
}

func (s *Server) ownerBundle(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	s.writeBundle(w, r, user.ID)
}

func (s *Server) ownerSecrets(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	s.writeSecrets(w, r, user.ID)
}

func (s *Server) writeBundle(w http.ResponseWriter, r *http.Request, userID string) {
	b, err := s.store.LatestBundle(r.Context(), userID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	raw, sum, err := bundle.Archive(b.YAML, b.Instructions)
	if err != nil || sum != b.SHA256 {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Castbot-Bundle-Version", b.Version)
	w.Header().Set("X-Castbot-Bundle-SHA256", b.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func (s *Server) writeSecrets(w http.ResponseWriter, r *http.Request, userID string) {
	secrets, err := s.store.GrantedSecrets(r.Context(), userID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	type item struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	out := struct {
		Secrets []item `json:"secrets"`
	}{Secrets: []item{}}
	for _, sec := range secrets {
		out.Secrets = append(out.Secrets, item{Name: sec.Name, Value: sec.Value})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ownerFiles(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id")); s.missing(w, err) {
		return
	}
	g, ok := s.lookupGuest(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	s.proxyGuest(w, r, g, "/castlet/files")
}

func (s *Server) ownerFile(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id")); s.missing(w, err) {
		return
	}
	guestPath, ok := fileGuestPath(r.PathValue("path"))
	if !ok {
		writeText(w, http.StatusBadRequest, "Bad request\n")
		return
	}
	g, ok := s.lookupGuest(r.PathValue("id"))
	if !ok {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	s.proxyGuest(w, r, g, guestPath)
}

func fileGuestPath(rel string) (string, bool) {
	rel = strings.Trim(rel, "/")
	if rel == "" || strings.Contains(rel, "\\") || strings.Contains(rel, "..") {
		return "", false
	}
	var parts []string
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
		parts = append(parts, url.PathEscape(part))
	}
	return "/castlet/files/" + strings.Join(parts, "/"), true
}

func (s *Server) proxyGuest(w http.ResponseWriter, r *http.Request, g guest, path string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, g.Base+path, nil)
	if err != nil {
		writeText(w, http.StatusBadGateway, "Error\n")
		return
	}
	req.Header.Set("X-Cast-Authorization", "Bearer "+g.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		writeText(w, http.StatusBadGateway, "Error\n")
		return
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func bearerToken(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
