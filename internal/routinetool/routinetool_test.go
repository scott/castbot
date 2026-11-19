package routinetool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTools(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\n    - helper\nharness:\n  permissions:\n    create_routine: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "agents", "helper", SkillName, SkillDir, "always-allow", ToolList, ToolCreate, ToolUpdate, ToolDelete} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "create_routine: deny") {
		t.Fatalf("level was not forced\n%s", once)
	}
	if strings.Contains(once, "env:") || strings.Contains(once, "TOKEN") {
		t.Fatalf("apply wrote a credential marker\n%s", once)
	}
	bare := Apply("name: Atlas\n")
	if strings.Contains(bare, "mcp:") {
		t.Fatalf("apply added a server\n%s", bare)
	}
}

func TestApplyLeavesInvalidYAML(t *testing.T) {
	src := "name: [unterminated"
	if Apply(src) != src {
		t.Fatal("invalid yaml was rewritten")
	}
	alias := "name: Atlas\nskills:\n  - &s review\ncopy: *s\n"
	if Apply(alias) != alias {
		t.Fatal("alias yaml was rewritten")
	}
}

func TestSkillHasNoCredential(t *testing.T) {
	for name, body := range map[string]string{
		SkillFile: SkillMarkdown(),
		"guide":   Guide(),
	} {
		if strings.Contains(body, "env:") || strings.Contains(body, "TOKEN") {
			t.Fatalf("%s %s", name, body)
		}
	}
	if !strings.Contains(SkillMarkdown(), "name: "+SkillName) || !strings.Contains(SkillMarkdown(), Fence) {
		t.Fatal("skill shape")
	}
	merged := MergeExtras(map[string]string{SkillFile: "nope"})
	if strings.Contains(merged[SkillFile], "nope") || !strings.Contains(merged[SkillFile], SkillName) {
		t.Fatal("stored extra replaced the skill")
	}
}

func TestInstructionsAppendOnce(t *testing.T) {
	got := Instructions("Be brief.\n")
	if !strings.Contains(got, Heading) || !strings.HasPrefix(got, "Be brief.") {
		t.Fatal(got)
	}
	if Instructions(got) != got {
		t.Fatal("guide was appended twice")
	}
}

func TestParseFence(t *testing.T) {
	body := "On it.\n\n```castbot-routine\n{\"action\":\"create\",\"name\":\"Morning digest\",\"cron\":\"0 8 * * 1-5\",\"prompt\":\"Check the inbox.\"}\n```\n"
	visible, req, ok := Parse(body)
	if !ok || visible != "On it." || req.Action != "create" || req.Name == nil || *req.Name != "Morning digest" {
		t.Fatalf("%v %q %+v", ok, visible, req)
	}
	for _, raw := range []string{
		"```castbot-routine\n{\"action\":\"create\",\"name\":\"NAME\",\"cron\":\"CRON\",\"prompt\":\"PROMPT\"}\n```",
		"```castbot-routine\n{\"action\":\"create\",\"name\":\"Morning digest\"}\n```",
		"```castbot-routine\n{\"action\":\"nope\",\"id\":\"abc\"}\n```",
		"```castbot-routine\n{\"action\":\"delete\"}\n```",
		"```castbot-routine\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-routine") && !strings.Contains(visible, "castbot-routine") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
	upd := "```castbot-routine\n{\"action\":\"update\",\"id\":\"abc\",\"enabled\":false}\n```"
	_, req, ok = Parse(upd)
	if !ok || req.Action != "update" || req.ID != "abc" || req.Enabled == nil || *req.Enabled {
		t.Fatalf("update %+v %v", req, ok)
	}
}
