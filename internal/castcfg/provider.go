package castcfg

import (
	"encoding/json"
	"os"
	"strings"
)

// Inference is the provider a saved model id belongs to.
type Inference struct {
	Key     string
	EnvVar  string
	Keyless bool
}

// InferenceFor returns the provider whose prefix matches model.
// The longest prefix wins, which is how Cast chooses a provider.
func InferenceFor(model string) (Inference, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return Inference{}, false
	}
	var list []provider
	if err := json.Unmarshal(manifestJSON, &list); err != nil {
		return Inference{}, false
	}
	var best Inference
	bestLen := -1
	found := false
	for _, p := range list {
		if p.Impl == "reserved" || p.Key == "" {
			continue
		}
		for _, prefix := range p.Prefixes {
			if prefix == "" || !strings.HasPrefix(model, prefix) {
				continue
			}
			if len(prefix) <= bestLen {
				continue
			}
			bestLen = len(prefix)
			found = true
			best = Inference{Key: p.Key, EnvVar: strings.TrimSpace(p.EnvVar), Keyless: p.Keyless}
		}
	}
	return best, found
}

// ProviderKeyEnv is the control-plane environment variable for one provider.
// digitalocean becomes CASTBOT_PROVIDER_KEY_DIGITALOCEAN.
// The value is not stored in the bundle.
func ProviderKeyEnv(key string) string {
	key = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
	if key == "" {
		return ""
	}
	return "CASTBOT_PROVIDER_KEY_" + key
}

// MissingProviderKey is the user-facing error when a run needs a key.
// The name is the provider's display name, not the manifest key.
func MissingProviderKey(key string) string {
	name := ProviderName(key)
	if name == "" {
		name = strings.TrimSpace(key)
	}
	return "No key for provider " + name + ". Ask the owner to add it."
}

// ProviderSecret is the Cast environment variable and its value for model.
// missing is set when the model needs a key and the control plane does not
// have one. A keyless provider returns empty strings.
func ProviderSecret(model string) (castEnv, value, missing string) {
	inf, ok := InferenceFor(model)
	if !ok || inf.Keyless || inf.EnvVar == "" {
		return "", "", ""
	}
	value = os.Getenv(ProviderKeyEnv(inf.Key))
	if strings.TrimSpace(value) == "" {
		return inf.EnvVar, "", MissingProviderKey(inf.Key)
	}
	return inf.EnvVar, value, ""
}

// ProviderSecretFrom prefers a vault value when one is set. An empty vault
// value keeps the process environment, so a deploy without a vault row still
// runs. The vault value is not logged.
func ProviderSecretFrom(model, vault string) (castEnv, value, missing string) {
	inf, ok := InferenceFor(model)
	if !ok || inf.Keyless || inf.EnvVar == "" {
		return "", "", ""
	}
	if strings.TrimSpace(vault) != "" {
		return inf.EnvVar, vault, ""
	}
	return ProviderSecret(model)
}

// ChatCompletions is the OpenAI-compatible chat URL and the model id that
// provider expects. The key is not part of either value.
func ChatCompletions(model string) (endpoint, apiModel string, ok bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", false
	}
	var list []provider
	if err := json.Unmarshal(manifestJSON, &list); err != nil {
		return "", "", false
	}
	var best provider
	bestLen := -1
	prefix := ""
	found := false
	for _, p := range list {
		if p.Impl == "reserved" || p.Key == "" || strings.TrimSpace(p.BaseURL) == "" {
			continue
		}
		for _, pre := range p.Prefixes {
			if pre == "" || !strings.HasPrefix(model, pre) || len(pre) <= bestLen {
				continue
			}
			bestLen = len(pre)
			found = true
			best = p
			prefix = pre
		}
	}
	if !found {
		return "", "", false
	}
	endpoint = strings.TrimRight(strings.TrimSpace(best.BaseURL), "/") + "/chat/completions"
	apiModel = strings.TrimPrefix(model, prefix)
	if apiModel == "" || strings.ContainsAny(endpoint, " \t") {
		return "", "", false
	}
	return endpoint, apiModel, true
}

// KeyedProvider is one manifest provider that needs a key.
// Name is the display name. Mark is the short badge.
type KeyedProvider struct {
	Key          string
	EnvVar       string
	Name         string
	Mark         string
	DefaultModel string
}

// KeyedProviders returns manifest providers that need a key, in file order.
func KeyedProviders() []KeyedProvider {
	var out []KeyedProvider
	for _, p := range keyedManifest() {
		out = append(out, KeyedProvider{
			Key: p.Key, EnvVar: strings.TrimSpace(p.EnvVar),
			Name: providerDisplay(p), Mark: providerMark(p.Key),
			DefaultModel: strings.TrimSpace(p.DefaultModel),
		})
	}
	return out
}

// ProviderName is the display name for a manifest key, such as OpenRouter.
func ProviderName(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	for _, p := range manifestProviders() {
		if p.Key == key {
			return providerDisplay(p)
		}
	}
	return ""
}

func providerDisplay(p provider) string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	return strings.TrimSpace(p.DisplayName)
}

// providerMark is the short badge for a keyed provider.
// The letters stay unique so two providers are not both "O".
func providerMark(key string) string {
	switch key {
	case "openrouter":
		return "OR"
	case "digitalocean":
		return "DO"
	case "openai-api":
		return "OA"
	case "anthropic-api":
		return "AN"
	case "gemini":
		return "GG"
	case "xai":
		return "XA"
	default:
		return ""
	}
}

func manifestProviders() []provider {
	var list []provider
	if err := json.Unmarshal(manifestJSON, &list); err != nil {
		return nil
	}
	return list
}

func keyedManifest() []provider {
	var out []provider
	for _, p := range manifestProviders() {
		if p.Impl == "reserved" || p.Keyless || strings.TrimSpace(p.EnvVar) == "" || p.Key == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// RewritePlaceholderModel maps a stored mock model, such as
// "Provider A / model-large", onto the manifest id it stood in for.
// A model that already selects a provider is left alone, so an existing
// agent keeps the id it was saved with.
func RewritePlaceholderModel(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" || !strings.Contains(model, "Provider ") {
		return model, false
	}
	if _, ok := InferenceFor(model); ok {
		return model, false
	}
	keyed := KeyedProviders()
	small := strings.Contains(model, "model-small")
	for i, p := range keyed {
		if i >= 26 {
			break
		}
		if !strings.Contains(model, "Provider "+string(rune('A'+i))) {
			continue
		}
		if small {
			if id := providerModelAt(p.Key, 1); id != "" && id != model {
				return id, true
			}
		}
		if p.DefaultModel != "" && p.DefaultModel != model {
			return p.DefaultModel, true
		}
	}
	return model, false
}

// RewritePlaceholderText replaces mock model phrases in a saved document.
// Only those phrases change. Other text, including a real model id, stays.
func RewritePlaceholderText(text string) (string, bool) {
	if !strings.Contains(text, "Provider ") {
		return text, false
	}
	out := text
	for i, p := range KeyedProviders() {
		if i >= 26 {
			break
		}
		letter := "Provider " + string(rune('A'+i))
		large := p.DefaultModel
		smallID := providerModelAt(p.Key, 1)
		if smallID == "" {
			smallID = large
		}
		if smallID != "" {
			out = strings.ReplaceAll(out, letter+" / model-small", smallID)
		}
		if large != "" {
			out = strings.ReplaceAll(out, letter+" / model-large", large)
		}
	}
	return out, out != text
}

func providerModelAt(key string, n int) string {
	models, err := Models()
	if err != nil {
		return ""
	}
	seen := 0
	for _, model := range models {
		inf, ok := InferenceFor(model.ID)
		if !ok || inf.Key != key {
			continue
		}
		if seen == n {
			return model.ID
		}
		seen++
	}
	return ""
}
