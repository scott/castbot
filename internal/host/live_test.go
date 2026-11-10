//go:build live

package host

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestLiveTiming creates one machine and deletes it.
//
// Run it with:
//
//	go test -tags live -timeout 45m ./internal/host
//
// It refuses a shorter deadline. go test's default timeout is 10 minutes,
// and a timeout panic does not run t.Cleanup, which would leave the machine
// running. SIGINT and SIGTERM delete it too. Leftover machines tagged
// castbot-live are deleted at the start. Demo machines and the castbot-s00
// tag are refused by GuardDelete.
//
// Set CASTBOT_DO_TOKEN, CASTBOT_IMAGE, and an https CASTBOT_PUBLIC_URL.
// DIGITALOCEAN_ACCESS_TOKEN and DIGITALOCEAN_TOKEN are ignored.
//
// This does not measure first-create-to-HTTP-200, resume-to-first-200, or a
// 30 second idle stream. Those need a deployed control plane that can issue
// a real agent token and hold an SSE stream. A fake bootstrap cannot get a
// 200 from castlet, and GET / is not an idle stream. The only timing logged
// here is create until the machine API reports running.
func TestLiveTiming(t *testing.T) {
	deadline, ok := t.Deadline()
	if err := LiveWindow(deadline, ok, time.Now()); err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(os.Getenv("CASTBOT_DO_TOKEN"))
	if token == "" {
		t.Skip("CASTBOT_DO_TOKEN is not set")
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
	for _, vm := range LiveLeftovers(before) {
		if err := client.Delete(ctx, vm.ID); err != nil {
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
		_ = safeDelete(client, id)
		os.Exit(1)
	}()
	t.Cleanup(func() {
		signal.Stop(sig)
		mu.Lock()
		id := created
		created = ""
		mu.Unlock()
		if err := safeDelete(client, id); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	agentID := "s04" + strings.ToLower(time.Now().UTC().Format("150405"))
	spec := CreateSpec{
		Name: MachineName(agentID), AgentID: agentID, Image: image,
		Region: DefaultRegion, CPU: DefaultCPU, MemoryMiB: DefaultMemoryMiB,
		Idle: DefaultIdle, HTTPPort: DefaultHTTPPort, ControlURL: publicURL,
		Bootstrap: "cbt_live_bootstrap_not_logged",
		Tags:      []string{"castbot-live"},
	}
	started := time.Now()
	vm, err := client.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	created = vm.ID
	mu.Unlock()
	t.Logf("api running after %s state %s", time.Since(started).Round(time.Millisecond), vm.State)
	if err := safeDelete(client, vm.ID); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	created = ""
	mu.Unlock()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer waitCancel()
	if err := client.WaitGone(waitCtx, vm.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.WaitUnlisted(waitCtx, func(got Machine) bool {
		return got.Name == spec.Name || (hasTag(got, "castbot-live") && strings.Contains(got.Name, agentID))
	}); err != nil {
		t.Fatal(err)
	}
	after, err := client.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range after {
		if got.Name == spec.Name || (hasTag(got, "castbot-live") && strings.Contains(got.Name, agentID)) {
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
