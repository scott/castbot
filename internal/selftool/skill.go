// Package selftool is the skill and fence an agent uses to read its own
// account. The runtime is not modified. The tools are listed on the agents
// MCP server. The control plane reads a castbot-account fence after the
// turn when that image does not list them.
package selftool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "account"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/account/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/account"
	// ToolDescribe says who this agent is.
	ToolDescribe = "describe_me"
	// ToolUsage is this month's machine estimate for this agent and the account.
	ToolUsage = "account_usage"
	// ToolMachine is the machine panel, including history.
	ToolMachine = "machine_status"
	// ToolApprovals lists waiting and earlier approvals. Query is optional.
	ToolApprovals = "approval_history"
	// ToolRuns lists routine runs. ID is optional.
	ToolRuns = "routine_runs"
	// ToolSecrets lists secret names and labels. Values are not included.
	ToolSecrets = "list_secret_names"
	// ToolFiles lists the workspace cache and uploads.
	ToolFiles = "list_files"
	// ToolConnections lists connection status and tool levels.
	ToolConnections = "list_connections"
	// ToolCLI lists Cast CLI token prefixes and status. The token is not included.
	ToolCLI = "list_cli_tokens"
	// ToolAccount is the account around this agent.
	ToolAccount = "account_overview"
	// ToolSearch matches agent names and file names. Query is required.
	ToolSearch = "search_account"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-account"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## This account"
	// PlaceholderTool is the sample tool name in the fence example.
	PlaceholderTool = "TOOL"
	// PlaceholderQuery is the sample search text in the fence example.
	PlaceholderQuery = "QUERY"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Answer questions about this agent, its machine, and this castbot account when the person asks."
)

const skillBody = `Call these tools only when the person in this chat asks about this agent, its machine, or this castbot account.

These tools only read. They do not pause, wake, replace, edit, delete, approve, or change a secret, a connection, a routine, or a setting. Do not invent a change. Do not pass user_id or agent_id.

describe_me says who this agent is. account_usage is this month's machine estimate. machine_status includes the machine history. approval_history lists what is waiting and what was decided. Pass query only when they asked to look for a word. routine_runs lists recent runs. Pass id from list_routines when they named one routine. list_secret_names lists names and labels. Values stay in the vault and are not shown. list_files lists the workspace cache and uploads. It does not wake a paused machine. list_connections lists each connection and tool level. A connection token stays in the vault. list_cli_tokens lists prefixes and status. The token itself is not included. account_overview is the account around this agent. search_account needs query.

Password, email, two-factor, and sign-out stay on the settings screen.

If the tools are not listed, end your reply with one castbot-account fence and no other account fence. Do not write that fence after the tools are listed.

` + "```castbot-account\n" + `{"tool":"TOOL"}` + "\n```" + `

Replace TOOL with the tool name. Add query or id only when that tool uses them. Do not leave TOOL in place.`

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

// Spec is one read tool the guest lists.
type Spec struct {
	Name          string
	Description   string
	Query         bool
	QueryRequired bool
	ID            bool
}

// Specs is the stable tool list, in the order the guest lists them.
func Specs() []Spec {
	return []Spec{
		{Name: ToolDescribe, Description: "Describe this agent: name, job, model, status, and where it sits in the sidebar."},
		{Name: ToolUsage, Description: "This month's machine estimate for this agent and the account. An estimate, not a bill."},
		{Name: ToolMachine, Description: "Machine status, image, and the full machine history."},
		{Name: ToolApprovals, Description: "Approvals waiting for this agent, and earlier decisions. Pass query only when the person asked to look for a word.", Query: true},
		{Name: ToolRuns, Description: "Recent routine runs for this agent. Pass id from list_routines when the person named one routine.", ID: true},
		{Name: ToolSecrets, Description: "Secret names and labels granted to this agent. Values are not included."},
		{Name: ToolFiles, Description: "Workspace files and uploads for this agent. Does not wake a paused machine."},
		{Name: ToolConnections, Description: "Whether each connection is on, and each tool's level. The token is not included."},
		{Name: ToolCLI, Description: "Cast CLI token prefixes and status for this agent. The token is not included."},
		{Name: ToolAccount, Description: "The account around this agent: people, appearance, sections, and groups."},
		{Name: ToolSearch, Description: "Search agent names and file names in this account.", Query: true, QueryRequired: true},
	}
}

// Known reports whether name is one of the read tools.
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
