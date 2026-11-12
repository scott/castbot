package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/store"
)

func (s *Server) vaultList(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	v, err := s.listView(r, user, vaultForm{}, "")
	if s.missing(w, err) {
		return
	}
	s.renderVault(w, r, user, http.StatusOK, v)
}

func (s *Server) vaultNew(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	form, status, highlight := s.addForm(r, user)
	v, err := s.listView(r, user, form, highlight)
	if s.missing(w, err) {
		return
	}
	s.renderVault(w, r, user, status, v)
}

func (s *Server) addForm(r *http.Request, user store.User) (vaultForm, int, string) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if r.Method == http.MethodPost {
		name = strings.TrimSpace(r.FormValue("name"))
	}
	picks, err := s.picks(r.Context(), user.ID, checkedSet(r))
	if err != nil {
		return vaultForm{}, http.StatusInternalServerError, ""
	}
	form := vaultForm{
		Title: "Add secret", Action: "/settings/secrets", Name: name,
		Submit: "Save secret", Agents: picks,
		Intro: "Only your agents are listed. You can change this later.",
	}
	status := http.StatusOK
	highlight := ""
	if r.URL.Query().Get("bad") == "1" || r.FormValue("bad") == "1" {
		form.NameErr = "Use capital letters, digits and underscores, starting with a letter. For example SEARCH_API_KEY."
		status = http.StatusUnprocessableEntity
	}
	if r.URL.Query().Get("taken") == "1" && name != "" {
		sec, err := s.store.SecretByName(r.Context(), user.ID, name)
		if err == nil {
			form.NameErr = "You already have a secret named " + name + ". Pick another name, or"
			form.ConflictHref = "/settings/secrets/item/" + sec.ID + "/replace"
			highlight = sec.ID
			status = http.StatusConflict
		}
	}
	return form, status, highlight
}

func (s *Server) vaultCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	value := r.FormValue("value")
	ids := r.Form["agent"]
	if s.reloadBlocked(r.Context(), user.ID, ids) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	if !store.SecretNameOK(name) {
		s.renderAddError(w, r, user, name, "Use capital letters, digits and underscores, starting with a letter. For example SEARCH_API_KEY.", "", "")
		return
	}
	sec, err := s.store.CreateSecret(r.Context(), user.ID, store.SecretInput{Name: name, Value: value})
	if errors.Is(err, store.ErrConflict) {
		existing, lookErr := s.store.SecretByName(r.Context(), user.ID, name)
		href := ""
		if lookErr == nil {
			href = "/settings/secrets/item/" + existing.ID + "/replace"
		}
		s.renderAddError(w, r, user, name, "You already have a secret named "+name+". Pick another name, or", href, existing.ID)
		return
	}
	if errors.Is(err, store.ErrInvalid) {
		s.renderAddError(w, r, user, name, "", "", "")
		return
	}
	if s.missing(w, err) {
		return
	}
	if len(ids) == 0 {
		s.audit(r.Context(), user, "secret.added", sec.ID, sec.Name, "", "")
	}
	for i, id := range ids {
		agent, err := s.store.GetAgent(r.Context(), user.ID, id)
		if errors.Is(err, store.ErrNotFound) {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		if err != nil {
			writeText(w, http.StatusInternalServerError, "Error\n")
			return
		}
		if err := s.store.GrantSecret(r.Context(), user.ID, id, sec.ID); err != nil {
			if s.missing(w, err) {
				return
			}
		}
		action := "secret.granted"
		if i == 0 {
			action = "secret.added"
		}
		s.audit(r.Context(), user, action, sec.ID, sec.Name, agent.ID, agent.Name)
	}
	s.reloadAgents(r.Context(), user.ID, sec.ID, ids)
	http.Redirect(w, r, "/settings/secrets", http.StatusSeeOther)
}

func (s *Server) renderAddError(w http.ResponseWriter, r *http.Request, user store.User, name, nameErr, href, highlight string) {
	picks, err := s.picks(r.Context(), user.ID, checkedSet(r))
	if s.missing(w, err) {
		return
	}
	form := vaultForm{
		Title: "Add secret", Action: "/settings/secrets", Name: name,
		Submit: "Save secret", Agents: picks, NameErr: nameErr, ConflictHref: href,
		Intro: "Only your agents are listed. You can change this later.",
	}
	if nameErr == "" {
		form.ValueErr = "Paste a value with no line breaks. 8192 bytes at most."
	}
	v, err := s.listView(r, user, form, highlight)
	if s.missing(w, err) {
		return
	}
	status := http.StatusUnprocessableEntity
	if href != "" {
		status = http.StatusConflict
	}
	s.renderVault(w, r, user, status, v)
}

func (s *Server) vaultKeyNew(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	slot, found := slotByID(r.PathValue("slot"))
	if !found {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	form := vaultForm{
		Title: "Add " + slot.Title + " key", Action: "/settings/secrets/keys/" + slot.ID,
		Submit: "Save key", Key: true, Slot: slot.ID,
		Intro: "castbot uses this to call the model for agents you own. Other people on this castbot never see it.",
	}
	v, err := s.listView(r, user, form, "")
	if s.missing(w, err) {
		return
	}
	s.renderVault(w, r, user, http.StatusOK, v)
}

func (s *Server) vaultKeyCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	slot, found := slotByID(r.PathValue("slot"))
	if !found {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	value := r.FormValue("value")
	users, err := s.agentsUsingSlot(r.Context(), user.ID, slot)
	if err != nil {
		writeText(w, http.StatusInternalServerError, "Error\n")
		return
	}
	var ids []string
	for _, agent := range users {
		ids = append(ids, agent.ID)
	}
	if s.reloadBlocked(r.Context(), user.ID, ids) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	name := labelToName(label)
	if label == "" || name == "" || !store.SecretNameOK(name) {
		s.renderKeyError(w, r, user, slot, label, "Use a short label, like provider-a-main.")
		return
	}
	name, err = s.uniqueSecretName(r, user.ID, name)
	if s.missing(w, err) {
		return
	}
	sec, err := s.store.CreateSecret(r.Context(), user.ID, store.SecretInput{
		Name: name, Label: label, Provider: slot.ID, Value: value,
	})
	if errors.Is(err, store.ErrInvalid) {
		s.renderKeyError(w, r, user, slot, label, "Paste a value with no line breaks. 8192 bytes at most.")
		return
	}
	if s.missing(w, err) {
		return
	}
	s.audit(r.Context(), user, "secret.added", sec.ID, sec.Name, "", "")
	s.reloadAgents(r.Context(), user.ID, sec.ID, ids)
	http.Redirect(w, r, "/settings/secrets/item/"+sec.ID+"?watch=1", http.StatusSeeOther)
}

func (s *Server) uniqueSecretName(r *http.Request, userID, base string) (string, error) {
	name := base
	for i := 2; i < 20; i++ {
		_, err := s.store.SecretByName(r.Context(), userID, name)
		if errors.Is(err, store.ErrNotFound) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
		suffix := "_" + itoa(i)
		name = base
		if len(name)+len(suffix) > 64 {
			name = strings.Trim(name[:64-len(suffix)], "_")
		}
		name += suffix
	}
	return "", store.ErrConflict
}

func (s *Server) renderKeyError(w http.ResponseWriter, r *http.Request, user store.User, slot keySlot, label, msg string) {
	form := vaultForm{
		Title: "Add " + slot.Title + " key", Action: "/settings/secrets/keys/" + slot.ID,
		Submit: "Save key", Key: true, Slot: slot.ID, Label: label, NameErr: msg,
		Intro: "castbot uses this to call the model for agents you own. Other people on this castbot never see it.",
	}
	v, err := s.listView(r, user, form, "")
	if s.missing(w, err) {
		return
	}
	s.renderVault(w, r, user, http.StatusUnprocessableEntity, v)
}

func (s *Server) vaultDetail(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	v, err := s.vaultItem(r, user, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	s.renderVault(w, r, user, http.StatusOK, v)
}

func (s *Server) vaultLive(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	v, err := s.vaultItem(r, user, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p.Vault = v
	body, err := execTemplate("vaultLive", finish(p))
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func (s *Server) vaultReplaceForm(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	q.Set("modal", "replace")
	r.URL.RawQuery = q.Encode()
	v, err := s.vaultItem(r, user, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	s.renderVault(w, r, user, http.StatusOK, v)
}

func (s *Server) vaultReplace(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	sec, err := s.store.SecretMeta(r.Context(), user.ID, id)
	if s.missing(w, err) {
		return
	}
	ids, err := s.reloadTargets(r, user, sec)
	if s.missing(w, err) {
		return
	}
	if s.reloadBlocked(r.Context(), user.ID, ids) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	value := r.FormValue("value")
	if _, err := s.store.ReplaceSecret(r.Context(), user.ID, id, value); errors.Is(err, store.ErrInvalid) {
		q := r.URL.Query()
		q.Set("modal", "replace")
		r.URL.RawQuery = q.Encode()
		v, viewErr := s.vaultItem(r, user, id)
		if s.missing(w, viewErr) {
			return
		}
		v.Form.ValueErr = "Paste a value with no line breaks. 8192 bytes at most."
		s.renderVault(w, r, user, http.StatusUnprocessableEntity, v)
		return
	} else if s.missing(w, err) {
		return
	}
	s.audit(r.Context(), user, "secret.replaced", sec.ID, sec.Name, "", "")
	s.reloadAgents(r.Context(), user.ID, sec.ID, ids)
	http.Redirect(w, r, "/settings/secrets/item/"+id+"?watch=1", http.StatusSeeOther)
}

func (s *Server) reloadTargets(r *http.Request, user store.User, sec store.Secret) ([]string, error) {
	if sec.Provider == "" {
		return s.store.GrantAgentIDs(r.Context(), user.ID, sec.ID)
	}
	slot, ok := slotByID(sec.Provider)
	if !ok {
		return nil, nil
	}
	agents, err := s.agentsUsingSlot(r.Context(), user.ID, slot)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, agent := range agents {
		ids = append(ids, agent.ID)
	}
	return ids, nil
}

func (s *Server) vaultDeleteForm(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	q.Set("modal", "delete")
	r.URL.RawQuery = q.Encode()
	v, err := s.vaultItem(r, user, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	status := http.StatusOK
	if v.Modal.Class == "warnm" {
		status = http.StatusConflict
	}
	s.renderVault(w, r, user, status, v)
}

func (s *Server) vaultDelete(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	sec, err := s.store.SecretMeta(r.Context(), user.ID, id)
	if s.missing(w, err) {
		return
	}
	if sec.Provider != "" {
		if slot, ok := slotByID(sec.Provider); ok {
			blocked, err := s.keyInUse(r.Context(), user.ID, sec, slot)
			if s.missing(w, err) {
				return
			}
			if blocked != "" {
				q := r.URL.Query()
				q.Set("modal", "delete")
				r.URL.RawQuery = q.Encode()
				v, viewErr := s.vaultItem(r, user, id)
				if s.missing(w, viewErr) {
					return
				}
				s.renderVault(w, r, user, http.StatusConflict, v)
				return
			}
		}
	}
	ids, err := s.reloadTargets(r, user, sec)
	if s.missing(w, err) {
		return
	}
	if s.reloadBlocked(r.Context(), user.ID, ids) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	if err := s.store.DeleteSecret(r.Context(), user.ID, id); s.missing(w, err) {
		return
	}
	s.audit(r.Context(), user, "secret.deleted", sec.ID, sec.Name, "", "")
	s.reloadAgents(r.Context(), user.ID, sec.ID, ids)
	http.Redirect(w, r, "/settings/secrets", http.StatusSeeOther)
}

func (s *Server) vaultReloadAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	agentID := r.PathValue("agent")
	sec, err := s.store.SecretMeta(r.Context(), user.ID, id)
	if s.missing(w, err) {
		return
	}
	if _, err := s.store.GetAgent(r.Context(), user.ID, agentID); s.missing(w, err) {
		return
	}
	if s.reloadBlocked(r.Context(), user.ID, []string{agentID}) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	s.reloadOne(r.Context(), user.ID, sec.ID, agentID)
	http.Redirect(w, r, "/settings/secrets/item/"+id+"?watch=1", http.StatusSeeOther)
}

func (s *Server) vaultGrantAgents(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	sec, err := s.store.SecretMeta(r.Context(), user.ID, id)
	if s.missing(w, err) {
		return
	}
	if sec.Provider != "" {
		writeText(w, http.StatusNotFound, "Not found\n")
		return
	}
	ids := r.Form["agent"]
	if s.reloadBlocked(r.Context(), user.ID, ids) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	for _, agentID := range ids {
		agent, err := s.store.GetAgent(r.Context(), user.ID, agentID)
		if errors.Is(err, store.ErrNotFound) {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		if err != nil {
			writeText(w, http.StatusInternalServerError, "Error\n")
			return
		}
		if err := s.store.GrantSecret(r.Context(), user.ID, agentID, sec.ID); s.missing(w, err) {
			return
		}
		s.audit(r.Context(), user, "secret.granted", sec.ID, sec.Name, agent.ID, agent.Name)
	}
	s.reloadAgents(r.Context(), user.ID, sec.ID, ids)
	http.Redirect(w, r, "/settings/secrets/item/"+id+"?watch=1", http.StatusSeeOther)
}

func (s *Server) vaultGrant(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	agent, err := s.store.GetAgent(r.Context(), user.ID, agentID)
	if s.missing(w, err) {
		return
	}
	if s.reloadBlocked(r.Context(), user.ID, []string{agentID}) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	var granted []string
	for _, secretID := range r.Form["secret"] {
		sec, err := s.store.SecretMeta(r.Context(), user.ID, secretID)
		if errors.Is(err, store.ErrNotFound) {
			writeText(w, http.StatusNotFound, "Not found\n")
			return
		}
		if err != nil {
			writeText(w, http.StatusInternalServerError, "Error\n")
			return
		}
		if err := s.store.GrantSecret(r.Context(), user.ID, agentID, secretID); s.missing(w, err) {
			return
		}
		s.audit(r.Context(), user, "secret.granted", sec.ID, sec.Name, agent.ID, agent.Name)
		granted = append(granted, secretID)
	}
	s.reloadOne(r.Context(), user.ID, "", agentID)
	_ = granted
	http.Redirect(w, r, "/agents/"+agentID+"?panel=secrets", http.StatusSeeOther)
}

func (s *Server) vaultRevoke(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	secretID := r.FormValue("secret")
	agent, err := s.store.GetAgent(r.Context(), user.ID, agentID)
	if s.missing(w, err) {
		return
	}
	sec, err := s.store.SecretMeta(r.Context(), user.ID, secretID)
	if s.missing(w, err) {
		return
	}
	if s.reloadBlocked(r.Context(), user.ID, []string{agentID}) {
		writeText(w, http.StatusConflict, replaceSentence+"\n")
		return
	}
	if err := s.store.RevokeSecret(r.Context(), user.ID, agentID, secretID); s.missing(w, err) {
		return
	}
	s.audit(r.Context(), user, "secret.revoked", sec.ID, sec.Name, agent.ID, agent.Name)
	s.reloadOne(r.Context(), user.ID, sec.ID, agentID)
	back := r.FormValue("back")
	if back == "" {
		back = "/agents/" + agentID + "?panel=secrets"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
