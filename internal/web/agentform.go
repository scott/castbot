package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott/castbot/internal/agentfile"
	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

const maxAvatar = 512 << 10

// agentForm is the create and edit form. Field errors are shown inline
// and the values are what the user typed.
type agentForm struct {
	Action, ImportAction          string
	Heading, Sub, Submit, Version string
	Name, Title, SectionID        string
	Model, Instructions, GitHub   string
	NameErr, TitleErr             string
	SectionErr, ModelErr          string
	InstructionsErr, ToolErr      string
	AvatarErr, ImportErr, SaveErr string
	Notice, ToolNote, Kept        string
	Sections                      []sectionView
	Models                        []string
	Tools                         []toolField
	Skipped, LeftOut              []string
	HasAvatar, Editing            bool
	AgentID                       string
}

type toolField struct {
	Name, Level string
	On          bool
}

// SetGitHub points import at a public archive host. Tests pass a local server.
// An empty base uses the public archive host. A nil client uses the default,
// which refuses redirects away from that host.
func (s *Server) SetGitHub(base string, client *http.Client) {
	s.mu.Lock()
	s.gitBase = strings.TrimRight(strings.TrimSpace(base), "/")
	s.gitHTTP = client
	s.mu.Unlock()
}

func (s *Server) blankForm(ctx context.Context, user store.User) (agentForm, error) {
	f, err := s.prepareForm(ctx, user)
	if err != nil {
		return agentForm{}, err
	}
	f.Action = "/new-agent"
	f.ImportAction = "/new-agent/import"
	f.Heading = "New agent"
	f.Sub = "A draft on this account. The machine tab starts it."
	f.Submit = "Create agent"
	if f.SectionID == "" {
		for _, sec := range f.Sections {
			if sec.Kind == "agents" {
				f.SectionID = sec.ID
				break
			}
		}
	}
	return f, nil
}

func (s *Server) prepareForm(ctx context.Context, user store.User) (agentForm, error) {
	sections, err := s.store.ListSections(ctx, user.ID)
	if err != nil {
		return agentForm{}, err
	}
	models, err := castcfg.Models()
	if err != nil {
		return agentForm{}, err
	}
	f := agentForm{Tools: toolFields(nil)}
	for _, sec := range sections {
		f.Sections = append(f.Sections, sectionView{ID: sec.ID, Name: sec.Name, Kind: sec.Kind})
	}
	for _, model := range models {
		f.Models = append(f.Models, model.ID)
	}
	return f, nil
}

func (s *Server) formFromAgent(ctx context.Context, user store.User, agent store.Agent) (agentForm, error) {
	f, err := s.prepareForm(ctx, user)
	if err != nil {
		return agentForm{}, err
	}
	f.Editing = true
	f.AgentID = agent.ID
	f.Action = "/agents/" + url.PathEscape(agent.ID) + "/edit"
	f.ImportAction = "/agents/" + url.PathEscape(agent.ID) + "/import"
	f.Heading = "Edit agent"
	f.Sub = "Saving updates the bundle. A running machine reloads. A paused machine stays paused."
	f.Submit = "Save"
	f.Name = agent.Name
	f.Title = agent.Title
	f.SectionID = agent.SectionID
	f.Model = agent.Model
	f.HasAvatar = agent.AvatarPath != ""
	bundle, err := s.store.LatestBundle(ctx, user.ID, agent.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return f, nil
		}
		return agentForm{}, err
	}
	spec, err := castcfg.ParseYAML(bundle.YAML)
	if err != nil {
		f.Instructions = bundle.Instructions
		return f, nil
	}
	f.Instructions = bundle.Instructions
	f.Version = spec.Version
	f.Kept = string(spec.Kept)
	if f.Model == "" {
		f.Model = spec.Model
	}
	f.Tools = toolFields(selectedTools(spec))
	f.keepModel()
	return f, nil
}

func toolFields(on map[string]string) []toolField {
	var out []toolField
	for _, tool := range castcfg.Tools() {
		level := castcfg.DefaultLevel(tool.Name)
		checked := false
		if on != nil {
			if lv, ok := on[tool.Name]; ok {
				checked = true
				if norm, good := castcfg.NormalizeLevel(lv); good {
					level = norm
				}
			}
		}
		out = append(out, toolField{Name: tool.Name, Level: level, On: checked})
	}
	return out
}

func selectedTools(spec castcfg.Spec) map[string]string {
	on := map[string]string{}
	for _, name := range spec.Tools {
		if !castcfg.KnownTool(name) {
			continue
		}
		level := spec.Permissions[name]
		if level == "" {
			level = castcfg.DefaultLevel(name)
		}
		on[name] = level
	}
	return on
}

func (f *agentForm) keepModel() {
	if f.Model == "" {
		return
	}
	for _, id := range f.Models {
		if id == f.Model {
			return
		}
	}
	f.Models = append([]string{f.Model}, f.Models...)
}

func (s *Server) editAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	f, err := s.formFromAgent(r.Context(), user, agent)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	switch r.URL.Query().Get("notice") {
	case "reloaded":
		f.Notice = "Saved. The running agent was reloaded. No new machine was created."
	case "paused":
		f.Notice = "Saved. The machine is paused, so this applies the next time it wakes."
	case "starting":
		f.Notice = "Saved. The machine is still starting, so this applies when it is ready."
	case "saved":
		f.Notice = "Saved."
	}
	s.showForm(w, r, user, f, http.StatusOK)
}

func (s *Server) saveNewAgent(w http.ResponseWriter, r *http.Request, user store.User) {
	base, err := s.blankForm(r.Context(), user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	f, spec, avatar, mime, good := s.readForm(r, base)
	if !good {
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetSection(r.Context(), user.ID, f.SectionID); s.missing(w, err) {
		return
	}
	agent, _, err := s.store.CreateAgent(r.Context(), user.ID, store.AgentInput{
		SectionID: f.SectionID, Name: f.Name, Title: f.Title, Model: f.Model, Status: "draft", Color: "c0",
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			f.NameErr = "Choose a name that is not already used."
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
		if s.missing(w, err) {
			return
		}
		return
	}
	if _, err := s.storeBundle(r.Context(), user.ID, agent.ID, spec); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if len(avatar) > 0 {
		if err := s.store.PutAvatar(r.Context(), user.ID, agent.ID, mime, avatar); err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"/edit?notice=saved", http.StatusSeeOther)
}

func (s *Server) saveAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	base, err := s.formFromAgent(r.Context(), user, agent)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	f, spec, avatar, mime, good := s.readForm(r, base)
	if !good {
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetSection(r.Context(), user.ID, f.SectionID); s.missing(w, err) {
		return
	}
	if err := s.store.SaveAgentFields(r.Context(), user.ID, agent.ID, store.AgentInput{
		SectionID: f.SectionID, Name: f.Name, Title: f.Title, Model: f.Model,
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			f.NameErr = "Choose a name that is not already used."
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
		if s.missing(w, err) {
			return
		}
		return
	}
	version, err := s.storeBundle(r.Context(), user.ID, agent.ID, spec)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if len(avatar) > 0 {
		if err := s.store.PutAvatar(r.Context(), user.ID, agent.ID, mime, avatar); err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
	}
	fresh, err := s.store.GetAgent(r.Context(), user.ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	notice, reloadErr := s.applyBundle(r.Context(), user.ID, fresh, version)
	if reloadErr != nil {
		f.SaveErr = "The agent could not be reloaded. The saved copy is updated. No new machine was created."
		f.HasAvatar = fresh.AvatarPath != ""
		s.showForm(w, r, user, f, http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID)+"/edit?notice="+notice, http.StatusSeeOther)
}

func (s *Server) importNew(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	base, err := s.blankForm(r.Context(), user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.reviewImport(w, r, user, base)
}

func (s *Server) importAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	base, err := s.formFromAgent(r.Context(), user, agent)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.reviewImport(w, r, user, base)
}

func (s *Server) reviewImport(w http.ResponseWriter, r *http.Request, user store.User, base agentForm) {
	f, _, _, _, _ := s.readForm(r, base)
	f.ImportErr = ""
	f.NameErr, f.TitleErr, f.SectionErr = "", "", ""
	f.ModelErr, f.InstructionsErr, f.ToolErr, f.AvatarErr = "", "", "", ""
	raw, github, kind, msg := s.importBytes(r)
	f.GitHub = github
	if msg != "" {
		f.ImportErr = msg
		f.keepModel()
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	var folder agentfile.Folder
	var err error
	if kind == "github" {
		repo, perr := agentfile.ParseGitHub(github)
		if perr != nil {
			f.ImportErr = "Use a public https github.com URL. Private repositories are out of scope."
			f.keepModel()
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		baseURL, client := s.gitBase, s.gitHTTP
		s.mu.Unlock()
		raw, err = agentfile.Fetch(r.Context(), client, baseURL, repo)
		if err != nil {
			if agentfile.IsPrivate(err) {
				f.ImportErr = "Private repositories are out of scope. Use a public github.com URL."
			} else if agentfile.IsOversize(err) {
				f.ImportErr = "That archive is too large."
			} else if strings.Contains(err.Error(), "slash") {
				f.ImportErr = "A branch name with a slash needs ?ref= on the URL."
			} else {
				f.ImportErr = "The repository archive could not be downloaded."
			}
			f.keepModel()
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
		folder, err = agentfile.ReadArchive(raw, repo.Subpath)
	} else {
		folder, err = agentfile.ReadArchive(raw, "")
	}
	if err != nil {
		f.ImportErr = importMessage(err)
		f.keepModel()
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	spec, err := castcfg.ParseYAML(folder.YAML)
	if err != nil {
		f.ImportErr = "agent.yaml could not be read."
		f.keepModel()
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(spec.Name) != "" {
		f.Name = spec.Name
	}
	f.Title = spec.Title
	f.Model = spec.Model
	f.Instructions = folder.Instructions
	f.Version = spec.Version
	f.Tools = toolFields(selectedTools(spec))
	f.Skipped = folder.Skipped
	f.LeftOut = folder.LeftOut
	if note := unknownTools(spec); note != "" {
		f.ToolNote = note
	}
	f.Kept = string(spec.Kept)
	if folder.MissingInstructions {
		if folder.InstructionsFile != "" && !strings.Contains(folder.InstructionsFile, "..") && !strings.Contains(folder.InstructionsFile, "\\") {
			f.InstructionsErr = "The instructions file " + folder.InstructionsFile + " was not in the folder."
		} else {
			f.InstructionsErr = "That instructions file is not allowed."
		}
	}
	if f.Model != "" && !castcfg.KnownModel(f.Model) {
		f.ModelErr = "That model is not in the provider manifest."
	}
	f.keepModel()
	s.showForm(w, r, user, f, http.StatusOK)
}

func (s *Server) importBytes(r *http.Request) (raw []byte, github, kind, msg string) {
	github = strings.TrimSpace(r.FormValue("github"))
	file, _, err := r.FormFile("archive")
	if err == nil && file != nil {
		defer file.Close()
		body, rerr := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		if rerr != nil {
			return nil, github, "", "That archive could not be read."
		}
		if len(body) > 0 {
			return body, github, "file", ""
		}
	} else if err != nil && !errors.Is(err, http.ErrMissingFile) && !errors.Is(err, http.ErrNotMultipart) {
		return nil, github, "", "That archive could not be read."
	}
	if github == "" {
		return nil, github, "", "Choose an archive or a public github.com URL."
	}
	return nil, github, "github", ""
}

func importMessage(err error) string {
	switch {
	case agentfile.IsTraversal(err):
		return "That archive has a path that is not allowed."
	case agentfile.IsSymlink(err):
		return "That archive contains a link. Links are not imported."
	case agentfile.IsOversize(err):
		return "That archive is too large."
	case agentfile.IsTooMany(err):
		return "That archive has too many files."
	case agentfile.IsNoAgent(err):
		return "That folder has no agent.yaml."
	default:
		return "That archive could not be read."
	}
}

func unknownTools(spec castcfg.Spec) string {
	var bad []string
	for _, name := range spec.Tools {
		if !castcfg.KnownTool(name) {
			bad = append(bad, name)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return "These tools are not supported and were left out: " + strings.Join(bad, ", ") + "."
}

func (s *Server) readForm(r *http.Request, f agentForm) (agentForm, castcfg.Spec, []byte, string, bool) {
	f.Kept = r.FormValue("kept")
	f.Name = strings.TrimSpace(r.FormValue("name"))
	f.Title = strings.TrimSpace(r.FormValue("title"))
	f.SectionID = strings.TrimSpace(r.FormValue("section"))
	f.Model = strings.TrimSpace(r.FormValue("model"))
	f.Instructions = r.FormValue("instructions")
	f.Version = r.FormValue("version")
	f.GitHub = strings.TrimSpace(r.FormValue("github"))
	f.Notice = ""
	posted := map[string]bool{}
	for _, name := range r.Form["tool"] {
		posted[name] = true
	}
	good := true
	for i := range f.Tools {
		f.Tools[i].On = posted[f.Tools[i].Name]
		delete(posted, f.Tools[i].Name)
		if lv := strings.TrimSpace(r.FormValue("perm_" + f.Tools[i].Name)); lv != "" {
			norm, ok := castcfg.NormalizeLevel(lv)
			if !ok {
				f.ToolErr = "Choose ask, always allow, or deny."
				good = false
				f.Tools[i].Level = lv
				continue
			}
			f.Tools[i].Level = norm
		}
	}
	if len(posted) > 0 {
		f.ToolErr = "Unknown tool."
		good = false
	}
	if f.Name == "" {
		f.NameErr = "A name is required."
		good = false
	} else if len(f.Name) > 40 {
		f.NameErr = "Use a name up to 40 characters."
		good = false
	}
	if len(f.Title) > 80 {
		f.TitleErr = "Use a title up to 80 characters."
		good = false
	}
	if f.SectionID == "" {
		f.SectionErr = "Choose a section."
		good = false
	}
	if f.Model == "" {
		f.ModelErr = "Choose a model from the list."
		good = false
	} else if !castcfg.KnownModel(f.Model) {
		f.ModelErr = "That model is not in the provider manifest."
		good = false
	}
	if strings.TrimSpace(f.Instructions) == "" {
		f.InstructionsErr = "Write the instructions."
		good = false
	} else if len(f.Instructions) > 200000 {
		f.InstructionsErr = "Instructions are too long."
		good = false
	}
	avatar, mime, avatarErr := readAvatar(r)
	if avatarErr != "" {
		f.AvatarErr = avatarErr
		good = false
	}
	f.keepModel()
	spec := castcfg.Spec{
		Name: f.Name, Title: f.Title, Version: safeVersion(f.Version),
		Model: f.Model, Instructions: f.Instructions, Permissions: map[string]string{},
		Kept: []byte(f.Kept),
	}
	for _, tool := range f.Tools {
		if !tool.On {
			continue
		}
		level, ok := castcfg.NormalizeLevel(tool.Level)
		if !ok {
			level = castcfg.DefaultLevel(tool.Name)
		}
		spec.Tools = append(spec.Tools, tool.Name)
		spec.Permissions[tool.Name] = level
	}
	return f, spec, avatar, mime, good
}

func readAvatar(r *http.Request) ([]byte, string, string) {
	file, _, err := r.FormFile("avatar")
	if errors.Is(err, http.ErrMissingFile) || errors.Is(err, http.ErrNotMultipart) {
		return nil, "", ""
	}
	if err != nil || file == nil {
		return nil, "", "The avatar could not be read."
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxAvatar+1))
	if err != nil {
		return nil, "", "The avatar could not be read."
	}
	if len(body) == 0 {
		return nil, "", ""
	}
	if len(body) > maxAvatar {
		return nil, "", "Avatar images are limited to 512 KB."
	}
	mime := http.DetectContentType(body)
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return body, mime, ""
	default:
		return nil, "", "Use a PNG, JPEG, GIF, or WebP image."
	}
}

func safeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 32 {
		return "1.0"
	}
	for _, c := range v {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return "1.0"
		}
	}
	return v
}

func (s *Server) storeBundle(ctx context.Context, userID, agentID string, spec castcfg.Spec) (string, error) {
	raw, err := castcfg.RenderYAML(spec)
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(spec.Version)
	if version == "" {
		version = "1"
	}
	stamp := s.store.Now().UTC().Format("20060102150405.000000000")
	version = stamp + "-" + version
	_, err = s.store.PutBundle(ctx, userID, agentID, version, string(raw), spec.Instructions)
	return version, err
}

func (s *Server) applyBundle(ctx context.Context, userID string, agent store.Agent, version string) (string, error) {
	vm, err := s.store.LiveVM(ctx, userID, agent.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "saved", nil
	}
	if err != nil {
		return "", err
	}
	state, _ := s.observedMachine(ctx, userID, agent.ID, vm)
	if state == "paused" {
		if err := s.store.SetPendingBundle(ctx, userID, agent.ID, version); err != nil {
			return "", err
		}
		return "paused", nil
	}
	if state != "running" {
		if err := s.store.SetPendingBundle(ctx, userID, agent.ID, version); err != nil {
			return "", err
		}
		return "starting", nil
	}
	endpoint, token, err := s.store.VMCredentials(ctx, userID, agent.ID)
	if err != nil || endpoint == "" || token == "" {
		if err := s.store.SetPendingBundle(ctx, userID, agent.ID, version); err != nil {
			return "", err
		}
		return "starting", nil
	}
	h, _, _ := s.hostConfig()
	if h == nil {
		return "saved", nil
	}
	if err := s.store.SetPendingBundle(ctx, userID, agent.ID, version); err != nil {
		return "", err
	}
	if err := s.applyPending(ctx, userID, agent.ID); err != nil {
		return "", err
	}
	left, err := s.store.PendingBundle(ctx, userID, agent.ID)
	if err != nil {
		return "", err
	}
	if left != "" {
		return "starting", nil
	}
	return "reloaded", nil
}

func (s *Server) agentAvatar(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	mime, body, err := s.store.GetAvatar(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) showForm(w http.ResponseWriter, r *http.Request, user store.User, f agentForm, status int) {
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if f.Editing {
		p.Page = "edit"
		p.Title = "Edit agent"
	} else {
		p.Page = "new"
		p.Title = "New agent"
	}
	p.AgentForm = f
	s.render(w, status, "app", finish(p))
}
