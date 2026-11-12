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

// serverKeyLabel is the stand-in note for a key set on the server, shared by every agent.
// Adding a key in the vault overrides it.
const serverKeyLabel = "Server key (set by the admin, shared by all agents). Add your own key to override it."

// serverKeyShort is the row and card label for that same server key.
// The long note is shown once, not repeated as the label.
const serverKeyShort = "Server key"

func isServerKey(label string) bool {
	return strings.TrimSpace(label) == serverKeyLabel
}

// keySlot is one model key row. Title and Letter are what the page shows.
// ID stays a stable letter so saved vault rows and routes keep working.
// Manifest is only used to match an agent model and the process env fallback.
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
		id := strings.ToLower(providerLetter(i))
		mark := p.Mark
		if mark == "" {
			mark = strings.ToUpper(id)
		}
		title := p.Name
		if title == "" {
			title = p.Key
		}
		out = append(out, keySlot{
			ID: id, Title: title, Letter: mark,
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

func missingKeyRest(model string) string {
	name := "model"
	if slot, ok := slotForModel(model); ok && slot.Title != "" {
		name = slot.Title
	}
	return "Agents run on your own " + name + " key, and you have not added one yet."
}

func missingModelKeyCopy(model string) string {
	return "Add a model key first. " + missingKeyRest(model)
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
