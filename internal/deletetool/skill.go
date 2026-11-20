// Package deletetool is the skill and fence an agent uses to delete itself
// after the person types its name. The runtime is not modified. The tool is
// listed on the agents MCP server. The control plane reads a castbot-delete
// fence after the turn when that image does not list it.
package deletetool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "delete"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/delete/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/delete"
	// ToolDelete removes this agent and its machine after the person types the name.
	ToolDelete = "delete_agent"
	// Fence is the markdown language tag used when the tool is absent.
	Fence = "castbot-delete"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Delete"
	// PlaceholderConfirm is the sample name a fence must not keep.
	PlaceholderConfirm = "NAME"
	// Warning is the machine panel sentence. The reply must include it.
	Warning = "Deleting removes this agent and its machine. This cannot be undone. Memory export is not in this version."
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Delete this agent after the person in this chat has read the warning and typed the agent's name."
)

const skillBody = `Call this tool only when the person in this chat asks to delete this agent.

delete_agent states what deletion does. It does not delete. Say that result to the person in the same words.

` + Warning + `

After that reply is already in the chat and the person types this agent's name, call delete_agent again with confirm set to that name. Do not pass confirm on the first call. A question is not the name. Do not delete from the question alone.

Do not pass user_id or agent_id.

If the tool is not listed, end your reply with one castbot-delete fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-delete\n" + `{"action":"delete_agent"}` + "\n```" + `

Add "confirm":"NAME" only after the person has typed the agent name. Do not leave NAME in place.`

// SkillMarkdown is the SKILL.md file, frontmatter plus body.
func SkillMarkdown() string {
	return "---\nname: " + SkillName + "\ndescription: " + SkillSummary + "\n---\n\n" + skillBody + "\n"
}

// Guide is the instructions section served with every bundle.
// The stored instructions file stays unchanged.
func Guide() string {
	return Heading + "\n\n" + skillBody + "\n"
}

// Instructions appends the guide when the heading is not already there.
func Instructions(base string) string {
	if strings.Contains(base, Heading) {
		return base
	}
	base = strings.TrimRight(base, "\n")
	if base == "" {
		return Guide()
	}
	return base + "\n\n" + Guide()
}

// BundleFiles is the skill file merged into a bundle.
func BundleFiles() map[string]string {
	return map[string]string{SkillFile: SkillMarkdown()}
}

// MergeExtras adds the skill file. That path wins over a stored copy.
func MergeExtras(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for name, body := range in {
		out[name] = body
	}
	for name, body := range BundleFiles() {
		out[name] = body
	}
	return out
}

// Arg is one tool argument the guest lists.
type Arg struct {
	Name        string
	Description string
	Required    bool
}

// Spec is the delete tool the guest lists.
type Spec struct {
	Name        string
	Description string
	Args        []Arg
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	confirm := Arg{Name: "confirm", Description: "The agent name, only after the person has typed it. Leave it out the first time.", Required: false}
	return []Spec{
		{Name: ToolDelete, Description: "Delete this agent and its machine after the person types the agent's name.", Args: []Arg{confirm}},
	}
}

// Known reports whether name is the delete tool.
func Known(name string) bool {
	return strings.TrimSpace(name) == ToolDelete
}

// HasMarker reports whether body already states the delete warning.
func HasMarker(body string) bool {
	return strings.Contains(body, Warning)
}
