package grouptool

import (
	"strings"
	"testing"
)

func TestSkillFiles(t *testing.T) {
	if !strings.Contains(SkillMarkdown(), "name: "+SkillName) || !strings.Contains(SkillMarkdown(), Fence) || !strings.Contains(SkillMarkdown(), PlaceholderTitle) {
		t.Fatal(SkillMarkdown())
	}
	if _, _, ok := Parse(SkillMarkdown()); ok {
		t.Fatal("example fence was usable")
	}
	merged := MergeExtras(map[string]string{SkillFile: "nope"})
	if strings.Contains(merged[SkillFile], "nope") || !strings.Contains(merged[SkillFile], SkillName) {
		t.Fatal(merged[SkillFile])
	}
	if Instructions("Be brief.\n") == "Be brief.\n" || !strings.Contains(Instructions("Be brief.\n"+Heading), Heading) {
		t.Fatal("guide")
	}
	if !strings.Contains(Instructions("Be brief."), Heading) || strings.Count(Instructions(Guide()), Heading) != 1 {
		t.Fatal("guide once")
	}
	yaml := Apply("name: Ada\n")
	if !strings.Contains(yaml, SkillName) || !strings.Contains(yaml, ToolCreateGroup+": always-allow") || !strings.Contains(yaml, ToolMoveAgents+": always-allow") {
		t.Fatal(yaml)
	}
	if Apply(":\n") != ":\n" || Apply("*alias") != "*alias" {
		t.Fatal("invalid yaml changed")
	}
	if !Known(ToolPinGroup) || Known("delete_agent") {
		t.Fatal("known")
	}
}

func TestParseFence(t *testing.T) {
	body := "I will add it.\n\n```castbot-organize\n{\"action\":\"create_group\",\"title\":\"TITLE\",\"agents\":[\"NAME\",\"NAME\"]}\n```\n\n```castbot-organize\n{\"action\":\"create_section\",\"name\":\"Writing\"}\n```"
	visible, req, ok := Parse(body)
	if !ok || req.Action != ToolCreateSection || req.Name != "Writing" || !strings.Contains(visible, "TITLE") || strings.Contains(visible, "Writing") {
		t.Fatalf("%v %+v %q", ok, req, visible)
	}
	_, req, ok = Parse("```castbot-organize\n{\"action\":\"move_agents\",\"agents\":[\"Ada\"],\"out\":true}\n```")
	if !ok || !req.Out || len(req.Agents) != 1 || req.Section != "" {
		t.Fatalf("%v %+v", ok, req)
	}
	_, req, ok = Parse("```castbot-organize\n{\"action\":\"add_group_members\",\"group\":\"Standup\",\"people\":[\"PERSON\"]}\n```")
	if ok {
		t.Fatalf("placeholder applied %+v", req)
	}
	kept := "Leave this.\n```castbot-organize\n{\"action\":\"nope\"}\n```"
	if visible, _, ok = Parse(kept); ok || visible != kept {
		t.Fatalf("invalid removed %q", visible)
	}
	_, req, ok = Parse("```castbot-organize\n{\"action\":\"update_group\",\"group\":\"Standup\",\"title\":\"Planning\"}\n```")
	if !ok || req.Group != "Standup" || req.Title != "Planning" {
		t.Fatalf("%v %+v", ok, req)
	}
}
