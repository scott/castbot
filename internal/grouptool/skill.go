// Package grouptool is the skill and fence an agent uses to change groups
// and sidebar sections. The runtime is not modified. The tools are listed
// on the agents MCP server. The control plane reads a castbot-organize
// fence after the turn when that image does not list them.
package grouptool

import "strings"

const (
	// SkillName is the skill the runtime loads from the agent directory.
	SkillName = "groups"
	// SkillFile is the skill path merged into a served bundle.
	SkillFile = "skills/groups/SKILL.md"
	// SkillDir is the directory named by the skill path in agent.yaml.
	SkillDir = "skills/groups"
	// ToolCreateGroup saves a group for this person.
	ToolCreateGroup = "create_group"
	// ToolUpdateGroup renames a group this person created.
	ToolUpdateGroup = "update_group"
	// ToolAddMembers adds agents or people to a group.
	ToolAddMembers = "add_group_members"
	// ToolRemoveMembers removes agents or people from a group.
	ToolRemoveMembers = "remove_group_members"
	// ToolDeleteGroup removes a group this person created.
	ToolDeleteGroup = "delete_group"
	// ToolPinGroup pins a group for this person.
	ToolPinGroup = "pin_group"
	// ToolUnpinGroup unpins a group for this person.
	ToolUnpinGroup = "unpin_group"
	// ToolCreateSection adds a sidebar section.
	ToolCreateSection = "create_section"
	// ToolRenameSection renames a section this person added.
	ToolRenameSection = "rename_section"
	// ToolDeleteSection removes a section this person added.
	ToolDeleteSection = "delete_section"
	// ToolMoveAgents moves agents into a section, or back to Agents.
	ToolMoveAgents = "move_agents"
	// ToolPlaceGroup moves a group into a sidebar section, or back to Groups.
	ToolPlaceGroup = "place_group"
	// Fence is the markdown language tag used when the tools are absent.
	Fence = "castbot-organize"
	// Heading marks the served instructions section. Stored instructions omit it.
	Heading = "## Groups and sections"
	// PlaceholderTitle is the sample title in the fence example.
	PlaceholderTitle = "TITLE"
	// PlaceholderName is the sample name in the fence example.
	PlaceholderName = "NAME"
	// PlaceholderPerson is the sample person in the fence example.
	PlaceholderPerson = "PERSON"
	// SkillSummary is the one-line description in the skill file and agent.yaml.
	SkillSummary = "Create and change groups and sidebar sections when the person in this chat asks."
)

const skillBody = `Call these tools only when the person in this chat asks you to change a group or a sidebar section.

Ask before you call a tool when the group, the people, the agents, or the section is unclear. Do not invent a member. Do not invent a section. A group needs at least two of this person's agents. People on this account can be added. Delete a group or a section only when they asked to delete it. Pin and unpin change this person's sidebar only.

create_group needs a title and at least two agents. Pass people only when they named someone else on this account. update_group changes the title. add_group_members and remove_group_members change who is in the group. delete_group removes the group. The agents stay. pin_group and unpin_group change the pin for this person.

create_section adds a sidebar section. rename_section changes a section this person added. delete_section removes one after the agents have been moved out. The Agents and Pinned sections stay. move_agents puts agents in a section. To move an agent out of a section, set out to true. That puts the agent back in Agents.

place_group puts a group in a sidebar section. Pass the group, and a section when they named one. Leave section empty, or pass groups, to put the group back in the Groups list. This places the group in this person's sidebar only.

Use a name or an id. If a name matches more than one, ask which one. Do not pass user_id or agent_id.

If the tools are not listed, end your reply with one castbot-organize fence and no other fence of that type. Do not write that fence after the tools are listed.

` + "```castbot-organize\n" + `{"action":"create_group","title":"TITLE","agents":["NAME","NAME"]}` + "\n```" + `

Replace TITLE and NAME with what they asked for. Use action update_group, add_group_members, remove_group_members, delete_group, pin_group, unpin_group, create_section, rename_section, delete_section, move_agents, or place_group when that is what they asked. For move_agents, pass agents and either section or out. For place_group, pass group and section. Do not leave TITLE, NAME, or PERSON in place.`

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

// Spec is one group or section tool the guest lists.
type Spec struct {
	Name        string
	Description string
}

// Specs is the stable tool list, in the order the guest lists them.
func Specs() []Spec {
	return []Spec{
		{Name: ToolCreateGroup, Description: "Create a group with a title and at least two of this person's agents. Add people only when the person named them."},
		{Name: ToolUpdateGroup, Description: "Rename a group this person created."},
		{Name: ToolAddMembers, Description: "Add agents or people to a group this person created."},
		{Name: ToolRemoveMembers, Description: "Remove agents or people from a group this person created. This person stays in the group."},
		{Name: ToolDeleteGroup, Description: "Delete a group this person created. The agents stay."},
		{Name: ToolPinGroup, Description: "Pin a group in this person's sidebar."},
		{Name: ToolUnpinGroup, Description: "Unpin a group in this person's sidebar."},
		{Name: ToolCreateSection, Description: "Add a sidebar section for this person."},
		{Name: ToolRenameSection, Description: "Rename a sidebar section this person added."},
		{Name: ToolDeleteSection, Description: "Remove a sidebar section this person added, after its agents have been moved out."},
		{Name: ToolMoveAgents, Description: "Move this person's agents into a sidebar section, or back to Agents when out is true."},
		{Name: ToolPlaceGroup, Description: "Move a group into a sidebar section for this person, or back to Groups when section is empty or groups."},
	}
}

// Known reports whether name is one of the group or section tools.
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
