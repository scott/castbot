package machinetool

import (
	"strings"
	"testing"
)

func TestSkillFiles(t *testing.T) {
	if !strings.Contains(SkillMarkdown(), "name: "+SkillName) || !strings.Contains(SkillMarkdown(), Fence) || !strings.Contains(SkillMarkdown(), PlaceholderAction) {
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
	if !strings.Contains(yaml, SkillName) || !strings.Contains(yaml, ToolPause+": always-allow") || !strings.Contains(yaml, ToolRefresh+": always-allow") {
		t.Fatal(yaml)
	}
	if Apply(":\n") != ":\n" || Apply("*alias") != "*alias" {
		t.Fatal("invalid yaml changed")
	}
	if !Known(ToolWake) || Known("delete_agent") || Known("replace_machine") {
		t.Fatal("known")
	}
}

func TestParseFence(t *testing.T) {
	body := "Pausing it.\n\n```castbot-machine\n{\"action\":\"ACTION\"}\n```\n\n```castbot-machine\n{\"action\":\"pause_machine\",\"user_id\":\"other\"}\n```"
	visible, req, ok := Parse(body)
	if !ok || req.Action != ToolPause || !strings.Contains(visible, "ACTION") || strings.Contains(visible, "pause_machine") {
		t.Fatalf("%v %+v %q", ok, req, visible)
	}
	_, req, ok = Parse("```castbot-machine\n{\"action\":\"refresh_machine\"}\n```")
	if !ok || req.Action != ToolRefresh {
		t.Fatalf("%v %+v", ok, req)
	}
	kept := "Leave this.\n```castbot-machine\n{\"action\":\"replace_machine\"}\n```"
	if visible, _, ok = Parse(kept); ok || visible != kept {
		t.Fatalf("invalid removed %q", visible)
	}
	kept = "```castbot-machine\nnot json\n```"
	if visible, _, ok = Parse(kept); ok || visible != kept {
		t.Fatalf("bad json removed %q", visible)
	}
}
