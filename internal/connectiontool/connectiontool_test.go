package connectiontool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTools(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\nharness:\n  permissions:\n    disconnect_connection: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "agents", SkillName, SkillDir, "always-allow", ToolDisconnect, ToolLevel} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "disconnect_connection: deny") {
		t.Fatalf("level was not forced\n%s", once)
	}
	if strings.Contains(once, "env:") || strings.Contains(once, "TOKEN") {
		t.Fatalf("apply wrote a credential marker\n%s", once)
	}
}

func TestSkillHasNoCredential(t *testing.T) {
	for name, body := range map[string]string{SkillFile: SkillMarkdown(), "guide": Guide()} {
		if strings.Contains(body, "env:") || strings.Contains(body, "TOKEN") || strings.Contains(body, "private key value") {
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
	body := "Saving it.\n\n```castbot-connection\n{\"action\":\"set_tool_level\",\"connection\":\"shell\",\"tool\":\"bash\",\"level\":\"deny\",\"user_id\":\"other\",\"token\":\"secret\"}\n```\n"
	visible, fields, ok := Parse(body)
	if !ok || visible != "Saving it." || fields.Action != ToolLevel || fields.Connection != "shell" || fields.Tool != "bash" || fields.Level != "deny" {
		t.Fatalf("%v %q %+v", ok, visible, fields)
	}
	for _, raw := range []string{
		"```castbot-connection\n{\"action\":\"set_tool_level\",\"connection\":\"CONNECTION\",\"tool\":\"bash\",\"level\":\"deny\"}\n```",
		"```castbot-connection\n{\"action\":\"set_tool_level\",\"connection\":\"shell\",\"tool\":\"TOOL\",\"level\":\"deny\"}\n```",
		"```castbot-connection\n{\"action\":\"set_tool_level\",\"connection\":\"shell\",\"tool\":\"bash\",\"level\":\"LEVEL\"}\n```",
		"```castbot-connection\n{\"action\":\"set_tool_level\",\"connection\":\"shell\"}\n```",
		"```castbot-connection\n{\"action\":\"disconnect_connection\",\"connection\":\"CONNECTION\"}\n```",
		"```castbot-connection\n{\"action\":\"disconnect_connection\"}\n```",
		"```castbot-connection\n{\"action\":\"nope\"}\n```",
		"```castbot-connection\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-connection") && !strings.Contains(visible, "castbot-connection") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
	_, fields, ok = Parse("```castbot-connection\n{\"action\":\"disconnect_connection\",\"connection\":\"Shell Actions\"}\n```")
	if !ok || fields.Action != ToolDisconnect || fields.Connection != "Shell Actions" {
		t.Fatalf("disconnect %+v %v", fields, ok)
	}
}
