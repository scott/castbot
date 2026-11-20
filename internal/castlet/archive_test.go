package castlet

import (
	"archive/tar"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/scott/castbot/internal/runtime"
)

func TestArchiveKeepsModeSymlinkAndLocal(t *testing.T) {
	oldDir := t.TempDir()
	agent := filepath.Join(oldDir, "agent")
	if err := os.MkdirAll(filepath.Join(agent, "workspace", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "workspace", "bin", "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "workspace", "inside.txt"), []byte("in"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(agent, "workspace", "link")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(agent, "workspace", "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(agent, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "memory", "note.txt"), []byte("remember"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(oldDir, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, ".local", "bin", "pkg"), []byte("pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	sup := testSup(t, agent)
	rec := authed(t, sup, "GET", "/castlet/archive", nil)
	if rec.Code != 200 {
		t.Fatalf("export %d", rec.Code)
	}
	body := rec.Body.Bytes()
	sawLink := false
	sawOut := false
	tr := tar.NewReader(bytes.NewReader(body))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name == "workspace/link" && hdr.Typeflag == tar.TypeSymlink && hdr.Linkname == "inside.txt" {
			sawLink = true
		}
		if hdr.Name == "workspace/out" && hdr.Typeflag == tar.TypeSymlink && hdr.Linkname == outside {
			sawOut = true
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(tr)
			if buf.Len() != 0 {
				t.Fatal("followed a symlink")
			}
		}
		if bytes.Contains([]byte(hdr.Name), []byte("secret")) {
			t.Fatal("archive named the secret file")
		}
	}
	if !sawLink || !sawOut {
		t.Fatalf("link %v out %v", sawLink, sawOut)
	}
	newRoot := t.TempDir()
	newAgent := filepath.Join(newRoot, "agent")
	dst := testSup(t, newAgent)
	put := authed(t, dst, "PUT", "/castlet/archive", bytes.NewReader(body))
	if put.Code != 200 {
		t.Fatalf("import %d %s", put.Code, put.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(newAgent, "workspace", "bin", "tool"))
	if err != nil || string(got) != "#!/bin/sh\n" {
		t.Fatalf("tool %q %v", got, err)
	}
	info, err := os.Stat(filepath.Join(newAgent, "workspace", "bin", "tool"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v %v", info, err)
	}
	target, err := os.Readlink(filepath.Join(newAgent, "workspace", "link"))
	if err != nil || target != "inside.txt" {
		t.Fatalf("link %q %v", target, err)
	}
	target, err = os.Readlink(filepath.Join(newAgent, "workspace", "out"))
	if err != nil || target != outside {
		t.Fatalf("out link %q %v", target, err)
	}
	if _, err := os.Stat(filepath.Join(newRoot, "secret")); !os.IsNotExist(err) {
		t.Fatal("import wrote through the symlink")
	}
	pkg, err := os.ReadFile(filepath.Join(newRoot, ".local", "bin", "pkg"))
	if err != nil || string(pkg) != "pkg" {
		t.Fatalf("local %q %v", pkg, err)
	}
	note, err := os.ReadFile(filepath.Join(newAgent, "memory", "note.txt"))
	if err != nil || string(note) != "remember" {
		t.Fatalf("memory %q %v", note, err)
	}
}

func TestArchiveRejectsEscapeAndSymlinkWrite(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "agent")
	sup := testSup(t, agent)
	outside := filepath.Join(root, "pwned")
	raw := tarBytes(t, &tar.Header{Name: "workspace/../../pwned", Mode: 0o644, Size: 4, Typeflag: tar.TypeReg, Format: tar.FormatPAX}, []byte("nope"))
	rec := authed(t, sup, "PUT", "/castlet/archive", bytes.NewReader(raw))
	if rec.Code == 200 {
		t.Fatal("escape was accepted")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("escape wrote a file")
	}
	link := tarBytes(t,
		&tar.Header{Name: "workspace/jump", Mode: 0o777, Typeflag: tar.TypeSymlink, Linkname: outside, Format: tar.FormatPAX}, nil,
		&tar.Header{Name: "workspace/jump/evil", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg, Format: tar.FormatPAX}, []byte("bad"),
	)
	rec = authed(t, sup, "PUT", "/castlet/archive", bytes.NewReader(link))
	if rec.Code == 200 {
		t.Fatal("write through symlink was accepted")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatal("symlink write escaped")
	}
}

func TestArchiveLimitRejectsALargeBody(t *testing.T) {
	sup := testSup(t, filepath.Join(t.TempDir(), "agent"))
	raw := tarBytes(t, &tar.Header{Name: "workspace/big.txt", Mode: 0o644, Size: 80, Typeflag: tar.TypeReg, Format: tar.FormatPAX}, bytes.Repeat([]byte("a"), 80))
	req := httptest.NewRequest(http.MethodPut, "/castlet/archive", bytes.NewReader(raw))
	req.Header.Set("X-Cast-Authorization", "Bearer agent-token")
	req.Header.Set(archiveLimitHeader, "40")
	rec := httptest.NewRecorder()
	sup.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Fatalf("status %d", rec.Code)
	}
}

func testSup(t *testing.T, agent string) *Supervisor {
	t.Helper()
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "Atlas"})
	t.Cleanup(echo.Close)
	up := httptest.NewServer(echo)
	t.Cleanup(up.Close)
	sup, err := New(Config{Token: "agent-token", RuntimeURL: up.URL, AgentDir: agent})
	if err != nil {
		t.Fatal(err)
	}
	sup.mu.Lock()
	sup.ready = true
	sup.mu.Unlock()
	return sup
}

func tarBytes(t *testing.T, parts ...any) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < len(parts); i += 2 {
		hdr := parts[i].(*tar.Header)
		var body []byte
		if parts[i+1] != nil {
			body = parts[i+1].([]byte)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
