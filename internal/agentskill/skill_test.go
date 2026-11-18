package agentskill

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndServer(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - helper\nharness:\n  permissions:\n    create_agent: deny\n    list_agents: ask\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "helper", SkillName, ServerName, "always-allow", SkillDir} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "create_agent: deny") || strings.Contains(once, "list_agents: ask") {
		t.Fatalf("level was not forced\n%s", once)
	}
	if strings.Contains(once, "env:") || strings.Contains(once, "TOKEN") {
		t.Fatalf("apply wrote a credential marker\n%s", once)
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

func TestSkillAndShimHaveNoCredential(t *testing.T) {
	for name, body := range map[string]string{
		SkillFile: SkillMarkdown(),
		ShimFile:  ShimYAML(),
		"guide":   Guide(),
	} {
		if strings.Contains(body, "env:") || strings.Contains(body, "TOKEN") {
			t.Fatalf("%s %s", name, body)
		}
	}
	if !strings.Contains(ShimYAML(), shimCommand) || !strings.Contains(SkillMarkdown(), "name: "+SkillName) {
		t.Fatal("skill or shim shape")
	}
	merged := MergeExtras(map[string]string{ShimFile: "env:\n  TOKEN: kept\n", SkillFile: "nope"})
	if strings.Contains(merged[ShimFile], "env:") || strings.Contains(merged[SkillFile], "nope") {
		t.Fatal("stored extra replaced the skill or shim")
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
	body := "On it.\n\n```castbot-create\n{\"description\":\"Sort the support inbox and draft replies.\",\"name\":\"Quill\"}\n```\n"
	visible, req, ok := Parse(body)
	if !ok || visible != "On it." || req.Name != "Quill" || !strings.Contains(req.Description, "inbox") {
		t.Fatalf("%v %q %+v", ok, visible, req)
	}
	for _, raw := range []string{
		"```castbot-create\n{\"description\":\"DESCRIPTION\"}\n```",
		"```castbot-create\n{\"description\":\"hello\"}\n```",
		"```castbot-create\n{\"description\":\"\"}\n```",
		"```castbot-create\nnot json\n```",
		"no fence here",
	} {
		if _, _, ok := Parse(raw); ok {
			t.Fatalf("accepted %s", raw)
		}
	}
	long := "```castbot-create\n{\"description\":\"" + strings.Repeat("a", MaxRunes+1) + "\"}\n```"
	if _, _, ok := Parse(long); ok {
		t.Fatal("accepted a long description")
	}
}

func TestUsableDescription(t *testing.T) {
	if UsableDescription("hello") || UsableDescription(Placeholder) || UsableDescription("   ") {
		t.Fatal("short or sample text was usable")
	}
	if !UsableDescription("Sort the support inbox and draft replies.") {
		t.Fatal("a real description was refused")
	}
}
