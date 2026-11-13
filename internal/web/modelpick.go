package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/castcfg/modellist"
)

const (
	modelWarning = "This model is no longer listed. You can keep it, or pick another."
	modelHelp    = "The list is this provider's models. A saved model that is no longer listed stays selected."
)

// providerChoice is one row in the provider select.
type providerChoice struct {
	Key, Name string
	On        bool
}

// modelPick is the shared provider-then-model control.
// Model is the full prefixed id posted as name="model".
type modelPick struct {
	Surface   string
	Provider  string
	Model     string
	Providers []providerChoice
	Models    []modelChoice
	Warning   string
	Help      string
	Disabled  bool
	Refresh   bool
}

// keyFine is the setup card's model key line. It never carries the key value.
type keyFine struct {
	Line, Label string
	Server, OOB bool
}

// modelPick builds the control for a page. It uses a warm cache or the
// manifest and does not dial.
func (s *Server) modelPick(ctx context.Context, userID, current, surface string) modelPick {
	return s.assemblePick(ctx, userID, "", current, surface, false, false)
}

// AcceptModel is the create and edit gate. A live id, a manifest id, or the
// previously saved id is accepted. A different unknown id is not.
// A live id counts only when this user's cache already holds it. This does
// not dial and does not log the key.
func (s *Server) AcceptModel(ctx context.Context, userID, id, previous string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if previous != "" && id == strings.TrimSpace(previous) {
		return true
	}
	if castcfg.KnownModel(id) {
		return true
	}
	provider := providerOf(id)
	if provider == "" || provider == "bedrock" {
		return false
	}
	token, ok := s.modelToken(ctx, userID, provider)
	if !ok {
		return false
	}
	return s.modelCatalog().CachedID(provider, token, id)
}

func (s *Server) modelPicker(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	provider := clip(r.FormValue("provider"), 80)
	current := clip(r.FormValue("model"), 200)
	surface := r.FormValue("surface")
	if surface != "setup" {
		surface = "form"
	}
	pick := s.assemblePick(r.Context(), user.ID, provider, current, surface, true, true)
	token, _ := s.modelToken(r.Context(), user.ID, pick.Provider)
	body, err := execTemplate("modelPicker", pick)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if surface == "setup" {
		extra, err := execTemplate("modelKeyFine", s.keyFineFor(r.Context(), user.ID, pick.Model, true))
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		body += extra
	}
	if token != "" && strings.Contains(body, token) {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (s *Server) keyFineFor(ctx context.Context, userID, model string, oob bool) keyFine {
	label, ok := s.userModelKey(ctx, userID, model)
	if !ok {
		return keyFine{OOB: oob}
	}
	return keyFine{
		Line: modelKeyLine(label), Label: label, Server: label == serverKeyShort, OOB: oob,
	}
}

func (s *Server) assemblePick(ctx context.Context, userID, asked, current, surface string, fetch, explicit bool) modelPick {
	current = strings.TrimSpace(current)
	providers := s.configuredProviders(ctx, userID, fetch)
	pick := modelPick{Surface: surface}
	if surface == "form" {
		pick.Help = modelHelp
	}
	for _, p := range providers {
		pick.Providers = append(pick.Providers, p)
	}
	if len(providers) == 0 {
		pick.Disabled = true
		pick.Model = current
		if current != "" {
			pick.Models = []modelChoice{{ID: current, Label: modelLabel(current), On: true}}
			if !castcfg.KnownModel(current) {
				pick.Warning = modelWarning
			}
		}
		return pick
	}
	provider := ""
	if explicit {
		for _, p := range providers {
			if p.Key == asked {
				provider = asked
				break
			}
		}
	}
	if provider == "" && !explicit {
		saved := providerOf(current)
		for _, p := range providers {
			if p.Key == saved {
				provider = saved
				break
			}
		}
	}
	if provider == "" && current != "" && !explicit {
		pick.Model = current
		pick.Models = []modelChoice{{ID: current, Label: modelLabel(current), On: true}}
		pick.Warning = modelWarning
		return pick
	}
	if provider == "" {
		provider = providers[0].Key
	}
	pick.Provider = provider
	for i := range pick.Providers {
		pick.Providers[i].On = pick.Providers[i].Key == provider
	}
	token, _ := s.modelToken(ctx, userID, provider)
	if token != "" && strings.Contains(current, token) {
		current = ""
	}
	list, _ := s.modelsFor(ctx, userID, provider, fetch)
	keep := current != "" && providerOf(current) == provider
	selected, warn := chooseListed(provider, list.Models, current, keep)
	if warn && selected != "" && (token == "" || !strings.Contains(selected, token)) {
		list.Models = append([]modellist.Model{{ID: selected, Label: selected}}, list.Models...)
		pick.Warning = modelWarning
	} else if warn {
		selected = ""
		if len(list.Models) > 0 {
			selected = list.Models[0].ID
		}
	}
	pick.Model = selected
	for _, model := range list.Models {
		if token != "" && (strings.Contains(model.ID, token) || strings.Contains(model.Label, token)) {
			continue
		}
		pick.Models = append(pick.Models, modelChoice{
			ID: model.ID, Label: modelLabel(model.ID), On: model.ID == selected,
		})
	}
	if selected != "" {
		seen := false
		for i := range pick.Models {
			if pick.Models[i].ID == selected {
				pick.Models[i].On = true
				seen = true
			} else {
				pick.Models[i].On = false
			}
		}
		if !seen {
			pick.Model = ""
			if len(pick.Models) > 0 {
				pick.Models[0].On = true
				pick.Model = pick.Models[0].ID
			}
		}
	}
	if !explicit && list.Fallback && pick.Provider != "" && s.modelCatalog().AllowsFetch() {
		pick.Refresh = true
	}
	return pick
}

func chooseListed(provider string, models []modellist.Model, current string, keep bool) (string, bool) {
	if keep {
		for _, model := range models {
			if model.ID == current {
				return current, false
			}
		}
		return current, true
	}
	def := modellist.DefaultID(provider)
	for _, model := range models {
		if model.ID == def {
			return def, false
		}
	}
	if len(models) > 0 {
		return models[0].ID, false
	}
	return "", false
}

func (s *Server) configuredProviders(ctx context.Context, userID string, fetch bool) []providerChoice {
	var out []providerChoice
	for _, p := range castcfg.KeyedProviders() {
		if _, ok := s.modelToken(ctx, userID, p.Key); !ok {
			continue
		}
		name := p.Name
		if name == "" {
			name = p.Key
		}
		out = append(out, providerChoice{Key: p.Key, Name: name})
	}
	if s.ollamaReady(ctx, fetch) {
		name := castcfg.ProviderName("ollama")
		if name == "" {
			name = "ollama"
		}
		out = append(out, providerChoice{Key: "ollama", Name: name})
	}
	return out
}

func (s *Server) ollamaReady(ctx context.Context, fetch bool) bool {
	if !modellist.OllamaConfigured() {
		return false
	}
	cat := s.modelCatalog()
	if fetch && cat.AllowsFetch() {
		_, err := cat.List(ctx, "ollama", "")
		return err == nil
	}
	return cat.HasLive("ollama", "")
}

func (s *Server) modelsFor(ctx context.Context, userID, provider string, fetch bool) (modellist.List, bool) {
	token, ok := s.modelToken(ctx, userID, provider)
	if provider == "ollama" {
		if !s.ollamaReady(ctx, fetch) {
			return modellist.List{}, false
		}
		token = ""
		ok = true
	}
	if !ok {
		return modellist.List{}, false
	}
	cat := s.modelCatalog()
	if fetch && cat.AllowsFetch() {
		list, err := cat.List(ctx, provider, token)
		if err == nil {
			return list, true
		}
	}
	return cat.Snapshot(provider, token), true
}

func (s *Server) defaultConfiguredModel(ctx context.Context, userID string) string {
	for _, p := range s.configuredProviders(ctx, userID, false) {
		if id := modellist.DefaultID(p.Key); id != "" {
			return id
		}
	}
	return ""
}

func modelIDs(pick modelPick) []string {
	out := make([]string, 0, len(pick.Models))
	for _, model := range pick.Models {
		out = append(out, model.ID)
	}
	return out
}

func providerOf(id string) string {
	inf, ok := castcfg.InferenceFor(id)
	if !ok {
		return ""
	}
	return inf.Key
}

func clip(v string, n int) string {
	v = strings.TrimSpace(v)
	if len(v) > n {
		return ""
	}
	return v
}
