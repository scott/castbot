package web

import (
	"context"
	"html/template"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/store"
)

func TestActivityLabels(t *testing.T) {
	cases := []struct {
		name string
		in   activityIn
		want string
		mode string
		hide bool
	}{
		{name: "thinking", in: activityIn{Status: "streaming"}, want: "Thinking"},
		{name: "read", in: activityIn{Status: "streaming", ToolKind: "read"}, want: "Reading files"},
		{name: "bash", in: activityIn{Status: "streaming", ToolKind: "bash", HasText: true}, want: "Running command"},
		{name: "approval", in: activityIn{Status: "streaming", PendingApproval: true, HasText: true}, want: "Waiting for approval", mode: "held"},
		{name: "wake", in: activityIn{Status: "queued", Machine: "paused"}, want: "Waking up"},
		{name: "reconnect", in: activityIn{Reconnecting: true, HasText: true}, want: "Reconnecting", mode: "slow"},
		{name: "streaming text", in: activityIn{Status: "streaming", HasText: true}, want: "Working", hide: true},
		{name: "unknown tool", in: activityIn{Status: "streaming", ToolKind: "widget"}, want: "Working"},
		{name: "no tool events", in: activityIn{Status: "streaming", HasText: true}, want: "Working", hide: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideActivity(tc.in)
			if got.Label != tc.want || got.Mode != tc.mode || got.HideThread != tc.hide {
				t.Fatalf("got %+v", got)
			}
			if got.Label == "Writing" || got.Label == "Replying" {
				t.Fatal(got.Label)
			}
		})
	}
}

func TestElapsedAndDwell(t *testing.T) {
	if text, ok := formatElapsed(4 * time.Second); ok || text != "" {
		t.Fatalf("early %q %v", text, ok)
	}
	if text, ok := formatElapsed(14 * time.Second); !ok || text != "14s" {
		t.Fatalf("seconds %q %v", text, ok)
	}
	if text, ok := formatElapsed(65 * time.Second); !ok || text != "1m 05s" {
		t.Fatalf("minutes %q %v", text, ok)
	}
	var d labelDwell
	now := time.Now()
	if d.next("Thinking", now) != "Thinking" {
		t.Fatal("first")
	}
	if d.next("Working", now.Add(100*time.Millisecond)) != "Thinking" {
		t.Fatal("dwell")
	}
	if d.next("Working", now.Add(actMinDwell)) != "Working" {
		t.Fatal("release")
	}
}

func TestRedStampGone(t *testing.T) {
	body, err := execTemplate("pending", liveView{
		RunID: "run1", Color: "c1", Letter: "A",
		Act:  template.HTML(actSlotHTML("run1", actRowHTML(activityOut{Label: "Thinking"}, ""), false)),
		Tail: template.HTML(tailWrap("run1", "")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, ">Replying<") || strings.Contains(body, ">Waking<") || strings.Contains(body, `class="stamp"`) {
		t.Fatalf("old stamp %s", body)
	}
	if !strings.Contains(body, `class="act"`) || !strings.Contains(body, "Thinking") || !strings.Contains(body, `aria-live="polite"`) {
		t.Fatalf("indicator %s", body)
	}
	if !strings.Contains(body, `sse-swap="block"`) || !strings.Contains(body, `sse-swap="tail"`) || !strings.Contains(body, `sse-swap="actxt"`) {
		t.Fatalf("stream swaps %s", body)
	}
}

func TestCalmErrorCopy(t *testing.T) {
	partial := calmFor("Atlas", "The model provider did not answer for 60 s.", true)
	if partial.Title != "Reply stopped before it finished." || partial.Retry != "Retry" || !strings.Contains(partial.Line, "kept above") {
		t.Fatalf("partial %+v", partial)
	}
	none := calmFor("Atlas", "The agent is not reachable right now.", false)
	if none.Title != "Atlas could not reply." || none.Line != "The agent is not reachable right now." || none.Retry != "Retry" {
		t.Fatalf("empty %+v", none)
	}
	wake := calmFor("Wren", "The machine did not wake up.", false)
	if wake.Title != "Wren's machine did not wake up." || wake.Retry != "Try again" || wake.Second != "Machine" || !wake.Wake {
		t.Fatalf("wake %+v", wake)
	}
	body, err := execTemplate("runError", liveView{
		RunID: "run1", CSRF: "tok", AgentID: "agt",
		CalmTitle: partial.Title, CalmLine: partial.Line, CalmRetry: partial.Retry, CalmSecond: "Details",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "errbox") || !strings.Contains(body, `role="alert"`) || !strings.Contains(body, `action="/runs/run1/retry"`) {
		t.Fatalf("card %s", body)
	}
	if !strings.Contains(body, partial.Title) || !strings.Contains(body, ">Retry<") {
		t.Fatalf("actions %s", body)
	}
}

func TestUserPlainAndAgentMarkdown(t *testing.T) {
	now := time.Date(2026, 11, 12, 21, 4, 0, 0, time.UTC)
	views := presentMessages([]store.Message{
		{ID: "u", AuthorKind: "user", Body: "check **pricing** and # heading\nhttps://example.com/docs/pricing.", CreatedAt: now.Format(time.RFC3339)},
		{ID: "a", AuthorKind: "agent", Body: "**Pricing** lines match.", CreatedAt: now.Format(time.RFC3339)},
	}, agentView{Color: "c1", Letter: "A"}, now, time.UTC)
	user := string(views[0].HTML)
	if strings.Contains(user, "<strong>") || strings.Contains(user, "<h1>") {
		t.Fatalf("user markdown %s", user)
	}
	if !strings.Contains(user, "**pricing**") || !strings.Contains(user, "# heading") {
		t.Fatalf("literal %s", user)
	}
	if !strings.Contains(user, `href="https://example.com/docs/pricing"`) || !strings.Contains(user, `rel="noopener noreferrer nofollow"`) {
		t.Fatalf("link %s", user)
	}
	if strings.HasSuffix(user, ".") && strings.Contains(user, `pricing."`) {
		t.Fatal("punctuation joined the link")
	}
	agent := string(views[1].HTML)
	if !strings.Contains(agent, "<strong>Pricing</strong>") {
		t.Fatalf("agent %s", agent)
	}
}

func TestReducedMotionCSS(t *testing.T) {
	b, err := staticFS.ReadFile("static/castbot.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, needle := range []string{
		"--act-cycle: 800ms",
		"--act-held: .55",
		"--act-show-delay: 300ms",
		"--act-min-dwell: 600ms",
		"prefers-reduced-motion: reduce",
		".actico.held i:nth-child(n) { opacity: var(--act-held); }",
		`.act:has(.actmode[data-mode="held"]) .actico i { animation: none; opacity: var(--act-held); }`,
		".actico i:nth-child(1), .actico i:nth-child(2), .actico i:nth-child(3) { opacity: 1; }",
		".actico i, .actico.slow i { animation: none; }",
		".caret { animation: none; }",
	} {
		if !strings.Contains(css, needle) {
			t.Fatalf("missing %s", needle)
		}
	}
}

func TestOpenRunQueryScopesUser(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.account("owner-s22@example.com", "Owner", "correct-horse-1")
	other, _ := h.account("other-s22@example.com", "Other", "correct-horse-1")
	sec, err := h.st.SystemSection(context.Background(), owner.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), owner.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := h.st.CreateThread(context.Background(), owner.ID, "dm", agent.ID, "Chat")
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.st.CreateRun(context.Background(), owner.ID, thread.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AppendRunEvent(context.Background(), owner.ID, run.ID, "tool", `{"kind":"bash","label":"ls","status":"running"}`, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListOpenRuns(context.Background(), owner.ID)
	if err != nil || len(rows) != 1 || rows[0].ID != run.ID || !strings.Contains(rows[0].ToolPayload, "bash") {
		t.Fatalf("owner %#v %v", rows, err)
	}
	rows, err = h.st.ListOpenRuns(context.Background(), other.ID)
	if err != nil || len(rows) != 0 {
		t.Fatalf("other %#v %v", rows, err)
	}
}
