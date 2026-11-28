package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/castcfg"
)

const modelBodyCap = 8 << 20

var modelHTTP = &http.Client{
	Timeout: 10 * time.Minute,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (s *Server) machineModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeText(w, http.StatusMethodNotAllowed, "Method not allowed\n")
		return
	}
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	inf, found := castcfg.InferenceFor(agent.Model)
	if !found || inf.Keyless {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	route, found := castcfg.ProxyProviderFor(inf.Key)
	if !found {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	cleaned, ok := cleanProxyPath(r.PathValue("path"))
	if !ok {
		writeText(w, http.StatusBadRequest, "Request refused\n")
		return
	}
	if !methodAllows(r.Method, route.Key, cleaned) {
		writeText(w, http.StatusBadRequest, "Request refused\n")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, modelBodyCap+1))
	if err != nil || len(body) > modelBodyCap {
		writeText(w, http.StatusRequestEntityTooLarge, "Request refused\n")
		return
	}
	_, apiModel, _ := castcfg.APIModelID(agent.Model)
	if r.Method == http.MethodPost {
		got, allowed := castcfg.ModelAllowed(agent.Model, jsonModel(body))
		if !allowed {
			writeText(w, http.StatusForbidden, "Model not allowed\n")
			return
		}
		apiModel = got
		body = rewriteModel(body, apiModel)
	}
	if strings.HasPrefix(cleaned, "/models/") {
		id := strings.TrimPrefix(cleaned, "/models/")
		if id != apiModel && id != agent.Model {
			writeText(w, http.StatusForbidden, "Model not allowed\n")
			return
		}
	}
	if !s.allowModelCall(agent.ID, time.Now()) {
		writeText(w, http.StatusTooManyRequests, "Slow down\n")
		return
	}
	defer s.finishModelCall(agent.ID)
	_, value, missing := s.effectiveModelSecret(r.Context(), userID, agent.Model)
	if missing != "" || value == "" {
		writeText(w, http.StatusConflict, "No key for this model.\n")
		return
	}
	base := strings.TrimRight(route.BaseURL, "/")
	if up := strings.TrimRight(strings.TrimSpace(s.modelProxyUpstream), "/"); up != "" {
		base = up
	}
	if !upstreamOK(base) {
		writeText(w, http.StatusBadGateway, "Model unavailable\n")
		return
	}
	target := base + cleaned
	if r.Method == http.MethodGet && r.URL.RawQuery != "" && len(r.URL.RawQuery) <= 256 && !strings.ContainsAny(r.URL.RawQuery, "\r\n") {
		target += "?" + r.URL.RawQuery
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	up, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeText(w, http.StatusBadGateway, "Model unavailable\n")
		return
	}
	copyHeader(up.Header, r.Header, "Content-Type", 200)
	copyHeader(up.Header, r.Header, "Accept", 200)
	copyHeader(up.Header, r.Header, "x-opencode-session", 200)
	if route.Key == "opencode-go" && strings.TrimSpace(up.Header.Get("User-Agent")) == "" {
		up.Header.Set("User-Agent", "castbot/1.0")
	}
	if route.Key == "anthropic-api" {
		version := r.Header.Get("anthropic-version")
		if version == "" || strings.ContainsAny(version, "\r\n") {
			version = "2023-06-01"
		}
		up.Header.Set("x-api-key", value)
		up.Header.Set("anthropic-version", version)
		copyHeader(up.Header, r.Header, "anthropic-beta", 200)
	} else {
		up.Header.Set("Authorization", "Bearer "+value)
	}
	res, err := modelHTTP.Do(up)
	if err != nil {
		s.logger().Info("model_proxy", "provider", route.Key, "path", cleaned, "status", 0, "req_bytes", len(body))
		writeText(w, http.StatusBadGateway, "Model unavailable\n")
		return
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "" && !strings.ContainsAny(ct, "\r\n") && !strings.Contains(ct, value) {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	n := redactCopy(w, res.Body, value)
	s.logger().Info("model_proxy", "provider", route.Key, "path", cleaned, "status", res.StatusCode, "req_bytes", len(body), "res_bytes", n)
}

const defaultModelRate = 60
const defaultModelConc = 4

func (s *Server) allowModelCall(id string, now time.Time) bool {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	if s.modelHits == nil {
		s.modelHits = map[string][]time.Time{}
	}
	if s.modelInflight == nil {
		s.modelInflight = map[string]int{}
	}
	limit := s.modelRate
	if limit <= 0 {
		limit = defaultModelRate
	}
	conc := s.modelConc
	if conc <= 0 {
		conc = defaultModelConc
	}
	if s.modelInflight[id] >= conc {
		return false
	}
	window := s.modelWindow
	if window <= 0 {
		window = time.Minute
	}
	cut := now.Add(-window)
	prev := s.modelHits[id]
	kept := make([]time.Time, 0, len(prev)+1)
	for _, at := range prev {
		if at.After(cut) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= limit {
		s.modelHits[id] = kept
		return false
	}
	s.modelHits[id] = append(kept, now)
	s.modelInflight[id]++
	return true
}

func (s *Server) finishModelCall(id string) {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	if s.modelInflight[id] > 0 {
		s.modelInflight[id]--
	}
}

func methodAllows(method, provider, cleaned string) bool {
	switch cleaned {
	case "/chat/completions":
		return method == http.MethodPost && provider != "anthropic-api"
	case "/messages":
		return method == http.MethodPost && provider == "anthropic-api"
	case "/models":
		return method == http.MethodGet
	}
	if strings.HasPrefix(cleaned, "/models/") {
		return method == http.MethodGet
	}
	return false
}

func cleanProxyPath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "..") || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", false
	}
	cleaned := "/" + strings.Trim(raw, "/")
	switch cleaned {
	case "/v1/chat/completions", "/chat/completions":
		return "/chat/completions", true
	case "/v1/messages", "/messages":
		return "/messages", true
	case "/v1/models", "/models":
		return "/models", true
	}
	for _, prefix := range []string{"/v1/models/", "/models/"} {
		if strings.HasPrefix(cleaned, prefix) {
			id := strings.TrimPrefix(cleaned, prefix)
			if id != "" && !strings.Contains(id, "/") && id != "." && id != ".." {
				return "/models/" + id, true
			}
		}
	}
	return "", false
}

func jsonModel(body []byte) string {
	var peek struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &peek); err != nil {
		return ""
	}
	return peek.Model
}

func rewriteModel(body []byte, apiModel string) []byte {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil || doc == nil {
		return body
	}
	doc["model"] = apiModel
	raw, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return raw
}

func upstreamOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func copyHeader(dst, src http.Header, name string, limit int) {
	value := src.Get(name)
	if value == "" || strings.ContainsAny(value, "\r\n") || len(value) > limit {
		return
	}
	dst.Set(name, value)
}

func redactCopy(w http.ResponseWriter, src io.Reader, secret string) int64 {
	flusher, _ := w.(http.Flusher)
	hold := 0
	if secret != "" {
		hold = len(secret) - 1
	}
	buf := make([]byte, 32*1024)
	var pending []byte
	var n int64
	write := func(chunk []byte) bool {
		if len(chunk) == 0 {
			return true
		}
		nw, err := w.Write(chunk)
		n += int64(nw)
		if flusher != nil {
			flusher.Flush()
		}
		return err == nil && nw == len(chunk)
	}
	for {
		nr, err := src.Read(buf)
		if nr > 0 {
			acc := append(pending, buf[:nr]...)
			if secret != "" {
				acc = bytes.ReplaceAll(acc, []byte(secret), []byte("[redacted]"))
			}
			if err == nil && hold > 0 && len(acc) > hold {
				if !write(acc[:len(acc)-hold]) {
					return n
				}
				pending = append([]byte(nil), acc[len(acc)-hold:]...)
			} else if err == nil && hold > 0 {
				pending = acc
			} else {
				pending = nil
				if !write(acc) {
					return n
				}
			}
		}
		if err != nil {
			if len(pending) > 0 && secret != "" {
				pending = bytes.ReplaceAll(pending, []byte(secret), []byte("[redacted]"))
			}
			write(pending)
			return n
		}
	}
}
