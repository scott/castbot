package replacetool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTools(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\nharness:\n  permissions:\n    replace_machine: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "agents", SkillName, SkillDir, "always-allow", ToolReplace, ToolRollback} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "replace_machine: deny") {
		t.Fatalf("level was not forced\n%s", once)
	}
}

func TestSkillMatchesTheDialog(t *testing.T) {
	for name, body := range map[string]string{SkillFile: SkillMarkdown(), "guide": Guide()} {
		if strings.Contains(body, "GitHub") || strings.Contains(body, "DigitalOcean") {
			t.Fatalf("%s names a provider", name)
		}
	}
	if strings.Contains(SkillMarkdown(), Heading) {
		t.Fatal("skill file includes the instructions heading")
	}
	if !strings.Contains(Guide(), Heading) || !strings.Contains(SkillMarkdown(), Fence) {
		t.Fatal("skill shape")
	}
	if !strings.Contains(SkillMarkdown(), "Do not pass confirm on the first call") || !strings.Contains(SkillMarkdown(), "A question is not a yes") {
		t.Fatal("skill does not wait for a yes")
	}
	_, fields, ok := Parse("```castbot-replace\n{\"action\":\"replace_machine\"}\n```")
	if !ok || fields.Confirm || fields.Action != ToolReplace {
		t.Fatalf("sample %+v %v", fields, ok)
	}
	for _, line := range append(KeptLines(), LostLines()...) {
		if strings.Contains(line, "\u2014") || line == "" {
			t.Fatalf("dialog line %q", line)
		}
	}
}

func TestParseFence(t *testing.T) {
	body := "Starting.\n\n```castbot-replace\n{\"action\":\"replace_machine\",\"confirm\":\"yes\",\"user_id\":\"other\",\"value\":\"secret\"}\n```\n"
	visible, fields, ok := Parse(body)
	if !ok || visible != "Starting." || fields.Action != ToolReplace || !fields.Confirm {
		t.Fatalf("%v %q %+v", ok, visible, fields)
	}
	for _, raw := range []string{
		"```castbot-replace\n{\"action\":\"replace_machine\",\"confirm\":\"YES\"}\n```",
		"```castbot-replace\n{\"action\":\"replace_machine\",\"confirm\":\"true\"}\n```",
		"```castbot-replace\n{\"action\":\"roll_back_runtime\",\"confirm\":\"YES\"}\n```",
		"```castbot-replace\n{\"action\":\"nope\"}\n```",
		"```castbot-replace\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-replace") && !strings.Contains(visible, "castbot-replace") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
	_, fields, ok = Parse("```castbot-replace\n{\"action\":\"roll_back_runtime\"}\n```")
	if !ok || fields.Action != ToolRollback || fields.Confirm {
		t.Fatalf("rollback %+v %v", fields, ok)
	}
}
