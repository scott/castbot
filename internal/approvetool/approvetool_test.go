package approvetool

import (
	"strings"
	"testing"
)

func TestSkillFiles(t *testing.T) {
	md := SkillMarkdown()
	if !strings.Contains(md, SkillName) || !strings.Contains(md, Fence) || !strings.Contains(md, PlaceholderCard) {
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
	for _, name := range []string{ToolApprove, ToolDeny, ToolAlways} {
		if !strings.Contains(yaml, SkillName) || (!strings.Contains(yaml, name+": always-allow") && !strings.Contains(yaml, name+":always-allow")) {
			t.Fatal(yaml)
		}
	}
	if Apply(":\n") != ":\n" || Apply("*alias") != "*alias" {
		t.Fatal("invalid yaml changed")
	}
	if !Known(ToolApprove) || !Known(ToolDeny) || !Known(ToolAlways) || Known("delete_agent") || Known("update_agent") {
		t.Fatal("known")
	}
}

func TestParseFence(t *testing.T) {
	body := "Deciding.\n\n```castbot-approval\n{\"action\":\"approve_card\",\"card\":\"CARD\"}\n```\n\n```castbot-approval\n{\"action\":\"deny_card\",\"card\":\"git push\",\"user_id\":\"other\"}\n```"
	visible, fields, ok := Parse(body)
	if !ok || fields.Action != ToolDeny || fields.Card != "git push" {
		t.Fatalf("%+v %v", fields, ok)
	}
	if !strings.Contains(visible, "CARD") || strings.Contains(visible, "git push") {
		t.Fatal(visible)
	}
	visible, fields, ok = Parse("```castbot-approval\n{\"action\":\"always_approve_card\"}\n```")
	if !ok || fields.Action != ToolAlways || fields.Card != "" {
		t.Fatalf("%+v", fields)
	}
	for _, raw := range []string{
		"```castbot-approval\n{\"action\":\"delete_agent\"}\n```",
		"```castbot-approval\nnot json\n```",
		"```castbot-approval\n{\"action\":\"approve_card\",\"card\":\"CARD\"}\n```",
		"```castbot-approval\n{\"card\":\"bash\"}\n```",
	} {
		if _, _, ok := Parse(raw); ok {
			t.Fatal(raw)
		}
	}
}
