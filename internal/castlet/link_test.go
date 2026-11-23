package castlet

import (
	"archive/tar"
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func rootLink(t *testing.T, outside, inside string, body []byte) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Fatal("ownership test must not run as root")
	}
	if err := os.MkdirAll(filepath.Dir(inside), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, inside); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sudo", "-n", "chown", "0:0", outside).CombinedOutput()
	if err != nil {
		t.Fatalf("chown %s: %v", out, err)
	}
}

func assertRootKept(t *testing.T, outside, inside, body, written string) {
	t.Helper()
	out, err := exec.Command("sudo", "-n", "cat", outside).CombinedOutput()
	if err != nil || string(out) != body {
		t.Fatalf("outside %q %v", out, err)
	}
	info, err := os.Lstat(outside)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("outside uid %d mode %v", st.Uid, info.Mode())
	}
	got, err := os.ReadFile(inside)
	if err != nil || string(got) != written {
		t.Fatalf("inside %q %v", got, err)
	}
	inInfo, err := os.Lstat(inside)
	if err != nil {
		t.Fatal(err)
	}
	inSt, ok := inInfo.Sys().(*syscall.Stat_t)
	if !ok || inSt.Ino == st.Ino {
		t.Fatal("write used the existing inode")
	}
}

func TestMemoryImportLeavesARootLink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "kept")
	inside := filepath.Join(dir, "memory", "note.txt")
	rootLink(t, outside, inside, []byte("kept"))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("fresh")
	if err := tw.WriteHeader(&tar.Header{Name: "note.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	sup := readySup(t, dir)
	rec := authed(t, sup, http.MethodPut, "/castlet/memory", bytes.NewReader(buf.Bytes()))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("import %d %s", rec.Code, rec.Body.String())
	}
	assertRootKept(t, outside, inside, "kept", "fresh")
}

func TestArchiveRestoreLeavesARootLink(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent")
	outside := filepath.Join(dir, "kept")
	inside := filepath.Join(agent, "workspace", "note.txt")
	rootLink(t, outside, inside, []byte("kept"))
	localOut := filepath.Join(dir, "kept-local")
	localIn := filepath.Join(dir, ".local", "pkg")
	rootLink(t, localOut, localIn, []byte("local"))
	body := []byte("fresh")
	localBody := []byte("pkg")
	raw := tarBytes(t,
		&tar.Header{Name: "workspace/note.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}, body,
		&tar.Header{Name: "local/pkg", Mode: 0o644, Size: int64(len(localBody)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}, localBody,
	)
	sup := testSup(t, agent)
	rec := authed(t, sup, http.MethodPut, "/castlet/archive", bytes.NewReader(raw))
	if rec.Code != http.StatusOK {
		t.Fatalf("import %d %s", rec.Code, rec.Body.String())
	}
	assertRootKept(t, outside, inside, "kept", "fresh")
	assertRootKept(t, localOut, localIn, "local", "pkg")
}
