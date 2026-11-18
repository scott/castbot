// Package agentskill is the skill and tool every agent uses to create
// another agent from a description. The runtime is not modified. The
// control plane serves the files and reads a castbot-create fence after
// the turn when the tool is not available.
package agentskill

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "create-agent"
	// SkillFile is the only extra skill path a bundle may carry.
	SkillFile = "skills/create-agent/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/create-agent"
	// ServerName is the MCP server that exposes the tools.
	ServerName = "agents"
	// ShimFile is the command file for that server. It has no credential.
	ShimFile = "mcp/agents.yaml"
	// ToolCreate saves a new agent for the signed-in account.
	ToolCreate = "create_agent"
	// ToolList lists that account's agents.
	ToolList = "list_agents"
	// Fence is the markdown language tag used when the tool is absent.
	Fence = "castbot-create"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Creating an agent"
	// Placeholder is the sample word in the fence example. It is not a description.
	Placeholder = "DESCRIPTION"
	// MaxRunes is the longest description that can be saved.
	MaxRunes = 4000
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Create a new agent in this account when the person in this chat asks for one and describes it."

	shimCommand = "/usr/local/bin/castbot-mcp"
)

const skillBody = `Call create_agent when the person in this chat explicitly asks you to create a new agent and describes what it should do.

Pass what they asked for as description. Pass name only when they asked for a specific name.

Do not call the tool when they did not ask. Do not invent a description. If they did not say what the new agent should do, ask them before you call the tool.

If the tool says the name is already used, list the agents and pick a free name, or ask the person.

If create_agent is not available, end your reply with one castbot-create fence and no other fence.

` + "```castbot-create\n" + `{"description":"DESCRIPTION"}` + "\n```" + `

Replace DESCRIPTION with what they asked for. Add a name field only when they asked for a specific name. Do not write that fence after create_agent has returned.`

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

// ShimYAML is the MCP command file. It names the guest binary and no credential.
func ShimYAML() string {
	return "command: " + shimCommand + "\nargs:\n  - " + ServerName + "\n"
}

// BundleFiles is the skill file merged into a bundle. The shim is separate.
func BundleFiles() map[string]string {
	return map[string]string{SkillFile: SkillMarkdown()}
}

// MergeExtras adds the skill file and the shim. Those two paths win over
// a stored copy so a saved bundle cannot replace them.
func MergeExtras(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+2)
	for name, body := range in {
		out[name] = body
	}
	for name, body := range BundleFiles() {
		out[name] = body
	}
	out[ShimFile] = ShimYAML()
	return out
}

// UsableDescription reports whether text can be saved as a new agent's description.
// A greeting, the sample word DESCRIPTION, an empty value, and a text over
// the cap are refused.
func UsableDescription(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" || t == Placeholder || len([]rune(t)) > MaxRunes {
		return false
	}
	folded := strings.Trim(strings.ToLower(t), ".!?")
	switch folded {
	case "", "hi", "hello", "hey", "ok", "okay", "thanks", "thank you", "yo", "sup", "help", "test":
		return false
	}
	return len([]rune(folded)) >= 8
}
