//go:build shots

package web

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/store"
)

func shootNoMachineComposer(t *testing.T, browser context.Context, dir string, width, height int, theme, name string) {
	t.Helper()
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	h.srv.SetHost(host.NewFake(), "example.invalid/castbot-runtime:s07", "https://castbot.example")
	user, _ := h.account("nomachine-"+theme+"-"+name+"@example.com", "Start User", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := openS07(t, browser, h, user.ID, "/agents/"+agent.ID, width, height, `(function(){var t=document.body.innerText||"";return t.indexOf("No machine yet. Start it to chat.")>=0 && t.indexOf("Enter to send")<0 && document.querySelector("#composer textarea")!=null;})()`)
	if err := chromedp.Run(ctx, chromedp.Focus(`#composer textarea`, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(js(`document.body.innerText||""`), &text)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "Enter to send") {
		t.Fatal("hint is visible with no machine")
	}
	shotPage(t, ctx, filepath.Join(dir, "s07c-nomachine-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func shootReplace(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h, agent, path, ready := replaceShot(t, theme, kind)
	ctx := openS07(t, browser, h, agent.UserID, path, width, height, ready)
	if kind == "09b" || kind == "09d" {
		if err := chromedp.Run(ctx, chromedp.Click(`a[href*="/machine/replace"]`, chromedp.ByQuery)); err != nil && !strings.Contains(err.Error(), "navigated") {
			t.Fatal(err)
		}
		waitText(t, ctx, "Replace this machine?")
		if kind == "09b" {
			shotPage(t, ctx, filepath.Join(dir, "09b-"+theme+"-"+name+".png"))
			assertShotClean(t, ctx)
			return
		}
		if err := chromedp.Run(ctx, chromedp.Click(`form.rm-f button.btn.pri`, chromedp.ByQuery)); err != nil && !strings.Contains(err.Error(), "navigated") {
			t.Fatal(err)
		}
		waitText(t, ctx, "Machine replaced.")
	}
	shotPage(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
	assertShotClean(t, ctx)
}

func waitText(t *testing.T, ctx context.Context, needle string) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	var last error
	var text string
	for time.Now().Before(deadline) {
		last = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:""`), &text))
		if last == nil && strings.Contains(text, needle) {
			return
		}
		if last != nil && !strings.Contains(last.Error(), "navigated") && !strings.Contains(last.Error(), "context") {
			t.Fatal(last)
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("missing %q: %v\n%s", needle, last, text)
}

func replaceShot(t *testing.T, theme, kind string) (*harness, store.Agent, string, string) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	target := "example.invalid/castbot-runtime:ff6d7ea"
	current := target
	if kind != "09a2" {
		current = "example.invalid/castbot-runtime:7b5e414"
	}
	h.srv.SetHost(fake, target, "https://castbot.example")
	user, _ := h.account("shot-s07c@example.com", "Ada", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	status := "ready"
	if kind == "09g" {
		status = "draft"
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: status, Model: "model-large",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.UserID = user.ID
	path := "/agents/" + agent.ID + "/machine"
	if kind == "09g" {
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("has no machine right now")>=0;})()`
	}
	state := "running"
	if kind == "09a" || kind == "09b" || kind == "09d" {
		state = "paused"
	}
	if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, "castbot-old", current); err != nil {
		t.Fatal(err)
	}
	if kind == "09f3" {
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("The machine is still starting.")>=0;})()`
	}
	if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-old", "https://old.example", state, ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agent.ID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agent.ID, token); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-old"] = host.Machine{ID: "vm-old", Name: "castbot-old", State: state, Endpoint: "https://old.example", Image: current, Tags: []string{"agent:" + agent.ID}}
	fake.SeedDisk("https://old.example", []byte("mem"), map[string][]byte{"a.txt": []byte("aaa")})
	fake.OnCreate = func(spec host.CreateSpec) {
		next, err := h.st.ExchangeBootstrap(context.Background(), spec.AgentID, spec.Bootstrap)
		if err != nil {
			return
		}
		_, _ = h.st.PlaceVMToken(context.Background(), spec.AgentID, next)
	}
	ctx := context.Background()
	switch kind {
	case "09c", "09c2":
		row, _, err := h.st.BeginReplacement(ctx, user.ID, agent.ID, "web:"+user.Email, target, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.SetReplacementStep(ctx, user.ID, row.ID, "copy"); err != nil {
			t.Fatal(err)
		}
		if err := h.st.NoteReplacementCopy(ctx, user.ID, row.ID, true, 2202010, 7, 12, 4096); err != nil {
			t.Fatal(err)
		}
		if kind == "09c2" {
			path = "/agents/" + agent.ID
			return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("Replacing the machine: copying memory and files.")>=0;})()`
		}
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("7 of 12 files")>=0;})()`
	case "09f4":
		if _, _, err := h.st.BeginReplacement(ctx, user.ID, agent.ID, "cli", target, false); err != nil {
			t.Fatal(err)
		}
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("A replacement is already running.")>=0;})()`
	case "09e":
		row, _, err := h.st.BeginReplacement(ctx, user.ID, agent.ID, "web:"+user.Email, target, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.FailReplacement(ctx, user.ID, row.ID, "read_old", "read_old"); err != nil {
			t.Fatal(err)
		}
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("The old machine could not be read.")>=0;})()`
	case "09f":
		if _, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID); err != nil {
			t.Fatal(err)
		}
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("Finish or stop the reply in progress first.")>=0;})()`
	case "09f2":
		run, err := h.st.CreateRun(ctx, user.ID, thread.ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.st.RecordPermission(ctx, user.ID, run.ID, json.RawMessage(`{"id":"req-1","tool":"read","arguments":{}}`), time.Now().Add(time.Hour), 0); err != nil {
			t.Fatal(err)
		}
		if _, _, err := h.st.FinishRun(ctx, user.ID, run.ID, "done", 1); err != nil {
			t.Fatal(err)
		}
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("Answer the waiting approval first.")>=0;})()`
	case "09d2":
		if _, _, err := h.st.IssueCLIToken(ctx, user.ID, agent.ID); err != nil {
			t.Fatal(err)
		}
		fake.DeleteErr = errors.New("delete failed")
		if _, err := replace.Run(ctx, h.st, fake, replace.Options{
			UserID: user.ID, AgentID: agent.ID, ActorKind: "user", ActorID: user.ID, Actor: "web:" + user.Email,
			Image: target, PublicURL: "https://castbot.example", DataDir: t.TempDir(), BootWait: time.Second,
		}); err == nil {
			t.Fatal("expected delete pending")
		}
		return h, agent, path, `(function(){var t=document.body.innerText||"";return t.indexOf("Machine replaced.")>=0&&t.indexOf("not deleted yet")>=0;})()`
	case "09a2":
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("is on the current runtime")>=0;})()`
	default:
		return h, agent, path, `(function(){return (document.body.innerText||"").indexOf("A newer runtime is available")>=0;})()`
	}
}
