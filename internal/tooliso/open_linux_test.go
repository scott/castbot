//go:build linux

package tooliso

import (
	"bytes"
	"os"
	"path/filepath"
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

func TestToolEnvExplicitKeepsDeclaredNames(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"CAST_AGENT_DIR=/app/agent",
		"MCP_SERVER_NAME=notes",
		"OPENROUTER_API_KEY=fixture-model-key",
	}
	got := ToolEnvExplicit(parent, nil)
	text := ""
	for _, item := range got {
		text += item + "\n"
	}
	if !bytes.Contains([]byte(text), []byte("MCP_SERVER_NAME=notes")) {
		t.Fatalf("declared name missing\n%s", text)
	}
	if bytes.Contains([]byte(text), []byte("fixture-model-key")) {
		t.Fatal("model key was copied")
	}
}
