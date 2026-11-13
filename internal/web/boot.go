package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

var errNoHost = errors.New("no machine host")

type bootView struct {
	On, Poll, Failed, Up bool
	Title, Body, Foot    string
	Steps                []bootStep
	TryAgain             bool
	Started              string
	Placeholder          string
	Fragment             bool
}

type bootStep struct {
	Title, Detail  string
	Done, Now, Bad bool
}

func (s *Server) SetBootBudget(d time.Duration) {
	if d <= 0 {
		return
	}
	s.mu.Lock()
	s.bootFor = d
	s.mu.Unlock()
}

func (s *Server) bootBudget() time.Duration {
	s.mu.Lock()
	d := s.bootFor
	s.mu.Unlock()
	if d <= 0 {
		return 4 * time.Minute
	}
	return d
}

func (s *Server) launchNewAgent(ctx context.Context, userID string, agent store.Agent) error {
	h, image, publicURL := s.hostConfig()
	if h == nil || image == "" || publicURL == "" {
		return errNoHost
	}
	if s.replacing(ctx, userID, agent.ID) {
		return store.ErrConflict
	}
	if _, err := s.store.LiveVM(ctx, userID, agent.ID); err == nil {
		return store.ErrConflict
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if _, err := s.store.BeginVM(ctx, userID, agent.ID, host.MachineName(agent.ID), image); err != nil {
		return err
	}
	s.noteVM(ctx, userID, agent.ID, "", store.EventCreateRequested, 0)
	s.prov.Add(1)
	go func() {
		defer s.prov.Done()
		s.finishProvision(s.base, h, userID, agent, image, publicURL)
		s.maybeGreet(context.Background(), userID, agent.ID)
	}()
	return nil
}

func (s *Server) maybeGreet(ctx context.Context, userID, agentID string) {
	if ctx == nil {
		ctx = context.Background()
	}
	greeted, err := s.store.AgentGreeted(ctx, userID, agentID)
	if err != nil || greeted != "" {
		return
	}
	if !s.bootHealthy(ctx, userID, agentID) {
		return
	}
	agent, err := s.store.GetAgent(ctx, userID, agentID)
	if err != nil {
		return
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return
	}
	th, err := s.store.DMThread(ctx, userID, agentID)
	if err != nil {
		return
	}
	waiting := s.waitingBodies(ctx, userID, th.ID)
	schedule := s.setupNote(ctx, userID, th.ID)
	first := firstName(user.DisplayName)
	job := strings.TrimSpace(agent.Description)
	text := greetCopy(first, agent.Name, job, schedule, waiting)
	runRow, started, err := s.store.BeginGreet(ctx, userID, agentID, store.GreetWrite{
		ThreadID: th.ID, Body: text, AskJob: job == "",
	})
	if err != nil || !started {
		return
	}
	s.trackGreet(runRow.ID)
	s.runs.Start(runJob(userID, th.ID, agentID, runRow.ID, "", text, th.CastSessionID, nil))
}

func (s *Server) trackGreet(id string) {
	s.greetMu.Lock()
	if s.greetRuns == nil {
		s.greetRuns = map[string]struct{}{}
	}
	s.greetRuns[id] = struct{}{}
	s.greetMu.Unlock()
}

func (s *Server) isGreetRun(id string) bool {
	s.greetMu.Lock()
	_, ok := s.greetRuns[id]
	s.greetMu.Unlock()
	return ok
}

func (s *Server) settleGreetRun(job run.Job, status string) {
	if !s.isGreetRun(job.RunID) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if status != "done" {
		_ = s.store.SettleGreetRun(ctx, job.UserID, job.RunID)
	}
	if agent, err := s.store.GetAgent(ctx, job.UserID, job.AgentID); err == nil && strings.TrimSpace(agent.Description) == "" {
		_ = s.store.NoteAgent(ctx, job.UserID, job.AgentID, "waiting for a job")
	}
	s.greetMu.Lock()
	delete(s.greetRuns, job.RunID)
	s.greetMu.Unlock()
}

func (s *Server) bootHealthy(ctx context.Context, userID, agentID string) bool {
	vm, err := s.store.LiveVM(ctx, userID, agentID)
	if err != nil || vm.State != "running" || vm.MicroVMID == "" || vm.MicroVMID == "pending" {
		return false
	}
	if _, err := s.store.LatestBundle(ctx, userID, agentID); err != nil {
		return false
	}
	events, err := s.store.ListVMEvents(ctx, userID, agentID)
	if err != nil {
		return false
	}
	for _, ev := range events {
		if ev.Kind == store.EventGuestReady {
			return true
		}
	}
	return false
}

func (s *Server) waitingBodies(ctx context.Context, userID, threadID string) []string {
	msgs, err := s.store.ListMessages(ctx, userID, threadID)
	if err != nil {
		return nil
	}
	var out []string
	for _, msg := range msgs {
		if msg.Origin == "waiting" && msg.AuthorKind == "user" {
			out = append(out, msg.Body)
		}
	}
	return out
}

func (s *Server) setupNote(ctx context.Context, userID, threadID string) string {
	msgs, err := s.store.ListMessages(ctx, userID, threadID)
	if err != nil {
		return ""
	}
	for _, msg := range msgs {
		if msg.Origin == "setup" && strings.HasPrefix(msg.Body, "schedule: ") {
			return strings.TrimSpace(strings.TrimPrefix(msg.Body, "schedule: "))
		}
	}
	return ""
}

func greetCopy(first, name, job, schedule string, waiting []string) string {
	if strings.TrimSpace(first) == "" {
		first = "there"
	}
	if strings.TrimSpace(job) == "" {
		return "Hi " + first + ", I am " + name + ". I am all set up and happy to be here.\n\nWhat should my job be? Tell me in a sentence, or pick one of these to start:"
	}
	extra := "I am starting on it now."
	if len(waiting) > 0 {
		extra = "You asked me to start with " + greetAsk(waiting[0]) + ", so I am starting there now."
	}
	msg := "Hi " + first + ", I am " + name + ". Nice to meet you, and thanks for setting me up.\n\nMy job: " + job + " " + extra
	if schedule != "" {
		msg += " You said " + schedule + ". I can set that up as a routine for you."
	}
	return msg
}

func greetAsk(raw string) string {
	ask := strings.TrimSpace(raw)
	lower := strings.ToLower(ask)
	for _, prefix := range []string{"please start with ", "please begin with ", "start with ", "begin with "} {
		if strings.HasPrefix(lower, prefix) {
			ask = strings.TrimSpace(ask[len(prefix):])
			break
		}
	}
	ask = strings.Trim(ask, " .")
	if ask == "" {
		ask = strings.Trim(strings.TrimSpace(raw), " .")
	}
	if len(ask) > 180 {
		ask = ask[:180]
	}
	return ask
}

func firstName(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func (s *Server) attachBoot(r *http.Request, user store.User, agent store.Agent, p *page) {
	if !s.hasCreatedLine(r.Context(), user.ID, agent.ID) {
		return
	}
	s.maybeGreet(r.Context(), user.ID, agent.ID)
	agent, err := s.store.GetAgent(r.Context(), user.ID, agent.ID)
	if err != nil {
		return
	}
	view := s.bootCard(r.Context(), user, agent)
	p.Boot = view
	if view.Up && strings.TrimSpace(agent.Description) == "" {
		p.AskJob = true
		sub := "waiting for a job"
		if p.Agent.Title != "" {
			sub = p.Agent.Title + " · waiting for a job"
		}
		p.Agent.Sub = sub
		for i := range p.Sections {
			for j := range p.Sections[i].Agents {
				if p.Sections[i].Agents[j].ID == agent.ID {
					p.Sections[i].Agents[j].Sub = sub
				}
			}
		}
	}
	if view.On {
		p.Agent.Model = ""
	}
	if view.Placeholder != "" {
		p.Boot.Placeholder = view.Placeholder
	}
	s.reloadGreet(r, user, agent, p)
	for i := range p.Messages {
		if p.Messages[i].Origin != "waiting" && !p.Messages[i].Waiting {
			continue
		}
		p.Messages[i].Waiting = true
		if view.Failed {
			p.Messages[i].Wait = "Not sent yet · sends when " + agent.Name + " starts"
		} else {
			p.Messages[i].Wait = "Waiting · sends when " + agent.Name + " is up"
		}
	}
}

func (s *Server) reloadGreet(r *http.Request, user store.User, agent store.Agent, p *page) {
	if p.ThreadID == "" {
		return
	}
	for _, msg := range p.Messages {
		if msg.Origin == "greet" {
			return
		}
	}
	msgs, err := s.store.ListMessages(r.Context(), user.ID, p.ThreadID)
	if err != nil {
		return
	}
	p.Messages = presentMessages(msgs, p.Agent, s.store.Now(), userLocation(user.Timezone))
	s.linkMentionedFiles(r.Context(), user.ID, agent.ID, p.Messages)
}

func (s *Server) hasCreatedLine(ctx context.Context, userID, agentID string) bool {
	th, err := s.store.DMThread(ctx, userID, agentID)
	if err != nil {
		return false
	}
	msgs, err := s.store.ListMessages(ctx, userID, th.ID)
	if err != nil {
		return false
	}
	for _, msg := range msgs {
		if msg.Origin == "created" {
			return true
		}
	}
	return false
}

func (s *Server) bootCard(ctx context.Context, user store.User, agent store.Agent) bootView {
	vm, vmErr := s.store.LiveVM(ctx, user.ID, agent.ID)
	healthy := s.bootHealthy(ctx, user.ID, agent.ID)
	greeted, _ := s.store.AgentGreeted(ctx, user.ID, agent.ID)
	elapsed := s.bootElapsed(ctx, agent, vm, vmErr)
	failed := !healthy && (agent.Status == "failed" || elapsed > s.bootBudget())
	view := bootView{On: true, Poll: !healthy && !failed}
	if healthy && greeted != "" {
		view.Poll = false
		view.Up = true
		view.Title = agent.Name + " is up."
		secs := int(elapsed.Seconds())
		if secs < 1 {
			secs = 1
		}
		view.Started = "Started in " + itoa(secs) + " s."
		if strings.TrimSpace(agent.Description) == "" {
			view.Placeholder = "Tell " + agent.Name + " what its job is"
		}
		return view
	}
	if failed {
		view.Failed = true
		view.Poll = false
		view.Title = agent.Name + "'s machine did not start"
		view.Body = "Nothing is lost. " + agent.Name + ", its setup and your message are saved. It did not answer within 4 minutes."
		view.Foot = "Try again makes a fresh machine. " + agent.Name + " still says hello only once."
		view.TryAgain = true
		view.Steps = s.bootSteps(agent, vm, vmErr, healthy, greeted != "", true)
		return view
	}
	view.Title = agent.Name + " is starting up"
	view.Body = "Its own machine is getting ready. This usually takes under a minute."
	view.Foot = "You can write now. " + agent.Name + " reads it as soon as it is up."
	view.Steps = s.bootSteps(agent, vm, vmErr, healthy, greeted != "", false)
	return view
}

func (s *Server) bootElapsed(ctx context.Context, agent store.Agent, vm store.VM, vmErr error) time.Duration {
	start := agent.CreatedAt
	if vmErr == nil && vm.CreatedAt != "" {
		start = vm.CreatedAt
	}
	t, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return 0
	}
	d := s.store.Now().Sub(t)
	if d < 0 {
		return 0
	}
	return d
}

func (s *Server) bootSteps(agent store.Agent, vm store.VM, vmErr error, healthy, greeted, failed bool) []bootStep {
	saved := bootStep{Title: "Saved " + agent.Name + " and its setup", Done: true}
	creatingDone := vmErr == nil && vm.MicroVMID != "" && vm.MicroVMID != "pending" && vm.State != "creating"
	creating := bootStep{Title: "Creating its machine", Done: creatingDone || healthy}
	starting := bootStep{Title: "Starting " + agent.Name, Done: healthy}
	hello := bootStep{Title: "Saying hello", Done: greeted && healthy}
	steps := []bootStep{saved, creating, starting, hello}
	if failed {
		for i := range steps {
			if !steps[i].Done {
				steps[i].Bad = true
				break
			}
		}
		return steps
	}
	marked := false
	for i := range steps {
		if steps[i].Done {
			continue
		}
		if !marked {
			steps[i].Now = true
			marked = true
		}
	}
	return steps
}

func (s *Server) bootFragment(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if !s.hasCreatedLine(r.Context(), user.ID, agent.ID) {
		http.NotFound(w, r)
		return
	}
	s.maybeGreet(r.Context(), user.ID, agent.ID)
	agent, err = s.store.GetAgent(r.Context(), user.ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	p := page{
		Agent: agentView{ID: agent.ID, Name: agent.Name},
		CSRF:  from(r).CSRF,
	}
	p.Boot = s.bootCard(r.Context(), user, agent)
	p.Boot.Fragment = true
	if p.Boot.Up {
		w.Header().Set("HX-Redirect", "/agents/"+url.PathEscape(agent.ID))
	}
	s.render(w, http.StatusOK, "bootPoll", p)
}

func (s *Server) retryBoot(w http.ResponseWriter, r *http.Request) {
	user, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	agent, err := s.store.GetAgent(r.Context(), user.ID, r.PathValue("id"))
	if s.missing(w, err) {
		return
	}
	if s.refuseReplacing(w, r, user.ID, agent.ID) {
		return
	}
	if vm, err := s.store.LiveVM(r.Context(), user.ID, agent.ID); err == nil {
		h, _, _ := s.hostConfig()
		if h != nil && vm.MicroVMID != "" && vm.MicroVMID != "pending" {
			s.deleteQuiet(h, vm.MicroVMID)
		}
		_ = s.store.ReleaseVM(r.Context(), user.ID, agent.ID)
	}
	agent, err = s.store.GetAgent(r.Context(), user.ID, agent.ID)
	if err != nil {
		http.Error(w, "Error", http.StatusInternalServerError)
		return
	}
	if err := s.launchNewAgent(r.Context(), user.ID, agent); err != nil {
		if errors.Is(err, store.ErrQuota) {
			s.renderAgent(w, r, user, agent.ID, false, "chat", http.StatusConflict, "This account has reached its machine limit.")
			return
		}
		if errors.Is(err, errNoHost) {
			s.renderAgent(w, r, user, agent.ID, false, "chat", http.StatusBadRequest, "No machine host is configured.")
			return
		}
		if s.missing(w, err) {
			return
		}
		return
	}
	http.Redirect(w, r, "/agents/"+url.PathEscape(agent.ID), http.StatusSeeOther)
}

func (s *Server) holdingForGreet(ctx context.Context, userID, agentID string) bool {
	greeted, err := s.store.AgentGreeted(ctx, userID, agentID)
	if err != nil || greeted != "" {
		return false
	}
	return s.hasCreatedLine(ctx, userID, agentID)
}

func sizePair(size string) (int, int) {
	switch size {
	case "2:4096":
		return 2, 4096
	case "8:16384":
		return 8, 16384
	default:
		return host.DefaultCPU, host.DefaultMemoryMiB
	}
}
