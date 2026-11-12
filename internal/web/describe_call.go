package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/scott/castbot/internal/castcfg"
)

const describeTimeout = 20 * time.Second

const describeSystem = `You turn one description of a new agent into a JSON object and nothing else.
Fields:
name: a short proper name, or an empty string if the person did not give one.
title: a few words naming the job.
job: one sentence, or an empty string if they did not give a job.
instructions: plain text for the agent's instructions. No secrets.
schedule: a short phrase such as "every weekday morning", or an empty string.
reason: one short sentence about the name, or an empty string.
unclear: true when the description is too vague to set up an agent.
Do not invent a secret. Do not repeat a secret.`

// providerDescribe calls the server's provider. OpenRouter is the default
// model. The key is sent only in the Authorization header. Request and
// response bodies are not logged.
type providerDescribe struct {
	client *http.Client
	base   string
}

func (d providerDescribe) Describe(ctx context.Context, text string) (Proposal, error) {
	text, _ = redactSecrets(strings.TrimSpace(text))
	endpoint, apiModel, key, ok := d.target()
	if !ok || endpoint == "" || apiModel == "" || strings.TrimSpace(key) == "" {
		return Proposal{}, errDescribeDown
	}
	if err := ctx.Err(); err != nil {
		return Proposal{}, errDescribeDown
	}
	ctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()
	payload, err := json.Marshal(describeRequest{
		Model: apiModel,
		Messages: []describeMessage{
			{Role: "system", Content: describeSystem},
			{Role: "user", Content: text},
		},
		ResponseFormat: describeFormat{Type: "json_object"},
	})
	if err != nil {
		return Proposal{}, errDescribeDown
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Proposal{}, errDescribeDown
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := d.do(req)
	if err != nil {
		return Proposal{}, errDescribeDown
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil || res.StatusCode < 200 || res.StatusCode >= 300 {
		return Proposal{}, errDescribeDown
	}
	prop, ok := parseDescribe(raw)
	if !ok {
		return Proposal{Unclear: true}, nil
	}
	return validateProposal(prop), nil
}

func (d providerDescribe) target() (endpoint, apiModel, key string, ok bool) {
	models, err := castcfg.Models()
	if err != nil {
		return "", "", "", false
	}
	for _, model := range models {
		ep, api, found := castcfg.ChatCompletions(model.ID)
		if !found {
			continue
		}
		secret, found := describeKey(model.ID)
		if !found {
			continue
		}
		if d.base != "" {
			ep = strings.TrimRight(d.base, "/") + "/chat/completions"
		}
		return ep, api, secret, true
	}
	return "", "", "", false
}

func describeKey(model string) (string, bool) {
	_, value, missing := castcfg.ProviderSecret(model)
	if missing == "" && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), true
	}
	inf, ok := castcfg.InferenceFor(model)
	if !ok || inf.Keyless || inf.EnvVar == "" {
		return "", false
	}
	value = strings.TrimSpace(os.Getenv(inf.EnvVar))
	if value == "" {
		return "", false
	}
	return value, true
}

func (d providerDescribe) do(req *http.Request) (*http.Response, error) {
	if d.client != nil {
		return d.client.Do(req)
	}
	client := &http.Client{
		Timeout: describeTimeout + 5*time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client.Do(req)
}

type describeRequest struct {
	Model          string            `json:"model"`
	Messages       []describeMessage `json:"messages"`
	ResponseFormat describeFormat    `json:"response_format"`
}

type describeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type describeFormat struct {
	Type string `json:"type"`
}

func parseDescribe(raw []byte) (Proposal, bool) {
	var wrap struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil || len(wrap.Choices) == 0 {
		return Proposal{}, false
	}
	text := stripFence(contentText(wrap.Choices[0].Message.Content))
	obj := extractJSON(text)
	if obj == "" {
		return Proposal{}, false
	}
	var body struct {
		Name         string `json:"name"`
		Title        string `json:"title"`
		Job          string `json:"job"`
		Instructions string `json:"instructions"`
		Schedule     string `json:"schedule"`
		Reason       string `json:"reason"`
		Unclear      bool   `json:"unclear"`
	}
	if err := json.Unmarshal([]byte(obj), &body); err != nil {
		return Proposal{}, false
	}
	return Proposal{
		Name: body.Name, Title: body.Title, Job: body.Job,
		Instructions: body.Instructions, Schedule: body.Schedule,
		Reason: body.Reason, Unclear: body.Unclear,
	}, true
}

func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text
		}
	}
	if raw[0] == '[' {
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &parts) == nil {
			var b strings.Builder
			for _, part := range parts {
				b.WriteString(part.Text)
			}
			return b.String()
		}
	}
	return ""
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func validateProposal(p Proposal) Proposal {
	p = scrubProposal(p)
	p.Name = clampField(cleanName(p.Name), 40)
	p.Title = clampField(p.Title, 80)
	p.Job = clampField(p.Job, 160)
	p.Instructions = clampField(p.Instructions, 8000)
	p.Schedule = clampField(p.Schedule, 80)
	p.Reason = clampField(p.Reason, 200)
	if p.Name == "" && p.Job == "" && p.Instructions == "" {
		p.Unclear = true
	}
	return p
}

func clampField(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return strings.TrimSpace(s)
}
