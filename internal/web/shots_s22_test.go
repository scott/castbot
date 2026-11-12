//go:build shots

package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

func shootS22(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h, page, script, needle, userID := s22Shot(t, theme, kind)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	actions := cliOpen(ts.URL, sessionToken(t, h, userID), ts.URL+page, width, height)
	if kind == "15d-still" {
		actions = append([]chromedp.Action{chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{{
				Name:  "prefers-reduced-motion",
				Value: "reduce",
			}}).Do(ctx)
		})}, actions...)
	}
	if err := chromedp.Run(ctx, actions...); err != nil {
		t.Fatal(err)
	}
	if script != "" {
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(script), nil)); err != nil {
			t.Fatal(err)
		}
	}
	if needle != "" {
		if err := chromedp.Run(ctx, chromedp.Poll(js(needle), nil, chromedp.WithPollingTimeout(8*time.Second))); err != nil {
			var text string
			_ = chromedp.Run(ctx, chromedp.Evaluate(js(`document.body?document.body.innerText:''`), &text))
			t.Fatalf("needle: %v\n%s", err, text)
		}
	}
	assertNoSideScroll(t, ctx)
	assertShotClean(t, ctx)
	if width <= 400 {
		var bad string
		if err := chromedp.Run(ctx, chromedp.Evaluate(js(`(function(){var notes=[];[".msg-acts .ibtn",".md-code-h .copy",".m-agent .calm .acts .btn",".netbar .btn"].forEach(function(s){document.querySelectorAll(s).forEach(function(el){if(el.offsetParent===null)return;var r=el.getBoundingClientRect();if(r.width<44||r.height<44)notes.push(s+" "+Math.round(r.width)+"x"+Math.round(r.height));});});return notes.join("; ");})()`), &bad)); err != nil {
			t.Fatal(err)
		}
		if bad != "" {
			t.Fatalf("tap target %s", bad)
		}
	}
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
}

func s22Shot(t *testing.T, theme, kind string) (*harness, string, string, string, string) {
	t.Helper()
	h := newHarness(t)
	user, _ := h.account("s22-"+kind+"-"+theme+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	name := "Atlas"
	if kind == "15e3" || kind == "15d4" {
		name = "Wren"
	}
	agent, thread, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: name, Title: "Docs helper", Status: "ready", Color: "c1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind == "15d4" {
		if _, err := h.st.BeginVM(context.Background(), user.ID, agent.ID, "wren", "example.invalid/castbot-runtime:s22"); err != nil {
			t.Fatal(err)
		}
		if err := h.st.FinishVM(context.Background(), user.ID, agent.ID, "vm-s22", "https://vm.example", "paused", ""); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 11, 12, 21, 51, 0, 0, time.UTC)
	h.st.SetNow(func() time.Time { return start })
	ask := "Compare the three pricing tiers for the FAQ and give me the rollout steps."
	if kind == "15f" {
		ask = "Before you push, check:\n- the **pricing** lines in docs/faq.md\n- the GITHUB_TOKEN note in docs/setup_guide.md\n\nhttps://example.com/docs/pricing\n\n# fix these too"
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, "user", user.ID, ask); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "15a", "15f":
		s22Finish(t, h, user.ID, thread.ID, agent.ID, s22Reply(kind))
	case "15e":
		s22Fail(t, h, user.ID, thread.ID, agent.ID, s22Partial(), "The model provider did not answer for 60 s.")
	case "15e3":
		s22Fail(t, h, user.ID, thread.ID, agent.ID, "", "The machine did not wake up.")
	default:
		body := ""
		switch kind {
		case "15b", "15d-still":
			body = ""
		case "15b2":
			body = "```bash\ngit checkout -b docs/pricing-faq\n"
		case "15d2", "15e2":
			body = "The Spanish FAQ has the same three answers. I am running the lint now.\n"
		}
		if kind == "15b" {
			body = "## Pricing tiers\n\nThe FAQ is **out of date**.\n\n- Update `docs/faq.md`\n- Run the docs li"
		}
		run := s22Stream(t, h, user.ID, thread.ID, agent.ID, body)
		if kind == "15d2" || kind == "15e2" {
			payload, _ := json.Marshal(map[string]string{"kind": "bash", "label": "python scripts/lint_docs.py docs/faq.md", "status": "running"})
			if _, err := h.st.AppendRunEvent(context.Background(), user.ID, run.ID, "tool", string(payload), 2); err != nil {
				t.Fatal(err)
			}
		}
		if kind == "15d3" {
			raw := []byte(`{"id":"req-s22","tool":"bash","arguments":{"command":"git push origin docs/pricing-faq"}}`)
			if _, err := h.st.RecordPermission(context.Background(), user.ID, run.ID, raw, start.Add(time.Hour), 2); err != nil {
				t.Fatal(err)
			}
		}
	}
	h.st.SetNow(func() time.Time { return start.Add(14 * time.Second) })
	script := ""
	needle := `document.body.innerText.indexOf("Replying")<0`
	switch kind {
	case "15a":
		needle += `&&document.body.innerText.indexOf("Pricing tiers")>=0&&!!document.querySelector(".md-table")&&!!document.querySelector("[aria-label='Copy message as markdown']")`
	case "15b":
		needle += `&&document.querySelector(".md[aria-busy='true']")&&(function(){var c=document.querySelector(".caret");return !!(c&&getComputedStyle(c).visibility==="visible");})()`
	case "15b2":
		needle += `&&document.body.innerText.indexOf("Writing")>=0&&!!document.querySelector(".md-code.open")`
	case "15d", "15d-still":
		needle += `&&document.body.innerText.indexOf("Thinking")>=0` + actShown("Thinking")
	case "15d2":
		needle += `&&document.body.innerText.indexOf("Running command")>=0` + actShown("Running command")
	case "15d3":
		needle += `&&document.body.innerText.indexOf("Waiting for approval")>=0&&Array.prototype.some.call(document.querySelectorAll(".sub"),function(n){return n.textContent.indexOf("needs you")>=0})` + actShown("Waiting for approval")
	case "15d4":
		needle += `&&document.body.innerText.indexOf("Waking up")>=0&&document.body.innerText.indexOf("Waking")>=0` + actShown("Waking up")
	case "15e":
		needle += `&&document.body.innerText.indexOf("Reply stopped before it finished.")>=0&&document.body.innerText.indexOf("Retry")>=0`
	case "15e2":
		script = `function showBar(){var bar=document.getElementById("netbar");if(bar){bar.hidden=false;bar.classList.add("on");}}document.addEventListener("htmx:sseOpen",showBar);showBar();var act=document.querySelector(".pending .act");if(act){act.innerHTML='<span class="actico slow" aria-hidden="true"><i></i><i></i><i></i><i></i><i></i><i></i></span><span class="lbl">Reconnecting</span>';}var hold=document.querySelector(".act-hold");if(hold)hold.hidden=false;var sub=document.querySelector(".ag.on .sub");if(sub){sub.className="sub act-sub";sub.innerHTML='<span class="actico slow sm" aria-hidden="true"><i></i><i></i><i></i><i></i><i></i><i></i></span><span>Reconnecting</span>';}`
		needle += `&&document.body.innerText.indexOf("Reconnecting")>=0` + actShown("Reconnecting") + `&&(function(){var bar=document.getElementById("netbar");var r=bar&&bar.getBoundingClientRect();return !!(bar&&!bar.hidden&&r&&r.height>20&&r.top<780&&bar.textContent.indexOf("Connection lost.")>=0);})()`
	case "15e3":
		needle += `&&document.body.innerText.indexOf("did not wake up.")>=0&&document.body.innerText.indexOf("Try again")>=0`
	case "15f":
		needle += `&&document.body.innerText.indexOf("**pricing**")>=0&&document.body.innerText.indexOf("# fix these too")>=0&&!!document.querySelector(".m-user a")`
	}
	return h, "/agents/" + agent.ID, script, `(` + needle + `)`, user.ID
}

func actShown(label string) string {
	return `&&(function(){var a=document.querySelector(".pending .act");if(!a)return false;return a.innerText.indexOf("` + label + `")>=0&&parseFloat(getComputedStyle(a).opacity||"0")>0.85;})()`
}

func s22Reply(kind string) string {
	if kind == "15f" {
		return "Checked all three. The **pricing** lines match."
	}
	return "## Pricing tiers for the FAQ\n\nI read `docs/pricing.md`. The FAQ is **out of date in three answers**.\n\n### What each tier includes\n\n| Tier | Price | Seats |\n| --- | ---: | --- |\n| Starter | $0 | 1 |\n| Team | $24 | 50 |\n\n> Note: pricing.md is the source of truth.\n\n### Rollout steps\n\n1. Update three answers in `docs/faq.md`\n   - How much does it cost?\n2. Run the docs lint\n\n```bash\ngit checkout -b docs/pricing-faq\n```\n\nSources: [pricing.md](https://example.com/pricing).\n"
}

func s22Partial() string {
	return "## Spanish FAQ\n\nThe Spanish FAQ has the same three answers out of date:\n\n1. Cuanto cuesta?\n2. Puedo cambiar de plan?\n"
}

func s22Finish(t *testing.T, h *harness, userID, threadID, agentID, body string) {
	t.Helper()
	run, err := h.st.CreateRun(context.Background(), userID, threadID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), userID, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.st.FinishRun(context.Background(), userID, run.ID, body, 1); err != nil {
		t.Fatal(err)
	}
}

func s22Stream(t *testing.T, h *harness, userID, threadID, agentID, text string) store.Run {
	t.Helper()
	run, err := h.st.CreateRun(context.Background(), userID, threadID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), userID, run.ID); err != nil {
		t.Fatal(err)
	}
	if text != "" {
		payload, _ := json.Marshal(map[string]string{"text": text})
		if _, err := h.st.AppendRunEvent(context.Background(), userID, run.ID, "text", string(payload), 1); err != nil {
			t.Fatal(err)
		}
	}
	stop := h.srv.runs.PinWatch(run.ID, agentID)
	t.Cleanup(stop)
	return run
}

func s22Fail(t *testing.T, h *harness, userID, threadID, agentID, text, message string) {
	t.Helper()
	run := s22Stream(t, h, userID, threadID, agentID, text)
	if _, err := h.st.FailRun(context.Background(), userID, run.ID, message, 2); err != nil {
		t.Fatal(err)
	}
}
