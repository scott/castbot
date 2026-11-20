// Package approvetool is the skill and fence an agent uses to approve, deny,
// or always-approve one waiting card. The runtime is not modified. The tools
// are listed on the agents MCP server. The control plane reads a
// castbot-approval fence after the turn when that image does not list them.
package approvetool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "approvals"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/approvals/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/approvals"
	// ToolApprove approves one waiting card.
	ToolApprove = "approve_card"
	// ToolDeny denies one waiting card.
	ToolDeny = "deny_card"
	// ToolAlways approves one waiting card for the rest of that prompt.
	ToolAlways = "always_approve_card"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-approval"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Waiting cards"
	// PlaceholderCard is the sample card in the fence example.
	PlaceholderCard = "CARD"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Approve, deny, or always-approve one waiting card when the person in this chat asks."
)

const skillBody = `Call approve_card, deny_card, or always_approve_card only when the person in this chat asks you to decide one waiting card. Do not decide a card just because it is waiting. A question about what is waiting uses approval_history and does not decide anything.

Pass card as the tool name, the words on the card, or the card id. Leave card out only when one card is waiting. If more than one card matches, do not guess. Ask which one.

always_approve_card is the Always approve button. It covers the rest of that prompt. It does not change the saved tool level. approve_card allows one card. deny_card refuses one card.

A card that says to answer it in the Cast CLI stays there. A card that asks the person to type a value stays with the person. Do not pass the value. Do not pass user_id or agent_id. The call always uses this agent.

If the tools are not listed, end your reply with one castbot-approval fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-approval\n" + `{"action":"approve_card","card":"CARD"}` + "\n```" + `

Replace CARD with the card they named, or leave card out when only one is waiting. Use action deny_card or always_approve_card when that is what they asked. Do not leave CARD in place.`

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

// Spec is one approval tool the guest lists.
type Spec struct {
	Name        string
	Description string
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	return []Spec{
		{Name: ToolApprove, Description: "Approve one waiting card. Pass card when more than one is waiting. Call only when the person asked to approve it."},
		{Name: ToolDeny, Description: "Deny one waiting card. Pass card when more than one is waiting. Call only when the person asked to deny it."},
		{Name: ToolAlways, Description: "Always-approve one waiting card for the rest of that prompt. It does not change the saved tool level. Call only when the person asked."},
	}
}

// Known reports whether name is an approval decision tool.
func Known(name string) bool {
	switch strings.TrimSpace(name) {
	case ToolApprove, ToolDeny, ToolAlways:
		return true
	default:
		return false
	}
}
