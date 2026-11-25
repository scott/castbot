// Package chattool is the skill and fence an agent uses to stop a reply,
// retry a failed run, and open a thread. The runtime is not modified. The
// tools are listed on the agents MCP server. The control plane reads a
// castbot-chat fence after the turn when that image does not list them.
package chattool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "chat"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/chat/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/chat"
	// ToolStop stops the reply in progress.
	ToolStop = "stop_reply"
	// ToolRetry tries a failed reply again.
	ToolRetry = "retry_run"
	// ToolList lists this agent's threads, including routine runs.
	ToolList = "list_threads"
	// ToolOpen opens the thread the person names, only from Chat.
	ToolOpen = "open_thread"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-chat"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Threads"
	// PlaceholderThread is the sample thread in the fence example.
	PlaceholderThread = "THREAD"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Stop a reply, retry a failed run, or open a thread from Chat when the person asks."
)

const skillBody = `Call these tools only when the person in this chat asks.

stop_reply stops the reply in progress, the same as the Stop button. Pass thread when more than one reply is in progress. Leave it out when only one is running. A Cast CLI thread has no Stop.

retry_run tries a failed reply again. Pass thread when more than one thread has a failed reply. When that thread has more than one failed reply, the latest one is retried.

list_threads lists this agent's threads, including routine runs. open_thread returns the recent turns of the one they name, and only while this turn is Chat. A group or a routine cannot open another thread. Pass thread as the title or the id. Chat is the direct chat with the owner. A routine run uses the routine's name.

Do not pass user_id or agent_id. These tools use this agent only.

If the tools are not listed, end your reply with one castbot-chat fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-chat\n" + `{"action":"open_thread","thread":"THREAD"}` + "\n```" + `

Replace THREAD with the thread they named. Use action stop_reply, retry_run, or list_threads when that is what they asked. stop_reply and retry_run may add thread. Do not leave THREAD in place.`

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

// Spec is one chat tool the guest lists.
type Spec struct {
	Name        string
	Description string
	Thread      bool
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	return []Spec{
		{Name: ToolStop, Description: "Stop the reply in progress, the same as the Stop button. Pass thread when more than one reply is in progress.", Thread: true},
		{Name: ToolRetry, Description: "Try a failed reply again. Pass thread when more than one thread has a failed reply.", Thread: true},
		{Name: ToolList, Description: "List this agent's threads, including routine runs."},
		{Name: ToolOpen, Description: "Open the thread the person named, only while this turn is Chat. Pass thread as the title or the id.", Thread: true},
	}
}

// Known reports whether name is a chat tool.
func Known(name string) bool {
	switch strings.TrimSpace(name) {
	case ToolStop, ToolRetry, ToolList, ToolOpen:
		return true
	default:
		return false
	}
}
