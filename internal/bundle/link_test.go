package bundle

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestInstallLeavesARootLink(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("ownership test must not run as root")
	}
	raw, _, err := Archive("name: Atlas\n", "Be brief.\n")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "kept")
	inside := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(outside, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, inside); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sudo", "-n", "chown", "0:0", outside).CombinedOutput()
	if err != nil {
		t.Fatalf("chown %s: %v", out, err)
	}
	if err := Install(dir, raw); err != nil {
		t.Fatal(err)
	}
	kept, err := exec.Command("sudo", "-n", "cat", outside).CombinedOutput()
	if err != nil || string(kept) != "kept" {
		t.Fatalf("outside %q %v", kept, err)
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
	if err != nil || string(got) != "name: Atlas\n" {
		t.Fatalf("yaml %q %v", got, err)
	}
	inInfo, err := os.Lstat(inside)
	if err != nil {
		t.Fatal(err)
	}
	inSt, ok := inInfo.Sys().(*syscall.Stat_t)
	if !ok || inSt.Ino == st.Ino {
		t.Fatal("install used the existing inode")
	}
}
