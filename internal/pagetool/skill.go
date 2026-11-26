// Package pagetool is the skill and fence an agent uses to build a hosted
// page. The runtime is not modified. The tools are listed on the agents MCP
// server. The control plane reads a castbot-page fence after the turn when
// that image does not list them.
package pagetool

import (
	"strings"

	"github.com/scott/castbot/internal/page"
)

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "pages"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/pages/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/pages"
	// ToolCreate saves a new private page.
	ToolCreate = "create_page"
	// ToolUpdate replaces the document of a page this agent created.
	ToolUpdate = "update_page"
	// ToolRename changes the title and leaves the link alone.
	ToolRename = "rename_page"
	// ToolDelete soft-deletes a page this agent created.
	ToolDelete = "delete_page"
	// ToolList lists this agent's pages and pages shared with this agent.
	ToolList = "list_pages"
	// ToolRead returns one page, including the document.
	ToolRead = "read_page"
	// ToolShare changes who can read a page this agent created.
	ToolShare = "share_page"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-page"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Pages"
	// PlaceholderTitle is the sample title a fence must not keep.
	PlaceholderTitle = "TITLE"
	// PlaceholderDocument is the sample document a fence must not keep.
	PlaceholderDocument = "DOCUMENT"
	// PlaceholderPage is the sample page id a fence must not keep.
	PlaceholderPage = "PAGE"
	// ReadCap is the largest read_page reply egress will forward.
	// Other agent tools stay at 8000 bytes.
	ReadCap = 40 << 10
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Build a hosted page when the person asks for a report, a dashboard, a tracker, or an explainer."
)

func skillBodyText() string {
	return `Call these tools only when the person asks for a page, a report, a dashboard, a tracker, or an explainer.

create_page saves one new private page the person can open. Pass title and document. A successful call keeps the page. Save the page they asked for. If the call fails, nothing was saved: fix the document and call create_page again. If a page was already saved, change it with update_page instead of creating another.

` + page.Shape() + `

update_page replaces the document of a page this agent created. Pass page as the id or the token, and document. Pass revision from read_page when you have it. If the page changed, read it and try again.

rename_page changes the title. Pass page and title. The link does not change.

delete_page removes a page this agent created. Pass page.

list_pages lists this agent's pages and pages shared with this agent. It does not return the document.

read_page returns the document. Pass page.

share_page changes who can read a page this agent created. Pass page and visibility: only_you, workspace, invited, or link. invited also takes people and agents in this workspace, by name or id. Share only when the person says who should see it. A group chat does not share the page on its own.

Update the same page when the person asks to change it. Do not pass user_id or agent_id.

If the tools are not listed, end your reply with one castbot-page fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-page\n" + `{"action":"create_page","title":"TITLE","document":"DOCUMENT"}` + "\n```" + `

Replace TITLE and DOCUMENT. Use action update_page, rename_page, delete_page, list_pages, read_page, or share_page when that is what they asked. Those actions take page, except list_pages. Do not leave TITLE, DOCUMENT, or PAGE in place.`
}

// SkillMarkdown is the SKILL.md file, frontmatter plus body.
func SkillMarkdown() string {
	return "---\nname: " + SkillName + "\ndescription: " + SkillSummary + "\n---\n\n" + skillBodyText() + "\n"
}

// Guide is the instructions section served with every bundle.
// The stored instructions file stays unchanged.
func Guide() string {
	return Heading + "\n\n" + skillBodyText() + "\n"
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

// Spec is one page tool the guest lists.
type Spec struct {
	Name        string
	Description string
}

// Specs is the tool list, in the order the guest lists it.
func Specs() []Spec {
	return []Spec{
		{Name: ToolCreate, Description: "Save one new private page. Pass title and document. A successful call keeps the page. The document shape is on the document argument."},
		{Name: ToolUpdate, Description: "Replace the document of a page this agent created. Pass page and document, and revision when you have it."},
		{Name: ToolRename, Description: "Change the title of a page this agent created. The link stays the same."},
		{Name: ToolDelete, Description: "Remove a page this agent created."},
		{Name: ToolList, Description: "List this agent's pages and pages shared with this agent. Does not return the document."},
		{Name: ToolRead, Description: "Read one page, including the document. Pass page as an id or a token."},
		{Name: ToolShare, Description: "Change who can read a page this agent created. visibility is only_you, workspace, invited, or link."},
	}
}

// Known reports whether name is a page tool.
func Known(name string) bool {
	switch strings.TrimSpace(name) {
	case ToolCreate, ToolUpdate, ToolRename, ToolDelete, ToolList, ToolRead, ToolShare:
		return true
	default:
		return false
	}
}
