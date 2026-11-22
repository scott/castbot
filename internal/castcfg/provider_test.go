package castcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInferenceForDigitalOcean(t *testing.T) {
	got, ok := InferenceFor("digitalocean/deepseek-v3.2")
	if !ok || got.Key != "digitalocean" || got.EnvVar != "DIGITALOCEAN_MODEL_ACCESS_KEY" || got.Keyless {
		t.Fatalf("%+v ok %v", got, ok)
	}
	if ProviderKeyEnv(got.Key) != "CASTBOT_PROVIDER_KEY_DIGITALOCEAN" {
		t.Fatal(ProviderKeyEnv(got.Key))
	}
	if MissingProviderKey(got.Key) != "No key for provider DigitalOcean. Ask the owner to add it." {
		t.Fatal(MissingProviderKey(got.Key))
	}
}

func TestInferenceKeylessOllama(t *testing.T) {
	got, ok := InferenceFor("ollama/qwen3")
	if !ok || !got.Keyless {
		t.Fatalf("%+v", got)
	}
	env, value, missing := ProviderSecret("ollama/qwen3")
	if env != "" || value != "" || missing != "" {
		t.Fatalf("%q %q %q", env, value, missing)
	}
}

func TestChatCompletionsStripsThePrefix(t *testing.T) {
	endpoint, apiModel, ok := ChatCompletions("openrouter/minimax/minimax-m2.5")
	if !ok || apiModel != "minimax/minimax-m2.5" || !strings.HasSuffix(endpoint, "/chat/completions") {
		t.Fatalf("%q %q %v", endpoint, apiModel, ok)
	}
	if strings.Contains(endpoint, "CASTBOT") || strings.Contains(apiModel, "sk-") {
		t.Fatal("endpoint carried a key")
	}
}

func TestInstallerProviderMenuMatchesKeyedProviders(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "# model-providers:start\n")
	end := strings.Index(text, "# model-providers:end\n")
	if start < 0 || end < start {
		t.Fatal("model provider block is missing")
	}
	block := text[start:end]
	var got []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "}" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "model_provider") || line == "EOF" || line == "cat <<'EOF'" {
			continue
		}
		got = append(got, line)
	}
	keyed := KeyedProviders()
	if len(got) != len(keyed) {
		t.Fatalf("menu %q", got)
	}
	for i, p := range keyed {
		want := p.Key + "\t" + p.Name
		if got[i] != want {
			t.Fatalf("line %d %q want %q", i, got[i], want)
		}
	}
	if strings.Contains(block, "ollama") || strings.Contains(block, "bedrock") || strings.Contains(block, "Ollama") || strings.Contains(block, "Bedrock") {
		t.Fatal("keyless or reserved provider in the menu")
	}
}

func TestProviderDisplayNames(t *testing.T) {
	want := []struct{ key, name, mark string }{
		{"openrouter", "OpenRouter", "OR"},
		{"digitalocean", "DigitalOcean", "DO"},
		{"openai-api", "OpenAI", "OA"},
		{"anthropic-api", "Anthropic", "AN"},
		{"gemini", "Google Gemini", "GG"},
		{"xai", "xAI", "XA"},
	}
	got := KeyedProviders()
	if len(got) != len(want) {
		t.Fatalf("keyed %d", len(got))
	}
	for i, row := range want {
		if got[i].Key != row.key || got[i].Name != row.name || got[i].Mark != row.mark || got[i].DefaultModel == "" {
			t.Fatalf("%d %+v", i, got[i])
		}
		if ProviderName(row.key) != row.name {
			t.Fatal(ProviderName(row.key))
		}
	}
	if ProviderName("ollama") != "Ollama" || ProviderName("bedrock") != "Amazon Bedrock" {
		t.Fatalf("ollama %q bedrock %q", ProviderName("ollama"), ProviderName("bedrock"))
	}
}

func TestRewritePlaceholderKeepsRealModels(t *testing.T) {
	real := []string{
		"openrouter/minimax/minimax-m2.5",
		"digitalocean/deepseek-v4.1-flash",
		"digitalocean/deepseek-3.2",
		"openai-api/gpt-4o",
		"anthropic-api/claude-sonnet-5-5",
		"gemini/gemini-3.8-flash",
		"xai/grok-4.7",
		"ollama/qwen3",
	}
	for _, id := range real {
		if !KnownModel(id) {
			t.Fatalf("catalog lost %s", id)
		}
		got, changed := RewritePlaceholderModel(id)
		if changed || got != id {
			t.Fatalf("rewrote %s -> %s", id, got)
		}
		text, changed := RewritePlaceholderText("model: " + id + "\n")
		if changed || text != "model: "+id+"\n" {
			t.Fatalf("text %s", text)
		}
	}
	large, changed := RewritePlaceholderModel("Provider A / model-large")
	if !changed || large != "openrouter/minimax/minimax-m2.5" {
		t.Fatalf("large %s %v", large, changed)
	}
	small, changed := RewritePlaceholderModel("Provider A / model-small")
	if !changed || small != "openrouter/anthropic/claude-opus-4.5" {
		t.Fatalf("small %s %v", small, changed)
	}
	ocean, changed := RewritePlaceholderModel("Provider B / model-large")
	if !changed || ocean != "digitalocean/deepseek-v4.1-flash" {
		t.Fatalf("ocean %s %v", ocean, changed)
	}
	next, changed := RewritePlaceholderModel("Provider B / model-small")
	if !changed || next != "digitalocean/deepseek-3.2" {
		t.Fatalf("ocean small %s %v", next, changed)
	}
	plain, changed := RewritePlaceholderModel("model-large")
	if changed || plain != "model-large" {
		t.Fatalf("bare %s %v", plain, changed)
	}
	body, changed := RewritePlaceholderText("brain:\n  model: Provider A / model-large\nnotes: keep this\n")
	if !changed || !strings.Contains(body, "model: openrouter/minimax/minimax-m2.5") || !strings.Contains(body, "notes: keep this") || strings.Contains(body, "Provider A") {
		t.Fatalf("body\n%s", body)
	}
}

func TestProviderSecretMissing(t *testing.T) {
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", "")
	env, value, missing := ProviderSecret("digitalocean/deepseek-v3.2")
	if env != "DIGITALOCEAN_MODEL_ACCESS_KEY" || value != "" || missing != MissingProviderKey("digitalocean") {
		t.Fatalf("%q %q %q", env, value, missing)
	}
	t.Setenv("CASTBOT_PROVIDER_KEY_OPENAI_API", "fixture-openai")
	env, value, missing = ProviderSecret("openai-api/gpt-4o")
	if env != "OPENAI_API_KEY" || value != "fixture-openai" || missing != "" {
		t.Fatalf("%q %q %q", env, value, missing)
	}
}
