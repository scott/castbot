// Package edittool is the skill and fence an agent uses to change its own
// name, title, job, instructions, color, model, or delegate switch. The
// runtime is not modified. The tool is listed on the agents MCP server. The
// control plane reads a castbot-edit fence after the turn when that image
// does not list the tool.
package edittool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "edit"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/edit/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/edit"
	// ToolUpdate changes the fields the person asked to change on this agent.
	ToolUpdate = "update_agent"
	// Fence is the markdown language tag used when the tool is absent.
	Fence = "castbot-edit"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## This agent"
	// PlaceholderName is the sample name in the fence example.
	PlaceholderName = "NAME"
	// PlaceholderTitle is the sample title in the fence example.
	PlaceholderTitle = "TITLE"
	// PlaceholderJob is the sample job in the fence example.
	PlaceholderJob = "JOB"
	// PlaceholderColor is the sample color in the fence example.
	PlaceholderColor = "COLOR"
	// PlaceholderModel is the sample model in the fence example.
	PlaceholderModel = "MODEL"
	// PlaceholderNotes is the sample instructions in the fence example.
	PlaceholderNotes = "INSTRUCTIONS"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Change this agent's name, title, job, instructions, color, model, or delegate switch when the person in this chat asks."
)

const skillBody = `Call update_agent only when the person in this chat asks you to change this agent. Pass only the fields they named. Leave the others out.

name is the sidebar name, up to 40 characters. title is up to 80 characters. job is what other agents see, up to 160 characters. instructions replace instructions.md. color is one of Coral, Mint, Violet, Gold, Sky, Rose, Lime, Sand, or Aqua. model is a model id from the provider list. delegate is true or false. True lets this agent message the other agents on this account.

Do not pass a section. move_agents puts this agent in a sidebar section. Do not pass an avatar file. The person picks the picture on the edit screen. PNG, JPEG, GIF, or WebP, up to 512 KB. Do not pass user_id or agent_id. The call always uses this agent. Do not put a key or a token in the job or the instructions.

If the tool is not listed, end your reply with one castbot-edit fence and no other fence of that type. Do not write that fence after the tool is listed.

` + "```castbot-edit\n" + `{"action":"update_agent","name":"NAME"}` + "\n```" + `

Replace NAME with the name they asked for, or leave name out and set title, job, instructions, color, model, or delegate. Do not leave NAME, TITLE, JOB, COLOR, MODEL, or INSTRUCTIONS in place.`

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

// Spec is the edit tool the guest lists.
type Spec struct {
	Name        string
	Description string
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	return []Spec{{
		Name:        ToolUpdate,
		Description: "Change this agent's name, title, job, instructions, color, model, or delegate switch. Pass only the fields the person asked to change.",
	}}
}

// Known reports whether name is the edit tool.
func Known(name string) bool {
	return strings.TrimSpace(name) == ToolUpdate
}
