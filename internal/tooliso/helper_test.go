package tooliso

import (
	"bytes"
	"errors"
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

func TestCopiedEnvironmentDropsAGrantedSecret(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3")
	}
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
	script := `
import os, subprocess
env = os.environ.copy()
env["ADDED_MARKER"] = "added-ok"
out = subprocess.check_output(
    ["/bin/sh", "-c", 'printf "%s\\n%s\\n%s" "$NOTES_TOKEN" "$PUBLIC_TOKEN" "$ADDED_MARKER"'],
    env=env,
    text=True,
)
print(out)
`
	run := exec.Command("python3", "-c", script)
	run.Dir = root
	run.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PYTHONPATH=" + filepath.Join(root, "deploy", "tooliso"),
		"CASTBOT_TOOLRUN=" + bin,
		"CASTBOT_TOOLRUN_TEST=1",
		"NOTES_TOKEN=fixture-granted",
		"PUBLIC_TOKEN=fixture-public",
		"CASTBOT_TOOL_SECRET_NAMES=PUBLIC_TOKEN",
		"HOME=/tmp",
		"LANG=C",
	}
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("python: %v\n%s", err, out)
	}
	text := string(out)
	if strings.Contains(text, "fixture-granted") {
		t.Fatalf("copied secret reached the command\n%s", text)
	}
	if !strings.Contains(text, "fixture-public") || !strings.Contains(text, "added-ok") {
		t.Fatalf("opt-in or added name missing\n%s", text)
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

func TestHelperReturnsWhenOutputStaysOpen(t *testing.T) {
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
	t.Cleanup(func() { stopMatching("sleep", "600") })

	prompt := exec.Command(bin, "--", "/bin/sh", "-c", "echo hi")
	prompt.Env = append(os.Environ(), "CASTBOT_TOOLRUN_TEST=1")
	started := time.Now()
	out, err := prompt.CombinedOutput()
	if err != nil {
		t.Fatalf("echo: %v\n%s", err, out)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("echo took %s", time.Since(started))
	}
	if !bytes.Contains(out, []byte("hi")) {
		t.Fatalf("echo output %q", out)
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "shell", args: []string{"/bin/sh", "-c", "echo before-timeout; setsid sleep 600"}, want: "before-timeout"},
		{name: "setsid", args: []string{"setsid", "sleep", "600"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := exec.Command(bin, append([]string{"--"}, tc.args...)...)
			run.Env = append(os.Environ(), "CASTBOT_TOOLRUN_TEST=1")
			var buf bytes.Buffer
			run.Stdout = &buf
			run.Stderr = &buf
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
			if tc.want != "" && !bytes.Contains(buf.Bytes(), []byte(tc.want)) {
				t.Fatalf("output %q", buf.Bytes())
			}
		})
	}
}

func stopMatching(parts ...string) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		raw, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil {
			continue
		}
		match := true
		for _, part := range parts {
			if !bytes.Contains(raw, []byte(part)) {
				match = false
				break
			}
		}
		if match {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func TestHelperRefusesWithoutTheServer(t *testing.T) {
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
	run := exec.Command(bin, "--", "/bin/echo", "should-not-run")
	run.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"CASTBOT_TOOL_SOCK=" + filepath.Join(t.TempDir(), "missing.sock"),
	}
	out, err := run.CombinedOutput()
	if err == nil {
		t.Fatalf("helper ran: %s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 126 {
		t.Fatalf("exit %v\n%s", err, out)
	}
	if strings.Contains(string(out), "should-not-run") {
		t.Fatalf("command ran\n%s", out)
	}
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
