package castlet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/runtime"
)

func TestReloadAppliesARotatedSecret(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	t.Cleanup(echo.Close)
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	raw, sum, err := bundle.Archive("name: Atlas\n", "Reply briefly.\n")
	if err != nil {
		t.Fatal(err)
	}
	var value atomic.Value
	value.Store("alpha-value-1")
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/bundle"):
			w.Header().Set("X-Castbot-Bundle-SHA256", sum)
			w.Header().Set("X-Castbot-Bundle-Version", "1")
			w.Write(raw)
		case strings.HasSuffix(r.URL.Path, "/secrets"):
			fmt.Fprintf(w, `{"secrets":[{"name":"ROTATED","value":"%s"}]}`, value.Load().(string))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cp.Close)
	dir := t.TempDir()
	hashPath := filepath.Join(dir, "seen")
	script := fmt.Sprintf(`printf '%%s' "$ROTATED" | sha256sum | awk '{print $1}' > %q; exec sleep 60`, hashPath)
	sup, err := New(Config{
		Token:      "agent-token",
		ControlURL: cp.URL,
		AgentID:    "agt1",
		AgentDir:   filepath.Join(dir, "agent"),
		RuntimeURL: up.URL,
		RuntimeCmd: []string{"/bin/sh", "-c", script},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Close)
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !waitHash(t, hashPath, sha256hex("alpha-value-1")) {
		raw, err := os.ReadFile(hashPath)
		t.Fatalf("runtime env did not pick up the secret\nfile: %q %v\nlog: %s", raw, err, sup.logs.Bytes())
	}
	value.Store("beta-value-2")
	rec := authed(t, sup, http.MethodPost, "/castlet/reload", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reload %d %s", rec.Code, rec.Body.String())
	}
	if !waitHash(t, hashPath, sha256hex("beta-value-2")) {
		t.Fatal("reload did not apply the rotated secret")
	}
}

func TestReloadDoesNotOverlap(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	t.Cleanup(echo.Close)
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	raw, sum, err := bundle.Archive("name: Atlas\n", "Reply briefly.\n")
	if err != nil {
		t.Fatal(err)
	}
	var pulls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/bundle"):
			w.Header().Set("X-Castbot-Bundle-SHA256", sum)
			w.Header().Set("X-Castbot-Bundle-Version", "1")
			w.Write(raw)
		case strings.HasSuffix(r.URL.Path, "/secrets"):
			if pulls.Add(1) == 2 {
				close(entered)
				<-release
			}
			w.Write([]byte(`{"secrets":[{"name":"ROTATED","value":"alpha-value-1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cp.Close)
	dir := t.TempDir()
	sup, err := New(Config{
		Token:      "agent-token",
		ControlURL: cp.URL,
		AgentID:    "agt1",
		AgentDir:   filepath.Join(dir, "agent"),
		RuntimeURL: up.URL,
		RuntimeCmd: []string{"/bin/sh", "-c", "exec sleep 60"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Close)
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		rec := authed(t, sup, http.MethodPost, "/castlet/reload", nil)
		done <- rec.Code
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not reach the control plane")
	}
	second := authed(t, sup, http.MethodPost, "/castlet/reload", nil)
	if second.Code != http.StatusConflict {
		t.Fatalf("overlapping reload %d", second.Code)
	}
	close(release)
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("first reload %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first reload did not finish")
	}
}

func TestDeadRuntimeIsNotReady(t *testing.T) {
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	t.Cleanup(echo.Close)
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	raw, sum, err := bundle.Archive("name: Atlas\n", "Reply briefly.\n")
	if err != nil {
		t.Fatal(err)
	}
	cp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/bundle"):
			w.Header().Set("X-Castbot-Bundle-SHA256", sum)
			w.Header().Set("X-Castbot-Bundle-Version", "1")
			w.Write(raw)
		case strings.HasSuffix(r.URL.Path, "/secrets"):
			w.Write([]byte(`{"secrets":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cp.Close)
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "pid")
	sup, err := New(Config{
		Token:      "agent-token",
		ControlURL: cp.URL,
		AgentID:    "agt1",
		AgentDir:   filepath.Join(dir, "agent"),
		RuntimeURL: up.URL,
		RuntimeCmd: []string{"/bin/sh", "-c", fmt.Sprintf("echo $$ > %q; exec sleep 60", pidPath)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sup.Close)
	if err := sup.Boot(context.Background()); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := os.ReadFile(pidPath)
		if err == nil {
			if _, err := fmt.Sscan(strings.TrimSpace(string(raw)), &pid); err == nil && pid > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("runtime pid was not written")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		rec := authed(t, sup, http.MethodGet, "/castlet/health", nil)
		if rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), `"ready":false`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("health stayed ready: %d %s", rec.Code, rec.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	card, err := http.Get(up.URL + "/.well-known/agent.json")
	if err != nil || card.StatusCode != http.StatusOK {
		t.Fatal("probe target should still be up")
	}
	card.Body.Close()

	dead, err := New(Config{
		Token:      "agent-token",
		ControlURL: cp.URL,
		AgentID:    "agt1",
		AgentDir:   filepath.Join(dir, "agent2"),
		RuntimeURL: up.URL,
		RuntimeCmd: []string{"/bin/sh", "-c", "exit 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dead.Close)
	if err := dead.Boot(context.Background()); err != nil {
		rec := authed(t, dead, http.MethodGet, "/castlet/health", nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("dead boot health %d", rec.Code)
		}
		return
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		rec := authed(t, dead, http.MethodGet, "/castlet/health", nil)
		if rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), `"ready":false`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a runtime that exits at once stayed ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitHash(t *testing.T, path, want string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(raw)) == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
