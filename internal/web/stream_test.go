package web

import (
	"bufio"
	"context"
	"encoding/json"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
)

type sseEvent struct {
	ID    string
	Event string
	Data  string
}

func TestDurableEchoSurvivesCutsAndReload(t *testing.T) {
	msg := strings.TrimSpace(strings.Repeat("alpha ", 24))
	want := runtime.EchoText("echo", msg)
	words := strings.Split(want, " ")
	delay := 60 * time.Second / time.Duration(len(words))
	if delay*time.Duration(len(words)) < 50*time.Second {
		t.Fatalf("reply would be shorter than 50s: %s", delay*time.Duration(len(words)))
	}
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo", WordDelay: delay})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	proxy := runtime.NewCutProxy(up.URL, runtime.DefaultCut)
	px := httptest.NewServer(proxy)
	t.Cleanup(px.Close)
	h := newHarnessWith(t, runtime.NewHTTP(px.URL))
	ip := "203.0.113.210:9"
	user, secret := h.account("stream@example.com", "Stream", "correct-horse-1")
	h.login("stream@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {msg}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || !strings.Contains(body, `class="act"`) || strings.Contains(body, ">Replying<") {
		t.Fatalf("post %d %s", code, body)
	}
	runID := runIDFrom(t, body)
	eventsURL := live.URL + "/runs/" + runID + "/events"

	first, err := readSSE(ctx, eventsURL, h.cookieList(), "", func(ev []sseEvent) bool {
		return countEvent(ev, "text") >= 3
	})
	if err != nil {
		t.Fatal(err)
	}
	last := lastID(first)
	if last == "" {
		t.Fatal("reload had no event id")
	}
	second, err := readSSE(ctx, eventsURL, h.cookieList(), last, func(ev []sseEvent) bool {
		return countEvent(ev, "done") > 0
	})
	if err != nil {
		t.Fatal(err)
	}
	combined := append(append([]sseEvent{}, first...), second...)
	got, seqs := textFrom(t, combined)
	if got != want {
		t.Fatalf("text mismatch\n got %q\nwant %q", got, want)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("seq not increasing: %v", seqs)
		}
	}
	stored, err := h.st.ListRunEvents(ctx, user.ID, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var db strings.Builder
	var dbSeq []int
	for i, ev := range stored {
		if ev.Seq != i+1 {
			t.Fatalf("db seq %d at %d", ev.Seq, i)
		}
		if ev.Kind != "text" {
			continue
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		db.WriteString(payload.Text)
		dbSeq = append(dbSeq, ev.Seq)
	}
	if db.String() != want {
		t.Fatalf("stored text mismatch")
	}
	if strings.Join(ints(seqs), ",") != strings.Join(ints(dbSeq), ",") {
		t.Fatalf("browser seqs %v db %v", seqs, dbSeq)
	}
	runRow, err := h.st.GetRun(ctx, user.ID, runID)
	if err != nil || runRow.Status != "done" {
		t.Fatalf("status %#v %v", runRow, err)
	}
	msgs, err := h.st.ListMessages(ctx, user.ID, runRow.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	var users, agents int
	for _, m := range msgs {
		switch m.AuthorKind {
		case "user":
			users++
			if m.Body != msg {
				t.Fatalf("user body %q", m.Body)
			}
		case "agent":
			agents++
			if m.Body != want {
				t.Fatalf("agent body %q", m.Body)
			}
		}
	}
	if users != 1 || agents != 1 {
		t.Fatalf("messages user %d agent %d", users, agents)
	}
	if proxy.Cuts() < 3 {
		t.Fatalf("proxy cuts %d, want at least 3", proxy.Cuts())
	}
	if echo.Starts() != 1 {
		t.Fatalf("producers %d", echo.Starts())
	}
	page := h.poll(ip, "/agents/"+agent.ID, "You said:")
	if strings.Contains(page, "sse-connect") || strings.Count(page, "You said:") != 1 {
		t.Fatalf("page after reload path: %s", page)
	}
}

func TestTwoBrowsersSeeTheSameRun(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo", WordDelay: 15 * time.Millisecond})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	ip := "203.0.113.211:9"
	user, secret := h.account("two@example.com", "Two", "correct-horse-1")
	h.login("two@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	msg := "hello there"
	want := runtime.EchoText("echo", msg)
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {msg}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	runID := runIDFrom(t, body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	eventsURL := live.URL + "/runs/" + runID + "/events"
	var wg sync.WaitGroup
	var first, second []sseEvent
	var err1, err2 error
	wg.Add(2)
	cookies := h.cookieList()
	stop := func(ev []sseEvent) bool {
		return countEvent(ev, "done") > 0 || countEvent(ev, "fail") > 0
	}
	go func() {
		defer wg.Done()
		first, err1 = readSSE(ctx, eventsURL, cookies, "", stop)
	}()
	go func() {
		defer wg.Done()
		second, err2 = readSSE(ctx, eventsURL, cookies, "", stop)
	}()
	wg.Wait()
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	a, aSeq := textFrom(t, first)
	b, bSeq := textFrom(t, second)
	if a != want || b != want {
		t.Fatalf("a %q b %q", a, b)
	}
	if strings.Join(ints(aSeq), ",") != strings.Join(ints(bSeq), ",") {
		t.Fatalf("seqs %v %v", aSeq, bSeq)
	}
	stored, err := h.st.ListRunEvents(ctx, user.ID, runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	textEvents := 0
	for _, ev := range stored {
		if ev.Kind == "text" {
			textEvents++
		}
	}
	if textEvents != len(aSeq) {
		t.Fatalf("db text events %d browser %d", textEvents, len(aSeq))
	}
}

func TestReplySurvivesBrowserDisconnect(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "echo", WordDelay: 40 * time.Millisecond})
	up := httptest.NewServer(echo)
	t.Cleanup(func() {
		up.Close()
		echo.Close()
	})
	h := newHarnessWith(t, runtime.NewHTTP(up.URL))
	ip := "203.0.113.212:9"
	user, secret := h.account("gone@example.com", "Gone", "correct-horse-1")
	h.login("gone@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	live := httptest.NewServer(h.srv.Handler())
	t.Cleanup(live.Close)
	msg := "hello there"
	want := runtime.EchoText("echo", msg)
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {msg}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatal(code, body)
	}
	runID := runIDFrom(t, body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	readCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = readSSE(readCtx, live.URL+"/runs/"+runID+"/events", h.cookieList(), "", func(ev []sseEvent) bool {
			return countEvent(ev, "text") >= 1
		})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("browser did not see the start")
	}
	stop()
	deadline := time.Now().Add(5 * time.Second)
	for {
		row, err := h.st.GetRun(ctx, user.ID, runID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status == "done" {
			break
		}
		if row.Status == "failed" || time.Now().After(deadline) {
			t.Fatalf("status %s", row.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	threads, err := h.st.ListThreadsByAgent(ctx, user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	msgs, err := h.st.ListMessages(ctx, user.ID, threads[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range msgs {
		if m.AuthorKind == "agent" && m.Body == want {
			found = true
		}
	}
	if !found {
		t.Fatal("reply was not stored after the browser left")
	}
	page := h.poll(ip, "/agents/"+agent.ID, "You said:")
	if strings.Contains(page, "sse-connect") || strings.Count(page, "You said:") != 1 {
		t.Fatalf("page: %s", page)
	}
}

func TestRuntimeDownShowsRetry(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	h := newHarnessWith(t, runtime.NewHTTP("http://"+addr))
	h.srv.SetRetryBudget(400 * time.Millisecond)
	ip := "203.0.113.213:9"
	user, secret := h.account("down@example.com", "Down", "correct-horse-1")
	h.login("down@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	code, body, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/messages", h.form(url.Values{"body": {"hello there"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || !strings.Contains(body, `class="act"`) || strings.Contains(body, ">Replying<") {
		t.Fatalf("post %d %s", code, body)
	}
	page := h.poll(ip, "/agents/"+agent.ID, "The agent is not reachable right now.")
	if !strings.Contains(page, "The agent is not reachable right now.") || !strings.Contains(page, ">Retry<") {
		t.Fatalf("error ui: %s", page)
	}
	if strings.Contains(page, "sse-connect") {
		t.Fatal("failed run is still subscribed")
	}
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	runs, err := h.st.ListThreadRuns(context.Background(), user.ID, threads[0].ID)
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" {
		t.Fatalf("runs %#v %v", runs, err)
	}
	events, err := h.st.ListRunEvents(context.Background(), user.ID, runs[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, ev := range events {
		if ev.Kind == "error" && strings.Contains(ev.Payload, "The agent is not reachable right now.") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("events %#v", events)
	}
}

func TestRetryDoesNotDuplicateTheUserMessage(t *testing.T) {
	h := newHarness(t)
	ip := "203.0.113.214:9"
	user, secret := h.account("retry@example.com", "Retry", "correct-horse-1")
	h.login("retry@example.com", "correct-horse-1", secret, ip, false)
	agent := h.newAgent(ip, "Atlas")
	threads, err := h.st.ListThreadsByAgent(context.Background(), user.ID, agent.ID)
	if err != nil || len(threads) == 0 {
		t.Fatal(err)
	}
	msg, err := h.st.CreateMessage(context.Background(), user.ID, threads[0].ID, "user", user.ID, "ping the echo")
	if err != nil {
		t.Fatal(err)
	}
	runRow, err := h.st.CreateRun(context.Background(), user.ID, threads[0].ID, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetMessageRun(context.Background(), user.ID, msg.ID, runRow.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.FailRun(context.Background(), user.ID, runRow.ID, "The agent is not reachable right now.", 0); err != nil {
		t.Fatal(err)
	}
	code, body, _ := h.do(http.MethodPost, "/runs/"+runRow.ID+"/retry", h.form(url.Values{}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 || !strings.Contains(body, "sse-connect") || strings.Contains(body, "ping the echo") {
		t.Fatalf("retry fragment %d %s", code, body)
	}
	page := h.poll(ip, "/agents/"+agent.ID, "You said: ping the echo")
	if strings.Count(page, `class="m-user`) != 1 || strings.Count(page, "You said:") != 1 {
		t.Fatalf("page: %s", page)
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

func (h *harness) newAgent(ip, name string) store.Agent {
	h.t.Helper()
	_ = ip
	user := h.currentUser()
	sec, err := h.st.SystemSection(context.Background(), user, "agents")
	if err != nil {
		h.t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(context.Background(), user, store.AgentInput{
		SectionID: sec.ID,
		Name:      name,
		Title:     "Helper",
		Status:    "draft",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return agent
}

func (h *harness) currentUser() string {
	h.t.Helper()
	c := h.jar["castbot_session"]
	if c == nil {
		h.t.Fatal("no session")
	}
	id, ok := openSeal("session", c.Value, h.srv.key)
	if !ok {
		h.t.Fatal("session seal")
	}
	sess, err := h.st.GetSession(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return sess.UserID
}

func (h *harness) cookieList() []*http.Cookie {
	out := make([]*http.Cookie, 0, len(h.jar))
	for _, c := range h.jar {
		out = append(out, c)
	}
	return out
}

func runIDFrom(t *testing.T, body string) string {
	t.Helper()
	const key = `sse-connect="/runs/`
	i := strings.Index(body, key)
	if i < 0 {
		t.Fatalf("no run in %s", body)
	}
	rest := body[i+len(key):]
	j := strings.Index(rest, `/events`)
	if j < 0 {
		t.Fatal(rest)
	}
	return rest[:j]
}

func readSSE(ctx context.Context, rawURL string, cookies []*http.Cookie, lastID string, stop func([]sseEvent) bool) ([]sseEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(res.Body)
		return nil, &statusError{code: res.StatusCode, body: string(raw)}
	}
	sc := bufio.NewReader(res.Body)
	var out []sseEvent
	var cur sseEvent
	var data []string
	flush := func() {
		if cur.ID == "" && cur.Event == "" && len(data) == 0 {
			return
		}
		cur.Data = strings.Join(data, "\n")
		out = append(out, cur)
		cur = sseEvent{}
		data = nil
	}
	for {
		if stop != nil && stop(out) {
			return out, nil
		}
		line, err := sc.ReadString('\n')
		if err != nil {
			return out, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "id:") {
			cur.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
			continue
		}
		if strings.HasPrefix(line, "event:") {
			cur.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			v := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(v, " ") {
				v = v[1:]
			}
			data = append(data, v)
		}
	}
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return "status " + strconv.Itoa(e.code) + " " + e.body
}

func countEvent(ev []sseEvent, name string) int {
	n := 0
	for _, e := range ev {
		if e.Event == name {
			n++
		}
	}
	return n
}

func lastID(ev []sseEvent) string {
	id := ""
	for _, e := range ev {
		if e.ID != "" {
			id = e.ID
		}
	}
	return id
}

func textFrom(t *testing.T, ev []sseEvent) (string, []int) {
	t.Helper()
	var b strings.Builder
	var seqs []int
	seen := map[int]bool{}
	for _, e := range ev {
		if e.Event != "text" {
			continue
		}
		id, err := strconv.Atoi(e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("duplicate event %d", id)
		}
		seen[id] = true
		seqs = append(seqs, id)
		b.WriteString(html.UnescapeString(e.Data))
	}
	return b.String(), seqs
}

func ints(v []int) []string {
	out := make([]string, len(v))
	for i, n := range v {
		out[i] = strconv.Itoa(n)
	}
	return out
}
