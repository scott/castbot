package web

import (
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/store"
)

func (s *Server) listSections(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if id := r.URL.Query().Get("id"); id != "" {
		sec, err := s.store.GetSection(r.Context(), user.ID, id)
		if mapStoreErr(w, r, err) {
			return
		}
		writeText(w, http.StatusOK, sec.ID+"\t"+sec.Name+"\n")
		return
	}
	list, err := s.store.ListSections(r.Context(), user.ID)
	if mapStoreErr(w, r, err) {
		return
	}
	var b strings.Builder
	for _, sec := range list {
		b.WriteString(sec.ID + "\t" + sec.Name + "\n")
	}
	writeText(w, http.StatusOK, b.String())
}

func (s *Server) getSection(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	sec, err := s.store.GetSection(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	writeText(w, http.StatusOK, sec.ID+"\t"+sec.Name+"\n")
}

func (s *Server) updateSection(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.UpdateSection(r.Context(), user.ID, r.PathValue("id"), r.FormValue("name"))
	if mapStoreErr(w, r, err) {
		return
	}
	s.done(w, r)
}

func (s *Server) deleteSection(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteSection(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	s.done(w, r)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	var list []store.Agent
	var err error
	if sectionID := r.URL.Query().Get("section"); sectionID != "" {
		list, err = s.store.ListAgentsInSection(r.Context(), user.ID, sectionID)
	} else {
		list, err = s.store.ListAgents(r.Context(), user.ID)
	}
	if mapStoreErr(w, r, err) {
		return
	}
	var b strings.Builder
	for _, a := range list {
		b.WriteString(a.ID + "\t" + a.Name + "\n")
	}
	writeText(w, http.StatusOK, b.String())
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.UpdateAgent(r.Context(), user.ID, r.PathValue("id"), r.FormValue("title"))
	if mapStoreErr(w, r, err) {
		return
	}
	s.done(w, r)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteAgent(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	s.done(w, r)
}

func (s *Server) listThreads(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	var list []store.Thread
	var err error
	if agentID := r.URL.Query().Get("agent"); agentID != "" {
		list, err = s.store.ListThreadsByAgent(r.Context(), user.ID, agentID)
	} else {
		list, err = s.store.ListThreads(r.Context(), user.ID)
	}
	if mapStoreErr(w, r, err) {
		return
	}
	var b strings.Builder
	for _, t := range list {
		b.WriteString(t.ID + "\t" + t.Title + "\n")
	}
	writeText(w, http.StatusOK, b.String())
}

func (s *Server) createThread(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	kind := r.FormValue("kind")
	if kind == "" {
		kind = "dm"
	}
	th, err := s.store.CreateThread(r.Context(), user.ID, kind, r.FormValue("agent"), r.FormValue("title"))
	if mapStoreErr(w, r, err) {
		return
	}
	writeText(w, http.StatusOK, th.ID+"\n")
}

func (s *Server) getThread(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	th, err := s.store.GetThread(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	writeText(w, http.StatusOK, th.ID+"\t"+th.Title+"\n")
}

func (s *Server) updateThread(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.UpdateThread(r.Context(), user.ID, r.PathValue("id"), r.FormValue("title"))
	if mapStoreErr(w, r, err) {
		return
	}
	s.done(w, r)
}

func (s *Server) deleteThread(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteThread(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	s.done(w, r)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	list, err := s.store.ListMessages(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	var b strings.Builder
	for _, m := range list {
		b.WriteString(m.ID + "\t" + m.AuthorKind + "\n")
	}
	writeText(w, http.StatusOK, b.String())
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	m, err := s.store.GetMessage(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	writeText(w, http.StatusOK, m.ID+"\t"+m.AuthorKind+"\n")
}

func (s *Server) updateMessage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.UpdateMessage(r.Context(), user.ID, r.PathValue("id"), r.FormValue("body"))
	if mapStoreErr(w, r, err) {
		return
	}
	s.done(w, r)
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteMessage(r.Context(), user.ID, r.PathValue("id"))
	if mapStoreErr(w, r, err) {
		return
	}
	s.done(w, r)
}

func (s *Server) done(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("ui") == "1" {
		http.Redirect(w, r, back(r), http.StatusSeeOther)
		return
	}
	writeText(w, http.StatusOK, "ok\n")
}
