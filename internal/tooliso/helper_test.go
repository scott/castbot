package tooliso

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHelperScrubsTheCommandEnvironment(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "castbot-toolrun")
	build := exec.Command("go", "build", "-o", bin, "./cmd/castbot-toolrun")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	script := `printf '%s\n' "PUBLIC=${PUBLIC_TOKEN-}" "MODEL=${OPENROUTER_API_KEY-}" "NOTES=${NOTES_TOKEN-}" "NAMES=${CASTBOT_TOOL_SECRET_NAMES-}" "USER=${USER-}" "HOME=${HOME-}"`
	run := exec.Command(bin, "--", "/bin/sh", "-c", script)
	run.Env = []string{
		"CASTBOT_TOOLRUN_TEST=1",
		"PATH=" + os.Getenv("PATH"),
		"OPENROUTER_API_KEY=fixture-iso-model",
		"NOTES_TOKEN=fixture-iso-notes",
		"PUBLIC_TOKEN=fixture-public",
		"CASTBOT_TOOL_SECRET_NAMES=PUBLIC_TOKEN,OPENROUTER_API_KEY",
		"CAST_AGENT_DIR=/app/agent",
		"HOME=/tmp",
		"LANG=C",
	}
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	text := string(out)
	for _, want := range []string{"PUBLIC=fixture-public", "MODEL=", "NOTES=", "NAMES=", "USER=agent", "HOME=/app/agent"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s\n%s", want, text)
		}
	}
	if strings.Contains(text, "fixture-iso-model") || strings.Contains(text, "fixture-iso-notes") {
		t.Fatal("command saw a secret")
	}
}

func TestHelperStopsTheCommandGroup(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "castbot-toolrun")
	build := exec.Command("go", "build", "-o", bin, "./cmd/castbot-toolrun")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	run := exec.Command(bin, "--", "/bin/sh", "-c", "sleep 30 & sleep 30")
	run.Env = append(os.Environ(), "CASTBOT_TOOLRUN_TEST=1")
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := run.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- run.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = run.Process.Kill()
		t.Fatal("helper still running")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !commandStillRunning("sleep") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("command still running")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func commandStillRunning(name string) bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil {
			continue
		}
		if bytes.Contains(raw, []byte(name)) && bytes.Contains(raw, []byte("30")) {
			return true
		}
	}
	return false
}

func TestPythonPathSelftest(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", filepath.Join(root, "deploy", "tooliso", "castbot_tooliso.py"))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "tooliso-ok") {
		t.Fatalf("python: %v\n%s", err, out)
	}
}
