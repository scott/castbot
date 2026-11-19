// Package routinetool is the skill and fence an agent uses to create and
// change its own routines. The runtime is not modified. The tools are
// listed on the agents MCP server. The control plane reads a
// castbot-routine fence after the turn when that image does not list them.
package routinetool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "routines"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/routines/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/routines"
	// ToolList lists this agent's routines.
	ToolList = "list_routines"
	// ToolCreate saves a routine for this agent.
	ToolCreate = "create_routine"
	// ToolUpdate changes one of this agent's routines, including pause and resume.
	ToolUpdate = "update_routine"
	// ToolDelete removes one of this agent's routines.
	ToolDelete = "delete_routine"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-routine"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Routines"
	// PlaceholderName is the sample name in the fence example.
	PlaceholderName = "NAME"
	// PlaceholderCron is the sample schedule in the fence example.
	PlaceholderCron = "CRON"
	// PlaceholderPrompt is the sample prompt in the fence example.
	PlaceholderPrompt = "PROMPT"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Create and change this agent's routines when the person in this chat asks."
)

const skillBody = `Call these tools only when the person in this chat asks you to do something on a schedule, or asks you to change or remove a routine.

Ask before you call a tool when the time, the days, or what you should do is unclear. Do not invent a schedule. Do not create a routine from the setup greeting "I can set that up as a routine for you" until the person agrees.

create_routine needs name, cron, and prompt. cron is 5 fields: minute hour day month weekday. Pass timezone, missed, and approval only when the person named them. timezone is an IANA name. missed is once or skip. approval is wait or deny.

list_routines before you update or delete, and use an id from that list. update_routine changes only the fields you pass. Set enabled to false to pause and true to resume. delete_routine removes one routine.

Do not pass user_id or agent_id. These tools change this agent only.

If the tools are not listed, end your reply with one castbot-routine fence and no other routine fence. Do not write that fence after the tools are listed.

` + "```castbot-routine\n" + `{"action":"create","name":"NAME","cron":"CRON","prompt":"PROMPT"}` + "\n```" + `

Replace NAME, CRON, and PROMPT with what they asked for. Use action update or delete with an id from list_routines when they asked to change or remove one. Add timezone, missed, or approval only when they named them. Add enabled only when they asked to pause or resume.`

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
