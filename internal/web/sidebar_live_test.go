package web

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/run"
)

func TestChatCreateSidebarFrame(t *testing.T) {
	clearServerProviderKeys(t)
	h := newHarness(t)
	user, _ := h.account("side-create@example.com", "Ada", "correct-horse-1")
	addModelKey(t, h, user.ID)
	other, _ := h.account("side-other@example.com", "Bea", "correct-horse-1")
	addModelKey(t, h, other.ID)
	caller := seedCaller(t, h, user, "Ada")
	_ = seedCaller(t, h, other, "Bea")
	token := guestToken(t, h, caller)
	th, err := h.st.DMThread(context.Background(), user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	desc := "Sort the support inbox and draft replies for me to check."
	code, body := machinePost(t, h, "203.0.113.250:9", token, caller.ID, "/agents", createPayload(desc, "Quill", other.ID))
	if code != 200 || strings.Contains(body, `"error":true`) {
		t.Fatalf("create %d %s", code, body)
	}
	made := agentNamed(t, h, user.ID, "Quill")
	var buf bytes.Buffer
	if err := h.srv.writeSidebar(context.Background(), &buf, user, runRow, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	frame := buf.String()
	for _, needle := range []string{
		`id="sb-kind-agents"`,
		`hx-swap-oob="outerHTML"`,
		`data-name="Quill"`,
		`/agents/` + made.ID,
		`data-name="Ada"`,
		`class="ag on"`,
		`aria-current="page"`,
		`name="csrf" value="csrf-token"`,
	} {
		if !strings.Contains(frame, needle) {
			t.Fatalf("missing %s in %s", needle, frame)
		}
	}
	if strings.Contains(frame, "<!doctype") || strings.Contains(frame, `id="stage"`) || strings.Contains(frame, `class="sb"`) || strings.Contains(frame, `data-name="Bea"`) {
		t.Fatalf("frame replaced the page %s", frame)
	}
	if strings.Contains(frame, `class="ag on" href="/agents/`+made.ID) {
		t.Fatal("new agent took the current selection")
	}
	buf.Reset()
	if err := h.srv.writeSidebar(context.Background(), &buf, user, runRow, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("second frame %s", buf.String())
	}
}

func TestChatCreateRejectAddsNoSidebarRow(t *testing.T) {
	clearServerProviderKeys(t)
	h := newHarness(t)
	user, _ := h.account("side-bad@example.com", "Ada", "correct-horse-1")
	addModelKey(t, h, user.ID)
	caller := seedCaller(t, h, user, "Ada")
	token := guestToken(t, h, caller)
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ToggleFold(context.Background(), user.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	th, err := h.st.DMThread(context.Background(), user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := len(agentRows(t, h, user.ID))
	code, body := machinePost(t, h, "203.0.113.251:9", token, caller.ID, "/agents", createPayload("hello", "Nope", ""))
	if code != 200 || !strings.Contains(body, `"error":true`) || len(agentRows(t, h, user.ID)) != before {
		t.Fatalf("reject %d %s", code, body)
	}
	var buf bytes.Buffer
	if err := h.srv.writeSidebar(context.Background(), &buf, user, runRow, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Fatalf("reject frame %s", buf.String())
	}
	folds, err := h.st.FoldedSections(context.Background(), user.ID)
	if err != nil || !folds[sec.ID] {
		t.Fatalf("fold changed %+v %v", folds, err)
	}
}

func TestFenceDoneFrameListsNewAgent(t *testing.T) {
	clearServerProviderKeys(t)
	h := newHarness(t)
	user, _ := h.account("side-fence@example.com", "Ada", "correct-horse-1")
	addModelKey(t, h, user.ID)
	other, _ := h.account("side-fence-other@example.com", "Bea", "correct-horse-1")
	caller := seedCaller(t, h, user, "Ada")
	_ = seedCaller(t, h, other, "Bea")
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ToggleFold(context.Background(), user.ID, sec.ID); err != nil {
		t.Fatal(err)
	}
	th, err := h.st.DMThread(context.Background(), user.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), user.ID, th.ID, caller.ID)
	if err != nil {
		t.Fatal(err)
	}
	desc := "Sort the support inbox and draft replies for me to check."
	fence := "I will add that.\n\n```castbot-create\n{\"description\":\"" + desc + "\",\"name\":\"Quill\"}\n```\n"
	msg, ev, err := h.st.FinishRun(context.Background(), user.ID, runRow.ID, fence, 0)
	if err != nil {
		t.Fatal(err)
	}
	runRow, err = h.st.GetRun(context.Background(), user.ID, runRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	fw := &frameWriter{w: &buf, seq: ev.Seq, from: -1}
	if err := h.srv.writeDone(context.Background(), fw, user, runRow, msg.ID, "csrf-token"); err != nil {
		t.Fatal(err)
	}
	frame := buf.String()
	made := agentNamed(t, h, user.ID, "Quill")
	if made.ID == "" || made.UserID != user.ID {
		t.Fatal("fence did not create")
	}
	for _, needle := range []string{`id="sb-kind-agents"`, `hx-swap-oob="outerHTML"`, `data-name="Quill"`, `data-name="Ada"`, `class="ag on"`, `aria-expanded="true"`, "I will add that."} {
		if !strings.Contains(frame, needle) {
			t.Fatalf("missing %s in %s", needle, frame)
		}
	}
	if strings.Contains(frame, "castbot-create") || strings.Contains(frame, `data-name="Bea"`) || strings.Contains(frame, "<!doctype") || strings.Contains(frame, `id="stage"`) {
		t.Fatalf("bad frame %s", frame)
	}
	folds, err := h.st.FoldedSections(context.Background(), user.ID)
	if err != nil || folds[sec.ID] {
		t.Fatalf("section stayed collapsed %+v %v", folds, err)
	}
	before := len(agentRows(t, h, user.ID))
	h.srv.maybeCreate(run.Job{UserID: user.ID, ThreadID: th.ID, AgentID: caller.ID, RunID: runRow.ID})
	if len(agentRows(t, h, user.ID)) != before {
		t.Fatal("hook created a second agent")
	}
}
