package deletetool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTool(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\nharness:\n  permissions:\n    delete_agent: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "agents", SkillName, SkillDir, "always-allow", ToolDelete} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "delete_agent: deny") {
		t.Fatalf("level was not forced\n%s", once)
	}
}

func TestSkillStatesTheWarning(t *testing.T) {
	for name, body := range map[string]string{SkillFile: SkillMarkdown(), "guide": Guide()} {
		if strings.Contains(body, "GitHub") || strings.Contains(body, "DigitalOcean") {
			t.Fatalf("%s names a provider", name)
		}
		if !strings.Contains(body, Warning) || !strings.Contains(body, "cannot be undone") || !strings.Contains(body, "Memory export is not in this version") {
			t.Fatalf("%s missing the warning", name)
		}
	}
	if strings.Contains(SkillMarkdown(), Heading) {
		t.Fatal("skill file includes the instructions heading")
	}
	if !strings.Contains(Guide(), Heading) || !strings.Contains(SkillMarkdown(), Fence) {
		t.Fatal("skill shape")
	}
	if !strings.Contains(SkillMarkdown(), "Do not pass confirm on the first call") || !strings.Contains(SkillMarkdown(), "A question is not the name") {
		t.Fatal("skill does not wait for the name")
	}
	_, fields, ok := Parse("```castbot-delete\n{\"action\":\"delete_agent\"}\n```")
	if !ok || fields.Confirm != "" || fields.Action != ToolDelete {
		t.Fatalf("sample %+v %v", fields, ok)
	}
}

func TestParseFence(t *testing.T) {
	body := "Removing it.\n\n```castbot-delete\n{\"action\":\"delete_agent\",\"confirm\":\"Ada\",\"user_id\":\"other\",\"value\":\"secret\"}\n```\n"
	visible, fields, ok := Parse(body)
	if !ok || visible != "Removing it." || fields.Action != ToolDelete || fields.Confirm != "Ada" {
		t.Fatalf("%v %q %+v", ok, visible, fields)
	}
	for _, raw := range []string{
		"```castbot-delete\n{\"action\":\"delete_agent\",\"confirm\":\"NAME\"}\n```",
		"```castbot-delete\n{\"action\":\"nope\"}\n```",
		"```castbot-delete\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-delete") && !strings.Contains(visible, "castbot-delete") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
}
