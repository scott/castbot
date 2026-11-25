package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/scott/castbot/internal/update"
)

func TestApplyConfigIgnoresOverrideWithoutRoot(t *testing.T) {
	t.Setenv("CASTBOT_APPLY_ROOT", "")
	t.Setenv("CASTBOT_APPLY_RELEASE_BASE", "https://example.invalid/download")
	t.Setenv("CASTBOT_APPLY_COMPARE_BASE", "http://127.0.0.1/compare/")
	t.Setenv("CASTBOT_APPLY_PUBKEY", "/tmp/not-used")
	cfg, err := applyConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != "" || cfg.Checker.ReleaseBase != "" || cfg.Checker.Client != nil {
		t.Fatal("test override applied without a root")
	}
}

func TestApplyConfigRejectsRemoteBases(t *testing.T) {
	root := t.TempDir()
	pub := filepath.Join(root, "pub")
	if err := os.WriteFile(pub, []byte("test-public-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASTBOT_APPLY_ROOT", root)
	t.Setenv("CASTBOT_APPLY_PUBKEY", pub)
	t.Setenv("CASTBOT_APPLY_COMPARE_BASE", "http://127.0.0.1:9/compare/")
	cases := []string{
		"https://127.0.0.1/download",
		"http://localhost/download",
		"http://user@127.0.0.1/download",
		"http://8.8.8.8/download",
		"http://127.0.0.1/download?x=1",
	}
	for _, raw := range cases {
		t.Setenv("CASTBOT_APPLY_RELEASE_BASE", raw)
		if _, err := applyConfigFromEnv(); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	t.Setenv("CASTBOT_APPLY_ROOT", "relative/root")
	t.Setenv("CASTBOT_APPLY_RELEASE_BASE", "http://127.0.0.1:9/download")
	if _, err := applyConfigFromEnv(); err == nil {
		t.Fatal("relative root accepted")
	}
}

func TestApplyConfigTestLayout(t *testing.T) {
	root := t.TempDir()
	pub := filepath.Join(root, "pub")
	if err := os.WriteFile(pub, []byte("test-public-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CASTBOT_APPLY_ROOT", root)
	t.Setenv("CASTBOT_APPLY_RELEASE_BASE", "http://127.0.0.1:9/download")
	t.Setenv("CASTBOT_APPLY_COMPARE_BASE", "http://127.0.0.1:9/compare/")
	t.Setenv("CASTBOT_APPLY_REGISTRY", "http://127.0.0.1:9")
	t.Setenv("CASTBOT_APPLY_PUBKEY", pub)
	t.Setenv("CASTBOT_APPLY_HEALTH", "http://127.0.0.1:9/login")
	t.Setenv("CASTBOT_APPLY_HEALTH_WAIT", "1s")
	t.Setenv("CASTBOT_APPLY_NOW", "2026-11-24T18:00:00Z")
	t.Setenv("CASTBOT_APPLY_OWNER", "someone")
	t.Setenv("CASTBOT_APPLY_GROUP", "someone")
	cfg, err := applyConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != filepath.Join(root, "opt", "castbot") {
		t.Fatalf("root %s", cfg.Root)
	}
	if cfg.EnvFile != filepath.Join(root, "etc", "castbot", "castbot.env") {
		t.Fatalf("env %s", cfg.EnvFile)
	}
	if cfg.HealthURL != "http://127.0.0.1:9/login" || cfg.HealthFor != time.Second {
		t.Fatal("health override")
	}
	if cfg.Owner != "someone" || cfg.Group != "someone" {
		t.Fatal("owner override")
	}
	if string(cfg.Checker.Key) != "test-public-key\n" || cfg.Checker.RegistryBase != "http://127.0.0.1:9" {
		t.Fatal("checker override")
	}
	if cfg.Now == nil || !cfg.Now().Equal(time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)) {
		t.Fatal("clock override")
	}
	t.Setenv("CASTBOT_APPLY_HEALTH", "http://127.0.0.1:9/healthz")
	if _, err := applyConfigFromEnv(); err == nil {
		t.Fatal("health path accepted")
	}
}

func TestStartHelperDoesNotWait(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "helper")
	marker := filepath.Join(dir, "ran")
	// Not "sleep 30". The tool runner test treats any process whose
	// command line contains sleep and 30 as its own child.
	body := "#!/bin/sh\nprintf 'ran\\n' > '" + marker + "'\nsleep 5\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd, err := startHelper(script)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		// The helper is a session leader, so kill the whole group.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("helper did not start")
}

func TestStartHelperMissing(t *testing.T) {
	if _, err := startHelper(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing helper started")
	}
}

func TestUpdateHelperUsesSudo(t *testing.T) {
	cmd := helperCommand()
	want := []string{"sudo", "-n", "--", update.WrapperPath}
	if strings.Join(cmd.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("args %v", cmd.Args)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("helper session")
	}
}

func TestUpdateUsage(t *testing.T) {
	bin := buildCastbot(t)
	cmd := exec.Command(bin, "update")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "usage: castbot update apply") {
		t.Fatalf("usage %v %s", err, out)
	}
}

func buildCastbot(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "castbot")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = "."
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	return bin
}
