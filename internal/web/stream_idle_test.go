package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
)

func TestIdleStreamSurvivesCutsAndReload(t *testing.T) {
	const msg = "hi"
	want := runtime.EchoText("echo", msg)
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo", Quiet: 30 * time.Second})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	proxy := runtime.NewCutProxy(up.URL, runtime.DefaultCut)
	px := httptest.NewServer(proxy)
	t.Cleanup(px.Close)
	h := newHarnessWith(t, runtime.NewHTTP(px.URL))
	ip := "203.0.113.235:9"
	user, secret := h.account("idle-s07@example.com", "Idle", "correct-horse-1")
	h.login("idle-s07@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {msg}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || !strings.Contains(body, "Replying") {
		t.Fatalf("post %d", code)
	}
	runID := runIDFrom(t, body)
	eventsURL := live.URL + "/runs/" + runID + "/events"
	cookies := h.cookieList()
	started := time.Now()
	readPartial := func(last string, d time.Duration) []sseEvent {
		t.Helper()
		pctx, stop := context.WithTimeout(ctx, d)
		defer stop()
		ev, err := readSSE(pctx, eventsURL, cookies, last, nil)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		if countEvent(ev, "text") != 0 || countEvent(ev, "fail") != 0 {
			t.Fatalf("events during silence %d text %d fail", countEvent(ev, "text"), countEvent(ev, "fail"))
		}
		return ev
	}
	first := readPartial("", 10*time.Second)
	second := readPartial(lastID(first), 10*time.Second)
	if time.Since(started) < 18*time.Second {
		t.Fatal("reloads were not during the silence")
	}
	third, err := readSSE(ctx, eventsURL, cookies, lastID(second), func(ev []sseEvent) bool {
		return countEvent(ev, "done") > 0
	})
	if err != nil {
		t.Fatal(err)
	}
	combined := append(append(append([]sseEvent{}, first...), second...), third...)
	got, seqs := textFrom(t, combined)
	if got != want || strings.Count(got, want) != 1 {
		t.Fatalf("text %q", got)
	}
	if strings.Contains(got, run.Stopped) || countEvent(combined, "fail") != 0 {
		t.Fatal("false stop")
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq %v", seqs)
		}
	}
	row, err := h.st.GetRun(ctx, user.ID, runID)
	if err != nil || row.Status != "done" || row.Error != "" {
		t.Fatalf("%+v %v", row, err)
	}
}
