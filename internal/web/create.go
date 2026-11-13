package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

const (
	createIdle = 900
	createSize = "1:2048"
)

type createView struct {
	Placeholder  string
	ShowAdvanced bool
	Examples     []chipFill
	Turns        []createTurn
	Card         setupView
	HasCard      bool
	Editing      bool
	Error        string
	DraftID      string
}

type chipFill struct {
	Title, Body string
}

type createTurn struct {
	User     bool
	Body     string
	Lead     string
	KeyOut   bool
	Reprompt bool
	Samples  []string
}

type setupView struct {
	Name, Title, Instructions, Job, Schedule, Reason, Letter, Color string
	Model, ModelLabel, KeyLabel, KeyLine, KeyMiss, SizeLabel        string
	Size                                                            string
	Idle                                                            int
	IdleLabel, HourCost, DayCost, Lead                              string
	Auto, Stripped, Conflict, HasKey, Editing, ServerKey            bool
	Alt, Block, Why, ModelErr                                       string
	Models                                                          []modelChoice
	Pick                                                            modelPick
	KeyFine                                                         keyFine
	Sizes                                                           []sizeChoice
	Idles                                                           []idleChoice
}

type modelChoice struct {
	ID, Label string
	On        bool
}

type sizeChoice struct {
	ID, Label string
	On        bool
}

type idleChoice struct {
	Sec   int
	Label string
	On    bool
}

type draftDoc struct {
	Turns   []draftTurn `json:"turns"`
	Card    *setupState `json:"card,omitempty"`
	Editing bool        `json:"editing"`
}

type draftTurn struct {
	Role string `json:"role"`
	Body string `json:"body"`
	Kind string `json:"kind"`
	Lead string `json:"lead,omitempty"`
}

type setupState struct {
	Name, Title, Instructions, Job, Schedule, Reason, Color string
	Model, KeyLabel, ModelErr, ModelHold                    string
	Size                                                    string
	Idle                                                    int
	Auto, Stripped, Conflict, HasKey                        bool
	Alt, Lead                                               string
}

func exampleChips() []chipFill {
	return []chipFill{
		{"Docs helper", "Keep the FAQ in example-org/docs-bot in sync with docs/pricing.md"},
		{"Inbox triage", "Sort my support inbox each morning and draft replies for me to check"},
		{"Release notes", "Write release notes from merged PRs in example-org/app every Friday"},
		{"Just a blank agent", "Call it Juno. I will tell it what to do later"},
	}
}

func (s *Server) createChat(w http.ResponseWriter, r *http.Request, user store.User) {
	p, ok := s.createPage(w, r, user)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "app", finish(p))
}

func (s *Server) createPage(w http.ResponseWriter, r *http.Request, user store.User) (page, bool) {
	_ = s.store.DropStaleCreateDrafts(r.Context())
	p, err := s.shell(r, user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return page{}, false
	}
	p.Page = "create"
	p.Title = "New agent"
	view := createView{
		Placeholder:  "Describe your agent. For example: a helper named Scout that checks our docs for broken links.",
		ShowAdvanced: true,
		Examples:     exampleChips(),
	}
	if doc, id, err := s.loadDraft(r.Context(), user.ID); err == nil {
		view = s.presentDraft(r.Context(), user, doc, id)
	}
	p.Create = view
	return p, true
}

func (s *Server) loadDraft(ctx context.Context, userID string) (draftDoc, string, error) {
	row, err := s.store.LoadCreateDraft(ctx, userID)
	if err != nil {
		return draftDoc{}, "", err
	}
	var doc draftDoc
	if err := json.Unmarshal([]byte(row.Payload), &doc); err != nil {
		return draftDoc{}, "", err
	}
	return doc, row.ID, nil
}

func (s *Server) saveDraft(ctx context.Context, userID string, doc draftDoc) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = s.store.SaveCreateDraft(ctx, userID, string(raw))
	return err
}

func (s *Server) presentDraft(ctx context.Context, user store.User, doc draftDoc, id string) createView {
	view := createView{
		Placeholder: "Reply to change something, or describe a different agent.",
		Examples:    exampleChips(),
		DraftID:     id,
		Editing:     doc.Editing && doc.Card != nil,
	}
	if len(doc.Turns) == 0 {
		view.Placeholder = "Describe your agent. For example: a helper named Scout that checks our docs for broken links."
		view.ShowAdvanced = true
	}
	for _, turn := range doc.Turns {
		ct := createTurn{Body: turn.Body, Lead: turn.Lead}
		switch turn.Kind {
		case "user":
			ct.User = true
		case "reprompt":
			ct.Reprompt = true
			ct.Samples = []string{
				"Check our docs for broken links every morning.",
				"Draft replies to my support inbox.",
			}
		case "note":
			ct.KeyOut = true
		}
		view.Turns = append(view.Turns, ct)
	}
	if doc.Card != nil {
		view.HasCard = true
		view.ShowAdvanced = false
		card := *doc.Card
		s.refreshCard(ctx, user.ID, &card)
		shown := card.Model
		if card.ModelHold != "" {
			shown = card.ModelHold
		}
		view.Card = presentSetup(card, doc.Editing)
		view.Card.ModelErr = card.ModelErr
		view.Card.Pick = s.modelPick(ctx, user.ID, shown, "setup")
	}
	return view
}

func (s *Server) refreshCard(ctx context.Context, userID string, card *setupState) {
	if card.Model == "" {
		card.Model = s.defaultConfiguredModel(ctx, userID)
		if card.Model == "" {
			card.Model = defaultModelID()
		}
	}
	if card.Size == "" {
		card.Size = createSize
	}
	if card.Idle == 0 {
		card.Idle = createIdle
	}
	label, ok := s.userModelKey(ctx, userID, card.Model)
	card.HasKey = ok
	card.KeyLabel = label
	used := s.usedNames(ctx, userID)
	if used[strings.ToLower(card.Name)] {
		card.Conflict = true
		if card.Alt == "" || used[strings.ToLower(card.Alt)] {
			card.Alt = freeAlt(card.Name, used)
		}
	} else {
		card.Conflict = false
	}
	card.Lead = leadFor(*card)
	if !store.ValidAgentColor(card.Color) {
		if next, err := s.store.NextAgentColor(ctx, userID); err == nil {
			card.Color = next
		}
	}
}

// userModelKey is the label for the key this model will run with.
// A vault row wins. A non-empty server provider key still counts as available
// and is labeled serverKeyShort. The server value is not copied onto the agent.
// Launch resolves it the same way an existing agent does.
func (s *Server) userModelKey(ctx context.Context, userID, model string) (string, bool) {
	slot, ok := slotForModel(model)
	if !ok {
		return "", false
	}
	rows, err := s.store.ProviderSecrets(ctx, userID, []string{slot.ID, slot.Manifest})
	if err != nil {
		return "", false
	}
	if len(rows) > 0 && strings.TrimSpace(rows[0].Value) != "" {
		return strings.TrimSpace(rows[0].Label), true
	}
	if strings.TrimSpace(providerEnv(slot.Manifest)) != "" {
		return serverKeyShort, true
	}
	return "", false
}

func (s *Server) usedNames(ctx context.Context, userID string) map[string]bool {
	out := map[string]bool{}
	agents, err := s.store.ListAgents(ctx, userID)
	if err != nil {
		return out
	}
	for _, agent := range agents {
		out[strings.ToLower(agent.Name)] = true
	}
	return out
}

func freeAlt(name string, used map[string]bool) string {
	alt := strings.TrimSpace(name) + " Spanish"
	if !used[strings.ToLower(alt)] {
		return alt
	}
	alt = strings.TrimSpace(name) + " 2"
	if !used[strings.ToLower(alt)] {
		return alt
	}
	return strings.TrimSpace(name) + " new"
}

func leadFor(card setupState) string {
	if card.Conflict {
		return "Here is the setup. You already have an agent with that name, so pick another one."
	}
	if !card.HasKey {
		return "Here is what I understood. One thing is missing before " + card.Name + " can run."
	}
	if strings.TrimSpace(card.Job) == "" {
		return card.Name + " it is. You did not give it a job yet, which is fine. " + card.Name + " will ask you once it is up."
	}
	if card.Auto {
		reason := card.Reason
		if reason == "" {
			reason = "that fits what you described"
		}
		return "I called it " + card.Name + ", since " + reason + ". You can rename it here or later in its settings."
	}
	return "Here is what I understood. Check it, then create " + card.Name + "."
}

func presentSetup(card setupState, editing bool) setupView {
	hour, day := costCopy(card.Size)
	v := setupView{
		Name: card.Name, Title: card.Title, Instructions: card.Instructions, Job: card.Job,
		Schedule: card.Schedule, Reason: card.Reason, Model: card.Model,
		ModelLabel: modelLabel(card.Model), KeyLabel: card.KeyLabel, KeyLine: modelKeyLine(card.KeyLabel), KeyMiss: missingKeyRest(card.Model),
		Size: card.Size, SizeLabel: machineLine(card.Size), Idle: card.Idle,
		IdleLabel: pauseLine(card.Idle), HourCost: hour, DayCost: day, Lead: card.Lead,
		Auto: card.Auto, Stripped: card.Stripped, Conflict: card.Conflict, HasKey: card.HasKey, ServerKey: card.KeyLabel == serverKeyShort,
		Alt: card.Alt, Editing: editing, Letter: letter(card.Name), Color: card.Color,
	}
	if v.Job == "" {
		v.Job = ""
	}
	if card.Conflict {
		v.Why = "Pick a different name to create. Use Edit, or reply with a new name."
		v.Block = "You already have an agent named " + card.Name + ". Try " + card.Alt + " or another name."
	}
	if !card.HasKey {
		v.Why = "Add a model key to create."
		v.Block = "Add a model key first."
	}
	v.KeyFine = keyFine{Line: v.KeyLine, Label: v.KeyLabel, Server: v.ServerKey}
	v.Models = modelChoices(card.Model)
	v.Sizes = []sizeChoice{
		{createSize, "Small · $0.039/h", card.Size == createSize || card.Size == ""},
		{"2:4096", "Medium · $0.078/h", card.Size == "2:4096"},
		{"8:16384", "Large · $0.312/h", card.Size == "8:16384"},
	}
	v.Idles = []idleChoice{
		{900, "15 minutes", card.Idle == 900 || card.Idle == 0},
		{300, "5 minutes", card.Idle == 300},
		{3600, "1 hour", card.Idle == 3600},
	}
	return v
}

func modelLabel(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if inf, ok := castcfg.InferenceFor(id); ok {
		if name := castcfg.ProviderName(inf.Key); name != "" {
			return name + " / " + id
		}
	}
	return id
}

func modelKeyLine(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return ""
	}
	if label == serverKeyShort {
		return serverKeyShort
	}
	return "From your vault · " + label
}

func modelChoices(current string) []modelChoice {
	large := defaultModelID()
	small := smallModelID()
	if current == "" {
		current = large
	}
	out := []modelChoice{{large, modelLabel(large), current == large}}
	if small != "" && small != large {
		out = append(out, modelChoice{small, modelLabel(small), current == small})
	}
	if current != large && current != small {
		out = append(out, modelChoice{current, modelLabel(current), true})
	}
	return out
}

func defaultModelID() string {
	models, err := castcfg.Models()
	if err != nil || len(models) == 0 {
		return ""
	}
	return models[0].ID
}

func smallModelID() string {
	models, err := castcfg.Models()
	if err != nil || len(models) < 2 {
		return ""
	}
	return models[1].ID
}

func machineLine(size string) string {
	switch size {
	case "2:4096":
		return "Medium, 2 vCPU / 4 GiB"
	case "8:16384":
		return "Large, 8 vCPU / 16 GiB"
	default:
		return "Small, 1 vCPU / 2 GiB"
	}
}

func pauseLine(sec int) string {
	switch sec {
	case 300:
		return "Pauses after 5 min idle"
	case 3600:
		return "Pauses after 1 hour idle"
	default:
		return "Pauses after 15 min idle"
	}
}

func costCopy(size string) (string, string) {
	switch size {
	case "2:4096":
		return "$0.078 an hour while running", "About $0.02 a day while paused"
	case "8:16384":
		return "$0.312 an hour while running", "About $0.08 a day while paused"
	default:
		return "$0.039 an hour while running", "About $0.01 a day while paused"
	}
}

// setupProposal is the proposal used to build a setup card.
// Short greetings still ask the opening question. For a real description,
// a down, unclear, empty, or unparseable describe result falls back to a
// local proposal so the page still shows a card with a proposed name.
func setupProposal(text string, prop Proposal, err error) (Proposal, bool) {
	if unclearText(text) {
		return Proposal{Unclear: true}, true
	}
	if err == nil {
		prop = scrubProposal(prop)
		if !prop.Unclear && (prop.Name != "" || prop.Job != "" || prop.Instructions != "" || nameFromText(text) != "") {
			return prop, false
		}
	}
	return localProposal(text), false
}

func (s *Server) describeAgent(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	raw := strings.TrimSpace(r.FormValue("body"))
	if raw == "" {
		// An empty post must not wipe an existing card or append the greeting.
		s.showCreate(w, r, user)
		return
	}
	if unclearText(raw) {
		s.replyDescribe(w, r, user, s.repromptDoc(r.Context(), user.ID, raw))
		return
	}
	redacted, stripped := redactSecrets(raw)
	prop, err := s.describer().Describe(r.Context(), redacted)
	if err != nil {
		s.logger().Info("describe", "user_id", user.ID, "ok", false, "fallback", true)
	} else {
		s.logger().Info("describe", "user_id", user.ID, "ok", true)
	}
	prop, greet := setupProposal(redacted, prop, err)
	if greet {
		s.replyDescribe(w, r, user, s.repromptDoc(r.Context(), user.ID, redacted))
		return
	}
	card := s.cardFrom(r.Context(), user.ID, redacted, prop, stripped)
	doc, _, _ := s.loadDraft(r.Context(), user.ID)
	kind := "user"
	doc.Turns = append(doc.Turns, draftTurn{Role: "user", Kind: kind, Body: redacted})
	if stripped {
		doc.Turns = append(doc.Turns, draftTurn{Role: "castbot", Kind: "note", Body: keyOutCopy(card.Name)})
	}
	doc.Turns = append(doc.Turns, draftTurn{Role: "castbot", Kind: "card", Lead: card.Lead, Body: card.Name})
	doc.Card = &card
	doc.Editing = false
	if err := s.saveDraft(r.Context(), user.ID, doc); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.showCreate(w, r, user)
}

func keyOutCopy(name string) string {
	if name == "" {
		name = "the agent"
	}
	return "I left the token out. It looked like a key, so I did not put it in " + name + "'s instructions or keep it in this chat. Add it in Settings, Secrets after " + name + " is made, then give it to " + name + " there."
}

func (s *Server) repromptDoc(ctx context.Context, userID, raw string) draftDoc {
	doc, _, _ := s.loadDraft(ctx, userID)
	if strings.TrimSpace(raw) != "" {
		shown, _ := redactSecrets(raw)
		doc.Turns = append(doc.Turns, draftTurn{Role: "user", Kind: "user", Body: shown})
	}
	doc.Turns = append(doc.Turns, draftTurn{
		Role: "castbot", Kind: "reprompt",
		Body: "Hi. Happy to set one up. What should your agent do? One sentence is plenty, for example:",
	})
	doc.Card = nil
	doc.Editing = false
	_ = s.saveDraft(ctx, userID, doc)
	return doc
}

func (s *Server) replyDescribe(w http.ResponseWriter, r *http.Request, user store.User, _ draftDoc) {
	s.showCreate(w, r, user)
}

func (s *Server) showCreate(w http.ResponseWriter, r *http.Request, user store.User) {
	if r.Header.Get("HX-Request") == "true" {
		p, ok := s.createPage(w, r, user)
		if !ok {
			return
		}
		s.render(w, http.StatusOK, "createSwap", finish(p))
		return
	}
	http.Redirect(w, r, "/new-agent", http.StatusSeeOther)
}

func (s *Server) cardFrom(ctx context.Context, userID, text string, prop Proposal, stripped bool) setupState {
	name := nameFromText(text)
	auto := false
	reason := prop.Reason
	if name == "" {
		name = cleanName(prop.Name)
	}
	used := s.usedNames(ctx, userID)
	if name == "" {
		name, reason = autoName(text, prop.Job, used)
		auto = true
	}
	job := strings.TrimSpace(prop.Job)
	if job == "" {
		job = jobLine(text, name)
	}
	job, _ = redactSecrets(job)
	instructions := strings.TrimSpace(prop.Instructions)
	if instructions == "" {
		instructions = instructionsFor(name, job, text)
	}
	instructions, _ = redactSecrets(instructions)
	title := strings.TrimSpace(prop.Title)
	if title == "" {
		title = titleFor(job)
	}
	if job == "" {
		title = "New agent"
	}
	schedule := strings.TrimSpace(prop.Schedule)
	if schedule == "" {
		schedule = scheduleFrom(text)
	}
	model := s.defaultConfiguredModel(ctx, userID)
	if model == "" {
		model = defaultModelID()
	}
	card := setupState{
		Name: name, Title: title, Instructions: instructions, Job: job,
		Schedule: schedule, Reason: reason, Model: model, Size: createSize, Idle: createIdle,
		Auto: auto, Stripped: stripped,
	}
	s.refreshCard(ctx, userID, &card)
	if auto && card.Reason == "" {
		card.Reason = reason
		card.Lead = leadFor(card)
	}
	return card
}

func (s *Server) editCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	doc, _, err := s.loadDraft(r.Context(), user.ID)
	if err != nil || doc.Card == nil {
		http.Redirect(w, r, "/new-agent", http.StatusSeeOther)
		return
	}
	doc.Editing = true
	if err := s.saveDraft(r.Context(), user.ID, doc); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.showCreate(w, r, user)
}

func (s *Server) cancelCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	doc, _, err := s.loadDraft(r.Context(), user.ID)
	if err != nil {
		http.Redirect(w, r, "/new-agent", http.StatusSeeOther)
		return
	}
	doc.Editing = false
	_ = s.saveDraft(r.Context(), user.ID, doc)
	s.showCreate(w, r, user)
}

// resetCreate discards the in-progress create draft.
// Start over returns to a fresh /new-agent page. Close ([X] or Escape)
// goes home. A later visit to /new-agent is a new conversation.
func (s *Server) resetCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	_ = s.store.DeleteCreateDraft(r.Context(), user.ID)
	http.Redirect(w, r, resetCreateNext(r.FormValue("next")), http.StatusSeeOther)
}

func resetCreateNext(next string) string {
	if strings.TrimSpace(next) == "/" {
		return "/"
	}
	return "/new-agent"
}

func (s *Server) saveCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	doc, _, err := s.loadDraft(r.Context(), user.ID)
	if err != nil || doc.Card == nil {
		http.Redirect(w, r, "/new-agent", http.StatusSeeOther)
		return
	}
	card := doc.Card
	name := cleanName(r.FormValue("name"))
	if name == "" || len(name) > 40 {
		s.showCreate(w, r, user)
		return
	}
	job, _ := redactSecrets(strings.TrimSpace(r.FormValue("job")))
	instructions, _ := redactSecrets(r.FormValue("instructions"))
	if strings.TrimSpace(instructions) == "" {
		instructions = instructionsFor(name, job, job)
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		model = card.Model
	} else if !s.AcceptModel(r.Context(), user.ID, model, card.Model) {
		card.Name = name
		card.Job = job
		card.Instructions = instructions
		card.ModelErr = "That model is not in the provider manifest."
		card.ModelHold = model
		card.Auto = false
		doc.Editing = true
		doc.Card = card
		if err := s.saveDraft(r.Context(), user.ID, doc); err != nil {
			http.Error(w, "Error", http.StatusInternalServerError)
			return
		}
		s.showCreate(w, r, user)
		return
	}
	card.ModelErr = ""
	card.ModelHold = ""
	size := strings.TrimSpace(r.FormValue("size"))
	switch size {
	case "1:2048", "2:4096", "8:16384":
	default:
		size = createSize
	}
	idle := createIdle
	switch strings.TrimSpace(r.FormValue("idle")) {
	case "300":
		idle = 300
	case "3600":
		idle = 3600
	case "900", "":
		idle = 900
	}
	card.Name = name
	card.Job = job
	card.Instructions = instructions
	card.Model = model
	card.Size = size
	card.Idle = idle
	card.Auto = false
	if job == "" {
		card.Title = "New agent"
	} else if card.Title == "" || card.Title == "New agent" {
		card.Title = titleFor(job)
	}
	s.refreshCard(r.Context(), user.ID, card)
	doc.Card = card
	doc.Editing = false
	if err := s.saveDraft(r.Context(), user.ID, doc); err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	s.showCreate(w, r, user)
}

func (s *Server) confirmCreate(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if id := strings.TrimSpace(r.FormValue("draft")); id != "" {
		if _, err := s.store.CreateDraftByID(r.Context(), user.ID, id); s.missing(w, err) {
			return
		}
	}
	confirm := strings.TrimSpace(r.FormValue("confirm"))
	if confirm != "card" && confirm != "advanced" {
		http.Error(w, "Create needs the setup card.\n", http.StatusBadRequest)
		return
	}
	if confirm == "advanced" {
		s.saveNewAgent(w, r, user)
		return
	}
	doc, _, err := s.loadDraft(r.Context(), user.ID)
	if err != nil || doc.Card == nil || doc.Editing {
		http.Error(w, "Create needs the setup card.\n", http.StatusBadRequest)
		return
	}
	card := *doc.Card
	s.refreshCard(r.Context(), user.ID, &card)
	if card.Conflict || !card.HasKey {
		doc.Card = &card
		_ = s.saveDraft(r.Context(), user.ID, doc)
		s.showCreate(w, r, user)
		return
	}
	h, image, publicURL := s.hostConfig()
	agent, err := s.materialize(r.Context(), user, card, h != nil && image != "" && publicURL != "")
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			card.Conflict = true
			card.Alt = freeAlt(card.Name, s.usedNames(r.Context(), user.ID))
			card.Lead = leadFor(card)
			doc.Card = &card
			_ = s.saveDraft(r.Context(), user.ID, doc)
			s.showCreate(w, r, user)
			return
		}
		if s.missing(w, err) {
			return
		}
		return
	}
	_ = s.store.DeleteCreateDraft(r.Context(), user.ID)
	if err := s.launchNewAgent(r.Context(), user.ID, agent); err != nil {
		if errors.Is(err, store.ErrQuota) {
			_ = s.store.DeleteAgent(r.Context(), user.ID, agent.ID)
			p, _ := s.shell(r, user)
			p.Page = "create"
			p.Title = "New agent"
			view := s.presentDraft(r.Context(), user, doc, "")
			view.Error = "This account has reached its machine limit."
			p.Create = view
			s.render(w, http.StatusConflict, "app", finish(p))
			return
		}
		if errors.Is(err, errNoHost) {
			http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID), http.StatusSeeOther)
			return
		}
		if s.missing(w, err) {
			return
		}
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID), http.StatusSeeOther)
}

func (s *Server) materialize(ctx context.Context, user store.User, card setupState, pending bool) (store.Agent, error) {
	sec, err := s.store.SystemSection(ctx, user.ID, "agents")
	if err != nil {
		return store.Agent{}, err
	}
	idle := card.Idle
	if idle <= 0 {
		idle = createIdle
	}
	size := card.Size
	if size == "" {
		size = createSize
	}
	title := card.Title
	if title == "" {
		title = titleFor(card.Job)
	}
	color := ""
	if store.ValidAgentColor(card.Color) {
		color = card.Color
	}
	agent, _, err := s.store.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: card.Name, Title: title, Description: card.Job,
		Model: card.Model, Status: "draft", Color: color, IdleTimeout: idle, Size: size,
		PendingGreet: pending,
	})
	if err != nil {
		return store.Agent{}, err
	}
	spec := castcfg.Spec{
		Name: card.Name, Title: title, Version: "1", Model: card.Model,
		Instructions: card.Instructions, Permissions: map[string]string{},
	}
	if _, err := s.storeBundle(ctx, user.ID, agent.ID, spec, false); err != nil {
		_ = s.store.DeleteAgent(ctx, user.ID, agent.ID)
		return store.Agent{}, err
	}
	if err := s.noteCreated(ctx, user, agent); err != nil {
		return store.Agent{}, err
	}
	if card.Schedule != "" {
		th, err := s.store.DMThread(ctx, user.ID, agent.ID)
		if err == nil {
			_, _ = s.store.CreateOriginMessage(ctx, user.ID, th.ID, "system", agent.ID, "schedule: "+card.Schedule, "setup")
		}
	}
	return agent, nil
}

func (s *Server) noteCreated(ctx context.Context, user store.User, agent store.Agent) error {
	th, err := s.store.DMThread(ctx, user.ID, agent.ID)
	if err != nil {
		return err
	}
	when := clockLabel(agent.CreatedAt, userLocation(user.Timezone))
	if when == "" {
		when = "just now"
	}
	body := "You created " + agent.Name + " at " + when + " from a description."
	_, err = s.store.CreateOriginMessage(ctx, user.ID, th.ID, "system", agent.ID, body, "created")
	return err
}

func (s *Server) createDraft(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if _, err := s.store.CreateDraftByID(r.Context(), user.ID, r.PathValue("id")); s.missing(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) advancedForm(w http.ResponseWriter, r *http.Request, user store.User) {
	form, err := s.blankForm(r.Context(), user)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	form.Heading = "New agent, all fields"
	form.Sub = "Change anything, then create."
	form.Submit = "Create and start"
	form.Back = "/new-agent"
	form.Advanced = true
	if doc, _, err := s.loadDraft(r.Context(), user.ID); err == nil && doc.Card != nil {
		card := *doc.Card
		s.refreshCard(r.Context(), user.ID, &card)
		form.Prefill = true
		form.Sub = "Prefilled from your chat with castbot. Change anything, then create."
		form.Name = card.Name
		form.Title = card.Title
		form.Job = card.Job
		form.Instructions = card.Instructions
		form.Model = card.Model
		form.keepModel()
		form.Size = card.Size
		form.Idle = card.Idle
		if store.ValidAgentColor(card.Color) {
			form.Color = card.Color
		}
		if line := modelKeyLine(card.KeyLabel); line != "" {
			form.KeyLine = line
		}
	}
	s.showForm(w, r, user, form, http.StatusOK)
}
