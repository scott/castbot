package importtool

import (
	"strings"
	"testing"
)

func TestApplyKeepsSkillAndForcesTool(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\ntools:\n  mcp:\n    - agents\nharness:\n  permissions:\n    import_agent: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", "agents", SkillName, SkillDir, "always-allow", ToolImport} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "import_agent: deny") {
		t.Fatalf("level was not forced\n%s", once)
	}
}

func TestSkillMatchesTheForm(t *testing.T) {
	for name, body := range map[string]string{SkillFile: SkillMarkdown(), "guide": Guide()} {
		if strings.Contains(body, "DigitalOcean") {
			t.Fatalf("%s names a provider", name)
		}
		if !strings.Contains(body, "zip") || !strings.Contains(body, "tar") || !strings.Contains(body, "github.com") {
			t.Fatalf("%s missing the folder kinds", name)
		}
		if !strings.Contains(body, "skipped") || !strings.Contains(body, "Secret values are removed") || !strings.Contains(body, "Do not repeat a secret value") {
			t.Fatalf("%s missing the review", name)
		}
	}
	if strings.Contains(SkillMarkdown(), Heading) {
		t.Fatal("skill file includes the instructions heading")
	}
	if !strings.Contains(Guide(), Heading) || !strings.Contains(SkillMarkdown(), Fence) {
		t.Fatal("skill shape")
	}
	_, fields, ok := Parse("```castbot-import\n{\"action\":\"import_agent\"}\n```")
	if !ok || fields.URL != "" || fields.ArchiveB64 != "" || fields.Action != ToolImport {
		t.Fatalf("sample %+v %v", fields, ok)
	}
}

func TestParseFence(t *testing.T) {
	body := "Reading it.\n\n```castbot-import\n{\"action\":\"import_agent\",\"url\":\"https://github.com/acme/helper\",\"user_id\":\"other\",\"value\":\"secret\"}\n```\n"
	visible, fields, ok := Parse(body)
	if !ok || visible != "Reading it." || fields.Action != ToolImport || fields.URL != "https://github.com/acme/helper" || fields.ArchiveB64 != "" {
		t.Fatalf("%v %q %+v", ok, visible, fields)
	}
	for _, raw := range []string{
		"```castbot-import\n{\"action\":\"import_agent\",\"url\":\"URL\"}\n```",
		"```castbot-import\n{\"action\":\"import_agent\",\"archive_b64\":\"ARCHIVE\"}\n```",
		"```castbot-import\n{\"action\":\"nope\"}\n```",
		"```castbot-import\nnot json\n```",
		"no fence here",
	} {
		visible, _, ok := Parse("Keep.\n\n" + raw + "\n")
		if ok || !strings.Contains(visible, "Keep.") {
			t.Fatalf("accepted %s -> %v %s", raw, ok, visible)
		}
		if strings.Contains(raw, "castbot-import") && !strings.Contains(visible, "castbot-import") {
			t.Fatalf("invalid fence was removed %s", raw)
		}
	}
}
