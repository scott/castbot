//go:build shots

package web

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
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
