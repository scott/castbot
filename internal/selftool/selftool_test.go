package selftool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTools(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\n    - helper\nharness:\n  permissions:\n    account_usage: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	needles := []string{"review", "agents", "helper", SkillName, SkillDir, "always-allow"}
	for _, spec := range Specs() {
		needles = append(needles, spec.Name)
	}
	for _, needle := range needles {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "account_usage: deny") {
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
	body := "On it.\n\n```castbot-account\n{\"tool\":\"account_usage\"}\n```\n"
	visible, req, ok := Parse(body)
	if !ok || visible != "On it." || req.Tool != ToolUsage || req.Query != "" || req.ID != "" {
		t.Fatalf("%v %q %+v", ok, visible, req)
	}
	visible, req, ok = Parse("```castbot-account\n{\"tool\":\"search_account\",\"query\":\"notes\"}\n```")
	if !ok || visible != "" || req.Tool != ToolSearch || req.Query != "notes" {
		t.Fatalf("search %v %q %+v", ok, visible, req)
	}
	visible, req, ok = Parse("```castbot-account\n{\"tool\":\"routine_runs\",\"id\":\"abc\",\"query\":\"nope\"}\n```")
	if !ok || req.Tool != ToolRuns || req.ID != "abc" || req.Query != "" {
		t.Fatalf("runs %+v %v", req, ok)
	}
	for _, raw := range []string{
		"```castbot-account\n{\"tool\":\"TOOL\"}\n```",
		"```castbot-account\n{\"tool\":\"search_account\"}\n```",
		"```castbot-account\n{\"tool\":\"search_account\",\"query\":\"QUERY\"}\n```",
		"```castbot-account\n{\"tool\":\"delete_agent\"}\n```",
		"```castbot-account\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-account") && !strings.Contains(visible, "castbot-account") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
}

func TestKnown(t *testing.T) {
	if !Known(ToolUsage) || Known("delete_agent") || Known(PlaceholderTool) {
		t.Fatal("known set")
	}
	if len(Specs()) != 11 {
		t.Fatalf("specs %d", len(Specs()))
	}
}
