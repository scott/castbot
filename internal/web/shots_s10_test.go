//go:build shots

package web

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

func shootS10(t *testing.T, browser context.Context, dir string, width, height int, theme, name, kind string) {
	t.Helper()
	h, page, script, needle, userID := s10Shot(t, theme, kind, width)
	ts := httptest.NewTLSServer(h.srv.Handler())
	t.Cleanup(ts.Close)
	ctx := tab(t, browser)
	if err := chromedp.Run(ctx, cliOpen(ts.URL, sessionToken(t, h, userID), ts.URL+page, width, height)...); err != nil {
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
	saveShot(t, ctx, filepath.Join(dir, kind+"-"+theme+"-"+name+".png"))
}

func s10Shot(t *testing.T, theme, kind string, width int) (*harness, string, string, string, string) {
	t.Helper()
	h := newHarness(t)
	fake := host.NewFake()
	h.srv.SetHost(fake, "example.invalid/castbot-runtime:s10", "https://castbot.example")
	h.srv.SetDataDir(t.TempDir())
	user, _ := h.account("s10-"+kind+"-"+theme+"@example.com", "Alex Rivera", "correct-horse-1")
	if err := h.st.UpdateTheme(context.Background(), user.ID, theme); err != nil {
		t.Fatal(err)
	}
	sec, err := h.st.SystemSection(context.Background(), user.ID, "agents")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Docs helper", Status: "ready",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := "running"
	if kind == "12e" || kind == "12i" {
		state = "paused"
	}
	if kind != "12j" {
		s10Machine(t, h, fake, user.ID, agent.ID, state)
	}
	now := mst(2026, 11, 12, 14, 43)
	switch kind {
	case "12e":
		now = mst(2026, 11, 12, 12, 46)
		s10SeedFiles(t, h, fake, user.ID, agent.ID, bytes.Repeat([]byte("p"), 3*1024), now)
		s10Upload(t, h, user.ID, agent.ID, "brand-guide.pdf", "copied", int64(41*1024*1024/10), mst(2026, 11, 3, 9, 0))
		s10Upload(t, h, user.ID, agent.ID, "tiers-2026.csv", "copied", 12*1024, mst(2026, 11, 6, 9, 0))
		s10Upload(t, h, user.ID, agent.ID, "q4-numbers.xlsx", "held", int64(23*1024*1024/10), now)
	case "12g":
		now = mst(2026, 11, 12, 14, 42)
		body := []byte("# Pricing\n\nThe public tiers live in this file.\n\n| Plan | Price |\n| --- | --- |\n| Free | 0 |\n| Pro | 20 |\n| Team | 40 |\nAsk Atlas before changing a number.")
		s10SeedFiles(t, h, fake, user.ID, agent.ID, body, now)
	case "12j":
	case "12i":
		s10SeedFiles(t, h, fake, user.ID, agent.ID, bytes.Repeat([]byte("p"), 3*1024), now)
		for _, name := range []string{"Ledger", "Pip", "Wren"} {
			if _, _, err := h.st.CreateAgent(context.Background(), user.ID, store.AgentInput{SectionID: sec.ID, Name: name, Title: "Helper", Status: "ready"}); err != nil {
				t.Fatal(err)
			}
		}
	default:
		s10SeedFiles(t, h, fake, user.ID, agent.ID, bytes.Repeat([]byte("p"), 3*1024), now)
		s10Upload(t, h, user.ID, agent.ID, "brand-guide.pdf", "copied", int64(41*1024*1024/10), mst(2026, 11, 3, 9, 0))
		s10Upload(t, h, user.ID, agent.ID, "tiers-2026.csv", "copied", 12*1024, mst(2026, 11, 6, 9, 0))
	}
	h.st.SetNow(func() time.Time { return now })
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	var thread store.Thread
	for _, th := range threads {
		if th.Kind != "cli" {
			thread = th
			break
		}
	}
	if thread.ID == "" {
		thread, err = h.st.CreateThread(context.Background(), user.ID, "dm", agent.ID, "Chat")
		if err != nil {
			t.Fatal(err)
		}
	}
	userText := "Can you turn tiers-2026.csv into a pricing table in docs/pricing.md?"
	if kind == "12f" {
		userText += "\n\nAtlas got the file at workspace/uploads/tiers-2026.csv, named in your message."
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, "user", user.ID, userText); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateMessage(context.Background(), user.ID, thread.ID, "agent", agent.ID, "Done. The table is in docs/pricing.md."); err != nil {
		t.Fatal(err)
	}
	if kind == "12k" || kind == "12k2" {
		h.srv.SetDiskFree(func(string) (uint64, error) { return 1 << 30, nil })
	}
	page := "/agents/" + agent.ID + "?panel=files"
	script := draftJS
	needle := `(document.body.innerText||'').indexOf('faq.md')>=0`
	switch kind {
	case "12a2":
		page += "&view=uploads"
		needle = `(document.body.innerText||'').indexOf('tiers-2026.csv')>=0&&(document.body.innerText||'').indexOf('uploaded by you Fri')>=0`
	case "12b":
		script += queueJS
		needle = `(document.body.innerText||'').indexOf('17.6 of 28.4 MB')>=0&&(document.body.innerText||'').indexOf('Waiting')>=0`
	case "12c":
		if width >= 761 {
			script += `document.dispatchEvent(new Event('dragenter'));`
			needle = `!!(document.getElementById('fdrop')&&!document.getElementById('fdrop').hidden)`
		}
	case "12d":
		script += tooBigJS
		needle = `(document.body.innerText||'').indexOf('Too large. Files can be up to 32 MB.')>=0&&(document.body.innerText||'').indexOf('Nothing was saved.')>=0&&!!document.querySelector('[aria-label="Dismiss training-video.mp4"]')`
	case "12e":
		page += "&view=uploads"
		script = ""
		needle = `(document.body.innerText||'').indexOf('Opening Files does not wake it.')>=0&&(document.body.innerText||'').indexOf('Saved in castbot.')>=0&&(document.body.innerText||'').indexOf('12:46 PM')>=0`
	case "12f":
		page = "/agents/" + agent.ID
		script = attachJS
		needle = `(document.body.innerText||'').indexOf('named in your message.')>=0&&document.getElementById('send-btn').disabled&&document.getElementById('send-hint').textContent.indexOf('Send is ready when the upload finishes')>=0&&!!document.querySelector('[aria-label="Download pricing.md"]')`
	case "12g":
		cached, err := h.st.ListCached(context.Background(), user.ID, agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		for _, row := range cached {
			if row.Name == "docs/pricing.md" {
				id = row.ID
			}
		}
		if id == "" {
			t.Fatal("pricing.md was not cached")
		}
		page += "&file=" + id
		script = ""
		needle = `(document.body.innerText||'').indexOf('Mention in chat')>=0&&(document.body.innerText||'').indexOf('Today 2:42 PM')>=0&&(document.body.innerText||'').indexOf('first 10 of 10 lines')>=0`
	case "12i":
		page = "/?q=pricing"
		script = ""
		needle = `(document.body.innerText||'').indexOf('Files, all your agents')>=0&&(document.body.innerText||'').indexOf('pricing.md')>=0`
	case "12j":
		script = ""
		needle = `(document.body.innerText||'').indexOf('No files yet')>=0&&(document.body.innerText||'').indexOf('Upload a file')>=0`
	case "12k":
		script = ""
		needle = `!!(document.getElementById('upload-btn')&&document.getElementById('upload-btn').disabled)&&(document.body.innerText||'').indexOf('The server is low on space.')>=0`
	case "12k2":
		page = "/agents/" + agent.ID
		script = `document.getElementById('attach').click();`
		needle = `!!(document.getElementById('low-space-note')&&!document.getElementById('low-space-note').hidden&&document.getElementById('low-space-note').getAttribute('role')==='alert'&&document.getElementById('attach').disabled)`
	}
	return h, page, script, needle, user.ID
}

func s10Machine(t *testing.T, h *harness, fake *host.Fake, userID, agentID, state string) {
	t.Helper()
	if _, err := h.st.BeginVM(context.Background(), userID, agentID, host.MachineName(agentID), "example.invalid/castbot-runtime:s10"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FinishVM(context.Background(), userID, agentID, "vm-shot", "https://vm.example", state, ""); err != nil {
		t.Fatal(err)
	}
	plain, err := h.st.IssueBootstrap(context.Background(), userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := h.st.ExchangeBootstrap(context.Background(), agentID, plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SaveVMToken(context.Background(), agentID, token); err != nil {
		t.Fatal(err)
	}
	fake.Machines["vm-shot"] = host.Machine{ID: "vm-shot", State: state, Endpoint: "https://vm.example"}
	fake.SeedDisk("https://vm.example", nil, map[string][]byte{})
}

func s10SeedFiles(t *testing.T, h *harness, fake *host.Fake, userID, agentID string, pricing []byte, latest time.Time) {
	t.Helper()
	files := []struct {
		name string
		body []byte
		at   time.Time
	}{
		{"notes-for-alex.md", bytes.Repeat([]byte("e"), 1024), mst(2026, 11, 9, 10, 0)},
		{"reports/broken-links-1110.csv", bytes.Repeat([]byte("d"), 2*1024), mst(2026, 11, 10, 10, 0)},
		{"docs/setup.md", bytes.Repeat([]byte("b"), 9*1024), mst(2026, 11, 12, 8, 2)},
		{"reports/q4-docs-health.md", bytes.Repeat([]byte("c"), 14*1024), mst(2026, 11, 12, 8, 2)},
		{"docs/pricing.md", pricing, mst(2026, 11, 12, 14, 42)},
		{"docs/faq.md", bytes.Repeat([]byte("a"), 6*1024), latest},
	}
	disk := map[string][]byte{}
	var listed []store.ListedFile
	for _, file := range files {
		disk[file.name] = file.body
		listed = append(listed, store.ListedFile{Name: file.name, Size: int64(len(file.body))})
		at := file.at
		if at.After(latest) {
			at = latest
		}
		h.st.SetNow(func() time.Time { return at })
		if err := h.st.ObserveWorkspace(context.Background(), userID, agentID, append([]store.ListedFile(nil), listed...)); err != nil {
			t.Fatal(err)
		}
	}
	fake.SeedDisk("https://vm.example", nil, disk)
}

func s10Upload(t *testing.T, h *harness, userID, agentID, name, status string, size int64, at time.Time) {
	t.Helper()
	h.st.SetNow(func() time.Time { return at })
	if _, err := h.st.InsertUpload(context.Background(), userID, agentID, "", name, "", "ab", "workspace/uploads/"+name, status, size); err != nil {
		t.Fatal(err)
	}
}

func mst(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, time.FixedZone("MST", -7*3600)).UTC()
}

const draftJS = `var box=document.querySelector('#composer textarea');if(box)box.value='Use the numbers in support-export-2026.csv for the Q4 section';var hint=document.getElementById('send-hint');if(hint){hint.textContent='Draft kept';hint.classList.add('keep');}`

const queueJS = `window.castbotFiles.render([
{name:'support-export-2026.csv',meta:'28.4 MB',state:'up',line:'17.6 of 28.4 MB · about 12 s left · keep this tab open',pct:62},
{name:'logo-dark.svg',meta:'',state:'wait',line:'Waiting',pct:0},
{name:'tiers-2026.csv',meta:'',state:'done',line:'Copied to workspace/uploads. Atlas is told the path in your next message.',pct:100}
]);`

const tooBigJS = `(function(){var buf=new Uint8Array(32*1024*1024+1);var f=new File([buf],'training-video.mp4');window.castbotFiles.enqueue([f],'panel');window.castbotFiles.render([
{name:'training-video.mp4',meta:'41.2 MB',state:'err',line:'Too large. Files can be up to 32 MB. Split it or share a link in the chat instead.',pct:0},
{name:'support-export-2026.csv',meta:'28.4 MB',state:'err',line:'Stopped at 11.6 of 28.4 MB: the connection dropped. Nothing was saved.',pct:40,retry:true}
]);})();`

const attachJS = `var chips=document.getElementById('attach-chips');if(chips){chips.hidden=false;chips.innerHTML='<span class="fchip"><span class="fn">support-export-2026.csv</span><span class="t2">62%</span></span>';}var hint=document.getElementById('send-hint');if(hint){hint.textContent='Send is ready when the upload finishes';hint.classList.add('keep');}var btn=document.getElementById('send-btn');if(btn){btn.disabled=true;btn.setAttribute('aria-label','Send, waiting for the upload');}var box=document.querySelector('#composer textarea');if(box)box.value='Use the numbers in support-export-2026.csv for the Q4 section';`
