package tooliso

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
