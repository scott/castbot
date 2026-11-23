package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/scott/castbot/internal/store"
)

func TestProgressLogOnTheFinishedMessage(t *testing.T) {
	body := "Let me check the files.The records are missing.The blog is deployed.\n\n## Status\n\n- Live\n"
	steps, err := json.Marshal([]string{"Let me check the files.", "The records are missing."})
	if err != nil {
		t.Fatal(err)
	}
	views := presentMessages([]store.Message{{
		ID: "m1", AuthorKind: "agent", Body: body, Steps: string(steps),
		CreatedAt: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}}, agentView{Color: "c1", Letter: "A", Name: "Atlas"}, time.Date(2026, 11, 23, 15, 4, 0, 0, time.UTC), time.UTC)
	if len(views) != 1 {
		t.Fatalf("views %d", len(views))
	}
	want := "The blog is deployed.\n\n## Status\n\n- Live\n"
	if views[0].Body != want {
		t.Fatalf("answer %q", views[0].Body)
	}
	raw, err := base64.StdEncoding.DecodeString(views[0].Copy)
	if err != nil || string(raw) != want {
		t.Fatalf("copy %q %v", raw, err)
	}
	if strings.Contains(string(raw), "Let me check") {
		t.Fatal("copy includes the progress log")
	}
	pageHTML, err := execTemplate("messages", page{Messages: views})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pageHTML, `class="step-log"`) || !strings.Contains(pageHTML, "2 steps") || strings.Contains(pageHTML, `class="step-log" open`) {
		t.Fatalf("log %s", pageHTML)
	}
	if !strings.Contains(pageHTML, "Let me check the files.") || !strings.Contains(pageHTML, "The records are missing.") {
		t.Fatalf("missing steps %s", pageHTML)
	}
	if strings.Contains(pageHTML, "files.The") || !strings.Contains(pageHTML, "Status") {
		t.Fatalf("answer formatting %s", pageHTML)
	}
}

func TestProgressLogStreamsAboveTheAnswer(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { clearDwell("run-steps") })
	freeze(h, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	runRow := store.Run{ID: "run-steps", Status: "streaming", StartedAt: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC).Format(time.RFC3339)}
	st := &liveSend{run: runRow}
	out, elapsed := h.srv.activityNow(st)
	st.noteAct(out, elapsed)

	first := emitRunEvent(t, h, st, runRow, textRunEvent(1, "Let me check the files."))
	if countEvent(first, "log") != 0 {
		t.Fatalf("first turn opened the log %#v", first)
	}
	if !strings.Contains(eventData(t, first, "tail"), "Let me check the files.") {
		t.Fatalf("tail %#v", first)
	}
	second := emitRunEvent(t, h, st, runRow, textRunEvent(2, "The blog is deployed.\n\n## Status\n\n- Live\n"))
	log := eventData(t, second, "log")
	if !strings.Contains(log, "Let me check the files.") || strings.Contains(log, "step-log") || strings.Contains(log, "The blog is deployed") {
		t.Fatalf("log %s", log)
	}
	var blocks strings.Builder
	var tail string
	for _, ev := range second {
		if ev.Event == "block" {
			blocks.WriteString(ev.Data)
		}
		if ev.Event == "tail" {
			tail = ev.Data
		}
	}
	got := blocks.String()
	if strings.Contains(got, "Let me check") || !strings.Contains(got, "The blog is deployed") || !strings.Contains(got, "Status") {
		t.Fatalf("blocks %s", got)
	}
	if strings.Contains(tail, "Let me check") {
		t.Fatalf("tail kept the step %s", tail)
	}
}

func TestProgressLogClearsBlocksFromAStep(t *testing.T) {
	h := newHarness(t)
	t.Cleanup(func() { clearDwell("run-clear") })
	freeze(h, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	runRow := store.Run{ID: "run-clear", Status: "streaming", StartedAt: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC).Format(time.RFC3339)}
	st := &liveSend{run: runRow}
	out, elapsed := h.srv.activityNow(st)
	st.noteAct(out, elapsed)

	first := emitRunEvent(t, h, st, runRow, textRunEvent(1, "I looked around.\n\nMore notes."))
	if countEvent(first, "block") != 1 || !strings.Contains(eventData(t, first, "block"), "I looked around.") {
		t.Fatalf("first block %#v", first)
	}
	second := emitRunEvent(t, h, st, runRow, textRunEvent(2, "The site is up."))
	log := eventData(t, second, "log")
	if !strings.Contains(log, "I looked around.") || !strings.Contains(log, `hx-swap-oob="innerHTML"`) || !strings.Contains(log, `id="md-run-clear"`) {
		t.Fatalf("log %s", log)
	}
	if countEvent(second, "block") != 0 {
		t.Fatalf("new turn reused a block %#v", second)
	}
	if !strings.Contains(eventData(t, second, "tail"), "The site is up.") || strings.Contains(eventData(t, second, "tail"), "More notes") {
		t.Fatalf("tail %#v", second)
	}
}

func TestStepLogCollapsesInTheBrowser(t *testing.T) {
	chrome := "/usr/bin/google-chrome"
	if p := os.Getenv("CASTBOT_CHROME"); p != "" {
		chrome = p
	}
	if _, err := os.Stat(chrome); err != nil {
		t.Skip("chrome is not installed")
	}
	body := "Let me check the files.The records are missing.The blog is deployed.\n\n## Status\n\n- Live at https://blog.castagent.dev/posts/hello-world\n"
	steps, err := json.Marshal([]string{"Let me check the files.", "The records are missing."})
	if err != nil {
		t.Fatal(err)
	}
	views := presentMessages([]store.Message{{
		ID: "m1", AuthorKind: "agent", Body: body, Steps: string(steps),
		CreatedAt: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}}, agentView{Color: "c1", Letter: "A", Name: "Atlas"}, time.Date(2026, 11, 23, 15, 4, 0, 0, time.UTC), time.UTC)
	fragment, err := execTemplate("messages", page{Messages: views})
	if err != nil {
		t.Fatal(err)
	}
	pageHTML := "<!doctype html><html><head><meta charset=\"utf-8\"><link rel=\"stylesheet\" href=\"/castbot.css\"></head><body><div class=\"msgs\">" + fragment + "</div></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/castbot.css" {
			http.ServeFile(w, r, "static/castbot.css")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(pageHTML))
	}))
	t.Cleanup(srv.Close)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
	)
	allocCtx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.Cleanup(cancel)
	ctx, stop := chromedp.NewContext(allocCtx)
	t.Cleanup(stop)
	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(cancel)

	for _, vp := range [][2]int64{{1280, 800}, {390, 844}} {
		var started, open bool
		var step string
		var heading string
		var overflow bool
		if err := chromedp.Run(ctx,
			chromedp.EmulateViewport(vp[0], vp[1]),
			chromedp.Navigate(srv.URL),
			chromedp.Evaluate(`document.querySelector("details.step-log").open`, &started),
			chromedp.Evaluate(`document.querySelector(".md h2") ? document.querySelector(".md h2").textContent : ""`, &heading),
			chromedp.Click(`details.step-log summary`, chromedp.ByQuery),
			chromedp.Evaluate(`document.querySelector("details.step-log").open`, &open),
			chromedp.Evaluate(`document.querySelector("details.step-log li").textContent`, &step),
			chromedp.Evaluate(`document.documentElement.scrollWidth > document.documentElement.clientWidth + 1`, &overflow),
		); err != nil {
			t.Fatalf("viewport %d: %v", vp[0], err)
		}
		if started {
			t.Fatalf("viewport %d log started open", vp[0])
		}
		if heading != "Status" {
			t.Fatalf("viewport %d heading %q", vp[0], heading)
		}
		if !open || step != "Let me check the files." {
			t.Fatalf("viewport %d open %v step %q", vp[0], open, step)
		}
		if overflow {
			t.Fatalf("viewport %d scrolls sideways", vp[0])
		}
	}
}
