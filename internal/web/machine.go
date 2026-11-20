package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/approvetool"
	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/chattool"
	"github.com/scott/castbot/internal/connectiontool"
	"github.com/scott/castbot/internal/deletetool"
	"github.com/scott/castbot/internal/edittool"
	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/machinetool"
	"github.com/scott/castbot/internal/replacetool"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/secretask"
	"github.com/scott/castbot/internal/secretstool"
	"github.com/scott/castbot/internal/selftool"
	"github.com/scott/castbot/internal/settingstool"
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
	stored, storedSum, err := bundle.ArchiveFiles(b.YAML, b.Instructions, b.Extras)
	if err != nil || storedSum != b.SHA256 {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	raw, sum := serveBundle(b.YAML, b.Instructions, b.Extras, stored, storedSum)
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Castbot-Bundle-Version", b.Version)
	w.Header().Set("X-Castbot-Bundle-SHA256", sum)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// serveBundle checks the stored tar, then serves a copy that includes the
// create-agent skill, the routines skill, the API key skill, the account
// skill, the groups skill, the machine skill, the edit skill, the approvals
// skill, the chat skill, the settings skill, the connections skill, the secrets skill, the replace skill, the delete skill, and those instructions sections. The header sum is the served bytes.
// A later pull is stable because the additions are deterministic.
func serveBundle(yaml, instructions string, extras map[string]string, stored []byte, storedSum string) ([]byte, string) {
	yaml = deletetool.Apply(replacetool.Apply(secretstool.Apply(connectiontool.Apply(settingstool.Apply(chattool.Apply(approvetool.Apply(edittool.Apply(machinetool.Apply(grouptool.Apply(selftool.Apply(secretask.Apply(routinetool.Apply(agentskill.Apply(yaml))))))))))))))
	notes := deletetool.Instructions(replacetool.Instructions(secretstool.Instructions(connectiontool.Instructions(settingstool.Instructions(chattool.Instructions(approvetool.Instructions(edittool.Instructions(machinetool.Instructions(grouptool.Instructions(selftool.Instructions(secretask.Instructions(routinetool.Instructions(agentskill.Instructions(instructions))))))))))))))
	extras = deletetool.MergeExtras(replacetool.MergeExtras(secretstool.MergeExtras(connectiontool.MergeExtras(settingstool.MergeExtras(chattool.MergeExtras(approvetool.MergeExtras(edittool.MergeExtras(machinetool.MergeExtras(grouptool.MergeExtras(selftool.MergeExtras(secretask.MergeExtras(routinetool.MergeExtras(agentskill.MergeExtras(extras))))))))))))))
	raw, sum, err := bundle.ArchiveFiles(yaml, notes, extras)
	if err != nil || sum == "" {
		return stored, storedSum
	}
	return raw, sum
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
