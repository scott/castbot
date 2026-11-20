package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/scott/castbot/internal/agentfile"
	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/importtool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const importRejected = "That request was rejected."

// machineImport runs the import tool for the agent in the path.
// A user_id, agent_id, token, or value argument is ignored.
// The folder is read the same way as the import form, then saved the same way.
func (s *Server) machineImport(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, importRejected, true)
		return
	}
	agent, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, importRejected, true)
		return
	}
	if name != importtool.ToolImport {
		writeAgentResult(w, importRejected, true)
		return
	}
	var args importArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, importRejected, true)
			return
		}
	}
	text, failed := s.decideImport(r.Context(), user, agent, args.URL, args.Archive)
	writeAgentResult(w, text, failed)
}

type importArgs struct {
	URL     string `json:"url"`
	Archive string `json:"archive_b64"`
}

// maybeImport reads one castbot-import fence from the latest reply.
func (s *Server) maybeImport(job run.Job) {
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	ctx := context.Background()
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
		return
	}
	visible, fields, ok := importtool.Parse(reply.Body)
	if !ok {
		return
	}
	user, err := s.store.GetUser(ctx, job.UserID)
	if err != nil {
		return
	}
	agent, err := s.store.GetAgent(ctx, job.UserID, job.AgentID)
	if err != nil {
		return
	}
	text, _ := s.decideImport(ctx, user, agent, fields.URL, fields.ArchiveB64)
	s.writeImportReply(ctx, job.UserID, reply.ID, visible, text)
}

func (s *Server) writeImportReply(ctx context.Context, userID, replyID, visible, text string) {
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if !strings.Contains(body, text) {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, userID, replyID, body)
}

func (s *Server) decideImport(ctx context.Context, user store.User, agent store.Agent, url, archive string) (string, bool) {
	if s.replacing(ctx, user.ID, agent.ID) {
		return replaceSentence, true
	}
	url = strings.TrimSpace(url)
	archive = strings.TrimSpace(archive)
	if url == "" && archive == "" {
		return "Choose an archive or a public github.com URL.", true
	}
	var raw []byte
	kind := "github"
	if archive != "" {
		kind = "file"
		decoded, err := base64.StdEncoding.DecodeString(archive)
		if err != nil {
			return "That archive could not be read.", true
		}
		raw = decoded
	}
	folder, spec, msg := s.openImport(ctx, raw, url, kind)
	if msg != "" {
		return msg, true
	}
	return s.saveImport(ctx, user, agent, folder, spec)
}

func (s *Server) saveImport(ctx context.Context, user store.User, agent store.Agent, folder agentfile.Folder, spec castcfg.Spec) (string, bool) {
	report := importLists(folder, spec)
	if problem := instructionBlock(folder); problem != "" {
		return joinLimit(problem, report, 8000), true
	}
	name := agent.Name
	if strings.TrimSpace(spec.Name) != "" {
		name = strings.TrimSpace(spec.Name)
	}
	if name == "" {
		return joinLimit("A name is required.", report, 8000), true
	}
	if len(name) > 40 {
		return joinLimit("Use a name up to 40 characters.", report, 8000), true
	}
	title := strings.TrimSpace(spec.Title)
	if len(title) > 80 {
		return joinLimit("Use a title up to 80 characters.", report, 8000), true
	}
	model := strings.TrimSpace(spec.Model)
	if model == "" {
		model = agent.Model
	}
	if model == "" {
		return joinLimit("Choose a model from the list.", report, 8000), true
	}
	if !s.AcceptModel(ctx, user.ID, model, agent.Model) {
		return joinLimit("That model is not in the provider manifest.", report, 8000), true
	}
	instructions := folder.Instructions
	if strings.TrimSpace(instructions) == "" {
		return joinLimit("Write the instructions.", report, 8000), true
	}
	if len(instructions) > 200000 {
		return joinLimit("Instructions are too long.", report, 8000), true
	}
	base, err := s.formFromAgent(ctx, user, agent)
	if err != nil {
		return importRejected, true
	}
	spec.Name = name
	spec.Title = title
	spec.Model = model
	spec.Instructions = instructions
	spec.Version = safeVersion(spec.Version)
	spec.Tools = knownImportTools(spec.Tools)
	spec.Permissions = knownImportLevels(spec.Permissions)
	draftID, err := s.store.PutImportDraft(ctx, user.ID, agent.ID, string(spec.Kept))
	if err != nil {
		return joinLimit("That agent file is too large.", report, 8000), true
	}
	form := agentForm{DraftID: draftID, Removed: spec.Removed}
	if err := s.bindKept(ctx, user.ID, agent.ID, &form, &spec); err != nil {
		return joinLimit(keptErr(err), report, 8000), true
	}
	before, err := s.store.EnabledConnections(ctx, user.ID, agent.ID)
	if err != nil {
		return importRejected, true
	}
	if err := s.store.SaveAgentFields(ctx, user.ID, agent.ID, store.AgentInput{
		SectionID: base.SectionID, Name: name, Title: title, Description: base.Job,
		CanDelegate: base.CanDelegate, Model: model, Color: base.Color,
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return joinLimit("Choose a name that is not already used.", report, 8000), true
		}
		if errors.Is(err, store.ErrInvalid) {
			return joinLimit(colorRejectedCopy, report, 8000), true
		}
		return importRejected, true
	}
	mergeScopes(&spec, scopesJSON(spec.Permissions), true, nil)
	version, err := s.storeBundle(ctx, user.ID, agent.ID, spec, spec.Model == agent.Model && agent.Model != "")
	if err != nil {
		if errors.Is(err, errFormMismatch) {
			return joinLimit("The agent file does not match this form.", report, 8000), true
		}
		return importRejected, true
	}
	_ = s.store.SetKeptYAML(ctx, user.ID, agent.ID, string(spec.Kept))
	_ = s.store.DeleteImportDraft(ctx, user.ID, draftID)
	fresh, err := s.store.GetAgent(ctx, user.ID, agent.ID)
	if err != nil {
		return importRejected, true
	}
	after, err := s.store.EnabledConnections(ctx, user.ID, agent.ID)
	if err != nil {
		return importRejected, true
	}
	var reloadErr error
	if !sameNames(before, after) {
		reloadErr = s.scheduleBundleReload(ctx, user.ID, agent.ID, version)
	} else {
		_, reloadErr = s.applyBundle(ctx, user.ID, fresh, version)
	}
	saved := "Saved. Name is " + name + "."
	if reloadErr != nil {
		return joinLimit("The agent could not be reloaded. The saved copy is updated. No new machine was created.", joinLimit(saved, report, 8000), 8000), true
	}
	return joinLimit(saved, report, 8000), false
}

func knownImportTools(names []string) []string {
	var out []string
	for _, name := range names {
		if castcfg.KnownTool(name) {
			out = append(out, name)
		}
	}
	return out
}

func knownImportLevels(in map[string]string) map[string]string {
	out := map[string]string{}
	for name, level := range in {
		norm, ok := castcfg.NormalizeLevel(level)
		if !ok {
			continue
		}
		out[name] = norm
	}
	return out
}

func instructionBlock(folder agentfile.Folder) string {
	if folder.SecretInstructions {
		return instructionsProblem(folder.InstructionsFile, "was skipped because it can hold secrets")
	}
	if folder.BinaryInstructions {
		return instructionsProblem(folder.InstructionsFile, "is not text")
	}
	if folder.MissingInstructions {
		return instructionsProblem(folder.InstructionsFile, "was not in the folder")
	}
	return ""
}

func importLists(folder agentfile.Folder, spec castcfg.Spec) string {
	var b strings.Builder
	if len(folder.Skipped) > 0 {
		b.WriteString("Skipped files\n")
		for _, name := range folder.Skipped {
			b.WriteString(name)
			b.WriteByte('\n')
		}
		b.WriteString("These files can hold secrets, so they were not added.\n")
	}
	if len(spec.Removed) > 0 {
		b.WriteString("Removed secret values\n")
		for _, name := range spec.Removed {
			b.WriteString(name)
			b.WriteByte('\n')
		}
		b.WriteString("Secret values were removed. A vault reference can stay.\n")
	}
	if len(folder.LeftOut) > 0 {
		b.WriteString("Other files were left out. The saved agent keeps the instructions and the agent file only.\n")
	}
	if note := unknownTools(spec); note != "" {
		b.WriteString(note)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
