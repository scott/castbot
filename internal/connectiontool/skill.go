// Package connectiontool is the skill and fence an agent uses to disconnect
// a connection or set a tool to ask, always allow, or deny. The runtime is
// not modified. The tools are listed on the agents MCP server. The control
// plane reads a castbot-connection fence after the turn when that image
// does not list them.
package connectiontool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "connections"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/connections/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/connections"
	// ToolDisconnect turns one connection off for every agent this person owns.
	ToolDisconnect = "disconnect_connection"
	// ToolLevel sets one tool to ask, always allow, or deny.
	ToolLevel = "set_tool_level"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-connection"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Connections"
	// PlaceholderConnection is the sample connection in the fence example.
	PlaceholderConnection = "CONNECTION"
	// PlaceholderTool is the sample tool a fence must not keep.
	PlaceholderTool = "TOOL"
	// PlaceholderLevel is the sample level a fence must not keep.
	PlaceholderLevel = "LEVEL"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Disconnect a connection or set a tool to ask, always allow, or deny when the person in this chat asks."
)

const skillBody = `Call these tools only when the person in this chat asks.

disconnect_connection turns one connection off for every agent this person owns. Pass connection as the title or the id from list_connections. Tool choices stay. A running agent reloads. No new machine.

set_tool_level sets one tool to ask, always allow, or deny. Pass connection, tool, and level. Ask waits before the tool runs. Always allow runs it without asking. Deny keeps it listed and blocks it. The choice applies to every agent this person owns. A running agent reloads. No new machine.

The token and the SSH private key stay in the vault. Do not pass them. Do not ask the person to paste them here. Do not pass user_id or agent_id.

If the tools are not listed, end your reply with one castbot-connection fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-connection\n" + `{"action":"set_tool_level","connection":"CONNECTION","tool":"TOOL","level":"LEVEL"}` + "\n```" + `

Replace CONNECTION, TOOL, and LEVEL. Use action disconnect_connection with connection when they asked to disconnect. Do not leave CONNECTION, TOOL, or LEVEL in place.`

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

// Spec is one connection tool the guest lists.
type Spec struct {
	Name        string
	Description string
	Args        []Arg
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	return []Spec{
		{Name: ToolDisconnect, Description: "Turn one connection off for every agent this person owns. Tool choices stay.", Args: []Arg{{Name: "connection", Description: "Connection title or id from list_connections.", Required: true}}},
		{Name: ToolLevel, Description: "Set one tool to ask, always allow, or deny for every agent this person owns.", Args: []Arg{
			{Name: "connection", Description: "Connection title or id from list_connections.", Required: true},
			{Name: "tool", Description: "Tool name or title on that connection.", Required: true},
			{Name: "level", Description: "ask, always allow, or deny.", Required: true},
		}},
	}
}

// Known reports whether name is a connection tool.
func Known(name string) bool {
	switch strings.TrimSpace(name) {
	case ToolDisconnect, ToolLevel:
		return true
	default:
		return false
	}
}
