//go:build linux

package castlet

import (
	"archive/tar"
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/scott/castbot/internal/bundle"
	"github.com/scott/castbot/internal/tooliso"
)

func TestAppBoundary(t *testing.T) {
	if os.Getenv("CASTBOT_BOUNDARY_CHILD") == "1" || os.Getenv("CASTBOT_BOUNDARY") != "1" {
		t.Skip()
	}
	if os.Geteuid() != 0 {
		t.Fatal("boundary")
	}
	root := t.TempDir()
	app := filepath.Join(root, "app")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "marker"), []byte("marker"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	beforeSt := before.Sys().(*syscall.Stat_t)
	if err := os.Symlink(target, filepath.Join(app, ".cast")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(app, ".castlet")); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(app, "agent")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(os.Stderr)
	if err := tooliso.PrepareApp(agent); err != nil {
		t.Fatal(err)
	}
	text := logged.String()
	if !strings.Contains(text, "fs.protected_hardlinks") || !strings.Contains(text, "fs.protected_symlinks") {
		t.Fatal(text)
	}
	for _, name := range []string{".cast", ".castlet"} {
		info, err := os.Lstat(filepath.Join(app, name))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s %v %v", name, info, err)
		}
	}
	marker, err := os.ReadFile(filepath.Join(target, "marker"))
	if err != nil || string(marker) != "marker" {
		t.Fatalf("marker %q %v", marker, err)
	}
	after, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	afterSt := after.Sys().(*syscall.Stat_t)
	if afterSt.Uid != beforeSt.Uid || afterSt.Gid != beforeSt.Gid {
		t.Fatalf("target owner changed %d %d", afterSt.Uid, afterSt.Gid)
	}
	appInfo, err := os.Lstat(app)
	if err != nil {
		t.Fatal(err)
	}
	appSt := appInfo.Sys().(*syscall.Stat_t)
	if appSt.Uid != 0 || appInfo.Mode().Perm() != 0o755 {
		t.Fatalf("app %d %o", appSt.Uid, appInfo.Mode().Perm())
	}

	kept := filepath.Join(root, "kept")
	if err := os.WriteFile(kept, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(kept, filepath.Join(agent, "agent.yaml")); err != nil {
		t.Fatal(err)
	}
	raw, _, err := bundle.Archive("name: Atlas\n", "Be brief.\n")
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Install(agent, raw); err != nil {
		t.Fatal(err)
	}
	assertSameInodeKept(t, kept, filepath.Join(agent, "agent.yaml"), "kept", "name: Atlas\n")

	memOut := filepath.Join(root, "mem-kept")
	memIn := filepath.Join(agent, "memory", "note.txt")
	if err := os.MkdirAll(filepath.Dir(memIn), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memOut, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(memOut, memIn); err != nil {
		t.Fatal(err)
	}
	body := []byte("fresh")
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
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
	if rec.Code != http.StatusNoContent {
		t.Fatalf("memory %d %s", rec.Code, rec.Body.String())
	}
	assertSameInodeKept(t, memOut, memIn, "kept", "fresh")

	arcOut := filepath.Join(root, "arc-kept")
	arcIn := filepath.Join(agent, "workspace", "note.txt")
	if err := os.MkdirAll(filepath.Dir(arcIn), 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(arcOut, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(arcOut, arcIn); err != nil {
		t.Fatal(err)
	}
	rawArc := tarBytes(t, &tar.Header{Name: "workspace/note.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg, Format: tar.FormatPAX}, []byte("fresh"))
	rec = authed(t, sup, http.MethodPut, "/castlet/archive", bytes.NewReader(rawArc))
	if rec.Code != http.StatusOK {
		t.Fatalf("archive %d %s", rec.Code, rec.Body.String())
	}
	assertSameInodeKept(t, arcOut, arcIn, "kept", "fresh")

	hard, _ := os.ReadFile("/proc/sys/fs/protected_hardlinks")
	sym, _ := os.ReadFile("/proc/sys/fs/protected_symlinks")
	t.Logf("sysctl %s %s", strings.TrimSpace(string(hard)), strings.TrimSpace(string(sym)))
	linkDir := filepath.Join(root, "links")
	if err := os.MkdirAll(linkDir, 0o777); err != nil {
		t.Fatal(err)
	}
	account, err := user.Lookup("nobody")
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)
	if err := os.Chown(linkDir, uid, gid); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run", "^TestAppBoundaryChild$")
	child.Dir = "/tmp"
	child.Env = []string{
		"CASTBOT_BOUNDARY_CHILD=1",
		"CASTBOT_APP=" + app,
		"CASTBOT_KEPT=" + kept,
		"CASTBOT_LINK=" + filepath.Join(linkDir, "extra"),
		"PATH=" + os.Getenv("PATH"),
	}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
		Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{uint32(gid)},
	}}
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("child %v %s", err, out)
	}
	if bytes.Contains(out, []byte("link-created")) {
		if err := bundle.Install(agent, raw); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(kept)
		if err != nil || string(got) != "kept" {
			t.Fatalf("kept after extra link %q %v", got, err)
		}
	}
}

func TestAppBoundaryChild(t *testing.T) {
	if os.Getenv("CASTBOT_BOUNDARY_CHILD") != "1" {
		t.Skip()
	}
	app := os.Getenv("CASTBOT_APP")
	for _, name := range []string{".cast", ".castlet"} {
		if err := os.Symlink("/tmp/elsewhere", filepath.Join(app, name)); err == nil {
			t.Fatalf("swapped %s", name)
		}
	}
	if err := os.Link(os.Getenv("CASTBOT_KEPT"), os.Getenv("CASTBOT_LINK")); err != nil {
		fmt.Println("link-refused")
		return
	}
	fmt.Println("link-created")
}

func assertSameInodeKept(t *testing.T, outside, inside, body, written string) {
	t.Helper()
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != body {
		t.Fatalf("outside %q %v", got, err)
	}
	info, err := os.Lstat(outside)
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Uid != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("outside uid %d mode %o", st.Uid, info.Mode().Perm())
	}
	in, err := os.ReadFile(inside)
	if err != nil || string(in) != written {
		t.Fatalf("inside %q %v", in, err)
	}
	inInfo, err := os.Lstat(inside)
	if err != nil {
		t.Fatal(err)
	}
	if inInfo.Sys().(*syscall.Stat_t).Ino == st.Ino {
		t.Fatal("write used the existing inode")
	}
}
