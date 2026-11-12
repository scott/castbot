package web

import (
	"context"
	"os"
	"strings"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

func providerEnv(manifest string) string {
	if manifest == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(castcfg.ProviderKeyEnv(manifest)))
}

// keySlot is one model key row. The title is castbot copy. The manifest id
// is only used to match an agent model and the process env fallback.
type keySlot struct {
	ID       string
	Title    string
	Letter   string
	Manifest string
	Env      string
}

func modelSlots() []keySlot {
	keyed := castcfg.KeyedProviders()
	var out []keySlot
	for i, p := range keyed {
		letter := providerLetter(i)
		out = append(out, keySlot{
			ID: strings.ToLower(letter), Title: "Provider " + letter, Letter: letter,
			Manifest: p.Key, Env: p.EnvVar,
		})
	}
	return out
}

func providerLetter(i int) string {
	if i >= 0 && i < 26 {
		return string(rune('A' + i))
	}
	return "Z"
}

func slotByID(id string) (keySlot, bool) {
	for _, slot := range modelSlots() {
		if slot.ID == id || slot.Manifest == id {
			return slot, true
		}
	}
	return keySlot{}, false
}

func slotForModel(model string) (keySlot, bool) {
	slots := modelSlots()
	if strings.Contains(model, "Provider A") && len(slots) > 0 {
		return slots[0], true
	}
	if strings.Contains(model, "Provider B") && len(slots) > 1 {
		return slots[1], true
	}
	inf, ok := castcfg.InferenceFor(model)
	if !ok || inf.Keyless || inf.EnvVar == "" {
		return keySlot{}, false
	}
	for _, slot := range slots {
		if slot.Manifest == inf.Key {
			return slot, true
		}
	}
	return keySlot{}, false
}

// effectiveModelSecret returns the guest env name and value for a model.
// The newest vault row for that provider wins. Otherwise the process env is used.
func (s *Server) effectiveModelSecret(ctx context.Context, userID, model string) (envName, value, missing string) {
	vault := ""
	slot, slotted := slotForModel(model)
	if slotted {
		rows, err := s.store.ProviderSecrets(ctx, userID, []string{slot.ID, slot.Manifest})
		if err == nil && len(rows) > 0 {
			vault = strings.TrimSpace(rows[0].Value)
		}
	}
	if _, ok := castcfg.InferenceFor(model); ok {
		envName, value, missing = castcfg.ProviderSecretFrom(model, vault)
		if envName != "" || missing != "" {
			return envName, value, missing
		}
	}
	if slotted {
		if vault != "" {
			return slot.Env, vault, ""
		}
		if v := strings.TrimSpace(providerEnv(slot.Manifest)); v != "" {
			return slot.Env, v, ""
		}
	}
	return castcfg.ProviderSecret(model)
}

func (s *Server) agentsUsingSlot(ctx context.Context, userID string, slot keySlot) ([]store.Agent, error) {
	agents, err := s.store.ListAgents(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []store.Agent
	for _, agent := range agents {
		got, ok := slotForModel(agent.Model)
		if ok && got.ID == slot.ID {
			out = append(out, agent)
		}
	}
	return out, nil
}

func labelToName(label string) string {
	var b strings.Builder
	prev := false
	for _, r := range strings.ToUpper(strings.TrimSpace(label)) {
		ok := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			prev = false
			continue
		}
		if !prev && b.Len() > 0 {
			b.WriteByte('_')
			prev = true
		}
	}
	name := strings.Trim(b.String(), "_")
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		name = "KEY_" + name
		name = strings.Trim(name, "_")
	}
	if len(name) > 64 {
		name = strings.Trim(name[:64], "_")
	}
	if !store.SecretNameOK(name) {
		return ""
	}
	return name
}
