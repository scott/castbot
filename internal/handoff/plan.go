// Package handoff reads the fence a chief leaves when it asks other agents
// to do part of a job. The Cast runtime is not modified. The control plane
// parses the fence after the turn and starts those agents itself.
package handoff

import (
	"encoding/json"
	"regexp"
	"strings"
)

const (
	// Fence is the markdown language tag the chief uses.
	Fence = "castbot-handoff"
	// Asked is the phrase a specialist prompt starts with after the chief's name.
	Asked = "asked you to do this"
	// Replies is the phrase the chief's follow-up prompt contains.
	Replies = "Specialist replies"
)

// Agent is one roster row. Title and description are the specialty.
type Agent struct {
	ID          string
	Name        string
	Title       string
	Description string
}

// Plan is the work the chief handed off in one reply.
type Plan struct {
	DM   *Direct
	Chat *Group
}

// Direct is one specialist.
type Direct struct {
	Agent string
	Task  string
}

// Group is two or more specialists on one chat.
type Group struct {
	Agents []string
	Task   string
}

type wire struct {
	DM   *wireDirect `json:"dm,omitempty"`
	Chat *wireGroup  `json:"chat,omitempty"`
}

type wireDirect struct {
	Agent string `json:"agent"`
	Task  string `json:"task"`
}

type wireGroup struct {
	Agents []string `json:"agents"`
	Task   string   `json:"task"`
}

var fenceRE = regexp.MustCompile("(?s)```castbot-handoff\\s*\\n?(.*?)\\n?```")

// Preface is prepended to the chief's runtime prompt. It is not stored
// on the owner's message. The example uses the word AGENT so it is not
// a real id.
func Preface(chiefID string, roster []Agent) string {
	var b strings.Builder
	b.WriteString("You are the chief " + oneLine(chiefID) + ".\n")
	b.WriteString("Agents you can ask, including you:\n")
	for _, agent := range roster {
		b.WriteString("- id " + oneLine(agent.ID) + " | " + oneLine(agent.Name) + " | " + oneLine(agent.Title) + " | " + oneLine(agent.Description) + "\n")
	}
	b.WriteString("\nWhen you need another agent, end your reply with one castbot-handoff fence. Leave out a key you do not use. In the example the word AGENT is a placeholder.\n\n")
	b.WriteString("```castbot-handoff\n")
	b.WriteString(`{"dm":{"agent":"AGENT","task":"what they should do"},"chat":{"agents":["AGENT","AGENT"],"task":"what the group should do"}}`)
	b.WriteString("\n```\n")
	return b.String()
}

// SpecialistPrompt is the only text a specialist receives for a task.
// Earlier turns of the owner's chat with the chief stay off this prompt.
func SpecialistPrompt(chiefName, task string) string {
	return strings.TrimSpace(chiefName) + " asked you to do this:\n\n" + strings.TrimSpace(task) + "\n\nReply with the result. You can direct-message " + strings.TrimSpace(chiefName) + " about this job. A message to any other agent is rejected."
}

// RelayPrompt is what the chief reads when a specialist writes back.
func RelayPrompt(from, task string) string {
	return strings.TrimSpace(from) + " sent a direct message about this job:\n\n" + strings.TrimSpace(task) + "\n\nReply to the owner if this changes the answer."
}

// DeliveryPrompt is the chief's follow-up. It names the specialist replies
// and must not be parsed as another handoff.
func DeliveryPrompt(lines []string) string {
	var b strings.Builder
	b.WriteString("Specialist replies are ready. Write one combined answer for the owner.\n\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// Format writes a fence. Tests and the parser share this shape.
func Format(plan Plan) string {
	body, err := json.Marshal(toWire(plan))
	if err != nil {
		return ""
	}
	return "```castbot-handoff\n" + string(body) + "\n```"
}

// Parse returns the reply without a valid fence. ok is false when the
// text has no usable fence. An invalid fence is left in place.
func Parse(body string) (visible string, plan Plan, ok bool) {
	matches := fenceRE.FindAllStringSubmatchIndex(body, -1)
	for _, loc := range matches {
		if len(loc) < 4 {
			continue
		}
		raw := strings.TrimSpace(body[loc[2]:loc[3]])
		parsed, good := decode(raw)
		if !good {
			continue
		}
		visible = strings.TrimSpace(body[:loc[0]] + body[loc[1]:])
		return visible, parsed, true
	}
	return body, Plan{}, false
}

func decode(raw string) (Plan, bool) {
	var w wire
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return Plan{}, false
	}
	if w.DM == nil && w.Chat == nil {
		return Plan{}, false
	}
	var plan Plan
	if w.DM != nil {
		agent := strings.TrimSpace(w.DM.Agent)
		task := strings.TrimSpace(w.DM.Task)
		if agent == "" || task == "" || len(task) > 8000 {
			return Plan{}, false
		}
		plan.DM = &Direct{Agent: agent, Task: task}
	}
	if w.Chat != nil {
		task := strings.TrimSpace(w.Chat.Task)
		if task == "" || len(task) > 8000 || len(w.Chat.Agents) < 2 {
			return Plan{}, false
		}
		seen := map[string]bool{}
		var ids []string
		for _, id := range w.Chat.Agents {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				return Plan{}, false
			}
			seen[id] = true
			ids = append(ids, id)
		}
		plan.Chat = &Group{Agents: ids, Task: task}
	}
	return plan, true
}

func toWire(plan Plan) wire {
	var w wire
	if plan.DM != nil {
		w.DM = &wireDirect{Agent: plan.DM.Agent, Task: plan.DM.Task}
	}
	if plan.Chat != nil {
		w.Chat = &wireGroup{Agents: plan.Chat.Agents, Task: plan.Chat.Task}
	}
	return w
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
