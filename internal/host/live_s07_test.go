//go:build live

package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestLiveIdleStream opens one throwaway machine and holds a quiet connection
// for 30 seconds. It does not call a model.
//
//	CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... \
//	  go test -tags live -timeout 45m -count=1 -run TestLiveIdleStream ./internal/host
func TestLiveIdleStream(t *testing.T) {
	vm, client := s07Machine(t, "castbot-s07-test-idle")
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(vm.Endpoint, "/")+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	res, err := http.DefaultClient.Do(req)
	held := time.Since(started)
	if err != nil {
		t.Logf("idle stream not observed after %s", held.Round(time.Millisecond))
	} else {
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		t.Logf("idle connection status %d held %s", res.StatusCode, held.Round(time.Millisecond))
	}
	_ = client
}

// TestLiveGuestClock pauses one throwaway machine for 2 minutes and records
// whether the guest clock and the CLI token cache can be observed.
//
//	CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... \
//	  go test -tags live -timeout 45m -count=1 -run TestLiveGuestClock ./internal/host
func TestLiveGuestClock(t *testing.T) {
	vm, client := s07Machine(t, "castbot-s07-test-clock")
	before := readClock(t, vm.Endpoint)
	if _, err := client.Pause(context.Background(), vm.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Minute)
	woke, err := client.Wake(context.Background(), vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if woke.Endpoint != "" {
		vm.Endpoint = woke.Endpoint
	}
	after := readClock(t, vm.Endpoint)
	t.Logf("guest clock before %s after %s", before, after)
	if before == "not observed" || after == "not observed" {
		t.Log("guest date jump: not observed")
		t.Log("cli token list refetch: not observed")
		return
	}
	t.Logf("guest clock samples %s -> %s", before, after)
	t.Log("cli token list refetch: compare cli_cached in the two samples")
}

func readClock(t *testing.T, endpoint string) string {
	t.Helper()
	if strings.TrimSpace(endpoint) == "" {
		return "not observed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/castlet/clock", nil)
	if err != nil {
		return "not observed"
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "not observed"
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	if res.StatusCode != http.StatusOK {
		return "not observed"
	}
	var got struct {
		Unix      int64 `json:"unix"`
		CLICached bool  `json:"cli_cached"`
	}
	if json.Unmarshal(body, &got) != nil || got.Unix == 0 {
		return "not observed"
	}
	if got.CLICached {
		return "unix cached"
	}
	return "unix empty"
}

func s07Machine(t *testing.T, name string) (Machine, *MicroVM) {
	t.Helper()
	deadline, ok := t.Deadline()
	if err := LiveWindow(deadline, ok, time.Now()); err != nil {
		t.Fatal(err)
	}
	token := DOToken()
	if token == "" {
		t.Skip("CASTBOT_DO_TOKEN is not set")
	}
	image := strings.TrimSpace(os.Getenv("CASTBOT_IMAGE"))
	publicURL := strings.TrimSpace(os.Getenv("CASTBOT_PUBLIC_URL"))
	if image == "" || publicURL == "" {
		t.Skip("CASTBOT_IMAGE and CASTBOT_PUBLIC_URL are required")
	}
	if !strings.HasPrefix(name, "castbot-s07-test-") {
		t.Fatal("name")
	}
	client, err := New(Options{Token: token, Interval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	protected := map[string]Machine{}
	for _, vm := range before {
		if strings.HasPrefix(vm.Name, "castbot-s07-test-") {
			continue
		}
		protected[vm.ID] = vm
	}
	for _, vm := range before {
		if !strings.HasPrefix(vm.Name, "castbot-s07-test-") {
			continue
		}
		if err := deleteS07(client, vm.ID); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var created string
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		mu.Lock()
		id := created
		mu.Unlock()
		_ = deleteS07(client, id)
		os.Exit(1)
	}()
	t.Cleanup(func() {
		signal.Stop(sig)
		mu.Lock()
		id := created
		created = ""
		mu.Unlock()
		if err := deleteS07(client, id); err != nil {
			t.Errorf("cleanup: %v", err)
		}
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
		seen := map[string]Machine{}
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
	spec := CreateSpec{
		Name: name, AgentID: "s07" + strings.ToLower(time.Now().UTC().Format("150405")), Image: image,
		Region: DefaultRegion, CPU: DefaultCPU, MemoryMiB: DefaultMemoryMiB,
		Idle: 30 * time.Minute, HTTPPort: DefaultHTTPPort, ControlURL: publicURL,
		Bootstrap: "cbt_s07_bootstrap_not_logged",
		Tags:      []string{"castbot-s07"},
	}
	vm, err := client.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(vm.Name, "castbot-s07-test-") {
		_ = deleteS07(client, vm.ID)
		t.Fatal("created a machine outside the test prefix")
	}
	mu.Lock()
	created = vm.ID
	mu.Unlock()
	return vm, client
}

func deleteS07(m *MicroVM, id string) error {
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
	if !strings.HasPrefix(got.Name, "castbot-s07-test-") {
		return errors.New("refusing to delete a machine outside castbot-s07-test")
	}
	if err := GuardDelete(got); err != nil {
		return err
	}
	return m.Delete(ctx, id)
}
