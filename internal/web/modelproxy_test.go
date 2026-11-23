package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

func TestModelProxyOmitsTheKeyAndStreams(t *testing.T) {
	const key = "fixture-proxy-value"
	const prompt = "fixture-prompt-body"
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case r.URL.Path == "/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer "+key {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			if strings.Contains(r.Header.Get("Authorization"), castcfg.ModelPlaceholder) {
				http.Error(w, "placeholder", http.StatusUnauthorized)
				return
			}
			raw, _ := readLimited(r)
			if !strings.Contains(raw, `"model":"deepseek-3.2"`) || strings.Contains(raw, "digitalocean/") {
				http.Error(w, "model", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: hello\n\n"))
			w.(http.Flusher).Flush()
			w.Write([]byte("data: " + key + "\n\n"))
			w.Write([]byte("data: done\n\n"))
		case r.URL.Path == "/v1/messages":
			if r.Header.Get("x-api-key") != key || r.Header.Get("Authorization") != "" {
				http.Error(w, "anthropic auth", http.StatusUnauthorized)
				return
			}
			if r.Header.Get("anthropic-version") != "2023-06-01" {
				http.Error(w, "version", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte("data: anthropic-ok\n\ndata: " + key + "\n\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)

	var logs bytes.Buffer
	ip := "203.0.113.90:9"
	h := newHarness(t)
	h.srv.SetLogger(slog.New(slog.NewTextHandler(&logs, nil)))
	h.srv.modelProxyUpstream = up.URL + "/v1"
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", key)
	t.Setenv("CASTBOT_PROVIDER_KEY_ANTHROPIC_API", key)
	ctx := context.Background()
	user, _ := h.account("ada-proxy@example.com", "Ada", "correct-horse-1")
	sec, err := h.st.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
		Model: "digitalocean/deepseek-3.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	anth, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Nova", Title: "Helper", Status: "ready",
		Model: "anthropic-api/claude-sonnet-5-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	token := proxyGuestToken(t, h, user.ID, agent.ID)
	other := proxyGuestToken(t, h, user.ID, anth.ID)

	code, secrets, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + token})
	if code != http.StatusOK || !strings.Contains(secrets, "DIGITALOCEAN_MODEL_ACCESS_KEY") || !strings.Contains(secrets, key) {
		t.Fatalf("old guest %d", code)
	}
	fp1, err := h.st.SecretFP(ctx, user.ID, agent.ID)
	if err != nil || fp1 == "" {
		t.Fatal(err)
	}
	code, secrets, _ = h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, map[string]string{
		"Authorization":          "Bearer " + token,
		castcfg.ModelProxyHeader: castcfg.ModelProxyValue,
	})
	if code != http.StatusOK || strings.Contains(secrets, key) || strings.Contains(secrets, "DIGITALOCEAN_MODEL_ACCESS_KEY") {
		t.Fatalf("proxy guest %d %s", code, secrets)
	}
	fp2, err := h.st.SecretFP(ctx, user.ID, agent.ID)
	if err != nil || fp1 != fp2 {
		t.Fatalf("fingerprint changed %s %s", fp1, fp2)
	}

	body := `{"model":"digitalocean/deepseek-3.2","messages":[{"role":"user","content":"` + prompt + `"}]}`
	code, streamed, _ := h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/model/v1/chat/completions", body, ip, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if code != http.StatusOK || !strings.Contains(streamed, "data: hello") || !strings.Contains(streamed, "data: done") || !strings.Contains(streamed, "[redacted]") || strings.Contains(streamed, key) {
		t.Fatalf("stream %d %s", code, streamed)
	}
	if strings.Contains(logs.String(), key) || strings.Contains(logs.String(), prompt) {
		t.Fatal("log contained a key or a prompt")
	}

	before := hits.Load()
	code, _, _ = h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/model/v1/chat/completions", `{"model":"openai-api/gpt-4o"}`, ip, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if code != http.StatusForbidden || hits.Load() != before {
		t.Fatalf("other model %d hits %d", code, hits.Load())
	}
	code, _, _ = h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/model/v1/chat/completions", body, ip, map[string]string{
		"Authorization": "Bearer wrong-token",
		"Content-Type":  "application/json",
	})
	if code != http.StatusNotFound {
		t.Fatalf("wrong token %d", code)
	}
	code, _, _ = h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/model/v1/chat/completions", body, ip, map[string]string{
		"Authorization": "Bearer " + other,
		"Content-Type":  "application/json",
	})
	if code != http.StatusNotFound {
		t.Fatalf("other agent token %d", code)
	}

	code, streamed, _ = h.do(http.MethodPost, "/api/castlet/agents/"+anth.ID+"/model/v1/messages", `{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"`+prompt+`"}]}`, ip, map[string]string{
		"Authorization": "Bearer " + other,
		"Content-Type":  "application/json",
	})
	if code != http.StatusOK || !strings.Contains(streamed, "anthropic-ok") || strings.Contains(streamed, key) || !strings.Contains(streamed, "[redacted]") {
		t.Fatalf("anthropic %d %s", code, streamed)
	}
	if strings.Contains(logs.String(), key) || strings.Contains(logs.String(), prompt) {
		t.Fatal("log contained a key or a prompt")
	}
}

func TestModelProxyStaysOffAfterTheHeader(t *testing.T) {
	const key = "fixture-proxy-value"
	h := newHarness(t)
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", key)
	ctx := context.Background()
	owner, _ := h.account("ada-sticky@example.com", "Ada", "correct-horse-1")
	other, _ := h.account("bea-sticky@example.com", "Bea", "correct-horse-1")
	sec, err := h.st.CreateSection(ctx, owner.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, owner.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
		Model: "digitalocean/deepseek-3.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, owner.ID, agent.ID, "castbot-atlas", "example.invalid/castbot-runtime:test"); err != nil {
		t.Fatal(err)
	}
	otherSec, err := h.st.CreateSection(ctx, other.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, _, err := h.st.CreateAgent(ctx, other.ID, store.AgentInput{
		SectionID: otherSec.ID, Name: "Nova", Title: "Helper", Status: "ready",
		Model: "digitalocean/deepseek-3.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, other.ID, otherAgent.ID, "castbot-nova", "example.invalid/castbot-runtime:test"); err != nil {
		t.Fatal(err)
	}
	token := proxyGuestToken(t, h, owner.ID, agent.ID)
	otherToken := proxyGuestToken(t, h, other.ID, otherAgent.ID)
	ip := "203.0.113.90:9"
	secretsOf := func(header bool) (int, string) {
		t.Helper()
		hdr := map[string]string{"Authorization": "Bearer " + token}
		if header {
			hdr[castcfg.ModelProxyHeader] = castcfg.ModelProxyValue
		}
		code, body, _ := h.do(http.MethodGet, "/api/castlet/agents/"+agent.ID+"/secrets", "", ip, hdr)
		return code, body
	}
	code, body := secretsOf(true)
	if code != http.StatusOK || strings.Contains(body, key) {
		t.Fatalf("header %d %s", code, body)
	}
	code, body = secretsOf(false)
	if code != http.StatusOK || strings.Contains(body, key) || strings.Contains(body, "DIGITALOCEAN_MODEL_ACCESS_KEY") {
		t.Fatalf("later read %d %s", code, body)
	}
	if ok, err := h.st.MarkModelProxy(ctx, other.ID, agent.ID); err != nil || ok {
		t.Fatalf("other user mark %v %v", ok, err)
	}
	seen, err := h.st.ModelProxy(ctx, other.ID, agent.ID)
	if err != nil || seen {
		t.Fatalf("other user read %v %v", seen, err)
	}
	code, body, _ = h.do(http.MethodGet, "/api/castlet/agents/"+otherAgent.ID+"/secrets", "", ip, map[string]string{"Authorization": "Bearer " + otherToken})
	if code != http.StatusOK || !strings.Contains(body, key) {
		t.Fatalf("other machine %d %s", code, body)
	}
	if err := h.st.ReleaseVM(ctx, owner.ID, agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.BeginVM(ctx, owner.ID, agent.ID, "castbot-atlas", "example.invalid/castbot-runtime:test"); err != nil {
		t.Fatal(err)
	}
	code, body = secretsOf(false)
	if code != http.StatusOK || !strings.Contains(body, key) {
		t.Fatalf("new machine %d %s", code, body)
	}
}

func TestModelProxyRateLimit(t *testing.T) {
	const key = "fixture-proxy-value"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(up.Close)
	h := newHarness(t)
	h.srv.modelProxyUpstream = up.URL + "/v1"
	h.srv.modelRate = 2
	h.srv.modelWindow = time.Minute
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", key)
	ctx := context.Background()
	user, _ := h.account("ada-rate@example.com", "Ada", "correct-horse-1")
	sec, err := h.st.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
		Model: "digitalocean/deepseek-3.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	token := proxyGuestToken(t, h, user.ID, agent.ID)
	body := `{"model":"digitalocean/deepseek-3.2","messages":[{"role":"user","content":"fixture-prompt-body"}]}`
	ip := "203.0.113.90:9"
	for i := 0; i < 2; i++ {
		code, _, _ := h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/model/v1/chat/completions", body, ip, map[string]string{
			"Authorization": "Bearer " + token,
			"Content-Type":  "application/json",
		})
		if code != http.StatusOK {
			t.Fatalf("call %d status %d", i, code)
		}
	}
	code, text, _ := h.do(http.MethodPost, "/api/castlet/agents/"+agent.ID+"/model/v1/chat/completions", body, ip, map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if code != http.StatusTooManyRequests || strings.Contains(text, key) {
		t.Fatalf("limit %d %s", code, text)
	}
}

func proxyGuestToken(t *testing.T, h *harness, userID, agentID string) string {
	t.Helper()
	plain, err := h.st.IssueBootstrap(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agentID, plain)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func readLimited(r *http.Request) (string, error) {
	buf := make([]byte, 1<<20)
	n, err := r.Body.Read(buf)
	return string(buf[:n]), err
}
