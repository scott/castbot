package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

func TestTransient503StillDeliversTheReply(t *testing.T) {
	t.Run("first open", func(t *testing.T) {
		echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo"})
		t.Cleanup(echo.Close)
		var denied atomic.Int32
		until := time.Now().Add(5 * time.Second)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if time.Now().Before(until) {
				denied.Add(1)
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			echo.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		h := newHarnessWith(t, runtime.NewHTTP(srv.URL))
		msg := "hello there"
		want := runtime.EchoText("echo", msg)
		user, agent, page := postAndWait(t, h, "503a@example.com", "203.0.113.230:9", msg, "You said: hello there", 25*time.Second)
		if denied.Load() == 0 {
			t.Fatal("runtime never returned 503")
		}
		if strings.Count(page, "You said: hello there") != 1 {
			t.Fatalf("page duplicated or missed the reply: %s", page)
		}
		assertAgentBody(t, h, user.ID, agent.ID, want)
	})
	t.Run("mid run", func(t *testing.T) {
		echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo", WordDelay: 80 * time.Millisecond})
		t.Cleanup(echo.Close)
		var denied atomic.Int32
		var badUntil time.Time
		var mu sync.Mutex
		var seen int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			n := seen
			seen++
			bad := !badUntil.IsZero() && time.Now().Before(badUntil)
			mu.Unlock()
			if bad {
				denied.Add(1)
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			if n == 0 {
				ctx, cancel := context.WithTimeout(r.Context(), 300*time.Millisecond)
				defer cancel()
				echo.ServeHTTP(w, r.WithContext(ctx))
				mu.Lock()
				badUntil = time.Now().Add(5 * time.Second)
				mu.Unlock()
				return
			}
			echo.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		h := newHarnessWith(t, runtime.NewHTTP(srv.URL))
		msg := "hello there"
		want := runtime.EchoText("echo", msg)
		user, agent, page := postAndWait(t, h, "503b@example.com", "203.0.113.231:9", msg, "You said: hello there", 25*time.Second)
		if denied.Load() == 0 {
			t.Fatal("runtime never returned 503")
		}
		if echo.Starts() != 1 {
			t.Fatalf("producers %d", echo.Starts())
		}
		if strings.Count(page, "You said: hello there") != 1 {
			t.Fatalf("page: %s", page)
		}
		assertAgentBody(t, h, user.ID, agent.ID, want)
	})
}

func TestRuntimeThatCannotResumeFailsClean(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, textFrame("one two "))
		if fl != nil {
			fl.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	h := newHarnessWith(t, runtime.NewHTTP(srv.URL))
	ip := "203.0.113.232:9"
	user, secret := h.account("nomark@example.com", "No", "correct-horse-1")
	h.login("nomark@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	runID := runIDFrom(t, body)
	row := waitRun(t, h, user.ID, runID, "failed", 5*time.Second)
	if row.Error != run.Interrupted {
		t.Fatalf("error %q", row.Error)
	}
	if hits.Load() != 1 {
		t.Fatalf("runtime connections %d", hits.Load())
	}
	got := joinedText(t, h, user.ID, runID)
	if got != "one two " || strings.Count(got, "one two ") != 1 {
		t.Fatalf("stored %q", got)
	}
	page := h.poll(ip, "/agents/"+agent.ID, run.Interrupted)
	if !strings.Contains(page, ">Retry<") || strings.Count(page, "one two") != 1 {
		t.Fatalf("page: %s", page)
	}
}

func TestErrorFrameIsTerminal(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		raw, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      "1",
			"error":   map[string]any{"code": -32603, "message": "model exploded"},
		})
		_, _ = io.WriteString(w, "data: "+string(raw)+"\n\n")
	}))
	t.Cleanup(srv.Close)
	h := newHarnessWith(t, runtime.NewHTTP(srv.URL))
	ip := "203.0.113.233:9"
	user, secret := h.account("boom@example.com", "Boom", "correct-horse-1")
	h.login("boom@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	row := waitRun(t, h, user.ID, runIDFrom(t, body), "failed", 5*time.Second)
	if row.Error != "model exploded" {
		t.Fatalf("error %q", row.Error)
	}
	if hits.Load() != 1 {
		t.Fatalf("connections %d", hits.Load())
	}
	page := h.poll(ip, "/agents/"+agent.ID, "model exploded")
	if !strings.Contains(page, ">Retry<") {
		t.Fatalf("page: %s", page)
	}
}

func TestRestartMidRunShowsRetry(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo", WordDelay: 80 * time.Millisecond})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	ip := "203.0.113.234:9"
	user, secret := h.account("restart@example.com", "Restart", "correct-horse-1")
	h.login("restart@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	runID := runIDFrom(t, body)
	deadline := time.Now().Add(3 * time.Second)
	for {
		row, err := h.st.GetRun(context.Background(), user.ID, runID)
		if err != nil {
			t.Fatal(err)
		}
		events, err := h.st.ListRunEvents(context.Background(), user.ID, runID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == "streaming" && len(events) > 0 {
			break
		}
		if row.Status == "failed" || row.Status == "done" || time.Now().After(deadline) {
			t.Fatalf("before stop: %s events %d", row.Status, len(events))
		}
		time.Sleep(15 * time.Millisecond)
	}
	h.srv.Close()
	row := waitRun(t, h, user.ID, runID, "failed", 3*time.Second)
	if row.Error != run.Interrupted {
		t.Fatalf("error %q", row.Error)
	}
	if err := h.st.FailAbandoned(context.Background(), run.Interrupted); err != nil {
		t.Fatal(err)
	}
	next := New(h.st, h.srv.key, runtime.NewHTTP(up.URL))
	t.Cleanup(next.Close)
	req := httptest.NewRequest(http.MethodGet, "/agents/"+agent.ID, nil)
	req.RemoteAddr = ip
	for _, c := range h.cookieList() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	next.Handler().ServeHTTP(rec, req)
	page := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(page, run.Interrupted) || !strings.Contains(page, ">Retry<") {
		t.Fatalf("restart page %d %s", rec.Code, page)
	}
	if strings.Contains(page, "sse-connect") {
		t.Fatal("interrupted run is still live")
	}
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	left, err := h.st.CreateRun(context.Background(), user.ID, threads[0].ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), user.ID, left.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.FailAbandoned(context.Background(), run.Interrupted); err != nil {
		t.Fatal(err)
	}
	got, err := h.st.GetRun(context.Background(), user.ID, left.ID)
	if err != nil || got.Status != "failed" || got.Error != run.Interrupted {
		t.Fatalf("leftover %#v %v", got, err)
	}
	events, err := h.st.ListRunEvents(context.Background(), user.ID, left.ID, 0)
	if err != nil || len(events) != 1 || events[0].Kind != "error" {
		t.Fatalf("leftover events %#v %v", events, err)
	}
}

func TestConcurrentRetryCreatesOneRun(t *testing.T) {
	ln := closedAddr(t)
	h := newHarnessWith(t, runtime.NewHTTP("http://"+ln))
	h.srv.SetRetryBudget(300 * time.Millisecond)
	ip := "203.0.113.235:9"
	user, secret := h.account("retry@example.com", "Retry", "correct-horse-1")
	h.login("retry@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	runID := runIDFrom(t, body)
	waitRun(t, h, user.ID, runID, "failed", 5*time.Second)
	form := h.form(url.Values{})
	cookies := h.cookieList()
	path := "/runs/" + runID + "/retry"
	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
			req.RemoteAddr = ip
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			for _, c := range cookies {
				req.AddCookie(c)
			}
			rec := httptest.NewRecorder()
			h.srv.Handler().ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()
	got := map[int]int{}
	for _, c := range codes {
		got[c]++
	}
	if got[200] != 1 || got[409] != 1 {
		t.Fatalf("codes %v", codes)
	}
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	runs, err := h.st.ListThreadRuns(context.Background(), user.ID, threads[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs %d", len(runs))
	}
	if runs[0].Status != "retried" || runs[0].ID != runID {
		t.Fatalf("original %#v", runs[0])
	}
	msgs, err := h.st.ListMessages(context.Background(), user.ID, threads[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	users := 0
	for _, m := range msgs {
		if m.AuthorKind == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("user messages %d", users)
	}
}

func TestCaughtUpSendsTerminalEvent(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.236:9"
	user, secret := h.account("tail@example.com", "Tail", "correct-horse-1")
	h.login("tail@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	runID := runIDFrom(t, body)
	waitRun(t, h, user.ID, runID, "done", 5*time.Second)
	_, replay, _ := h.do(http.MethodGet, "/runs/"+runID+"/events?after=1", "", ip, nil)
	if strings.Contains(replay, "id: 1\n") {
		t.Fatalf("replayed the first event: %s", replay)
	}
	if !strings.Contains(replay, "event: done") {
		t.Fatalf("missing done: %s", replay)
	}
	_, caught, _ := h.do(http.MethodGet, "/runs/"+runID+"/events?after=999999", "", ip, nil)
	if !strings.Contains(caught, "event: done") || !strings.Contains(caught, "You said: hello there") {
		t.Fatalf("caught up: %s", caught)
	}
	_, headed, _ := h.do(http.MethodGet, "/runs/"+runID+"/events", "", ip, map[string]string{"Last-Event-ID": "999999"})
	if !strings.Contains(headed, "event: done") || !strings.Contains(headed, "event: stop") {
		t.Fatalf("header: %s", headed)
	}
}

func TestSSEKeepaliveAndOrphan(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo", WordDelay: 40 * time.Millisecond})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	h.srv.SetKeepalive(30 * time.Millisecond)
	ip := "203.0.113.237:9"
	user, secret := h.account("ka@example.com", "Ka", "correct-horse-1")
	h.login("ka@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, live.URL+"/runs/"+runIDFrom(t, body)+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range h.cookieList() {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewReader(res.Body)
	saw := false
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		line, err := sc.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, ": ka") {
			saw = true
			break
		}
	}
	if !saw {
		t.Fatal("no keepalive")
	}
	cancel()
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	left, err := h.st.CreateRun(context.Background(), user.ID, threads[0].ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.MarkStreaming(context.Background(), user.ID, left.ID); err != nil {
		t.Fatal(err)
	}
	octx, ocancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer ocancel()
	ev, err := readSSE(octx, live.URL+"/runs/"+left.ID+"/events", h.cookieList(), "", func(batch []sseEvent) bool {
		return countEvent(batch, "fail") > 0
	})
	if err != nil {
		t.Fatal(err)
	}
	if countEvent(ev, "fail") != 1 || !strings.Contains(ev[0].Data, run.Interrupted) {
		t.Fatalf("orphan %#v", ev)
	}
}

func TestHistoryIsSentOnce(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo"})
	t.Cleanup(echo.Close)
	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		mu.Lock()
		bodies = append(bodies, append([]byte(nil), raw...))
		mu.Unlock()
		echo.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h := newHarnessWith(t, runtime.NewHTTP(srv.URL))
	ip := "203.0.113.238:9"
	_, secret := h.account("hist@example.com", "Hist", "correct-horse-1")
	h.login("hist@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	h.poll(ip, "/agents/"+agent.ID, "Hello! I'm echo.")
	code, body, _ = h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"again"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	h.poll(ip, "/agents/"+agent.ID, "You said: again")
	mu.Lock()
	saved := append([][]byte(nil), bodies...)
	mu.Unlock()
	var saw bool
	for _, raw := range saved {
		var msg struct {
			Params struct {
				Message struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"message"`
				Metadata struct {
					Offset  int `json:"streamOffset"`
					History []struct {
						Role string `json:"role"`
						Text string `json:"text"`
					} `json:"history"`
				} `json:"metadata"`
			} `json:"params"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Params.Metadata.Offset > 0 && len(msg.Params.Metadata.History) > 0 {
			t.Fatal("history was sent on a resume")
		}
		if len(msg.Params.Message.Parts) == 0 || msg.Params.Message.Parts[0].Text != "again" || msg.Params.Metadata.Offset != 0 {
			continue
		}
		if len(msg.Params.Metadata.History) < 2 {
			t.Fatalf("history %#v", msg.Params.Metadata.History)
		}
		if msg.Params.Metadata.History[0].Role != "user" || msg.Params.Metadata.History[0].Text != "hello" {
			t.Fatalf("first turn %#v", msg.Params.Metadata.History[0])
		}
		if msg.Params.Metadata.History[1].Role != "agent" || !strings.Contains(msg.Params.Metadata.History[1].Text, "Hello! I'm echo.") {
			t.Fatalf("second turn %#v", msg.Params.Metadata.History[1])
		}
		saw = true
	}
	if !saw {
		t.Fatal("first open did not send history")
	}
}

func postAndWait(t *testing.T, h *harness, email, ip, msg, needle string, d time.Duration) (store.User, store.Agent, string) {
	t.Helper()
	user, secret := h.account(email, "User", "correct-horse-1")
	h.login(email, "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {msg}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	deadline := time.Now().Add(d)
	var page string
	for {
		_, page, _ = h.do(http.MethodGet, "/agents/"+agent.ID, "", ip, nil)
		if strings.Contains(page, needle) && !strings.Contains(page, "sse-connect") {
			return user, agent, page
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing %q in %s", needle, page)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

func assertAgentBody(t *testing.T, h *harness, userID, agentID, want string) {
	t.Helper()
	threads, err := h.st.ListThreadsByAgent(context.Background(), userID, agentID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	msgs, err := h.st.ListMessages(context.Background(), userID, threads[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.AuthorKind == "agent" && m.Body == want {
			return
		}
	}
	t.Fatal("stored reply mismatch")
}

func waitRun(t *testing.T, h *harness, userID, runID, status string, d time.Duration) store.Run {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		row, err := h.st.GetRun(context.Background(), userID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == status {
			return row
		}
		if row.Status == "failed" && status != "failed" {
			t.Fatalf("failed early: %s", row.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("status %s want %s (%s)", row.Status, status, row.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func joinedText(t *testing.T, h *harness, userID, runID string) string {
	t.Helper()
	events, err := h.st.ListRunEvents(context.Background(), userID, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range events {
		if ev.Kind != "text" {
			continue
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		b.WriteString(payload.Text)
	}
	return b.String()
}

func textFrame(text string) string {
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "1",
		"result":  map[string]any{"parts": []map[string]string{{"kind": "text", "text": text}}},
	})
	return "data: " + string(raw) + "\n\n"
}

func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}
