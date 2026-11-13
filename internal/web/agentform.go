package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott/castbot/internal/agentfile"
	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/connect"
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
	Notice, ToolNote, DraftID     string
	Sections                      []sectionView
	Models                        []string
	Tools                         []toolField
	Skipped, LeftOut, Removed     []string
	KeptLines                     []castcfg.KeptItem
	HasAvatar, Editing            bool
	Pick                          modelPick
	AgentID                       string
	Back, Job, KeyLine, Size      string
	Color, ColorErr, Letter       string
	Colors                        []colorSwatch
	Idle                          int
	Advanced, Prefill             bool
	Connections                   []connField
	Scopes                        string
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
	color, err := s.store.NextAgentColor(ctx, user.ID)
	if err != nil {
		return agentForm{}, err
	}
	f.Color = color
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
	if err := s.fillConnections(ctx, user.ID, agent.ID, &f); err != nil {
		return agentForm{}, err
	}
	f.Name = agent.Name
	f.Title = agent.Title
	f.SectionID = agent.SectionID
	f.Model = agent.Model
	f.HasAvatar = agent.AvatarPath != ""
	if store.ValidAgentColor(agent.Color) {
		f.Color = agent.Color
	} else {
		color, cerr := s.store.NextAgentColor(ctx, user.ID)
		if cerr != nil {
			return agentForm{}, cerr
		}
		f.Color = color
	}
	bundle, err := s.store.LatestBundle(ctx, user.ID, agent.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			f.keepModel()
			return f, nil
		}
		return agentForm{}, err
	}
	spec, err := castcfg.ParseYAML(bundle.YAML)
	if err != nil {
		f.Instructions = bundle.Instructions
		if msg := yamlErr(err); msg != "" {
			f.SaveErr = msg
		}
		f.keepModel()
		return f, nil
	}
	f.Instructions = bundle.Instructions
	f.Version = spec.Version
	f.KeptLines = castcfg.SummarizeKept(spec.Kept)
	if f.Model == "" {
		f.Model = spec.Model
	}
	f.Tools = toolFields(selectedTools(spec))
	f.Scopes = scopesJSON(spec.Permissions)
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
	base.Heading = "New agent, all fields"
	base.Sub = "Change anything, then create."
	base.Submit = "Create and start"
	base.Back = "/new-agent"
	base.Advanced = true
	if doc, _, derr := s.loadDraft(r.Context(), user.ID); derr == nil && doc.Card != nil {
		card := *doc.Card
		s.refreshCard(r.Context(), user.ID, &card)
		if store.ValidAgentColor(card.Color) {
			base.Color = card.Color
		}
	}
	f, spec, avatar, mime, good := s.readForm(r, base)
	f.Job, _ = redactSecrets(strings.TrimSpace(r.FormValue("job")))
	switch strings.TrimSpace(r.FormValue("size")) {
	case "2:4096", "8:16384":
		f.Size = strings.TrimSpace(r.FormValue("size"))
	default:
		f.Size = createSize
	}
	f.Idle = createIdle
	switch strings.TrimSpace(r.FormValue("idle")) {
	case "300":
		f.Idle = 300
	case "3600":
		f.Idle = 3600
	}
	if err := s.bindKept(r.Context(), user.ID, "", &f, &spec); err != nil {
		f.SaveErr = keptErr(err)
		good = false
	}
	if !good {
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetSection(r.Context(), user.ID, f.SectionID); s.missing(w, err) {
		return
	}
	h, image, publicURL := s.hostConfig()
	hosted := h != nil && image != "" && publicURL != ""
	if hosted {
		if _, ok := s.userModelKey(r.Context(), user.ID, f.Model); !ok {
			f.SaveErr = missingModelKeyCopy(f.Model)
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
	}
	agent, _, err := s.store.CreateAgent(r.Context(), user.ID, store.AgentInput{
		SectionID: f.SectionID, Name: f.Name, Title: f.Title, Description: f.Job, Model: f.Model, Status: "draft", Color: f.Color,
		IdleTimeout: f.Idle, Size: f.Size, PendingGreet: hosted,
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
	_, scopesPosted := r.Form["scopes"]
	mergeScopes(&spec, f.Scopes, scopesPosted, nil)
	if _, err := s.storeBundle(r.Context(), user.ID, agent.ID, spec, false); err != nil {
		if errors.Is(err, errFormMismatch) {
			f.SaveErr = "The agent file does not match this form."
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	_ = s.store.SetKeptYAML(r.Context(), user.ID, agent.ID, string(spec.Kept))
	if f.DraftID != "" {
		_ = s.store.DeleteImportDraft(r.Context(), user.ID, f.DraftID)
	}
	if len(avatar) > 0 {
		if err := s.store.PutAvatar(r.Context(), user.ID, agent.ID, mime, avatar); err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
	}
	if hosted {
		if err := s.noteCreated(r.Context(), user, agent); err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		if err := s.launchNewAgent(r.Context(), user.ID, agent); err != nil {
			if errors.Is(err, store.ErrQuota) {
				_ = s.store.DeleteAgent(r.Context(), user.ID, agent.ID)
				f.SaveErr = "This account has reached its machine limit."
				s.showForm(w, r, user, f, http.StatusConflict)
				return
			}
			if !errors.Is(err, errNoHost) {
				if s.missing(w, err) {
					return
				}
				return
			}
		}
	}
	_ = s.store.DeleteCreateDraft(r.Context(), user.ID)
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID), http.StatusSeeOther)
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
	if err := s.bindKept(r.Context(), user.ID, agent.ID, &f, &spec); err != nil {
		f.SaveErr = keptErr(err)
		good = false
	}
	if msg := s.connectionProblem(r, f); msg != "" {
		f.SaveErr = msg
		good = false
	}
	if !good {
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	if _, err := s.store.GetSection(r.Context(), user.ID, f.SectionID); s.missing(w, err) {
		return
	}
	if err := s.store.SaveAgentFields(r.Context(), user.ID, agent.ID, store.AgentInput{
		SectionID: f.SectionID, Name: f.Name, Title: f.Title, Model: f.Model, Color: f.Color,
	}); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			f.ColorErr = colorRejectedCopy
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
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
	before, err := s.store.EnabledConnections(r.Context(), user.ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if err := s.applyConnectionPosts(r.Context(), user.ID, agent.ID, r, f); err != nil {
		f.SaveErr = "That connection could not be saved."
		f.HasAvatar = agent.AvatarPath != ""
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	var previous map[string]string
	if current, err := s.store.LatestBundle(r.Context(), user.ID, agent.ID); err == nil {
		if parsed, perr := castcfg.ParseYAML(current.YAML); perr == nil {
			previous = parsed.Permissions
		}
	}
	_, scopesPosted := r.Form["scopes"]
	mergeScopes(&spec, f.Scopes, scopesPosted, previous)
	version, err := s.storeBundle(r.Context(), user.ID, agent.ID, spec, spec.Model == agent.Model && agent.Model != "")
	if err != nil {
		if errors.Is(err, errFormMismatch) {
			f.SaveErr = "The agent file does not match this form."
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	_ = s.store.SetKeptYAML(r.Context(), user.ID, agent.ID, string(spec.Kept))
	if f.DraftID != "" {
		_ = s.store.DeleteImportDraft(r.Context(), user.ID, f.DraftID)
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
	after, err := s.store.EnabledConnections(r.Context(), user.ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	var reloadErr error
	if !sameNames(before, after) {
		reloadErr = s.scheduleBundleReload(r.Context(), user.ID, agent.ID, version)
	} else {
		_, reloadErr = s.applyBundle(r.Context(), user.ID, fresh, version)
	}
	if reloadErr != nil {
		f.SaveErr = "The agent could not be reloaded. The saved copy is updated. No new machine was created."
		f.HasAvatar = fresh.AvatarPath != ""
		s.showForm(w, r, user, f, http.StatusBadGateway)
		return
	}
	target := "/agents/" + url.PathEscape(agent.ID)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
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
		var resolved agentfile.Repo
		raw, resolved, err = agentfile.Fetch(r.Context(), client, baseURL, repo)
		if err != nil {
			if agentfile.IsPrivate(err) {
				f.ImportErr = "Private repositories are out of scope. Use a public github.com URL."
			} else if agentfile.IsOversize(err) {
				f.ImportErr = "That archive is too large."
			} else if agentfile.IsRateLimit(err) {
				f.ImportErr = "GitHub is rate limiting imports, try again later or add ?ref="
			} else if strings.Contains(err.Error(), "slash") {
				f.ImportErr = "A branch name with a slash needs ?ref= on the URL."
			} else {
				f.ImportErr = "The repository archive could not be downloaded."
			}
			f.keepModel()
			s.showForm(w, r, user, f, http.StatusBadRequest)
			return
		}
		repo = resolved
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
		if msg := yamlErr(err); msg != "" {
			f.ImportErr = msg
		}
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
	f.Scopes = scopesJSON(spec.Permissions)
	f.Skipped = folder.Skipped
	f.LeftOut = folder.LeftOut
	f.Removed = spec.Removed
	if note := unknownTools(spec); note != "" {
		f.ToolNote = note
	}
	f.KeptLines = castcfg.SummarizeKept(spec.Kept)
	draftAgent := ""
	if f.Editing {
		draftAgent = f.AgentID
	}
	draftID, derr := s.store.PutImportDraft(r.Context(), user.ID, draftAgent, string(spec.Kept))
	if derr != nil {
		f.ImportErr = "That agent file is too large."
		f.keepModel()
		s.showForm(w, r, user, f, http.StatusBadRequest)
		return
	}
	f.DraftID = draftID
	if folder.SecretInstructions {
		f.Instructions = ""
		f.InstructionsErr = instructionsProblem(folder.InstructionsFile, "was skipped because it can hold secrets")
	} else if folder.BinaryInstructions {
		f.Instructions = ""
		f.InstructionsErr = instructionsProblem(folder.InstructionsFile, "is not text")
	} else if folder.MissingInstructions {
		f.InstructionsErr = instructionsProblem(folder.InstructionsFile, "was not in the folder")
	}
	if f.Model != "" && !s.AcceptModel(r.Context(), user.ID, f.Model, base.Model) {
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
	previous := f.Model
	if id := strings.TrimSpace(r.FormValue("draft")); draftID(id) {
		f.DraftID = id
	}
	f.Name = strings.TrimSpace(r.FormValue("name"))
	f.Title = strings.TrimSpace(r.FormValue("title"))
	f.SectionID = strings.TrimSpace(r.FormValue("section"))
	f.Model = strings.TrimSpace(r.FormValue("model"))
	if f.Editing && f.Model == "" && previous != "" {
		f.Model = previous
	}
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
	if f.Editing {
		chosen := map[string]bool{}
		for _, name := range r.Form["connection"] {
			chosen[strings.ToLower(strings.TrimSpace(name))] = true
		}
		known := map[string]bool{}
		for i := range f.Connections {
			known[f.Connections[i].ID] = true
			f.Connections[i].On = chosen[f.Connections[i].ID]
			delete(chosen, f.Connections[i].ID)
		}
		if len(chosen) > 0 {
			f.SaveErr = "Unknown connection."
			good = false
		}
		_ = known
	}
	if raw := strings.TrimSpace(r.FormValue("scopes")); raw != "" && len(raw) <= 8000 {
		f.Scopes = scopesJSON(parseScopePost(raw))
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
	userID := ""
	if u := from(r).User; u != nil {
		userID = u.ID
	}
	prev := ""
	if f.Editing {
		prev = previous
	}
	if f.Model == "" {
		f.ModelErr = "Choose a model from the list."
		good = false
	} else if !s.AcceptModel(r.Context(), userID, f.Model, prev) {
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
	if !applyPostedColor(r, &f) {
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

func applyPostedColor(r *http.Request, f *agentForm) bool {
	vals, ok := r.Form["color"]
	if !ok || len(vals) == 0 {
		return true
	}
	posted := strings.TrimSpace(vals[0])
	if !store.ValidAgentColor(posted) {
		f.ColorErr = colorRejectedCopy
		return false
	}
	f.Color = posted
	return true
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

var errFormMismatch = errors.New("rendered agent does not match the form")

func draftID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

func yamlErr(err error) string {
	var dup *castcfg.DuplicateKeyError
	if errors.As(err, &dup) {
		key := dup.Key
		if key == "" || len(key) > 80 || strings.ContainsAny(key, "\r\n<>\"") {
			return "agent.yaml has a duplicate key."
		}
		return "agent.yaml has the key " + key + " more than once."
	}
	if errors.Is(err, castcfg.ErrTooLarge) {
		return "That agent file is too large."
	}
	return ""
}

func keptErr(err error) string {
	if msg := yamlErr(err); msg != "" {
		return msg
	}
	if errors.Is(err, errDraftExpired) {
		return "That import expired. Review the import again."
	}
	return "The agent file could not be read."
}

var errDraftExpired = errors.New("import draft expired")

func instructionsProblem(name, why string) string {
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "\\") || strings.Contains(name, "/") && strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\r\n<>") {
		return "That instructions file is not allowed."
	}
	return "The instructions file " + name + " " + why + "."
}

func (s *Server) bindKept(ctx context.Context, userID, agentID string, f *agentForm, spec *castcfg.Spec) error {
	raw := ""
	if f.DraftID != "" {
		got, err := s.store.ImportDraft(ctx, userID, agentID, f.DraftID)
		if errors.Is(err, store.ErrNotFound) {
			return errDraftExpired
		}
		if err != nil {
			return err
		}
		raw = got
	} else if agentID != "" {
		got, err := s.store.KeptYAML(ctx, userID, agentID)
		if err != nil {
			return err
		}
		if got == "" {
			bundle, berr := s.store.LatestBundle(ctx, userID, agentID)
			if berr == nil {
				parsed, perr := castcfg.ParseYAML(bundle.YAML)
				if perr != nil {
					return perr
				}
				got = string(parsed.Kept)
			} else if !errors.Is(berr, store.ErrNotFound) {
				return berr
			}
		}
		raw = got
	}
	if strings.TrimSpace(raw) != "" {
		parsed, err := castcfg.ParseYAML(raw)
		if err != nil {
			return err
		}
		spec.Kept = parsed.Kept
		if len(f.Removed) == 0 {
			f.Removed = parsed.Removed
		}
	}
	f.KeptLines = castcfg.SummarizeKept(spec.Kept)
	return nil
}

func (s *Server) storeBundle(ctx context.Context, userID, agentID string, spec castcfg.Spec, allowUnknown bool) (string, error) {
	version := strings.TrimSpace(spec.Version)
	if version == "" {
		version = "1"
	}
	enabled, err := s.store.EnabledConnections(ctx, userID, agentID)
	if err != nil {
		return "", err
	}
	applyConnectionPerms(&spec, enabled)
	if err := castcfg.SetMCPServers(&spec, enabled); err != nil {
		return "", err
	}
	raw, err := castcfg.RenderYAML(spec)
	if err != nil {
		return "", err
	}
	parsed, err := castcfg.ParseYAML(string(raw))
	if err != nil {
		return "", err
	}
	if !castcfg.Agree(spec, parsed) || strings.TrimSpace(spec.Model) == "" || (!allowUnknown && !s.AcceptModel(ctx, userID, spec.Model, "")) {
		return "", errFormMismatch
	}
	for _, name := range parsed.Tools {
		if !castcfg.KnownTool(name) {
			return "", errFormMismatch
		}
	}
	for _, level := range parsed.Permissions {
		if _, ok := castcfg.NormalizeLevel(level); !ok {
			return "", errFormMismatch
		}
	}
	stamp := s.store.Now().UTC().Format("20060102150405.000000000")
	version = stamp + "-" + version
	_, err = s.store.PutBundleFiles(ctx, userID, agentID, version, string(raw), spec.Instructions, connect.ShimFiles(enabled))
	return version, err
}

func parseScopePost(raw string) map[string]string {
	var extra map[string]string
	if json.Unmarshal([]byte(raw), &extra) != nil {
		return nil
	}
	return extra
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
	if err := s.runReload(ctx, userID, agent.ID); err != nil {
		return "", err
	}
	left, err := s.store.PendingBundle(ctx, userID, agent.ID)
	if err != nil {
		return "", err
	}
	if left != "" && left != version {
		if err := s.runReload(ctx, userID, agent.ID); err != nil {
			return "", err
		}
		left, err = s.store.PendingBundle(ctx, userID, agent.ID)
		if err != nil {
			return "", err
		}
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
	f.Letter = letter(f.Name)
	f.Colors = colorSwatches(f.Color)
	f.Pick = s.modelPick(r.Context(), user.ID, f.Model, "form")
	f.Models = modelIDs(f.Pick)
	p.AgentForm = f
	s.render(w, status, "app", finish(p))
}
