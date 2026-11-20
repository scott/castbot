// Package assistant creates the first agent on a new account.
// The agent is CastBot: a personal assistant, an expert on this product,
// and able to delegate work to the other agents on the account.
package assistant

import (
	"context"
	"errors"
	"strings"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/store"
)

const (
	// Name is the sidebar name. It is unique per account.
	Name = "CastBot"
	// Title is the short label under the name.
	Title = "Personal assistant"
	// Job is what other agents see. It stays within the 160 character form limit.
	Job = "Faithful personal assistant and expert on CastBot. Delegates work to other agents."
	// Note is the first line in the chat, before the machine has started.
	Note = "CastBot was created with this account. Add a model key, then start the machine. This chat says hello once the machine is running."

	bundleVersion = "1"
	idleTimeout   = 900
	machineSize   = "1:2048"
)

// ErrInUse means the account already has agents and none of them is CastBot.
// A later install does not add CastBot beside agents the owner already made.
var ErrInUse = errors.New("an agent already exists")

// Result is the agent Ensure found or created.
type Result struct {
	Agent   store.Agent
	Created bool
}

// Instructions is the stored instructions.md. Tool guides are appended when
// the machine pulls the bundle, so this text is the role only.
func Instructions() string {
	return strings.TrimSpace(`
You are CastBot, the personal assistant for the owner of this account.

Be faithful to the owner. Do their work, keep their confidence, and do not act against them. Keep their notes, files, and secrets private. Ask before you spend money, delete something, push a change, change an account setting, or hand a secret to a tool. When you are unsure, ask.

You are the expert on using castbot. An agent is a chat with its own machine. The sidebar lists agents, groups, and sections. A reply streams into the thread. A risky tool call waits on an approval card until the owner allows it. A routine runs a saved prompt on a schedule. Secrets stay in the vault and are granted to one agent at a time. A connection attaches a tool the agent can call. A machine pauses when it is idle and wakes for the next message. Replace machine builds a new machine and copies memory and files across. The owner signs in with a password and an authenticator code.

When a job should keep going on its own, create another agent for that job. You stay the owner's assistant. You can delegate work to the other agents on this account. Ask one agent in a direct message, or ask two or more together in one chat. The roster and the handoff fence are added to your prompt when other agents exist. Tell the owner what you asked them to do, wait for the result, and answer in your own words. Do not invent a result you have not received.

Speak plainly. Do the work they asked for. Keep a short record of what changed.
`) + "\n"
}

// Ensure creates CastBot for userID when that account has no agents yet.
// A second call returns the same agent and does not rewrite it.
func Ensure(ctx context.Context, st *store.Store, userID string) (Result, error) {
	if _, err := st.GetUser(ctx, userID); err != nil {
		return Result{}, err
	}
	agents, err := st.ListAgents(ctx, userID)
	if err != nil {
		return Result{}, err
	}
	if existing, ok := findCastBot(agents); ok {
		return Result{Agent: existing, Created: false}, nil
	}
	if len(agents) > 0 {
		return Result{}, ErrInUse
	}
	return create(ctx, st, userID)
}

func findCastBot(agents []store.Agent) (store.Agent, bool) {
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), Name) {
			return agent, true
		}
	}
	return store.Agent{}, false
}

func create(ctx context.Context, st *store.Store, userID string) (Result, error) {
	sec, err := st.SystemSection(ctx, userID, "pinned")
	if err != nil {
		return Result{}, err
	}
	model, err := defaultModel()
	if err != nil {
		return Result{}, err
	}
	agent, thread, err := st.CreateAgent(ctx, userID, store.AgentInput{
		SectionID:    sec.ID,
		Name:         Name,
		Title:        Title,
		Description:  Job,
		CanDelegate:  true,
		Model:        model,
		Status:       "draft",
		IdleTimeout:  idleTimeout,
		Size:         machineSize,
		PendingGreet: true,
	})
	if err != nil {
		return Result{}, err
	}
	yaml, err := render(agent.Name, model)
	if err != nil {
		_ = st.DeleteAgent(ctx, userID, agent.ID)
		return Result{}, err
	}
	if _, err := st.PutBundle(ctx, userID, agent.ID, bundleVersion, yaml, Instructions()); err != nil {
		_ = st.DeleteAgent(ctx, userID, agent.ID)
		return Result{}, err
	}
	if _, err := st.CreateOriginMessage(ctx, userID, thread.ID, "system", agent.ID, Note, "created"); err != nil {
		_ = st.DeleteAgent(ctx, userID, agent.ID)
		return Result{}, err
	}
	return Result{Agent: agent, Created: true}, nil
}

func defaultModel() (string, error) {
	models, err := castcfg.Models()
	if err != nil {
		return "", err
	}
	if len(models) == 0 || strings.TrimSpace(models[0].ID) == "" {
		return "", errors.New("model list is empty")
	}
	return models[0].ID, nil
}

func render(name, model string) (string, error) {
	spec := castcfg.Spec{
		Name:         name,
		Title:        Title,
		Version:      bundleVersion,
		Model:        model,
		Instructions: Instructions(),
		Permissions:  map[string]string{},
	}
	for _, tool := range castcfg.Tools() {
		spec.Tools = append(spec.Tools, tool.Name)
		spec.Permissions[tool.Name] = castcfg.DefaultLevel(tool.Name)
	}
	raw, err := castcfg.RenderYAML(spec)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
