package settingstool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTools(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\nharness:\n  permissions:\n    update_name: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "agents", SkillName, SkillDir, "always-allow", ToolName, ToolTheme, ToolInvite, ToolRevoke} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "update_name: deny") {
		t.Fatalf("level was not forced\n%s", once)
	}
	if strings.Contains(once, "env:") || strings.Contains(once, "TOKEN") {
		t.Fatalf("apply wrote a credential marker\n%s", once)
	}
}

func TestSkillHasNoCredential(t *testing.T) {
	for name, body := range map[string]string{SkillFile: SkillMarkdown(), "guide": Guide()} {
		if strings.Contains(body, "env:") || strings.Contains(body, "TOKEN") || strings.Contains(body, "cbi_") {
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
	body := "Saving it.\n\n```castbot-settings\n{\"action\":\"update_name\",\"name\":\"Ada Lovelace\",\"user_id\":\"other\"}\n```\n"
	visible, fields, ok := Parse(body)
	if !ok || visible != "Saving it." || fields.Action != ToolName || fields.Name != "Ada Lovelace" {
		t.Fatalf("%v %q %+v", ok, visible, fields)
	}
	for _, raw := range []string{
		"```castbot-settings\n{\"action\":\"update_name\",\"name\":\"NAME\"}\n```",
		"```castbot-settings\n{\"action\":\"update_name\"}\n```",
		"```castbot-settings\n{\"action\":\"set_theme\",\"theme\":\"THEME\"}\n```",
		"```castbot-settings\n{\"action\":\"set_theme\"}\n```",
		"```castbot-settings\n{\"action\":\"create_invite\",\"name\":\"NAME\",\"email\":\"pip@example.com\"}\n```",
		"```castbot-settings\n{\"action\":\"create_invite\",\"name\":\"Pip\",\"email\":\"EMAIL\"}\n```",
		"```castbot-settings\n{\"action\":\"create_invite\",\"name\":\"Pip\"}\n```",
		"```castbot-settings\n{\"action\":\"revoke_invite\",\"invite\":\"INVITE\"}\n```",
		"```castbot-settings\n{\"action\":\"revoke_invite\"}\n```",
		"```castbot-settings\n{\"action\":\"nope\"}\n```",
		"```castbot-settings\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-settings") && !strings.Contains(visible, "castbot-settings") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
	_, fields, ok = Parse("```castbot-settings\n{\"action\":\"set_theme\",\"theme\":\"Light\"}\n```")
	if !ok || fields.Action != ToolTheme || fields.Theme != "Light" {
		t.Fatalf("theme %+v %v", fields, ok)
	}
	_, fields, ok = Parse("```castbot-settings\n{\"action\":\"create_invite\",\"name\":\"Pip Lane\",\"email\":\"pip@example.com\",\"agent_id\":\"other\"}\n```")
	if !ok || fields.Action != ToolInvite || fields.Name != "Pip Lane" || fields.Email != "pip@example.com" {
		t.Fatalf("invite %+v %v", fields, ok)
	}
	_, fields, ok = Parse("```castbot-settings\n{\"action\":\"revoke_invite\",\"invite\":\"pip@example.com\"}\n```")
	if !ok || fields.Action != ToolRevoke || fields.Invite != "pip@example.com" {
		t.Fatalf("revoke %+v %v", fields, ok)
	}
}
