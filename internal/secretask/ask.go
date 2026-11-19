// Package secretask is how an agent asks for a named API key and how it
// asks to delete one. The runtime is not modified. The control plane
// serves this section and reads a castbot-secret fence after the turn
// when the guest tool cannot take a name.
package secretask

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "api-keys"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/api-keys/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/api-keys"
	// ToolSave is the MCP tool that opens a masked card.
	ToolSave = "request_secret"
	// ToolDelete is the MCP tool that opens an approval card.
	ToolDelete = "delete_secret"
	// Fence is the markdown language tag used when the tool cannot take a name.
	Fence = "castbot-secret"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## API keys"
	// ShimFile is the command file for request_secret. It has no credential.
	ShimFile = "mcp/request_secret.yaml"
	// SampleName is the placeholder in the fence example.
	SampleName = "SERVICE_API_KEY"
	// SampleReason is the placeholder reason in the fence example.
	SampleReason = "Why they asked"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Ask for an API key in the chat, and ask before deleting one, when the person asks to connect or disconnect a service."

	shimCommand = "/usr/local/bin/castbot-mcp"
)

const skillBody = `When the person asks to connect an API or to save any secret other than the GitHub or DigitalOcean connections, do not ask them to paste the key in the chat.

If request_secret accepts a name argument, call it with name set to an environment variable such as SERVICE_API_KEY, a short reason, and a label only when a short title helps. Leave connection empty. The result is saved, declined, expired, already_configured, or rejected. The key is available on the next turn after saved.

If request_secret is missing, or it only accepts github and digitalocean, end your reply with one castbot-secret fence and no other secret fence.

` + "```castbot-secret\n" + `{"action":"save","name":"SERVICE_API_KEY","label":"Service API","reason":"Why they asked"}` + "\n```" + `

Replace the name, label, and reason. The name uses capital letters, digits, and underscores, and it starts with a letter. Do not invent a key. Do not copy a key into the fence. Do not write that fence after request_secret has returned.

When they ask to disconnect or delete that key, call delete_secret with the same name and a reason. The result is deleted, declined, expired, or not_found. If delete_secret is not listed, end the reply with one fence:

` + "```castbot-secret\n" + `{"action":"delete","name":"SERVICE_API_KEY","reason":"Why they asked"}` + "\n```" + `

Do not write that fence after delete_secret has returned. Do not delete a key they did not ask you to remove.`

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
	return "command: " + shimCommand + "\nargs:\n  - " + ToolSave + "\n"
}

// BundleFiles is the skill file merged into a bundle.
func BundleFiles() map[string]string {
	return map[string]string{SkillFile: SkillMarkdown()}
}

// MergeExtras adds the skill file and the request_secret command file.
// Those paths win over a stored copy so a saved bundle cannot replace them.
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
