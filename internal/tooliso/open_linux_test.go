//go:build linux

package tooliso

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadUnderRejectsASymlinkSwap(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "box")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note"), []byte("workspace-ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/self", filepath.Join(root, "via")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUnder(root, "via/environ"); err == nil {
		t.Fatal("symlink read succeeded")
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.RemoveAll(dir)
			_ = os.Symlink("/proc/self", dir)
			_ = os.Remove(dir)
			_ = os.Mkdir(dir, 0o755)
			_ = os.WriteFile(filepath.Join(dir, "note"), []byte("workspace-ok"), 0o644)
		}
	}()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		buf, err := ReadUnder(root, "box/note")
		if err == nil && len(buf) > 0 && !bytes.Equal(buf, []byte("workspace-ok")) {
			t.Fatalf("unexpected content %q", buf)
		}
		if _, err := ReadUnder(root, "box/environ"); err == nil {
			t.Fatal("read returned a path outside the root")
		}
	}
}

func TestToolEnvExplicitDropsACopiedSecret(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"CAST_AGENT_DIR=/app/agent",
		"NOTES_TOKEN=fixture-notes",
		"PUBLIC_TOKEN=fixture-public",
		"CASTBOT_TOOL_SECRET_NAMES=PUBLIC_TOKEN",
		"OPENROUTER_API_KEY=fixture-model-key",
	}
	var mark []string
	for _, item := range parent {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		mark = append(mark, key+"\t"+sha256hex(value))
	}
	caller := append(append([]string{}, parent...), "ADDED_MARKER=added-ok", ToolParentMark+"="+strings.Join(mark, "\n"))
	got := strings.Join(ToolEnvExplicit(caller, ParseNames("PUBLIC_TOKEN")), "\n")
	if strings.Contains(got, "fixture-notes") || strings.Contains(got, "fixture-model-key") {
		t.Fatalf("copied secret reached the command\n%s", got)
	}
	if !strings.Contains(got, "PUBLIC_TOKEN=fixture-public") || !strings.Contains(got, "ADDED_MARKER=added-ok") {
		t.Fatalf("opt-in or added name missing\n%s", got)
	}
}
