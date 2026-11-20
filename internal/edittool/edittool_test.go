package edittool

import (
	"strings"
	"testing"
)

func TestSkillFiles(t *testing.T) {
	md := SkillMarkdown()
	if !strings.Contains(md, SkillName) || !strings.Contains(md, Fence) || !strings.Contains(md, PlaceholderName) {
		t.Fatal(md)
	}
	if _, _, ok := Parse(md); ok {
		t.Fatal("example fence parsed")
	}
	merged := MergeExtras(map[string]string{SkillFile: "nope"})
	if strings.Contains(merged[SkillFile], "nope") || !strings.Contains(merged[SkillFile], SkillName) {
		t.Fatal(merged[SkillFile])
	}
	notes := Instructions("Be brief.")
	if strings.Count(notes, Heading) != 1 || Instructions(notes) != notes {
		t.Fatal(notes)
	}
	yaml := Apply("name: Ada\n")
	if !strings.Contains(yaml, SkillName) || !strings.Contains(yaml, ToolUpdate+": always-allow") && !strings.Contains(yaml, ToolUpdate+":always-allow") {
		t.Fatal(yaml)
	}
	if Apply(":\n") != ":\n" || Apply("*alias") != "*alias" {
		t.Fatal("invalid yaml changed")
	}
	if !Known(ToolUpdate) || Known("delete_agent") || Known("move_agents") {
		t.Fatal("known")
	}
}

func TestParseFence(t *testing.T) {
	body := "Renaming.\n\n```castbot-edit\n{\"action\":\"update_agent\",\"name\":\"NAME\"}\n```\n\n```castbot-edit\n{\"action\":\"update_agent\",\"name\":\"Quill\",\"user_id\":\"other\",\"delegate\":true}\n```"
	visible, fields, ok := Parse(body)
	if !ok || fields.Name == nil || *fields.Name != "Quill" || fields.Delegate == nil || !*fields.Delegate {
		t.Fatalf("%+v %v", fields, ok)
	}
	if !strings.Contains(visible, "NAME") || strings.Contains(visible, "Quill") {
		t.Fatal(visible)
	}
	visible, fields, ok = Parse("```castbot-edit\n{\"action\":\"update_agent\",\"color\":\"Mint\"}\n```")
	if !ok || fields.Color == nil || *fields.Color != "Mint" || fields.Name != nil || fields.Empty() {
		t.Fatalf("%+v", fields)
	}
	for _, raw := range []string{
		"```castbot-edit\n{\"action\":\"delete_agent\"}\n```",
		"```castbot-edit\nnot json\n```",
		"```castbot-edit\n{\"action\":\"update_agent\"}\n```",
		"```castbot-edit\n{\"action\":\"update_agent\",\"model\":\"MODEL\"}\n```",
	} {
		if _, _, ok := Parse(raw); ok {
			t.Fatal(raw)
		}
	}
}
