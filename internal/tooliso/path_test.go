package tooliso

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAllowRejectsProcAndEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("workspace-ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc/self/environ", filepath.Join(root, "envlink")); err != nil {
		t.Fatal(err)
	}
	procDir := filepath.Join(root, "procdir")
	if err := os.Symlink("/proc", procDir); err != nil {
		t.Fatal(err)
	}
	roots := []string{root}
	got, err := Allow("note.txt", roots)
	if err != nil || got != filepath.Join(root, "note.txt") {
		t.Fatalf("note %q %v", got, err)
	}
	for _, path := range []string{
		"/proc/self/environ",
		"/proc/1/mem",
		"/sys/kernel",
		"/dev/null",
		"/etc/passwd",
		filepath.Join(root, "escape"),
		filepath.Join(root, "envlink"),
		filepath.Join(procDir, "self", "environ"),
		"../" + filepath.Base(root) + "/../../etc/passwd",
		filepath.Join(root, "..", "..", "etc", "passwd"),
	} {
		if _, err := Allow(path, roots); err == nil {
			t.Fatalf("allowed %s", path)
		}
	}
}

func TestAllowMissingFileInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	got, err := Allow("missing.txt", []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "missing.txt") {
		t.Fatal(got)
	}
}
