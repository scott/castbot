//go:build shots

package web

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

const morningPrompt = "Check docs/ for broken links and pages that disagree with docs/pricing.md. Fix what you can on a branch and tell me what you changed."

func shootRoutine(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h, user, agent := s09Shot(t, theme, kind+"-"+name)
	path, ready := s09State(t, h, user, agent, kind)
	ctx := openS07(t, browser, h, user.ID, path, width, height, ready)
	if kind == "11b2" {
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var el=document.querySelector("[name=cron]");el.value="nope";el.dispatchEvent(new Event("input",{bubbles:true}));el.dispatchEvent(new Event("change",{bubbles:true}));return true;})()`), new(bool))); err != nil {
			t.Fatal(err)
		}
		waitText(t, ctx, "Not a valid schedule. Use 5 parts: minute hour day month weekday, like 0 8 * * 1-5")
		waitText(t, ctx, "No preview until the schedule is valid")
	}
	if kind == "11f" {
		if err := chromedp.Run(ctx, chromedp.Click(`a.danger-ghost`, chromedp.ByQuery)); err != nil && !containsNav(err) {
			t.Fatal(err)
		}
		waitText(t, ctx, "Delete Morning docs check?")
		waitText(t, ctx, "Future runs stop now. This cannot be undone.")
	}
	shotPage(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func containsNav(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "navigated") || strings.Contains(err.Error(), "context"))
}

func s09Shot(t *testing.T, theme, name string) (*harness, store.User, store.Agent) {
	t.Helper()
	h := newHarness(t)
	h.srv.pauseSweeps()
	h.srv.SetDevEcho(false)
	user, _ := h.account("s09-"+theme+"-"+name+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetTimezone(context.Background(), user.ID, "America/Denver"); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready", Model: "model-large",
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, user, agent
}

func s09State(t *testing.T, h *harness, user store.User, agent store.Agent, kind string) (string, string) {
	t.Helper()
	loc := denverAt(t)
	base := "/agents/" + agent.ID
	switch kind {
	case "11a2":
		pin(h, time.Date(2026, 11, 12, 10, 0, 0, 0, loc))
		return base + "?panel=routines", `(function(){var t=document.body.innerText||"";return t.indexOf("No routines yet")>=0 && t.indexOf("Morning docs check")>=0;})()`
	case "11b":
		pin(h, time.Date(2026, 11, 12, 7, 0, 0, 0, loc))
		return base + "?panel=routines&routine=new", `(function(){var t=document.body.innerText||"";var b=document.getElementById("routine-save");if(!b)return false;var r=b.getBoundingClientRect();return t.indexOf("New routine")>=0 && t.indexOf("Today, 8:00 AM")>=0 && t.indexOf("Mon Nov 16, 8:00 AM")>=0 && t.indexOf("Every weekday at 8:00 AM")>=0 && r.top>=0 && r.bottom<=window.innerHeight && r.width>0;})()`
	case "11b2":
		pin(h, time.Date(2026, 11, 12, 10, 0, 0, 0, loc))
		routine := makeShotRoutine(t, h, user, agent, "Morning docs check", "0 8 * * 1-5", store.MissedOnce, true)
		return base + "?panel=routines&routine=" + routine.ID + "&edit=1", `(function(){var t=document.body.innerText||"";var b=document.getElementById("routine-save");if(!b)return false;var r=b.getBoundingClientRect();return t.indexOf("Edit: Morning docs check")>=0 && t.indexOf("Your zone")>=0 && r.top>=0 && r.bottom<=window.innerHeight;})()`
	case "11a":
		s09List(t, h, user, agent, loc)
		return base + "?panel=routines", `(function(){var t=document.body.innerText||"";return t.indexOf("Morning docs check")>=0 && t.indexOf("Every Wednesday at 1:00 PM")>=0 && t.indexOf("Wed Nov 18, 1:00 PM")>=0 && t.indexOf("Needs approval now")>=0 && t.indexOf("Paused")>=0 && t.indexOf("Only Atlas's routines")>=0;})()`
	case "11c", "11f":
		routine := s09Morning(t, h, user, agent, loc)
		ready := `(function(){var t=document.body.innerText||"";return t.indexOf("Run once when back")>=0 && t.indexOf("Wait 10 min, then deny")>=0 && t.indexOf("Run now")>=0;})()`
		return base + "?panel=routines&routine=" + routine.ID, ready
	case "11d":
		path := s09Running(t, h, user, agent, loc)
		return path, `(function(){var t=document.body.innerText||"";var p=(document.querySelector("textarea.ph")||{}).placeholder||"";return t.indexOf("Run now by you")>=0 && t.indexOf("Routine prompt, sent by castbot")>=0 && t.indexOf("paused for 34 min")>=0 && p.indexOf("Reply in this run's thread")>=0;})()`
	case "11d2":
		path := s09Approval(t, h, user, agent, loc)
		return path, `(function(){var t=document.body.innerText||"";return t.indexOf("Needs approval")>=0 && t.indexOf("Denied automatically in 6 min if not answered. The run finishes without it.")>=0 && t.indexOf("Tool git_push is set to Ask")>=0 && t.indexOf("Weekly link sweep")>=0;})()`
	case "11d3":
		path := s09Failed(t, h, user, agent, loc)
		return path, `(function(){var t=document.body.innerText||"";return t.indexOf("This run did not start.")>=0 && t.indexOf("WAKE_TIMEOUT")>=0 && t.indexOf("the prompt was never sent")>=0 && t.indexOf("Routine prompt, sent by castbot")<0;})()`
	case "11d4":
		path := s09Done(t, h, user, agent, loc)
		return path, `(function(){var t=document.body.innerText||"";return t.indexOf("Run done in 2 min 14 s")>=0 && t.indexOf("paused for 15 h")>=0 && t.indexOf("Routine prompt, sent by castbot")>=0;})()`
	case "11e":
		routine := s09Missed(t, h, user, agent, loc)
		return base + "?panel=routines&routine=" + routine.ID, `(function(){var t=document.body.innerText||"";return t.indexOf("castbot was offline from Sat Nov 7, 6:10 PM to Tue Nov 10, 9:13 AM")>=0 && t.indexOf("missed 2 runs (Mon and Tue, 8:00 AM)")>=0 && t.indexOf("ran once at 9:14 AM")>=0 && t.indexOf("Same outage, a routine set to skip")>=0;})()`
	case "11g":
		s09Marker(t, h, user, agent, loc)
		return base, `(function(){var t=document.body.innerText||"";var m=t.indexOf("Routine run: Morning docs check");var u=t.indexOf("Thanks. What did the morning check change?");return m>=0 && u>m && t.indexOf("Open thread")>=0 && t.indexOf("fixed 3 links")>=0;})()`
	default:
		t.Fatalf("kind %s", kind)
		return "", ""
	}
}

func s09List(t *testing.T, h *harness, user store.User, agent store.Agent, loc *time.Location) {
	t.Helper()
	pin(h, time.Date(2026, 11, 12, 6, 0, 0, 0, loc))
	morning := makeShotRoutine(t, h, user, agent, "Morning docs check", "0 8 * * 1-5", store.MissedOnce, true)
	pin(h, time.Date(2026, 11, 12, 6, 1, 0, 0, loc))
	weekly := makeShotRoutine(t, h, user, agent, "Weekly link sweep", "0 13 * * 3", store.MissedOnce, true)
	pin(h, time.Date(2026, 11, 12, 6, 2, 0, 0, loc))
	month := makeShotRoutine(t, h, user, agent, "Month-end docs report", "0 9 1 * *", store.MissedOnce, true)
	pin(h, time.Date(2026, 11, 12, 8, 0, 0, 0, loc))
	if _, err := h.st.ClaimDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, morning.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("morning runs %v %+v", err, rows)
	}
	if _, _, _, err := h.st.BeginRoutineExecution(context.Background(), user.ID, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 8, 2, 0, 0, loc))
	runRow, err := h.st.GetRun(context.Background(), user.ID, mustRunID(t, h, user.ID, rows[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishRoutineRun(context.Background(), user.ID, runRow.ID, store.RoutineDone, "Woke the machine · 2 min 14 s"); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 1, 9, 3, 0, 0, loc))
	mrow, err := h.st.ClaimManual(context.Background(), user.ID, month.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.st.BeginRoutineExecution(context.Background(), user.ID, mrow.ID); err != nil {
		t.Fatal(err)
	}
	mrun, err := h.st.GetRun(context.Background(), user.ID, mustRunID(t, h, user.ID, mrow.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishRoutineRun(context.Background(), user.ID, mrun.ID, store.RoutineDone, "Woke the machine"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.SetRoutineEnabled(context.Background(), user.ID, month.ID, false); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 9, 0, 0, 0, loc))
	wrow, err := h.st.ClaimManual(context.Background(), user.ID, weekly.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, th, wrun, err := h.st.BeginRoutineExecution(context.Background(), user.ID, wrow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateOriginMessage(context.Background(), user.ID, th.ID, "user", user.ID, "Find broken external links across docs/, fix them on a branch, and push it for review.", "routine"); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 10, 0, 0, 0, loc))
	if _, err := h.st.RecordPermission(context.Background(), user.ID, wrun.ID, []byte(`{"id":"req-shot","tool":"git_push","arguments":{"remote":"origin","branch":"docs/link-sweep-1116"}}`), h.st.Now().Add(6*time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	s09Chat(t, h, user, agent)
}

func s09Morning(t *testing.T, h *harness, user store.User, agent store.Agent, loc *time.Location) store.Routine {
	t.Helper()
	pin(h, time.Date(2026, 11, 12, 7, 0, 0, 0, loc))
	routine := makeShotRoutine(t, h, user, agent, "Morning docs check", "0 8 * * 1-5", store.MissedOnce, true)
	pin(h, time.Date(2026, 11, 12, 10, 0, 0, 0, loc))
	return routine
}

func s09Running(t *testing.T, h *harness, user store.User, agent store.Agent, loc *time.Location) string {
	t.Helper()
	pin(h, time.Date(2026, 11, 12, 7, 0, 0, 0, loc))
	routine := makeShotRoutine(t, h, user, agent, "Morning docs check", "0 8 * * 1-5", store.MissedOnce, true)
	pin(h, time.Date(2026, 11, 12, 13, 5, 0, 0, loc))
	row, err := h.st.ClaimManual(context.Background(), user.ID, routine.ID)
	if err != nil {
		t.Fatal(err)
	}
	rr, th, _, err := h.st.BeginRoutineExecution(context.Background(), user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.NoteRoutineWake(context.Background(), user.ID, rr.ID, int((34*time.Minute)/time.Millisecond), int((600*time.Millisecond)/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateOriginMessage(context.Background(), user.ID, th.ID, "user", user.ID, morningPrompt, "routine"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, th.ID, "agent", agent.ID, "Starting the docs check."); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 13, 10, 0, 0, loc))
	return "/agents/" + agent.ID + "?thread=" + th.ID
}

func s09Approval(t *testing.T, h *harness, user store.User, agent store.Agent, loc *time.Location) string {
	t.Helper()
	pin(h, time.Date(2026, 11, 11, 12, 0, 0, 0, loc))
	routine := makeShotRoutine(t, h, user, agent, "Weekly link sweep", "0 13 * * 3", store.MissedOnce, true)
	pin(h, time.Date(2026, 11, 11, 13, 0, 0, 0, loc))
	if _, err := h.st.ClaimDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("approval slot %v %+v", err, rows)
	}
	rr, th, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.NoteRoutineWake(context.Background(), user.ID, rr.ID, 0, int((500*time.Millisecond)/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateOriginMessage(context.Background(), user.ID, th.ID, "user", user.ID, "Find broken external links across docs/, fix them on a branch, and push it for review.", "routine"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, th.ID, "agent", agent.ID, "Fixed 4 external links. Ready to push:"); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 11, 13, 4, 0, 0, loc))
	if _, err := h.st.RecordPermission(context.Background(), user.ID, runRow.ID, []byte(`{"id":"req-shot","tool":"git_push","arguments":{"remote":"origin","branch":"docs/link-sweep-1116"}}`), h.st.Now().Add(6*time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	return "/agents/" + agent.ID + "?thread=" + th.ID
}

func s09Failed(t *testing.T, h *harness, user store.User, agent store.Agent, loc *time.Location) string {
	t.Helper()
	pin(h, time.Date(2026, 11, 5, 7, 0, 0, 0, loc))
	routine := makeShotRoutine(t, h, user, agent, "Morning docs check", "0 8 * * 1-5", store.MissedOnce, true)
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, "castbot-atlas", "example.invalid/castbot-runtime:s09"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-shot", "https://vm.example", "paused", ""); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 5, 8, 0, 0, 0, loc))
	if _, err := h.st.ClaimDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("failed slot %v %+v", err, rows)
	}
	_, th, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateOriginMessage(context.Background(), user.ID, th.ID, "user", user.ID, morningPrompt, "routine"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AddVMEventCode(context.Background(), user.ID, agent.ID, store.EventWakeFailed, store.WakeTimeout, 90*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.FailRun(context.Background(), user.ID, runRow.ID, "The machine did not wake.", 0); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishRoutineRun(context.Background(), user.ID, runRow.ID, store.RoutineFailed, "The machine did not wake"); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 10, 0, 0, 0, loc))
	return "/agents/" + agent.ID + "?thread=" + th.ID
}

func s09Done(t *testing.T, h *harness, user store.User, agent store.Agent, loc *time.Location) string {
	t.Helper()
	pin(h, time.Date(2026, 11, 12, 7, 0, 0, 0, loc))
	routine := makeShotRoutine(t, h, user, agent, "Morning docs check", "0 8 * * 1-5", store.MissedOnce, true)
	pin(h, time.Date(2026, 11, 12, 8, 0, 0, 0, loc))
	if _, err := h.st.ClaimDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("done slot %v %+v", err, rows)
	}
	rr, th, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.NoteRoutineWake(context.Background(), user.ID, rr.ID, int((15*time.Hour)/time.Millisecond), int((600*time.Millisecond)/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateOriginMessage(context.Background(), user.ID, th.ID, "user", user.ID, morningPrompt, "routine"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, th.ID, "agent", agent.ID, "I fixed 3 broken links in docs/setup.md and docs/faq.md on the branch docs/fix-links. docs/pricing.md and the FAQ agree. Nothing was pushed."); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 8, 2, 14, 0, loc))
	if err := h.st.FinishRoutineRun(context.Background(), user.ID, runRow.ID, store.RoutineDone, "Woke the machine · 2 min 14 s"); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 10, 0, 0, 0, loc))
	return "/agents/" + agent.ID + "?thread=" + th.ID
}

func s09Missed(t *testing.T, h *harness, user store.User, agent store.Agent, loc *time.Location) store.Routine {
	t.Helper()
	pin(h, time.Date(2026, 11, 7, 18, 10, 0, 0, loc))
	morning := makeShotRoutine(t, h, user, agent, "Morning docs check", "0 8 * * 1-5", store.MissedOnce, true)
	weekend := makeShotRoutine(t, h, user, agent, "Weekend changelog check", "0 18 * * 0", store.MissedSkip, true)
	if _, err := h.st.ClaimDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 10, 9, 13, 0, 0, loc))
	if _, err := h.st.ClaimDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, morning.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var late store.RoutineRun
	for _, row := range rows {
		if row.Status == store.RoutineLate {
			late = row
		}
	}
	if late.ID == "" {
		t.Fatalf("no late row %+v", rows)
	}
	pin(h, time.Date(2026, 11, 10, 9, 14, 0, 0, loc))
	_, _, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, late.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishRoutineRun(context.Background(), user.ID, runRow.ID, store.RoutineLate, "castbot was offline · ran once at 9:14 AM"); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 11, 22, 0, 0, 0, loc))
	if _, err := h.st.UpdateRoutine(context.Background(), user.ID, morning.ID, store.RoutineInput{
		Name: morning.Name, Cron: morning.Cron, Timezone: "America/Denver", Prompt: morningPrompt,
		Enabled: true, ApprovalPolicy: store.ApprovalWait, MissedPolicy: store.MissedOnce,
	}); err != nil {
		t.Fatal(err)
	}
	_ = weekend
	return morning
}

func s09Marker(t *testing.T, h *harness, user store.User, agent store.Agent, loc *time.Location) {
	t.Helper()
	pin(h, time.Date(2026, 11, 12, 7, 0, 0, 0, loc))
	routine := makeShotRoutine(t, h, user, agent, "Morning docs check", "0 8 * * 1-5", store.MissedOnce, true)
	pin(h, time.Date(2026, 11, 12, 8, 0, 0, 0, loc))
	if _, err := h.st.ClaimDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := h.st.ListRoutineRuns(context.Background(), user.ID, routine.ID, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("marker %v %+v", err, rows)
	}
	_, th, runRow, err := h.st.BeginRoutineExecution(context.Background(), user.ID, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, th.ID, "agent", agent.ID, "fixed 3 links on docs/fix-links"); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 8, 2, 0, 0, loc))
	if err := h.st.FinishRoutineRun(context.Background(), user.ID, runRow.ID, store.RoutineDone, "Woke the machine · 2 min 14 s"); err != nil {
		t.Fatal(err)
	}
	pin(h, time.Date(2026, 11, 12, 10, 0, 0, 0, loc))
	s09Chat(t, h, user, agent)
}

func s09Chat(t *testing.T, h *harness, user store.User, agent store.Agent) {
	t.Helper()
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var thread store.Thread
	for _, th := range threads {
		if th.Kind == "dm" {
			thread = th
			break
		}
	}
	if thread.ID == "" {
		t.Fatal("no dm thread")
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, "user", user.ID, "Thanks. What did the morning check change?"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, "agent", agent.ID, "It fixed 3 broken links in docs/setup.md and docs/faq.md on the branch docs/fix-links. Nothing was pushed. The details are in the routine thread."); err != nil {
		t.Fatal(err)
	}
}

func makeShotRoutine(t *testing.T, h *harness, user store.User, agent store.Agent, name, cronExpr, missed string, on bool) store.Routine {
	t.Helper()
	routine, err := h.st.CreateRoutine(context.Background(), user.ID, agent.ID, store.RoutineInput{
		Name: name, Cron: cronExpr, Timezone: "America/Denver", Prompt: morningPrompt,
		Enabled: on, ApprovalPolicy: store.ApprovalWait, MissedPolicy: missed,
	})
	if err != nil {
		t.Fatal(err)
	}
	return routine
}

func mustRunID(t *testing.T, h *harness, userID, routineRunID string) string {
	t.Helper()
	row, err := h.st.GetRoutineRun(context.Background(), userID, routineRunID)
	if err != nil || row.RunID == "" {
		t.Fatalf("run id %v %+v", err, row)
	}
	return row.RunID
}

func denverAt(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func pin(h *harness, at time.Time) {
	frozen := at
	h.st.SetNow(func() time.Time { return frozen })
}
