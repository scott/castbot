// Package machinetool is the skill and fence an agent uses to start, pause,
// wake, and refresh its own machine. The runtime is not modified. The tools
// are listed on the agents MCP server. The control plane reads a
// castbot-machine fence after the turn when that image does not list them.
package machinetool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "machine"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/machine/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/machine"
	// ToolPause pauses this agent's machine.
	ToolPause = "pause_machine"
	// ToolWake wakes this agent's paused machine.
	ToolWake = "wake_machine"
	// ToolStart starts a machine when this agent does not have one.
	ToolStart = "start_machine"
	// ToolRetry starts again after a failed start.
	ToolRetry = "retry_machine"
	// ToolRefresh checks the cloud API and does not wake the machine.
	ToolRefresh = "refresh_machine"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-machine"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## This machine"
	// PlaceholderAction is the sample action in the fence example.
	PlaceholderAction = "ACTION"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Start, pause, wake, or refresh this agent's machine when the person in this chat asks."
)

const skillBody = `Call these tools only when the person in this chat asks you to start, pause, wake, or refresh this machine.

pause_machine stops the machine until it is woken. A chat message wakes it too. wake_machine starts the paused machine. start_machine starts a machine when this agent does not have one. retry_machine is for a failed start. It starts a new machine. It does not replace a machine that is running or paused. refresh_machine checks the cloud API. It does not wake the machine.

These tools do not replace the machine, roll back the runtime, or delete the agent. While a replace is in progress, pause, wake, start, retry, and refresh stay off.

Do not pass user_id or agent_id. The call always uses this agent.

If the tools are not listed, end your reply with one castbot-machine fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-machine\n" + `{"action":"ACTION"}` + "\n```" + `

Replace ACTION with pause_machine, wake_machine, start_machine, retry_machine, or refresh_machine. Do not leave ACTION in place.`

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

// Spec is one machine tool the guest lists.
type Spec struct {
	Name        string
	Description string
}

// Specs is the stable tool list, in the order the guest lists them.
func Specs() []Spec {
	return []Spec{
		{Name: ToolPause, Description: "Pause this agent's machine until it is woken. A chat message wakes it too."},
		{Name: ToolWake, Description: "Wake this agent's paused machine so it can work."},
		{Name: ToolStart, Description: "Start a machine for this agent when it does not have one."},
		{Name: ToolRetry, Description: "Try again after a failed start. Does not replace a machine that is running or paused."},
		{Name: ToolRefresh, Description: "Check this machine with the cloud API. This does not wake it."},
	}
}

// Known reports whether name is one of the machine tools.
func Known(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// Lookup returns the spec for a tool name.
func Lookup(name string) (Spec, bool) {
	name = strings.TrimSpace(name)
	for _, spec := range Specs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}
