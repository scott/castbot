package secretask

import (
	"strings"
	"testing"
)

func TestParseFence(t *testing.T) {
	body := "Connect it.\n\n```castbot-secret\n{\"action\":\"save\",\"name\":\"x_api_key\",\"label\":\"Monitor posts\",\"reason\":\"Watch posts\",\"value\":\"test-named-key-value\"}\n```\n"
	visible, req, ok := Parse(body)
	if !ok || visible != "Connect it." || req.Skip || req.Action != "save" || req.Name != "X_API_KEY" || req.Label != "Monitor posts" || req.Reason != "Watch posts" {
		t.Fatalf("%v %q %+v", ok, visible, req)
	}
	if strings.Contains(visible, "test-named-key-value") || strings.Contains(visible, "castbot-secret") {
		t.Fatal(visible)
	}
	visible, req, ok = Parse("```castbot-secret\n{\"action\":\"delete\",\"name\":\"x_api_key\",\"reason\":\"Stop watching\"}\n```")
	if !ok || visible != "" || req.Action != "delete" || req.Name != "X_API_KEY" || req.Reason != "Stop watching" || req.Skip {
		t.Fatalf("%v %q %+v", ok, visible, req)
	}
	visible, req, ok = Parse("```castbot-secret\n{\"action\":\"save\",\"name\":\"SERVICE_API_KEY\",\"label\":\"Service API\",\"reason\":\"Why they asked\"}\n```")
	if !ok || !req.Skip || req.Name != SampleName {
		t.Fatalf("sample %+v", req)
	}
	visible, req, ok = Parse("```castbot-secret\n{\"action\":\"save\",\"name\":\"SERVICE_API_KEY\",\"reason\":\"Watch posts\"}\n```")
	if !ok || req.Skip || req.Name != SampleName {
		t.Fatalf("real sample name %+v", req)
	}
	visible, req, ok = Parse("```castbot-secret\n{\"action\":\"save\",\"name\":\"NAME\",\"reason\":\"Watch posts\"}\n```")
	if !ok || !req.Skip || req.Name != "NAME" {
		t.Fatalf("placeholder %+v", req)
	}
	raw := "Leave this.\n\n```castbot-secret\nnot json\n```\n"
	visible, _, ok = Parse(raw)
	if ok || visible != raw {
		t.Fatalf("invalid fence changed %v %q", ok, visible)
	}
	visible, req, ok = Parse("```castbot-secret\nnot json\n```\n\n```castbot-secret\n{\"action\":\"save\",\"name\":\"x_api_key\",\"reason\":\"Watch posts\"}\n```")
	if !ok || req.Name != "X_API_KEY" || !strings.Contains(visible, "not json") || strings.Contains(visible, "X_API_KEY") {
		t.Fatalf("second fence %v %q %+v", ok, visible, req)
	}
	visible, req, ok = Parse("```castbot-secret\n{\"action\":\"save\",\"name\":\"1API\",\"reason\":\"Watch posts\"}\n```")
	if !ok || req.Name != "1API" {
		t.Fatalf("leading digit %+v %v", req, ok)
	}
}

func TestApplyKeepsSkillAndForcesTools(t *testing.T) {
	src := "name: Atlas\nskills:\n  - review\nharness:\n  permissions:\n    request_secret: deny\n    delete_secret: deny\n"
	once := Apply(src)
	twice := Apply(once)
	if once != twice {
		t.Fatalf("apply is not stable\n%s\n%s", once, twice)
	}
	for _, needle := range []string{"review", SkillName, SkillDir, ToolSave, ToolDelete, "always-allow"} {
		if !strings.Contains(once, needle) {
			t.Fatalf("missing %s\n%s", needle, once)
		}
	}
	if strings.Contains(once, "deny") || strings.Contains(once, "env:") || strings.Contains(once, "TOKEN") {
		t.Fatalf("apply kept a deny or a credential marker\n%s", once)
	}
	mapped := Apply("name: Atlas\nskills:\n  review: true\n  api-keys: true\n")
	if !strings.Contains(mapped, SkillName) || strings.Contains(mapped, "review: true") {
		t.Fatalf("mapping skills\n%s", mapped)
	}
	if Apply("name: [unterminated") != "name: [unterminated" {
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
		ShimFile:  ShimYAML(),
	} {
		if strings.Contains(body, "env:") || strings.Contains(body, "TOKEN") || strings.Contains(body, "test-named-key-value") {
			t.Fatalf("%s held a credential marker", name)
		}
	}
	if !strings.Contains(SkillMarkdown(), "name: "+SkillName) || !strings.Contains(SkillMarkdown(), Fence) || !strings.Contains(Guide(), Heading) {
		t.Fatal("skill shape")
	}
	merged := MergeExtras(map[string]string{SkillFile: "nope", ShimFile: "env: TOKEN"})
	if strings.Contains(merged[SkillFile], "nope") || strings.Contains(merged[ShimFile], "TOKEN") || !strings.Contains(merged[SkillFile], SkillName) {
		t.Fatal("stored extra replaced the skill")
	}
	got := Instructions("Be brief.\n")
	if !strings.Contains(got, Heading) || !strings.HasPrefix(got, "Be brief.") || Instructions(got) != got {
		t.Fatal(got)
	}
}
