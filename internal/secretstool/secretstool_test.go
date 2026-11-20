package secretstool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTools(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\nharness:\n  permissions:\n    grant_secret: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "agents", SkillName, SkillDir, "always-allow", ToolGrant, ToolRevoke, ToolMove, ToolDelete} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "grant_secret: deny") {
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
	if !strings.Contains(SkillMarkdown(), "request_secret") || !strings.Contains(SkillMarkdown(), "Do not pass the value") {
		t.Fatal("skill does not keep the value on the card")
	}
}

func TestParseFence(t *testing.T) {
	body := "Giving it.\n\n```castbot-secrets\n{\"action\":\"grant_secret\",\"secret\":\"NOTES_TOKEN\",\"agent\":\"Pip\",\"value\":\"secret\",\"user_id\":\"other\"}\n```\n"
	visible, fields, ok := Parse(body)
	if !ok || visible != "Giving it." || fields.Action != ToolGrant || fields.Secret != "NOTES_TOKEN" || fields.Agent != "Pip" {
		t.Fatalf("%v %q %+v", ok, visible, fields)
	}
	for _, raw := range []string{
		"```castbot-secrets\n{\"action\":\"grant_secret\",\"secret\":\"SECRET\",\"agent\":\"Pip\"}\n```",
		"```castbot-secrets\n{\"action\":\"grant_secret\",\"secret\":\"NOTES_TOKEN\",\"agent\":\"AGENT\"}\n```",
		"```castbot-secrets\n{\"action\":\"grant_secret\",\"secret\":\"NOTES_TOKEN\"}\n```",
		"```castbot-secrets\n{\"action\":\"revoke_secret\"}\n```",
		"```castbot-secrets\n{\"action\":\"move_connection_secret\"}\n```",
		"```castbot-secrets\n{\"action\":\"delete_vault_secret\",\"secret\":\"SECRET\"}\n```",
		"```castbot-secrets\n{\"action\":\"nope\"}\n```",
		"```castbot-secrets\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-secrets") && !strings.Contains(visible, "castbot-secrets") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
	_, fields, ok = Parse("```castbot-secrets\n{\"action\":\"delete_vault_secret\",\"secret\":\"NOTES_TOKEN\"}\n```")
	if !ok || fields.Action != ToolDelete || fields.Secret != "NOTES_TOKEN" || fields.Agent != "" {
		t.Fatalf("delete %+v %v", fields, ok)
	}
}
