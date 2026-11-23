package castcfg

import "testing"

func TestAPIModelIDStripsThePrefix(t *testing.T) {
	key, api, ok := APIModelID("digitalocean/deepseek-3.2")
	if !ok || key != "digitalocean" || api != "deepseek-3.2" {
		t.Fatalf("%s %s %v", key, api, ok)
	}
	got, ok := ModelAllowed("digitalocean/deepseek-3.2", "deepseek-3.2")
	if !ok || got != "deepseek-3.2" {
		t.Fatalf("stripped %s %v", got, ok)
	}
	if _, ok := ModelAllowed("digitalocean/deepseek-3.2", "openai-api/gpt-4o"); ok {
		t.Fatal("other provider was allowed")
	}
	if _, ok := ModelAllowed("ollama/qwen3", "qwen3"); ok {
		t.Fatal("keyless provider was proxied")
	}
	var sawOllama bool
	for _, p := range ProxyProviders() {
		if p.Key == "ollama" || p.Key == "bedrock" {
			t.Fatal(p.Key)
		}
		if p.Key == "openai-api" && p.BaseURLEnv != "" {
			t.Fatal("openai gained a base env")
		}
		sawOllama = sawOllama || p.Key == "openrouter"
	}
	if !sawOllama {
		t.Fatal("openrouter missing")
	}
}
