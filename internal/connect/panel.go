package connect

import "strings"

const Shell = "shell"

// PanelTool is one tool a settings connection adds.
type PanelTool struct {
	Name  string
	Title string
	Write bool
}

// DefaultLevel is ask for a write tool and always-allow for a read tool.
func (t PanelTool) DefaultLevel() string {
	if t.Write {
		return "ask"
	}
	return "always-allow"
}

// Panel is one row on the settings connections page.
// Proxy panels are also catalog entries. Shell is not a proxy and is not
// accepted by request_secret or the egress allowlist.
type Panel struct {
	ID          string
	Title       string
	Blurb       string
	Body        string
	TokenLabel  string
	NeedsSecret bool
	Tools       []PanelTool
}

// Panels is the settings list. GitHub and DigitalOcean come first.
// Shell Actions is the builtin tool policy.
func Panels() []Panel {
	var out []Panel
	for _, entry := range Catalog() {
		var tools []PanelTool
		for _, tool := range Tools(entry.ID) {
			tools = append(tools, PanelTool{Name: tool.Name, Title: tool.Title, Write: tool.Write})
		}
		body := entry.Detail + " The token stays in your vault. The agent never sees the value. Connecting turns this on for every agent you own. A running agent reloads. No new machine."
		out = append(out, Panel{
			ID: entry.ID, Title: entry.Title, Blurb: entry.Detail, Body: body,
			TokenLabel: entry.TokenLabel, NeedsSecret: true, Tools: tools,
		})
	}
	out = append(out, shellPanel())
	return out
}

// PanelLookup returns a settings connection. Unknown ids are rejected.
func PanelLookup(id string) (Panel, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, panel := range Panels() {
		if panel.ID == id {
			return panel, true
		}
	}
	return Panel{}, false
}

// DefaultOn reports whether a missing settings row means the connection is on.
// Shell Actions starts on so an existing agent keeps its shell tools.
// GitHub and DigitalOcean start off.
func DefaultOn(id string) bool {
	return strings.ToLower(strings.TrimSpace(id)) == Shell
}

func shellPanel() Panel {
	return Panel{
		ID:    Shell,
		Title: "Shell Actions",
		Blurb: "Run commands, read files, and use git.",
		Body:  "Shell Actions lets an agent run commands, read files, and use git. Write tools ask before they run. Read tools are allowed until you change them. Deny keeps a tool listed and blocks it. This applies to every agent you own. A running agent reloads. No new machine.",
		Tools: []PanelTool{
			{Name: "bash", Title: "Run a shell command", Write: true},
			{Name: "read", Title: "Read a file"},
			{Name: "glob", Title: "Find files"},
			{Name: "git_status", Title: "Git status"},
			{Name: "git_commit", Title: "Commit", Write: true},
			{Name: "git_push", Title: "Push", Write: true},
			{Name: "git_log", Title: "Git log"},
			{Name: "git_diff", Title: "Git diff"},
			{Name: "git_add", Title: "Stage files", Write: true},
		},
	}
}
