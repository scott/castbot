// Package secretstool is the skill and fence an agent uses to grant or
// revoke a secret, move a connection token into the vault, or delete a
// secret when the person asks. The runtime is not modified. The tools are
// listed on the agents MCP server. The control plane reads a
// castbot-secrets fence after the turn when that image does not list them.
package secretstool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "secrets"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/secrets/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/secrets"
	// ToolGrant gives one existing secret to one agent this person owns.
	ToolGrant = "grant_secret"
	// ToolRevoke takes one secret away from one agent. The secret stays in the vault.
	ToolRevoke = "revoke_secret"
	// ToolMove moves a connection token into the connection vault.
	ToolMove = "move_connection_secret"
	// ToolDelete removes one secret when the person asked to delete it.
	ToolDelete = "delete_vault_secret"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-secrets"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Secrets"
	// PlaceholderSecret is the sample secret a fence must not keep.
	PlaceholderSecret = "SECRET"
	// PlaceholderAgent is the sample agent a fence must not keep.
	PlaceholderAgent = "AGENT"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Grant or revoke a secret, move a connection token into the vault, or delete a secret when the person in this chat asks."
)

const skillBody = `Call these tools only when the person in this chat asks.

To add a secret or replace its value, call request_secret. The person types the value on the card. Do not pass the value. Do not ask the person to paste it here.

grant_secret gives one existing secret to one agent this person owns. Pass secret as the name or id from list_secret_names, and agent as the name or id from list_agents. The agent reloads. No new machine.

revoke_secret takes one secret away from one agent. The secret stays in the vault. The agent reloads. No new machine.

move_connection_secret moves one saved connection token into the connection vault. Pass secret as the name from list_secret_names. The value stays in the vault. The agent does not see it.

delete_vault_secret removes one secret only when the person asked to delete that secret. The reply names the agents that lose it. A model key those agents still use is refused. Do not pass the value.

Do not pass user_id or agent_id.

If the tools are not listed, end your reply with one castbot-secrets fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-secrets\n" + `{"action":"grant_secret","secret":"SECRET","agent":"AGENT"}` + "\n```" + `

Replace SECRET and AGENT. Use action revoke_secret, move_connection_secret, or delete_vault_secret when that is what they asked. Do not leave SECRET or AGENT in place.`

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

// Spec is one secret tool the guest lists.
type Spec struct {
	Name        string
	Description string
	Args        []Arg
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	secret := Arg{Name: "secret", Description: "Secret name or id from list_secret_names.", Required: true}
	agent := Arg{Name: "agent", Description: "Agent name or id from list_agents.", Required: true}
	return []Spec{
		{Name: ToolGrant, Description: "Give one existing secret to one agent this person owns. The value stays in the vault.", Args: []Arg{secret, agent}},
		{Name: ToolRevoke, Description: "Take one secret away from one agent. The secret stays in the vault.", Args: []Arg{secret, agent}},
		{Name: ToolMove, Description: "Move one saved connection token into the connection vault. The agent does not see the value.", Args: []Arg{secret}},
		{Name: ToolDelete, Description: "Delete one secret when the person asked, and say which agents lose it.", Args: []Arg{secret}},
	}
}

// Known reports whether name is a secret tool.
func Known(name string) bool {
	switch strings.TrimSpace(name) {
	case ToolGrant, ToolRevoke, ToolMove, ToolDelete:
		return true
	default:
		return false
	}
}
