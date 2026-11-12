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
func MissingProviderKey(key string) string {
	return "No key for provider " + strings.TrimSpace(key) + ". Ask the owner to add it."
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

// KeyedProvider is one manifest provider that needs a key.
type KeyedProvider struct {
	Key    string
	EnvVar string
}

// KeyedProviders returns manifest providers that need a key, in file order.
func KeyedProviders() []KeyedProvider {
	var list []provider
	if err := json.Unmarshal(manifestJSON, &list); err != nil {
		return nil
	}
	var out []KeyedProvider
	for _, p := range list {
		if p.Impl == "reserved" || p.Keyless || strings.TrimSpace(p.EnvVar) == "" || p.Key == "" {
			continue
		}
		out = append(out, KeyedProvider{Key: p.Key, EnvVar: strings.TrimSpace(p.EnvVar)})
	}
	return out
}
