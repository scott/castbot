package web

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/replacetool"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/store"
)

const (
	moveRejected    = "That request was rejected."
	moveNoHost      = "No machine host is configured."
	moveAlready     = "A replacement is already running."
	moveNoRollback  = "There is no previous runtime to roll back to."
	moveSameRuntime = "This agent is already on that runtime."
	moveReplaceGo   = "Replacing this machine. Chat is paused until it is done."
	moveRollbackGo  = "Rolling back this runtime. Chat is paused until it is done."
	replyBusy       = "Finish or stop the reply in progress first."
)

// machineReplace runs one replace tool for the agent in the path.
// A user_id, agent_id, token, or value argument is ignored.
// The first call states the dialog and does not start. A later call with
// confirm yes starts only after that dialog is already in the thread and
// the latest message from the person is a yes.
func (s *Server) machineReplace(w http.ResponseWriter, r *http.Request, userID, name string, raw json.RawMessage) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		writeAgentResult(w, moveRejected, true)
		return
	}
	agent, err := s.store.GetAgent(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		writeAgentResult(w, moveRejected, true)
		return
	}
	var args replaceArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			writeAgentResult(w, moveRejected, true)
			return
		}
	}
	text, failed := s.decideMove(r.Context(), user, agent, "", "", name, args.confirmYes(), true)
	writeAgentResult(w, text, failed)
}

type replaceArgs struct {
	Confirm string `json:"confirm"`
}

func (a replaceArgs) confirmYes() bool {
	return strings.TrimSpace(a.Confirm) == "yes"
}

// maybeReplace reads one castbot-replace fence from the latest reply.
// A replace that was confirmed during this turn starts here, after the
// run is done, so the reply in progress is not still open.
func (s *Server) maybeReplace(job run.Job) {
	if job.UserID == "" || job.RunID == "" || job.AgentID == "" {
		return
	}
	unlock := lockMove(job.RunID)
	defer unlock()
	ctx := context.Background()
	note, noted := takeMove(job.RunID)
	reply, err := s.store.LatestAgentReply(ctx, job.UserID, job.RunID)
	if err != nil {
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
	visible, fields, ok := replacetool.Parse(reply.Body)
	if ok {
		text, failed := s.decideMove(ctx, user, agent, job.ThreadID, reply.ID, fields.Action, fields.Confirm, false)
		s.writeMoveReply(ctx, job.UserID, reply.ID, visible, text, failed)
		return
	}
	if !noted {
		return
	}
	if note.start {
		if !s.moveAllowed(ctx, user.ID, s.moveThread(ctx, user.ID, agent.ID, job.ThreadID), reply.ID, note.action) {
			text, failed := s.explainMove(ctx, user, agent, note.action)
			if !failed {
				s.writeMoveReply(ctx, job.UserID, reply.ID, reply.Body, text, false)
			}
			return
		}
		text, failed := s.startMove(ctx, user, agent, note.action)
		s.writeMoveReply(ctx, job.UserID, reply.ID, reply.Body, text, failed)
		return
	}
	if replacetool.HasMarker(reply.Body, note.action) {
		return
	}
	text, failed := s.explainMove(ctx, user, agent, note.action)
	if failed {
		return
	}
	s.writeMoveReply(ctx, job.UserID, reply.ID, reply.Body, text, false)
}

func (s *Server) writeMoveReply(ctx context.Context, userID, replyID, visible, text string, failed bool) {
	body := strings.TrimSpace(visible)
	if body == "" {
		body = text
	} else if failed || !moveStarted(text) {
		body = joinLimit(body, text, 8000)
	}
	s.replaceReply(ctx, userID, replyID, body)
}

func (s *Server) decideMove(ctx context.Context, user store.User, agent store.Agent, threadID, skipID, action string, confirm, deferOK bool) (string, bool) {
	if !replacetool.Known(action) {
		return moveRejected, true
	}
	threadID = s.moveThread(ctx, user.ID, agent.ID, threadID)
	allowed := confirm && s.moveAllowed(ctx, user.ID, threadID, skipID, action)
	if !allowed {
		text, failed := s.explainMove(ctx, user, agent, action)
		if deferOK && !failed {
			if id, _, err := s.store.OpenRun(ctx, user.ID, agent.ID); err == nil && id != "" {
				noteMove(id, action, false)
			}
		}
		return text, failed
	}
	if deferOK {
		if id, _, err := s.store.OpenRun(ctx, user.ID, agent.ID); err == nil && id != "" {
			noteMove(id, action, true)
			return moveGo(action), false
		}
	}
	return s.startMove(ctx, user, agent, action)
}

func (s *Server) explainMove(ctx context.Context, user store.User, agent store.Agent, action string) (string, bool) {
	if s.replacing(ctx, user.ID, agent.ID) {
		return moveAlready, true
	}
	live, line, err := s.store.ReplacePrecheck(ctx, user.ID, agent.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return moveRejected, true
		}
		return "The machine record could not be read.", true
	}
	if line != "" && line != replyBusy {
		return line, true
	}
	h, image, publicURL := s.hostConfig()
	if action == replacetool.ToolRollback {
		if h == nil || publicURL == "" {
			return moveNoHost, true
		}
		rb, err := s.store.ActiveRollback(ctx, user.ID, agent.ID)
		if err != nil || strings.TrimSpace(rb.Image) == "" {
			return moveNoRollback, true
		}
		if replace.ImageTag(live.Image) == replace.ImageTag(rb.Image) {
			return moveSameRuntime, true
		}
		return rollbackExplain(agent.Name, replace.ImageTag(rb.Image), live.State == "paused"), false
	}
	if h == nil || image == "" || publicURL == "" {
		return moveNoHost, true
	}
	return replaceExplain(agent.Name, replace.ImageTag(image), live.State == "paused"), false
}

func (s *Server) startMove(ctx context.Context, user store.User, agent store.Agent, action string) (string, bool) {
	if s.replacing(ctx, user.ID, agent.ID) {
		return moveAlready, true
	}
	live, line, err := s.store.ReplacePrecheck(ctx, user.ID, agent.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return moveRejected, true
		}
		return "The machine record could not be read.", true
	}
	if line != "" {
		return line, true
	}
	h, image, publicURL := s.hostConfig()
	boot := image
	if action == replacetool.ToolRollback {
		rb, err := s.store.ActiveRollback(ctx, user.ID, agent.ID)
		if err != nil || strings.TrimSpace(rb.Image) == "" {
			return moveNoRollback, true
		}
		if replace.ImageTag(live.Image) == replace.ImageTag(rb.Image) {
			return moveSameRuntime, true
		}
		if h == nil || publicURL == "" {
			return moveNoHost, true
		}
		boot = rb.Image
	} else if h == nil || image == "" || publicURL == "" {
		return moveNoHost, true
	}
	s.launchReplace(user, agent.ID, boot)
	return moveGo(action), false
}

func (s *Server) launchReplace(user store.User, agentID, image string) {
	h, _, publicURL := s.hostConfig()
	s.prov.Add(1)
	go func() {
		defer s.prov.Done()
		ctx, cancel := context.WithTimeout(context.Background(), replace.RunLimit)
		defer cancel()
		_, err := replace.Run(ctx, s.store, h, replace.Options{
			UserID: user.ID, AgentID: agentID,
			ActorKind: "user", ActorID: user.ID, Actor: "web:" + user.Email,
			Image: image, PublicURL: publicURL, DataDir: s.dataDir,
		})
		if err != nil {
			log.Printf("replace finished agent %s class %s", agentID, replaceClass(err))
		}
		if !s.replacing(ctx, user.ID, agentID) {
			s.fireAfterRun(ctx, user.ID, agentID)
		}
	}()
}

func replaceExplain(name, tag string, paused bool) string {
	var b strings.Builder
	b.WriteString("castbot starts a new machine on the current runtime (")
	b.WriteString(tag)
	b.WriteString("), copies this agent's memory and workspace files, then deletes the old machine. It usually takes a few minutes. Chat is paused until it is done.\n\nKept:\n")
	for _, line := range replacetool.KeptLines() {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("\nLost:\n")
	for _, line := range replacetool.LostLines() {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(replacetool.ReplacePromise)
	if paused {
		b.WriteByte(' ')
		b.WriteString(name)
		b.WriteString(" is paused now, so castbot wakes it once to read it.")
	}
	return b.String()
}

func rollbackExplain(name, tag string, paused bool) string {
	days := int(store.RollbackWindow / (24 * time.Hour))
	var b strings.Builder
	b.WriteString("castbot starts a machine on the previous runtime (")
	b.WriteString(tag)
	b.WriteString("), copies this agent's memory and workspace files, then removes the machine it is on now. Chat is paused until it is done.\n\n")
	b.WriteString("Previous runtime ")
	b.WriteString(tag)
	b.WriteString(" can be restored for ")
	b.WriteString(strconv.Itoa(days))
	b.WriteString(" days. Memory and workspace files are copied again.\n\n")
	b.WriteString(replacetool.RollbackPromise)
	if paused {
		b.WriteByte(' ')
		b.WriteString(name)
		b.WriteString(" is paused now, so castbot wakes it once to read it.")
	}
	return b.String()
}

func moveGo(action string) string {
	if action == replacetool.ToolRollback {
		return moveRollbackGo
	}
	return moveReplaceGo
}

func moveStarted(text string) bool {
	return strings.HasPrefix(text, "Replacing this machine.") || strings.HasPrefix(text, "Rolling back this runtime.")
}

func (s *Server) moveThread(ctx context.Context, userID, agentID, threadID string) string {
	if strings.TrimSpace(threadID) != "" {
		return threadID
	}
	if _, th, err := s.store.OpenRun(ctx, userID, agentID); err == nil && th != "" {
		return th
	}
	th, err := s.store.DMThread(ctx, userID, agentID)
	if err != nil {
		return ""
	}
	return th.ID
}

func (s *Server) moveAllowed(ctx context.Context, userID, threadID, skipID, action string) bool {
	if threadID == "" || !replacetool.Known(action) {
		return false
	}
	msgs, err := s.store.ListMessages(ctx, userID, threadID)
	if err != nil {
		return false
	}
	told := false
	yes := false
	for _, msg := range msgs {
		if msg.ID == skipID {
			continue
		}
		if msg.AuthorKind == "agent" && replacetool.HasMarker(msg.Body, action) {
			told = true
			continue
		}
		if msg.AuthorKind != "user" {
			continue
		}
		if personSaidYes(msg.Body) && told {
			yes = true
			continue
		}
		yes = false
	}
	return yes
}

func personSaidYes(body string) bool {
	switch normalizeYes(body) {
	case "yes", "y", "yeah", "yep", "yup", "ok", "okay", "do it", "go ahead", "confirm", "confirmed", "please do", "yes please", "yes replace", "yes replace it", "yes replace the machine", "replace it", "yes roll back", "yes roll it back", "roll it back", "roll back":
		return true
	default:
		return false
	}
}

func normalizeYes(body string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(strings.TrimSpace(body)) {
		switch r {
		case ',', '.', '!', '?', '"', '\'', '`':
			continue
		case ' ', '\n', '\t', '\r':
			if b.Len() > 0 && !space {
				b.WriteByte(' ')
				space = true
			}
		default:
			space = false
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

type moveNote struct {
	action string
	start  bool
}

var (
	moveMu    sync.Mutex
	moveNotes = map[string]moveNote{}
	moveLocks = map[string]*sync.Mutex{}
)

func noteMove(runID, action string, start bool) {
	if runID == "" || action == "" {
		return
	}
	moveMu.Lock()
	moveNotes[runID] = moveNote{action: action, start: start}
	moveMu.Unlock()
}

func takeMove(runID string) (moveNote, bool) {
	moveMu.Lock()
	defer moveMu.Unlock()
	note, ok := moveNotes[runID]
	delete(moveNotes, runID)
	return note, ok
}

func lockMove(runID string) func() {
	moveMu.Lock()
	gate := moveLocks[runID]
	if gate == nil {
		gate = &sync.Mutex{}
		moveLocks[runID] = gate
	}
	moveMu.Unlock()
	gate.Lock()
	return gate.Unlock
}
