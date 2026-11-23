package castlet

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/scott/castbot/internal/castcfg"
)

const modelBodyCap = 8 << 20

func modelProxyBaseURL() string {
	addr, err := LoopbackAddr(os.Getenv("CASTLET_EGRESS_ADDR"))
	if err != nil {
		addr = "127.0.0.1:8090"
	}
	return "http://" + addr + "/model/v1"
}

// applyModelProxy removes provider keys from the runtime environment and
// points every proxied provider at the local forwarder. gate is a per-boot
// placeholder, not a key, and it is not copied into a command. A keyless
// provider is left alone.
func applyModelProxy(env []string, base, gate string) []string {
	drop := map[string]bool{}
	for _, p := range castcfg.ProxyProviders() {
		if p.EnvVar != "" {
			drop[p.EnvVar] = true
		}
		if p.BaseURLEnv != "" {
			drop[p.BaseURLEnv] = true
		}
	}
	out := make([]string, 0, len(env)+len(drop))
	for _, item := range env {
		key, _, ok := strings.Cut(item, "=")
		if !ok || drop[key] {
			continue
		}
		out = append(out, item)
	}
	for _, p := range castcfg.ProxyProviders() {
		out = append(out, p.EnvVar+"="+gate)
		if p.BaseURLEnv != "" {
			out = append(out, p.BaseURLEnv+"="+base)
		}
	}
	return out
}

func writeModelConfig(agentDir, base string) error {
	if strings.TrimSpace(agentDir) == "" || strings.TrimSpace(base) == "" {
		return nil
	}
	parent := filepath.Dir(agentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return nil
	}
	dir := filepath.Join(parent, ".cast")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("providers:\n")
	for _, p := range castcfg.ProxyProviders() {
		name := configSection(p.ConfigKey)
		if name == "" {
			continue
		}
		fmt.Fprintf(&b, "  %s:\n    base_url: %q\n", name, base)
	}
	return os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(b.String()), 0o600)
}

func configSection(configKey string) string {
	parts := strings.Split(configKey, ".")
	if len(parts) != 3 || parts[0] != "providers" || parts[2] != "api_key" {
		return ""
	}
	name := parts[1]
	if name == "" || strings.ContainsAny(name, " \t\r\n:\"'#") {
		return ""
	}
	return name
}

func (s *Supervisor) egressModel(w http.ResponseWriter, r *http.Request) {
	if !sameUserPeer(r) || !s.modelGateMatches(r) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writePlain(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest, ok := cleanModelPath(strings.TrimPrefix(r.URL.Path, "/model"))
	if !ok || len(r.URL.RawQuery) > 256 {
		writePlain(w, http.StatusBadRequest, "request refused")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, modelBodyCap+1))
	if err != nil || len(body) > modelBodyCap {
		writePlain(w, http.StatusRequestEntityTooLarge, "request refused")
		return
	}
	s.mu.Lock()
	token := s.token
	s.mu.Unlock()
	if token == "" {
		writePlain(w, http.StatusBadGateway, "model unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	upURL := s.api("/model" + rest)
	if r.Method == http.MethodGet && r.URL.RawQuery != "" {
		upURL += "?" + r.URL.RawQuery
	}
	up, err := http.NewRequestWithContext(ctx, r.Method, upURL, bytes.NewReader(body))
	if err != nil {
		writePlain(w, http.StatusBadGateway, "model unavailable")
		return
	}
	up.Header.Set("Authorization", "Bearer "+token)
	copyModelHeader(up.Header, r.Header, "Content-Type")
	copyModelHeader(up.Header, r.Header, "Accept")
	copyModelHeader(up.Header, r.Header, "anthropic-version")
	copyModelHeader(up.Header, r.Header, "anthropic-beta")
	client, err := s.controlClient()
	if err != nil {
		writePlain(w, http.StatusBadGateway, "model unavailable")
		return
	}
	client.Timeout = 0
	res, err := client.Do(up)
	if err != nil {
		writePlain(w, http.StatusBadGateway, "model unavailable")
		return
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "" && !strings.ContainsAny(ct, "\r\n") {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	flushCopy(w, res.Body)
}

func (s *Supervisor) modelGateMatches(r *http.Request) bool {
	s.mu.Lock()
	gate := s.modelGate
	s.mu.Unlock()
	if gate == "" {
		return false
	}
	got := modelBearer(r.Header.Get("Authorization"))
	if len(got) != len(gate) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(gate)) == 1
}

func modelBearer(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func copyModelHeader(dst, src http.Header, name string) {
	value := src.Get(name)
	if value == "" || strings.ContainsAny(value, "\r\n") || len(value) > 200 {
		return
	}
	dst.Set(name, value)
}

func cleanModelPath(raw string) (string, bool) {
	if raw == "" || strings.Contains(raw, "..") || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", false
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(raw, "/"))
	switch cleaned {
	case "/v1/chat/completions", "/v1/messages", "/v1/models":
		return cleaned, true
	}
	if strings.HasPrefix(cleaned, "/v1/models/") {
		id := strings.TrimPrefix(cleaned, "/v1/models/")
		if id != "" && !strings.Contains(id, "/") && id != "." && id != ".." {
			return cleaned, true
		}
	}
	return "", false
}

func flushCopy(w http.ResponseWriter, src io.Reader) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}
