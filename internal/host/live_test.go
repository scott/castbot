//go:build live

package host

import (
	"context"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestLiveTiming measures one machine and deletes it.
// It does not run in CI. go test ./... omits this file.
// Set DIGITALOCEAN_ACCESS_TOKEN or DIGITALOCEAN_TOKEN, CASTBOT_IMAGE, and
// CASTBOT_PUBLIC_URL (https) to run it. The harness refuses the demo
// machines and the leftover probe tag, creates at most two machines, and
// deletes every machine it creates.
func TestLiveTiming(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("DIGITALOCEAN_ACCESS_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("DIGITALOCEAN_TOKEN"))
	}
	if token == "" {
		t.Skip("no writable machine token")
	}
	image := strings.TrimSpace(os.Getenv("CASTBOT_IMAGE"))
	publicURL := strings.TrimSpace(os.Getenv("CASTBOT_PUBLIC_URL"))
	if image == "" || publicURL == "" {
		t.Skip("CASTBOT_IMAGE and CASTBOT_PUBLIC_URL are required")
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
		if strings.HasPrefix(vm.Name, "cast-demo-agent") || hasTag(vm, "castbot-s00") {
			protected[vm.ID] = vm
		}
	}
	agentID := "s04" + strings.ToLower(time.Now().UTC().Format("150405"))
	spec := CreateSpec{
		Name: MachineName(agentID), AgentID: agentID, Image: image,
		Region: DefaultRegion, CPU: DefaultCPU, MemoryMiB: DefaultMemoryMiB,
		Idle: 20 * time.Minute, HTTPPort: DefaultHTTPPort, ControlURL: publicURL,
		Bootstrap: "cbt_live_bootstrap_not_logged",
	}
	started := time.Now()
	vm, err := client.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := safeDelete(client, vm.ID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	created := time.Since(started)
	probeStart := time.Now()
	// The first HTTP 200 is measured with the machine token only when the
	// guest does not require an agent token yet. Probe sends both.
	pctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	err = client.Probe(pctx, vm.Endpoint, spec.Bootstrap)
	cancel()
	probed := time.Since(probeStart)
	t.Logf("create %s probe %s state %s", created.Round(time.Millisecond), probed.Round(time.Millisecond), vm.State)
	if err != nil {
		t.Logf("probe did not reach 200: %v", err)
	}

	// Wait until the API reports paused. Do not call the guest while waiting.
	deadline := time.Now().Add(25 * time.Minute)
	var paused Machine
	for time.Now().Before(deadline) {
		got, err := client.Status(ctx, vm.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == "paused" {
			paused = got
			break
		}
		time.Sleep(5 * time.Second)
	}
	if paused.State != "paused" {
		t.Fatal("machine did not auto-pause")
	}
	resumeStart := time.Now()
	woke, err := client.Wake(ctx, vm.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resume api %s state %s", time.Since(resumeStart).Round(time.Millisecond), woke.State)

	idleStart := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(vm.Endpoint, "/")+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Cast-Authorization", "Bearer "+spec.Bootstrap)
	idleClient := &http.Client{Timeout: 45 * time.Second}
	res, err := idleClient.Do(req)
	if err != nil {
		t.Logf("idle stream did not open")
	} else {
		t.Logf("idle stream status %d", res.StatusCode)
		defer res.Body.Close()
		buf := make([]byte, 1024)
		var reads atomic.Int32
		readCtx, readCancel := context.WithTimeout(ctx, 30*time.Second)
		defer readCancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				_, rerr := res.Body.Read(buf)
				reads.Add(1)
				if rerr != nil {
					return
				}
			}
		}()
		select {
		case <-done:
			t.Logf("idle connection closed after %s reads %d", time.Since(idleStart).Round(time.Millisecond), reads.Load())
		case <-readCtx.Done():
			t.Logf("idle connection still open after 30s reads %d", reads.Load())
		}
	}

	if err := safeDelete(client, vm.ID); err != nil {
		t.Fatal(err)
	}
	vm.ID = ""
	after, err := client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range after {
		if strings.HasPrefix(got.Name, "castbot-"+agentID) || got.Name == spec.Name {
			t.Fatalf("machine still listed %s", got.ID)
		}
	}
	for id, prev := range protected {
		found := false
		for _, got := range after {
			if got.ID == id {
				found = true
				if got.State != prev.State {
					t.Fatalf("protected machine %s changed state", id)
				}
			}
		}
		if !found {
			t.Fatalf("protected machine %s missing", id)
		}
	}
	t.Logf("deleted; protected %d unchanged", len(protected))
}

func safeDelete(m *MicroVM, id string) error {
	if id == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := m.Status(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "status 404") {
			return nil
		}
	} else if err := GuardDelete(got); err != nil {
		return err
	}
	return m.Delete(ctx, id)
}

func hasTag(m Machine, tag string) bool {
	for _, item := range m.Tags {
		if item == tag {
			return true
		}
	}
	return false
}
