package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/store"
)

type rollbackKey struct{}

func wantsRollback(r *http.Request) bool {
	v, _ := r.Context().Value(rollbackKey{}).(bool)
	return v
}

func (s *Server) confirmRollback(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), rollbackKey{}, true))
	s.renderChat(w, r, user, r.PathValue("id"), false, "machine", http.StatusOK, "")
}

func (s *Server) postRollback(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if s.replacing(r.Context(), user.ID, agent.ID) {
		writeText(w, http.StatusConflict, "A replacement is already running.\n")
		return
	}
	_, line, err := s.store.ReplacePrecheck(r.Context(), user.ID, agent.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if line != "" {
		writeText(w, http.StatusConflict, line+"\n")
		return
	}
	rb, err := s.store.ActiveRollback(r.Context(), user.ID, agent.ID)
	if s.missing(w, err) {
		return
	}
	live, err := s.store.LiveVM(r.Context(), user.ID, agent.ID)
	if s.missing(w, err) {
		return
	}
	if replace.ImageTag(live.Image) == replace.ImageTag(rb.Image) {
		writeText(w, http.StatusConflict, "This agent is already on that runtime.\n")
		return
	}
	h, _, publicURL := s.hostConfig()
	if h == nil || publicURL == "" || rb.Image == "" {
		writeText(w, http.StatusBadRequest, "No machine host is configured.\n")
		return
	}
	image := rb.Image
	s.prov.Add(1)
	go func() {
		defer s.prov.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		_, err := replace.Run(ctx, s.store, h, replace.Options{
			UserID: user.ID, AgentID: agent.ID,
			ActorKind: "user", ActorID: user.ID, Actor: "web:" + user.Email,
			Image: image, PublicURL: publicURL, DataDir: s.dataDir,
		})
		if err != nil {
			log.Printf("rollback finished agent %s class %s", agent.ID, replaceClass(err))
		}
		if !s.replacing(ctx, user.ID, agent.ID) {
			s.fireAfterRun(ctx, user.ID, agent.ID)
		}
	}()
	target := "/agents/" + url.PathEscape(agent.ID) + "/machine"
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
