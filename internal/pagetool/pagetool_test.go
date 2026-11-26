package pagetool

import (
	"strings"
	"testing"
)

func TestSkillFiles(t *testing.T) {
	for name, body := range map[string]string{SkillFile: SkillMarkdown(), "guide": Guide()} {
		if !strings.Contains(body, SkillName) || !strings.Contains(body, ToolCreate) || !strings.Contains(body, Fence) {
			t.Fatalf("%s missing the skill", name)
		}
		if strings.Contains(body, "\u2014") || strings.Contains(body, "\u2013") {
			t.Fatalf("%s has a dash", name)
		}
	}
	merged := MergeExtras(map[string]string{SkillFile: "nope"})
	if strings.Contains(merged[SkillFile], "nope") || !strings.Contains(merged[SkillFile], SkillName) {
		t.Fatal(merged[SkillFile])
	}
	if Instructions("Be brief.\n") == "Be brief.\n" || !strings.Contains(Instructions("Be brief.\n"+Heading), Heading) {
		t.Fatal("guide")
	}
	if strings.Count(Instructions(Guide()), Heading) != 1 {
		t.Fatal("guide doubled")
	}
	once := Apply("name: Atlas\nskills:\n  - review\nharness:\n  permissions:\n    create_page: deny\n")
	if !strings.Contains(once, "create_page: always-allow") || strings.Contains(once, "create_page: deny") || !strings.Contains(once, SkillDir) {
		t.Fatal(once)
	}
}

func TestFenceParse(t *testing.T) {
	body := "Saved it.\n\n```castbot-page\n{\"action\":\"create_page\",\"title\":\"Trials\",\"document\":\"{\\\"v\\\":1}\",\"user_id\":\"other\"}\n```\n"
	visible, fields, ok := Parse(body)
	if !ok || fields.Action != ToolCreate || fields.Title != "Trials" || !strings.Contains(fields.Document, `"v":1`) || strings.Contains(visible, Fence) {
		t.Fatalf("%v %q", fields, visible)
	}
	for _, raw := range []string{
		"```castbot-page\n{\"action\":\"create_page\",\"title\":\"TITLE\",\"document\":\"{}\"}\n```",
		"```castbot-page\n{\"action\":\"create_page\",\"title\":\"Trials\",\"document\":\"DOCUMENT\"}\n```",
		"```castbot-page\n{\"action\":\"update_page\",\"page\":\"PAGE\",\"document\":\"{}\"}\n```",
		"```castbot-page\n{\"action\":\"update_page\"}\n```",
		"```castbot-page\n{\"action\":\"nope\"}\n```",
		"```castbot-page\nnot json\n```",
	} {
		visible, _, ok = Parse(raw)
		if ok || !strings.Contains(visible, Fence) {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
	_, fields, ok = Parse("```castbot-page\n{\"action\":\"share_page\",\"page\":\"abc\",\"visibility\":\"workspace\"}\n```")
	if !ok || fields.Action != ToolShare || fields.Visibility != "workspace" {
		t.Fatal(fields)
	}
}
