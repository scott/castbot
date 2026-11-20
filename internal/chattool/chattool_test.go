package chattool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTools(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\nharness:\n  permissions:\n    stop_reply: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "agents", SkillName, SkillDir, "always-allow", ToolStop, ToolRetry, ToolList, ToolOpen} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "stop_reply: deny") {
		t.Fatalf("level was not forced\n%s", once)
	}
	if strings.Contains(once, "env:") || strings.Contains(once, "TOKEN") {
		t.Fatalf("apply wrote a credential marker\n%s", once)
	}
}

func TestSkillHasNoCredential(t *testing.T) {
	for name, body := range map[string]string{SkillFile: SkillMarkdown(), "guide": Guide()} {
		if strings.Contains(body, "env:") || strings.Contains(body, "TOKEN") {
			t.Fatalf("%s %s", name, body)
		}
	}
	if strings.Contains(SkillMarkdown(), Heading) {
		t.Fatal("skill file includes the instructions heading")
	}
	if !strings.Contains(Guide(), Heading) || !strings.Contains(SkillMarkdown(), Fence) {
		t.Fatal("skill shape")
	}
}

func TestParseFence(t *testing.T) {
	body := "Opening it.\n\n```castbot-chat\n{\"action\":\"open_thread\",\"thread\":\"Weekly link sweep\",\"user_id\":\"other\"}\n```\n"
	visible, fields, ok := Parse(body)
	if !ok || visible != "Opening it." || fields.Action != ToolOpen || fields.Thread != "Weekly link sweep" {
		t.Fatalf("%v %q %+v", ok, visible, fields)
	}
	for _, raw := range []string{
		"```castbot-chat\n{\"action\":\"open_thread\",\"thread\":\"THREAD\"}\n```",
		"```castbot-chat\n{\"action\":\"open_thread\"}\n```",
		"```castbot-chat\n{\"action\":\"nope\"}\n```",
		"```castbot-chat\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-chat") && !strings.Contains(visible, "castbot-chat") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
	_, fields, ok = Parse("```castbot-chat\n{\"action\":\"stop_reply\"}\n```")
	if !ok || fields.Action != ToolStop || fields.Thread != "" {
		t.Fatalf("stop %+v %v", fields, ok)
	}
	_, fields, ok = Parse("```castbot-chat\n{\"action\":\"list_threads\"}\n```")
	if !ok || fields.Action != ToolList {
		t.Fatalf("list %+v %v", fields, ok)
	}
}
