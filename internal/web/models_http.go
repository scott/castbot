package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/castcfg/modellist"
)

func (s *Server) modelCatalog() *modellist.Catalog {
	if s.models == nil {
		s.models = modellist.New()
	}
	return s.models
}

// ModelCatalog is the live model list. Tests point it at a stand-in host.
func (s *Server) ModelCatalog() *modellist.Catalog {
	return s.modelCatalog()
}

// modelToken is the key a run would use for this provider.
// A vault row wins. Otherwise the server environment is used.
// The value is not logged.
func (s *Server) modelToken(ctx context.Context, userID, provider string) (string, bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" || provider == "bedrock" {
		return "", false
	}
	if provider == "ollama" {
		return "", modellist.OllamaConfigured()
	}
	slot, ok := slotByID(provider)
	if !ok || slot.Manifest != provider {
		return "", false
	}
	rows, err := s.store.ProviderSecrets(ctx, userID, []string{slot.ID, slot.Manifest})
	if err != nil {
		return "", false
	}
	if len(rows) > 0 && strings.TrimSpace(rows[0].Value) != "" {
		return strings.TrimSpace(rows[0].Value), true
	}
	if v := strings.TrimSpace(providerEnv(slot.Manifest)); v != "" {
		return v, true
	}
	return "", false
}

func (s *Server) providerModels(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	provider := r.PathValue("provider")
	token, configured := s.modelToken(r.Context(), user.ID, provider)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !configured {
		writeModelMiss(w)
		return
	}
	list, err := s.modelCatalog().List(r.Context(), provider, token)
	if err != nil {
		writeModelMiss(w)
		return
	}
	type row struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	payload := struct {
		Models []row `json:"models"`
	}{Models: []row{}}
	for _, model := range list.Models {
		if token != "" && (strings.Contains(model.ID, token) || strings.Contains(model.Label, token)) {
			continue
		}
		payload.Models = append(payload.Models, row{ID: model.ID, Label: model.Label})
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

func writeModelMiss(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":"not configured"}`))
}
