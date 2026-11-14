package handoff

import (
	"strings"
	"testing"
)

func TestParseFence(t *testing.T) {
	raw := "Working on it.\n\n" + Format(Plan{
		DM:   &Direct{Agent: "ada", Task: "Draft the note."},
		Chat: &Group{Agents: []string{"bea", "cam"}, Task: "Check the facts."},
	})
	visible, plan, ok := Parse(raw)
	if !ok || visible != "Working on it." {
		t.Fatalf("visible %q ok %v", visible, ok)
	}
	if plan.DM == nil || plan.DM.Agent != "ada" || plan.DM.Task != "Draft the note." {
		t.Fatalf("dm %#v", plan.DM)
	}
	if plan.Chat == nil || len(plan.Chat.Agents) != 2 || plan.Chat.Task != "Check the facts." {
		t.Fatalf("chat %#v", plan.Chat)
	}
}

func TestParseRejectsBadFence(t *testing.T) {
	cases := []string{
		"no fence",
		"```castbot-handoff\nnot json\n```",
		"```castbot-handoff\n{\"dm\":{\"agent\":\"ada\",\"task\":\"\"}}\n```",
		"```castbot-handoff\n{\"chat\":{\"agents\":[\"ada\"],\"task\":\"one\"}}\n```",
		"```castbot-handoff\n{\"chat\":{\"agents\":[\"ada\",\"ada\"],\"task\":\"dup\"}}\n```",
		"```castbot-handoff\n{}\n```",
	}
	for _, body := range cases {
		visible, _, ok := Parse(body)
		if ok || visible != body {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestParseOnlyFence(t *testing.T) {
	visible, plan, ok := Parse(Format(Plan{DM: &Direct{Agent: "ada", Task: "Go."}}))
	if !ok || visible != "" || plan.DM == nil {
		t.Fatalf("visible %q plan %#v", visible, plan)
	}
}

func TestPrefaceRosterLines(t *testing.T) {
	text := Preface("chief-1", []Agent{
		{ID: "chief-1", Name: "Chief", Title: "Lead", Description: "Coordinates"},
		{ID: "ada", Name: "Ada", Title: "Writer", Description: "Drafts"},
	})
	if !strings.Contains(text, "You are the chief chief-1") {
		t.Fatalf("chief line missing\n%s", text)
	}
	if !strings.Contains(text, "Agents you can ask") {
		t.Fatalf("roster heading missing\n%s", text)
	}
	if !strings.Contains(text, "- id ada | Ada | Writer | Drafts") {
		t.Fatalf("roster line missing\n%s", text)
	}
	if !strings.Contains(text, `"agent":"AGENT"`) {
		t.Fatalf("placeholder missing\n%s", text)
	}
}

func TestSpecialistPromptHidesOwner(t *testing.T) {
	text := SpecialistPrompt("Chief", "Draft the note.")
	if !strings.Contains(text, "Chief asked you to do this:") || !strings.Contains(text, "Draft the note.") {
		t.Fatal(text)
	}
	if strings.Contains(text, "SECRET") {
		t.Fatal(text)
	}
}
