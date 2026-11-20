// Package settingstool is the skill and fence an agent uses to change the
// display name and the theme. The runtime is not modified. The tools are
// listed on the agents MCP server. The control plane reads a castbot-settings
// fence after the turn when that image does not list them.
package settingstool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "settings"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/settings/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/settings"
	// ToolName changes the name shown in Settings.
	ToolName = "update_name"
	// ToolTheme sets dark, light, or system.
	ToolTheme = "set_theme"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-settings"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Settings"
	// PlaceholderName is the sample name in the fence example.
	PlaceholderName = "NAME"
	// PlaceholderTheme is the sample theme a fence must not keep.
	PlaceholderTheme = "THEME"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Change the display name or the theme when the person in this chat asks."
)

const skillBody = `Call these tools only when the person in this chat asks.

update_name changes the name in Settings. Pass name, up to 80 characters.

set_theme saves dark, light, or system on the account. System follows the device setting.

Password, email, two-factor, recovery codes, sign out, and invites stay in Settings. Do not ask for them. Do not pass user_id or agent_id.

If the tools are not listed, end your reply with one castbot-settings fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-settings\n" + `{"action":"update_name","name":"NAME"}` + "\n```" + `

Replace NAME with the name they asked for. Use action set_theme with theme dark, light, or system. Do not leave NAME or THEME in place.`

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

// Spec is one settings tool the guest lists.
type Spec struct {
	Name        string
	Description string
	Args        []Arg
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	return []Spec{
		{Name: ToolName, Description: "Change the name in Settings. Up to 80 characters.", Args: []Arg{{Name: "name", Description: "Name shown in Settings, up to 80 characters.", Required: true}}},
		{Name: ToolTheme, Description: "Set the theme to dark, light, or system.", Args: []Arg{{Name: "theme", Description: "dark, light, or system.", Required: true}}},
	}
}

// Known reports whether name is a settings tool.
func Known(name string) bool {
	switch strings.TrimSpace(name) {
	case ToolName, ToolTheme:
		return true
	default:
		return false
	}
}
