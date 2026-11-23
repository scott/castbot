package castlet

import (
	"archive/tar"
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func foreignDir(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Fatal("ownership test must not run as root")
	}
	if err := os.MkdirAll(path, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o770); err != nil {
		t.Fatal(err)
	}
	spec := fmt.Sprintf("65534:%d", os.Getgid())
	out, err := exec.Command("sudo", "-n", "chown", spec, path).CombinedOutput()
	if err != nil {
		t.Fatalf("chown %s: %v", out, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == os.Getuid() {
		t.Fatalf("owner stayed with the test user: %v", info.Sys())
	}
	if err := os.Chmod(path, 0o770); err == nil {
		t.Fatal("chmod of a foreign directory succeeded")
	}
}

func assertForeignUnchanged(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o770 || info.Mode()&os.ModeSetgid != 0 {
		t.Fatalf("foreign dir mode %v", info.Mode())
	}
	st := info.Sys().(*syscall.Stat_t)
	if int(st.Uid) == os.Getuid() {
		t.Fatal("foreign dir owner changed")
	}
}

func TestUploadIntoAForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "workspace", "project")
	foreignDir(t, foreign)
	sup := readySup(t, dir)
	rec := authed(t, sup, http.MethodPut, "/castlet/files/project/hello.txt", strings.NewReader("hello file"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("put %d %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(foreign, "hello.txt"))
	if err != nil || string(got) != "hello file" {
		t.Fatalf("body %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(foreign, "hello.txt"))
	if err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("mode %v %v", info, err)
	}
	assertForeignUnchanged(t, foreign)
}

func TestMemoryImportIntoAForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "memory", "box")
	foreignDir(t, foreign)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("remember this")
	if err := tw.WriteHeader(&tar.Header{Name: "box/note.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
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
	got, err := os.ReadFile(filepath.Join(foreign, "note.txt"))
	if err != nil || string(got) != "remember this" {
		t.Fatalf("body %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(foreign, "note.txt"))
	if err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("mode %v %v", info, err)
	}
	assertForeignUnchanged(t, foreign)
}

func TestMemoryImportDoesNotFollowAPlantedLink(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "agent")
	sealed := filepath.Join(root, ".cast", "secret")
	if err := os.MkdirAll(filepath.Dir(sealed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sealed, []byte("sealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(agent, "memory"), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sealed, filepath.Join(agent, "memory", "note.txt")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{Name: "note.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	sup := readySup(t, agent)
	rec := authed(t, sup, http.MethodPut, "/castlet/memory", bytes.NewReader(buf.Bytes()))
	if rec.Code == http.StatusNoContent {
		t.Fatal("import followed a planted link")
	}
	got, err := os.ReadFile(sealed)
	if err != nil || string(got) != "sealed" {
		t.Fatalf("sealed %q %v", got, err)
	}
}

func TestArchiveImportIntoAForeignDirectory(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent")
	foreign := filepath.Join(agent, "workspace", "owned")
	foreignDir(t, foreign)
	raw := tarBytes(t, &tar.Header{Name: "workspace/owned/note.txt", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg, Format: tar.FormatPAX}, []byte("note"))
	sup := testSup(t, agent)
	rec := authed(t, sup, http.MethodPut, "/castlet/archive", bytes.NewReader(raw))
	if rec.Code != http.StatusOK {
		t.Fatalf("import %d %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(foreign, "note.txt"))
	if err != nil || string(got) != "note" {
		t.Fatalf("body %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(foreign, "note.txt"))
	if err != nil || info.Mode().Perm() != 0o660 {
		t.Fatalf("mode %v %v", info, err)
	}
	assertForeignUnchanged(t, foreign)
}
