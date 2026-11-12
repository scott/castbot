package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"sort"
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
	// A local boot has no machine row yet. The guest still needs the token.
	// A replacement bootstrap is bound to the new row and must not wake the old machine.
	live, _ := s.store.PlaceVMToken(r.Context(), r.PathValue("id"), token)
	if live {
		if userID, err := s.store.UserForAgentToken(r.Context(), r.PathValue("id"), token); err == nil {
			s.kickProbe(userID, r.PathValue("id"))
		}
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
	s.writeSecretNames(w, r, user.ID)
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

func (s *Server) writeSecretNames(w http.ResponseWriter, r *http.Request, userID string) {
	secrets, err := s.store.GrantedSecrets(r.Context(), userID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	type item struct {
		Name string `json:"name"`
	}
	out := struct {
		Secrets []item `json:"secrets"`
	}{Secrets: []item{}}
	for _, sec := range secrets {
		out.Secrets = append(out.Secrets, item{Name: sec.Name})
	}
	writeJSON(w, http.StatusOK, out)
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
	if agent, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id")); err == nil {
		if name, value, missing := s.effectiveModelSecret(r.Context(), userID, agent.Model); missing == "" && name != "" && value != "" {
			out.Secrets = append(out.Secrets, item{Name: name, Value: value})
		}
		pairs := make([]secretPair, 0, len(out.Secrets))
		for _, sec := range out.Secrets {
			pairs = append(pairs, secretPair{name: sec.Name, value: sec.Value})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].name < pairs[j].name })
		fp := hashSecretPairs(hmac.New(sha256.New, s.secretFPKey()), pairs)
		_ = s.store.SetSecretFP(r.Context(), userID, agent.ID, fp)
	}
	writeJSON(w, http.StatusOK, out)
}

func attachmentDisposition(name string) string {
	base := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		base = name[i+1:]
	}
	ascii := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		default:
			return -1
		}
	}, base)
	if ascii == "" || ascii == "." || ascii == ".." {
		ascii = "download"
	}
	if len(ascii) > 80 {
		ascii = ascii[:80]
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + rfc5987(base)
}

func rfc5987(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if attrChar(c) {
			b.WriteByte(c)
			continue
		}
		const hexd = "0123456789ABCDEF"
		b.WriteByte('%')
		b.WriteByte(hexd[c>>4])
		b.WriteByte(hexd[c&15])
	}
	return b.String()
}

func attrChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '&', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
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
