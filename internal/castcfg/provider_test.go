package castcfg

import (
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
	if MissingProviderKey(got.Key) != "No key for provider digitalocean. Ask the owner to add it." {
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
