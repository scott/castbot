package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

type cliFlash struct {
	blob string
	note string
}

type flashKey struct{}

type importBlob struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	AgentID     string `json:"agent_id"`
	URL         string `json:"url"`
	Provider    string `json:"provider"`
	Token       string `json:"token"`
}

func (s *Server) exportCLI(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	vm, err := s.store.LiveVM(r.Context(), user.ID, agent.ID)
	if err != nil && err != store.ErrNotFound {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	endpoint := ""
	if err == nil {
		endpoint = strings.TrimSpace(vm.Endpoint)
	}
	flash := cliFlash{}
	if endpoint == "" {
		flash.note = "This agent has no machine address yet."
	} else {
		slug, err := s.store.AssignCLISlug(r.Context(), user.ID, agent.ID)
		if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		id, plain, err := s.store.IssueCLIToken(r.Context(), user.ID, agent.ID)
		if err == store.ErrCLICap {
			flash.note = "Revoke a token before adding another."
		} else if err == store.ErrCLIRate {
			flash.note = "Too many tokens were added just now. Wait a minute and try again."
		} else if err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		} else {
			raw, err := json.MarshalIndent(importBlob{
				Name: slug, DisplayName: agent.Name, AgentID: agent.ID, URL: endpoint, Provider: "microvm", Token: plain,
			}, "", "  ")
			if err != nil {
				_ = s.store.RevokeCLIToken(r.Context(), user.ID, agent.ID, id)
				http.Error(w, "Error", http.StatusInternalServerError)
				return
			}
			flash.blob = string(raw)
			flash.note = "Copy this now. It is shown once."
			if push := s.pushCLITokens(r.Context(), user.ID, agent.ID); push != "" {
				flash.note = flash.note + " " + push
			}
			r = r.WithContext(context.WithValue(r.Context(), flashKey{}, flash))
			s.finishExport(w, r, user, agent.ID, id)
			return
		}
	}
	r = r.WithContext(context.WithValue(r.Context(), flashKey{}, flash))
	s.renderAgent(w, r, user, agent.ID, false, "machine", http.StatusOK, "")
}

// finishExport shows a newly minted token, or revokes it when the page
// never reaches the browser.
func (s *Server) finishExport(w http.ResponseWriter, r *http.Request, user store.User, agentID, tokenID string) {
	rec := httptest.NewRecorder()
	err := s.renderAgent(rec, r, user, agentID, false, "machine", http.StatusOK, "")
	if err != nil || rec.Code != http.StatusOK || r.Context().Err() != nil {
		_ = s.store.RevokeCLIToken(context.Background(), user.ID, agentID, tokenID)
		s.pushCLITokens(context.Background(), user.ID, agentID)
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	for k, vs := range rec.Header() {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	if _, werr := w.Write(rec.Body.Bytes()); werr != nil || r.Context().Err() != nil {
		_ = s.store.RevokeCLIToken(context.Background(), user.ID, agentID, tokenID)
		s.pushCLITokens(context.Background(), user.ID, agentID)
		return
	}
	_ = s.store.MarkCLITokenViewed(r.Context(), user.ID, agentID, tokenID)
}

func cliTokenView(tok store.CLIToken, loc *time.Location) cliTokView {
	view := cliTokView{ID: tok.ID, Prefix: tok.Prefix, Created: clockStamp(tok.CreatedAt, loc), LastUsed: "Not yet", Status: "Active"}
	if tok.LastUsedAt != "" {
		view.LastUsed = clockStamp(tok.LastUsedAt, loc)
	}
	if tok.ViewedAt == "" {
		view.Unseen = true
		view.Status = "Not shown"
	}
	if tok.RevokedAt != "" {
		view.Revoked = true
		view.Status = "Revoked"
	}
	return view
}

func (s *Server) revokeCLI(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	if _, err := s.store.GetAgent(r.Context(), user.ID, agentID); s.missing(w, err) {
		return
	}
	if err := s.store.RevokeCLIToken(r.Context(), user.ID, agentID, r.PathValue("token")); s.missing(w, err) {
		return
	}
	if push := s.pushCLITokens(r.Context(), user.ID, agentID); push != "" {
		r = r.WithContext(context.WithValue(r.Context(), flashKey{}, cliFlash{note: push + " The token stops working within 30 seconds."}))
	}
	if r.Header.Get("HX-Request") == "true" {
		s.renderCLISection(w, r, user, agentID)
		return
	}
	s.renderAgent(w, r, user, agentID, false, "machine", http.StatusOK, "")
}

func (s *Server) renderCLISection(w http.ResponseWriter, r *http.Request, user store.User, agentID string) {
	agent, err := s.store.GetAgent(r.Context(), user.ID, agentID)
	if s.missing(w, err) {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Agent = s.paintAgent(r.Context(), user.ID, agent, true, userLocation(user.Timezone))
	tokens, err := s.store.ListCLITokens(r.Context(), user.ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	loc := userLocation(user.Timezone)
	for _, tok := range tokens {
		p.CLITokens = append(p.CLITokens, cliTokenView(tok, loc))
	}
	if slug, err := s.store.CLISlug(r.Context(), user.ID, agent.ID); err == nil {
		p.CLIName = slug
	}
	if flash, ok := r.Context().Value(flashKey{}).(cliFlash); ok && flash.note != "" {
		p.CLINote = flash.note
	}
	s.render(w, http.StatusOK, "cliBox", p)
}

func (s *Server) machineCLITokens(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	tokens, err := s.store.ListCLITokenHashes(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	if tokens == nil {
		tokens = []store.CLIHash{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": tokens})
}

func (s *Server) machineCLIUsed(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	if err := dec.Decode(&body); err != nil || body.ID == "" {
		writeText(w, http.StatusBadRequest, "bad request\n")
		return
	}
	if err := s.store.TouchCLIToken(r.Context(), userID, r.PathValue("id"), body.ID); err != nil {
		if err == store.ErrNotFound {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) machineJournal(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.machineUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Entries []store.JournalEntry `json:"entries"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := dec.Decode(&body); err != nil && err != io.EOF {
		writeText(w, http.StatusBadRequest, "bad request\n")
		return
	}
	last, err := s.store.IngestJournal(r.Context(), userID, r.PathValue("id"), body.Entries)
	if err != nil {
		if err == store.ErrNotFound {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		if err == store.ErrInvalid {
			writeText(w, http.StatusBadRequest, "bad request\n")
			return
		}
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"last": last})
}

func (s *Server) pushCLITokens(ctx context.Context, userID, agentID string) string {
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if err != nil {
		return ""
	}
	state, endpoint := s.observedMachine(ctx, userID, agentID, vm)
	if state != "running" || endpoint == "" {
		return "The machine is not running, so it was not updated."
	}
	_, token, err := s.store.VMCredentials(ctx, userID, agentID)
	if err != nil || token == "" {
		return "The machine is not running, so it was not updated."
	}
	hashes, err := s.store.ListCLITokenHashes(ctx, userID, agentID)
	if err != nil {
		return ""
	}
	if hashes == nil {
		hashes = []store.CLIHash{}
	}
	body, err := json.Marshal(map[string]any{"tokens": hashes})
	if err != nil {
		return ""
	}
	s.mu.Lock()
	h := s.machine
	s.mu.Unlock()
	if h == nil {
		return ""
	}
	if err := h.PushCLI(ctx, endpoint, token, body); err != nil {
		return "The machine did not accept the token change."
	}
	return ""
}

func (s *Server) pullJournal(ctx context.Context, userID, agentID, endpoint, token string) error {
	s.mu.Lock()
	var h host.AgentHost
	h = s.machine
	s.mu.Unlock()
	if h == nil || endpoint == "" || token == "" {
		return nil
	}
	after, err := s.store.JournalCursor(ctx, userID, agentID)
	if err != nil {
		return err
	}
	raw, err := h.PullJournal(ctx, endpoint, token, after)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var body struct {
		Entries []store.JournalEntry `json:"entries"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return errors.New("journal pull was not readable")
	}
	if len(body.Entries) == 0 {
		return nil
	}
	_, err = s.store.IngestJournal(ctx, userID, agentID, body.Entries)
	return err
}

func (s *Server) pickThread(r *http.Request, userID, agentID string) (store.Thread, error) {
	threads, err := s.store.ListThreadsByAgent(r.Context(), userID, agentID)
	if err != nil {
		return store.Thread{}, err
	}
	want := strings.TrimSpace(r.FormValue("thread"))
	if want == "" {
		want = strings.TrimSpace(r.URL.Query().Get("thread"))
	}
	var dm store.Thread
	for _, t := range threads {
		if want != "" && t.ID == want {
			return t, nil
		}
		if t.Kind != "cli" && dm.ID == "" {
			dm = t
		}
	}
	if want != "" {
		return store.Thread{}, store.ErrNotFound
	}
	if dm.ID != "" {
		return dm, nil
	}
	if len(threads) > 0 {
		return threads[0], nil
	}
	return store.Thread{}, store.ErrNotFound
}
