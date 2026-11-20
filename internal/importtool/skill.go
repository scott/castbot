// Package importtool is the skill and fence an agent uses to import a Cast
// agent folder. The runtime is not modified. The tool is listed on the agents
// MCP server. The control plane reads a castbot-import fence after the turn
// when that image does not list it.
package importtool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "import"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/import/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/import"
	// ToolImport reads a zip, a tar, or a public repository URL into this agent.
	ToolImport = "import_agent"
	// Fence is the markdown language tag used when the tool is absent.
	Fence = "castbot-import"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Import"
	// PlaceholderURL is the sample URL a fence must not keep.
	PlaceholderURL = "URL"
	// PlaceholderArchive is the sample archive a fence must not keep.
	PlaceholderArchive = "ARCHIVE"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Import a Cast agent folder from a zip, a tar, or a public repository URL when the person in this chat asks."
)

const skillBody = `Call this tool only when the person in this chat asks to import a Cast agent folder into this agent.

Pass a zip or tar as archive_b64, or a public https github.com URL as url. Private repositories are out of scope. The call uses the same review as the import form. Files such as .env are skipped. Secret values are removed. Say the skipped files and the removed secret values in the same words. Do not repeat a secret value.

Do not pass user_id or agent_id.

If the tool is not listed, end your reply with one castbot-import fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-import\n" + `{"action":"import_agent"}` + "\n```" + `

Add "url" or "archive_b64" only when the person has given a folder. Do not leave URL or ARCHIVE in place.`

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

// Spec is the import tool the guest lists.
type Spec struct {
	Name        string
	Description string
	Args        []Arg
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	url := Arg{Name: "url", Description: "Public https github.com URL. Leave it out when archive_b64 is set.", Required: false}
	archive := Arg{Name: "archive_b64", Description: "Zip or tar, base64. Leave it out when url is set.", Required: false}
	return []Spec{
		{Name: ToolImport, Description: "Import a Cast agent folder from a zip, a tar, or a public repository URL into this agent.", Args: []Arg{url, archive}},
	}
}

// Known reports whether name is the import tool.
func Known(name string) bool {
	return strings.TrimSpace(name) == ToolImport
}
