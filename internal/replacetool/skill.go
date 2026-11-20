// Package replacetool is the skill and fence an agent uses to replace this
// machine or roll the runtime back. The runtime is not modified. The tools
// are listed on the agents MCP server. The control plane reads a
// castbot-replace fence after the turn when that image does not list them.
package replacetool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "replace"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/replace/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/replace"
	// ToolReplace starts a new machine on the current runtime after a yes.
	ToolReplace = "replace_machine"
	// ToolRollback starts a machine on the previous runtime after a yes.
	ToolRollback = "roll_back_runtime"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-replace"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Replace"
	// PlaceholderConfirm is the sample confirm a fence must not keep.
	PlaceholderConfirm = "YES"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Replace this machine or roll the runtime back after the person in this chat has seen what changes and replied yes."
	// KeptSettings is the first kept line in the replace dialog.
	KeptSettings = "The agent, its settings, instructions, model and keys"
	// KeptChats is the chats line in the replace dialog.
	KeptChats = "All chats, including Cast CLI threads"
	// KeptApprovals is the approvals line in the replace dialog.
	KeptApprovals = "Approvals and the audit log"
	// KeptMemory is the memory line in the replace dialog.
	KeptMemory = "Memory (copied)"
	// KeptWorkspace is the workspace line in the replace dialog.
	KeptWorkspace = "Workspace files (copied)"
	// LostPackages is the packages line in the replace dialog.
	LostPackages = "Files outside the workspace folder, such as installed packages"
	// LostResults is the past tool-result line in the replace dialog.
	LostResults = "Tool results inside past chats. The agent gets your recent chat back as plain text."
	// LostCLI is the Cast CLI line in the replace dialog.
	LostCLI = "The Cast CLI connection. Run Add to Cast CLI again afterwards."
	// LostLogs is the machine-log line in the replace dialog.
	LostLogs = "Machine logs"
	// ReplacePromise is the safety line in the replace dialog.
	ReplacePromise = "Safe if something goes wrong. If the old machine cannot be read, castbot stops and keeps it exactly as it is."
	// RollbackPromise is the safety line in the rollback dialog.
	RollbackPromise = "Safe if something goes wrong. If the current machine cannot be read, castbot stops and keeps it exactly as it is."
	// RollbackCopy is the copy sentence shared with the rollback dialog.
	RollbackCopy = "copies this agent's memory and workspace files"
)

const skillBody = `Call these tools only when the person in this chat asks to replace this machine or roll the runtime back.

replace_machine states what is kept and what is lost, in the same words as the confirm dialog. It does not start. Say that result to the person in the same words. Do not shorten the lists.

After that reply is already in the chat and the person replies yes, call replace_machine again with confirm set to yes. Do not pass confirm on the first call. A question is not a yes. Do not start from the question alone.

roll_back_runtime states what the rollback dialog says. It does not start. After that reply is already in the chat and the person replies yes, call it again with confirm set to yes.

Chat is paused until the new machine is done. Pause, wake, delete, and Add to Cast CLI stay off until it is done.

Do not pass user_id or agent_id.

If the tools are not listed, end your reply with one castbot-replace fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-replace\n" + `{"action":"replace_machine"}` + "\n```" + `

Use action roll_back_runtime when that is what they asked. Add "confirm":"yes" only after the person has replied yes. Do not leave YES in place.`

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

// Spec is one replace tool the guest lists.
type Spec struct {
	Name        string
	Description string
	Args        []Arg
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	confirm := Arg{Name: "confirm", Description: "yes only after the person has replied yes. Leave it out the first time.", Required: false}
	return []Spec{
		{Name: ToolReplace, Description: "State what a replace keeps and loses, then start it only after the person replies yes.", Args: []Arg{confirm}},
		{Name: ToolRollback, Description: "State what a runtime rollback does, then start it only after the person replies yes.", Args: []Arg{confirm}},
	}
}

// Known reports whether name is a replace tool.
func Known(name string) bool {
	switch strings.TrimSpace(name) {
	case ToolReplace, ToolRollback:
		return true
	default:
		return false
	}
}

// HasMarker reports whether body already states the dialog for action.
func HasMarker(body, action string) bool {
	if action == ToolRollback {
		return strings.Contains(body, "previous runtime") && strings.Contains(body, RollbackCopy)
	}
	return strings.Contains(body, KeptWorkspace) && strings.Contains(body, LostLogs)
}

// KeptLines is the replace dialog's kept list, in order.
func KeptLines() []string {
	return []string{KeptSettings, KeptChats, KeptApprovals, KeptMemory, KeptWorkspace}
}

// LostLines is the replace dialog's lost list, in order.
func LostLines() []string {
	return []string{LostPackages, LostResults, LostCLI, LostLogs}
}
