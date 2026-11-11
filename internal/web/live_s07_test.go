//go:build live

package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/store"
)

// The live tests read CASTBOT_DO_TOKEN and no other DigitalOcean token.
// CASTBOT_PUBLIC_URL must be an https URL the guest can reach that forwards
// to CASTBOT_LISTEN (default 127.0.0.1:18080), including /s07-infer/.
// CASTBOT_IMAGE is the S07 runtime image.
//
//	CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... \
//	  go test -tags live -timeout 45m -count=1 -run TestLiveIdleStream ./internal/web
//	CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... \
//	  go test -tags live -timeout 45m -count=1 -run TestLiveGuestClock ./internal/web

const liveKey = "fixture-s07-key" // gitleaks:allow

const liveReply = "Streamed from the fixture provider."

func TestLiveIdleStream(t *testing.T) {
	rig := s07Live(t, "castbot-s07-test-idle", 30*time.Second)
	endpoint, token := rig.waitReady(t)
	_ = endpoint
	_ = token
	ip := "203.0.113.240:9"
	code, body, _ := rig.h.do(http.MethodPost, "/agents/"+rig.agent.ID+"/messages", rig.h.form(map[string][]string{"body": {"hello"}}), ip, map[string]string{"HX-Request": "true"})
	if code != 200 {
		t.Fatalf("send %d", code)
	}
	runID := runIDFrom(t, body)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	events, ka, err := readLiveSSE(ctx, rig.local+"/runs/"+runID+"/events", rig.h.cookieList(), func(ev []sseEvent) bool {
		return countEvent(ev, "done") > 0 || countEvent(ev, "fail") > 0
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := textFrom(t, events)
	if ka < 1 {
		t.Fatalf("keepalives %d", ka)
	}
	if strings.Count(got, liveReply) != 1 || countEvent(events, "fail") != 0 {
		t.Fatalf("reply %q fail %d", got, countEvent(events, "fail"))
	}
	row, err := rig.h.st.GetRun(ctx, rig.agent.UserID, runID)
	if err != nil || row.Status != "done" || row.Error != "" {
		t.Fatalf("run %+v %v", row, err)
	}
}

func TestLiveGuestClock(t *testing.T) {
	rig := s07Live(t, "castbot-s07-test-clock", 0)
	endpoint, token := rig.waitReady(t)
	ctx := context.Background()
	_, plain, err := rig.h.st.IssueCLIToken(ctx, rig.agent.UserID, rig.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if msg := rig.h.srv.pushCLITokens(ctx, rig.agent.UserID, rig.agent.ID); msg != "" {
		t.Fatal(msg)
	}
	hostBefore := time.Now()
	guestBefore := guestUnix(t, rig.client, endpoint, token)
	if _, err := rig.client.Pause(ctx, rig.created()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Minute)
	woke, err := rig.client.Wake(ctx, rig.created())
	if err != nil {
		t.Fatal(err)
	}
	if woke.Endpoint != "" {
		endpoint = woke.Endpoint
	}
	hostAfter := time.Now()
	guestAfter := guestUnix(t, rig.client, endpoint, token)
	guestDelta := time.Duration(guestAfter-guestBefore) * time.Second
	hostDelta := hostAfter.Sub(hostBefore)
	t.Logf("guest unix %d -> %d host elapsed %s guest elapsed %s jump %s", guestBefore, guestAfter, hostDelta.Round(time.Second), guestDelta.Round(time.Second), (guestDelta - hostDelta).Round(time.Second))
	before := rig.fetches.Load()
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	code, _, callErr := rig.client.EdgeCall(callCtx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/", plain, []byte(`{"jsonrpc":"2.0","id":"1","method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"ping"}]}}}`))
	cancel()
	if rig.fetches.Load() <= before {
		if callErr != nil {
			t.Fatalf("cli token list was not refetched, status %d", code)
		}
		t.Fatalf("cli token list was not refetched, status %d", code)
	}
	t.Logf("cli token list refetch: %d", rig.fetches.Load()-before)
}

type liveRig struct {
	h       *harness
	agent   store.Agent
	client  *host.MicroVM
	local   string
	fetches *atomic.Int32
	mu      sync.Mutex
	id      string
}

func (r *liveRig) created() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

func (r *liveRig) waitReady(t *testing.T) (endpoint, token string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		vm, err := r.h.st.LiveVM(context.Background(), r.agent.UserID, r.agent.ID)
		if err == nil && vm.State == "running" && vm.Endpoint != "" {
			endpoint, token, err = r.h.st.VMCredentials(context.Background(), r.agent.UserID, r.agent.ID)
			if err == nil && token != "" && endpoint != "" {
				cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				code, _, callErr := r.client.EdgeCall(cctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/castlet/health", token, nil)
				cancel()
				if callErr == nil && code == http.StatusOK {
					got, serr := r.client.Status(context.Background(), vm.MicroVMID)
					if serr == nil && !strings.HasPrefix(got.Name, "castbot-s07-test-") {
						t.Fatal("created a machine outside the test prefix")
					}
					return endpoint, token
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatal("guest did not become ready")
	return "", ""
}

func s07Live(t *testing.T, name string, quiet time.Duration) *liveRig {
	t.Helper()
	deadline, ok := t.Deadline()
	if err := host.LiveWindow(deadline, ok, time.Now()); err != nil {
		t.Fatal(err)
	}
	token := host.DOToken()
	if token == "" {
		t.Skip("CASTBOT_DO_TOKEN is not set")
	}
	image := strings.TrimSpace(os.Getenv("CASTBOT_IMAGE"))
	publicURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CASTBOT_PUBLIC_URL")), "/")
	if image == "" || publicURL == "" {
		t.Skip("CASTBOT_IMAGE and CASTBOT_PUBLIC_URL are required")
	}
	if !strings.HasPrefix(publicURL, "https://") || !strings.HasPrefix(name, "castbot-s07-test-") {
		t.Fatal("live configuration")
	}
	client, err := host.New(host.Options{Token: token, Interval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	protected := map[string]host.Machine{}
	for _, vm := range before {
		if testMachineName(vm.Name) {
			continue
		}
		protected[vm.ID] = vm
	}
	if err := sweepThrowawayMachines(ctx, client); err != nil {
		t.Fatal(err)
	}
	rig := &liveRig{client: client, fetches: &atomic.Int32{}}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := sweepThrowawayMachines(cctx, client); err != nil {
			t.Errorf("throwaway cleanup: %v", err)
		}
	})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = sweepThrowawayMachines(cctx, client)
		cancel()
		os.Exit(1)
	}()
	done := make(chan struct{})
	if ok {
		go func() {
			wait := time.Until(deadline) - 20*time.Second
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-timer.C:
				cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_ = sweepThrowawayMachines(cctx, client)
				cancel()
			case <-done:
			}
		}()
	}
	h := newHarness(t)
	h.srv.SetDevEcho(false)
	t.Setenv("CASTBOT_PROVIDER_KEY_DIGITALOCEAN", liveKey)
	listen := strings.TrimSpace(os.Getenv("CASTBOT_LISTEN"))
	if listen == "" {
		listen = "127.0.0.1:18080"
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	infer := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+liveKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"deepseek-v3.2"}]}`)
			return
		}
		timer := time.NewTimer(quiet)
		select {
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\""+liveReply+"\"},\"finish_reason\":null}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/s07-infer/") {
			infer(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cli-tokens") {
			rig.fetches.Add(1)
		}
		h.srv.Handler().ServeHTTP(w, r)
	})}
	go func() { _ = srv.Serve(ln) }()
	rig.h = h
	rig.local = "http://" + ln.Addr().String()
	user, secret := h.account("live-s07@example.com", "Live", "correct-horse-1")
	h.login("live-s07@example.com", "correct-horse-1", secret, "203.0.113.240:9", false)
	sec, err := h.st.CreateSection(ctx, user.ID, "Work")
	if err != nil {
		t.Fatal(err)
	}
	agent, _, err := h.st.CreateAgent(ctx, user.ID, store.AgentInput{
		SectionID: sec.ID, Name: "Atlas", Title: "Helper", Status: "ready",
		Model: "digitalocean/deepseek-v3.2", IdleTimeout: 1800,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.UserID = user.ID
	yaml := "name: Atlas\nversion: \"1\"\nbrain:\n  model: digitalocean/deepseek-v3.2\n  instructions_file: instructions.md\ntools:\n  builtin: []\n  mcp: []\n  custom: []\n"
	if _, err := h.st.PutBundle(ctx, user.ID, agent.ID, "1", yaml, "Reply briefly.\n"); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetAgentModel(ctx, user.ID, agent.ID, "digitalocean/deepseek-v3.2"); err != nil {
		t.Fatal(err)
	}
	secretRow, err := h.st.PutSecret(ctx, user.ID, "DIGITALOCEAN_INFERENCE_BASE_URL", publicURL+"/s07-infer/v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.GrantSecret(ctx, user.ID, agent.ID, secretRow.ID); err != nil {
		t.Fatal(err)
	}
	named := &namedHost{MicroVM: client, name: name, note: func(id string) {
		rig.mu.Lock()
		rig.id = id
		rig.mu.Unlock()
	}}
	h.srv.SetHost(named, image, publicURL)
	rig.agent = agent
	t.Cleanup(func() {
		close(done)
		signal.Stop(sig)
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(shut)
		cancel()
		id := rig.created()
		if err := deleteS07(client, id); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		sweepCtx, sweepCancel := context.WithTimeout(context.Background(), 45*time.Second)
		if err := sweepThrowawayMachines(sweepCtx, client); err != nil {
			t.Errorf("throwaway cleanup: %v", err)
		}
		sweepCancel()
		if id != "" {
			waitCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			_ = client.WaitGone(waitCtx, id)
			cancel()
		}
		after, err := client.List(context.Background())
		if err != nil {
			t.Errorf("list: %v", err)
			return
		}
		seen := map[string]host.Machine{}
		for _, vm := range after {
			seen[vm.ID] = vm
		}
		for id, prev := range protected {
			got, ok := seen[id]
			if !ok || got.Name != prev.Name || got.State != prev.State {
				t.Errorf("machine %s changed", id)
			}
		}
	})
	ip := "203.0.113.240:9"
	code, _, _ := h.do(http.MethodPost, "/agents/"+agent.ID+"/machine", h.form(nil), ip, nil)
	if code != http.StatusSeeOther {
		t.Fatalf("start %d", code)
	}
	return rig
}

type namedHost struct {
	*host.MicroVM
	name string
	note func(string)
}

func (n *namedHost) Create(ctx context.Context, spec host.CreateSpec) (host.Machine, error) {
	spec.Name = n.name
	spec.Tags = append(append([]string{}, spec.Tags...), "castbot-s07")
	prev := spec.Accepted
	spec.Accepted = func(m host.Machine) {
		if n.note != nil && m.ID != "" {
			n.note(m.ID)
		}
		if prev != nil {
			prev(m)
		}
	}
	return n.MicroVM.Create(ctx, spec)
}

func deleteS07(m *host.MicroVM, id string) error {
	if id == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := m.Status(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil
		}
		return err
	}
	if !testMachineName(got.Name) {
		return errors.New("refusing to delete a machine outside the test prefix")
	}
	if err := host.GuardDelete(got); err != nil {
		return err
	}
	return m.Delete(ctx, id)
}

func guestUnix(t *testing.T, m *host.MicroVM, endpoint, token string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	code, body, err := m.EdgeCall(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/castlet/clock", token, nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("clock status %d", code)
	}
	var got struct {
		Unix int64 `json:"unix"`
	}
	if json.Unmarshal(body, &got) != nil || got.Unix == 0 {
		t.Fatal("clock body")
	}
	return got.Unix
}

func readLiveSSE(ctx context.Context, rawURL string, cookies []*http.Cookie, stop func([]sseEvent) bool) ([]sseEvent, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, err
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return nil, 0, errors.New("status " + res.Status + " " + string(raw))
	}
	sc := bufio.NewReader(res.Body)
	var out []sseEvent
	var cur sseEvent
	var data []string
	ka := 0
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
			return out, ka, nil
		}
		line, err := sc.ReadString('\n')
		if err != nil {
			return out, ka, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			ka++
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
