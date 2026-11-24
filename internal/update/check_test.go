package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckOffersANewerRelease(t *testing.T) {
	running := "1111111111111111111111111111111111111111"
	release := "2222222222222222222222222222222222222222"
	dir, pub := packRelease(t, "0.20261124.2", release, 37)
	srv := serveRelease(t, dir, "ahead")
	defer srv.Close()
	when := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	c := checkerFor(srv, pub, when)
	got, err := c.Check(context.Background(), State{
		Version: "0.20261124.1",
		Commit:  running,
		Schema:  37,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Version != "0.20261124.2" || got.Commit != release || got.ImageMoves || got.ImageBlocked {
		t.Fatalf("cache %+v", got)
	}
	if got.Notes != "https://github.com/scott/castbot/releases/tag/v0.20261124.2" {
		t.Fatalf("notes %s", got.Notes)
	}
	if got.CheckedAt != when.Format(time.RFC3339) {
		t.Fatalf("checked %s", got.CheckedAt)
	}
	if srv.compares != 1 || srv.downloads < 3 {
		t.Fatalf("downloads %d compares %d", srv.downloads, srv.compares)
	}
}

func TestCheckHidesAndKeepsThePreviousFile(t *testing.T) {
	release := "2222222222222222222222222222222222222222"
	running := "1111111111111111111111111111111111111111"
	dir, pub := packRelease(t, "0.20261124.2", release, 37)
	path := filepath.Join(t.TempDir(), "update.json")
	previous := Cache{Available: true, Version: "0.20261124.2", Commit: release, Notes: "https://github.com/scott/castbot/releases/tag/v0.20261124.2", CheckedAt: "2026-11-24T00:00:00Z"}
	if err := WriteCache(path, previous); err != nil {
		t.Fatal(err)
	}

	behind := serveRelease(t, dir, "behind")
	defer behind.Close()
	c := checkerFor(behind, pub, time.Now())
	got, err := c.Check(context.Background(), State{Version: "0.20261124.1", Commit: running, Schema: 37})
	if err != nil {
		t.Fatal(err)
	}
	if got.Available || got.Version != "" {
		t.Fatalf("behind %+v", got)
	}

	same := serveRelease(t, dir, "ahead")
	defer same.Close()
	c = checkerFor(same, pub, time.Now())
	got, err = c.Check(context.Background(), State{Version: "0.20261124.2", Commit: release, Schema: 37})
	if err != nil {
		t.Fatal(err)
	}
	if got.Available || same.compares != 0 {
		t.Fatalf("not newer available %v compares %d", got.Available, same.compares)
	}

	low := serveRelease(t, dir, "ahead")
	defer low.Close()
	c = checkerFor(low, pub, time.Now())
	got, err = c.Check(context.Background(), State{Version: "0.0.0+" + running, Commit: running, Schema: 38})
	if err != nil {
		t.Fatal(err)
	}
	if got.Available {
		t.Fatal("lower schema was offered")
	}

	flipped := filepath.Join(t.TempDir(), "flip")
	copyDir(t, dir, flipped)
	raw, err := os.ReadFile(filepath.Join(flipped, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0xff
	if err := os.WriteFile(filepath.Join(flipped, "release.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	bad := serveRelease(t, flipped, "ahead")
	defer bad.Close()
	c = checkerFor(bad, pub, time.Now())
	if _, err := c.Check(context.Background(), State{Version: "0.20261124.1", Commit: running, Schema: 37}); err == nil {
		t.Fatal("flipped manifest was accepted")
	}

	sigDir := filepath.Join(t.TempDir(), "sig")
	copyDir(t, dir, sigDir)
	sig, err := os.ReadFile(filepath.Join(sigDir, "SHA256SUMS.minisig"))
	if err != nil {
		t.Fatal(err)
	}
	sig[len(sig)-2] ^= 0xff
	if err := os.WriteFile(filepath.Join(sigDir, "SHA256SUMS.minisig"), sig, 0o644); err != nil {
		t.Fatal(err)
	}
	rejected := serveRelease(t, sigDir, "ahead")
	defer rejected.Close()
	c = checkerFor(rejected, pub, time.Now())
	if _, err := c.Check(context.Background(), State{Version: "0.20261124.1", Commit: running, Schema: 37}); err == nil {
		t.Fatal("flipped signature was accepted")
	}
	kept, err := ReadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if !kept.Available || kept.Version != previous.Version {
		t.Fatalf("cache replaced %+v", kept)
	}
}

func TestCheckImageMovesAndBlocks(t *testing.T) {
	release := "2222222222222222222222222222222222222222"
	imageCommit := "3333333333333333333333333333333333333333"
	dir, pub := packReleaseImage(t, "0.20261124.2", release, imageCommit, 37)
	running := "1111111111111111111111111111111111111111"
	officialOld := RuntimeImagePrefix + release
	srv := serveRelease(t, dir, "ahead")
	defer srv.Close()
	c := checkerFor(srv, pub, time.Now())
	c.Visible = func(context.Context, string) (bool, error) { return true, nil }
	got, err := c.Check(context.Background(), State{
		Version: "0.20261124.1",
		Commit:  running,
		Schema:  37,
		Image:   officialOld,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Available || !got.ImageMoves || got.ImageBlocked {
		t.Fatalf("move %+v", got)
	}
	c.Visible = func(context.Context, string) (bool, error) { return false, nil }
	got, err = c.Check(context.Background(), State{
		Version: "0.20261124.1",
		Commit:  running,
		Schema:  37,
		Image:   officialOld,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Available || !got.ImageBlocked || got.Version != "0.20261124.2" {
		t.Fatalf("blocked %+v", got)
	}
	got, err = c.Check(context.Background(), State{
		Version: "0.20261124.1",
		Commit:  running,
		Schema:  37,
		Image:   "registry.example/castbot:abcd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Available || !got.ImageBlocked {
		t.Fatalf("short %+v", got)
	}
}

func TestOnceSkipsDevAndOff(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	}))
	defer srv.Close()
	cfg := RunConfig{
		Version:   "dev",
		EnvMode:   ModePrompt,
		CachePath: filepath.Join(t.TempDir(), "update.json"),
		Checker:   Checker{Client: srv.Client(), ReleaseBase: srv.URL},
	}
	status, err := cfg.once(context.Background())
	if err != nil || status != "skipped" || hit {
		t.Fatalf("dev %s %v hit %v", status, err, hit)
	}
	cfg.Version = "0.20261124.1"
	cfg.EnvMode = ModeOff
	status, err = cfg.once(context.Background())
	if err != nil || status != "skipped" || hit {
		t.Fatalf("off %s %v hit %v", status, err, hit)
	}
}

func TestOnceKeepsCacheWhenTheDownloadFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	previous := Cache{Available: true, Version: "0.20261124.1", Commit: "2222222222222222222222222222222222222222", Notes: "https://github.com/scott/castbot/releases/tag/v0.20261124.1", CheckedAt: "2026-11-24T00:00:00Z"}
	if err := WriteCache(path, previous); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "secret-body", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := RunConfig{
		Version:   "0.20261124.1",
		Commit:    "1111111111111111111111111111111111111111",
		Schema:    37,
		EnvMode:   ModePrompt,
		CachePath: path,
		Checker:   Checker{Client: srv.Client(), ReleaseBase: srv.URL + "/download"},
	}
	if _, err := cfg.once(context.Background()); err == nil || strings.Contains(err.Error(), "secret-body") {
		t.Fatalf("err %v", err)
	}
	kept, err := ReadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Version != previous.Version || !kept.Available {
		t.Fatalf("cache %+v", kept)
	}
}

func TestRunStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, RunConfig{Startup: time.Hour, Version: "dev"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestCacheMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	empty, err := ReadCache(path)
	if err != nil || empty.Available {
		t.Fatalf("missing %+v %v", empty, err)
	}
	want := Cache{Available: true, Version: "0.20261124.2", Commit: "2222222222222222222222222222222222222222", Notes: "https://github.com/scott/castbot/releases/tag/v0.20261124.2", CheckedAt: "2026-11-24T12:00:00Z"}
	if err := WriteCache(path, want); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", fi.Mode().Perm())
	}
	got, err := ReadCache(path)
	if err != nil || got.Version != want.Version || !got.Available {
		t.Fatalf("read %+v %v", got, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCache(path); err == nil {
		t.Fatal("loose cache was trusted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = ReadCache(path)
	if err != nil || got.Version != "" {
		t.Fatalf("empty %+v %v", got, err)
	}
	bad := Cache{Available: true, Version: "0.20261124.2", Commit: "2222222222222222222222222222222222222222", Notes: "https://example.invalid/notes", CheckedAt: "2026-11-24T12:30:00Z"}
	if err := WriteCache(path, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCache(path); err == nil {
		t.Fatal("unexpected notes url was trusted")
	}
	wrote := Cache{Available: false, CheckedAt: "2026-11-24T13:00:00Z"}
	if err := writeInPlace(path, mustCache(t, wrote)); err != nil {
		t.Fatal(err)
	}
	got, err = ReadCache(path)
	if err != nil || got.CheckedAt != wrote.CheckedAt {
		t.Fatalf("inplace %+v %v", got, err)
	}
}

func packRelease(t *testing.T, version, commit string, schema int) (string, []byte) {
	t.Helper()
	return packReleaseImage(t, version, commit, commit, schema)
}

func packReleaseImage(t *testing.T, version, commit, imageCommit string, schema int) (string, []byte) {
	t.Helper()
	pub, priv := testKey(t)
	root := t.TempDir()
	stub := filepath.Join(root, "stub")
	if err := os.WriteFile(stub, []byte("binary-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(root, "out")
	err := Pack(out, Input{
		Version:            version,
		Commit:             commit,
		Schema:             schema,
		GuestID:            strings.Repeat("ab", 32),
		RuntimeImage:       RuntimeImagePrefix + imageCommit,
		RuntimeImageCommit: imageCommit,
		Notes:              "https://github.com/scott/castbot/releases/tag/v" + version,
		AMD64:              stub,
		ARM64:              stub,
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	return out, pub
}

type releaseServer struct {
	*httptest.Server
	downloads int
	compares  int
	status    string
}

func serveRelease(t *testing.T, dir, status string) *releaseServer {
	t.Helper()
	rs := &releaseServer{status: status}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.UserAgent() != UserAgent {
			t.Errorf("request %s %s query %q ua %q", r.Method, r.URL.Path, r.URL.RawQuery, r.UserAgent())
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("request carried an identity")
		}
		if strings.HasPrefix(r.URL.Path, "/compare/") {
			rs.compares++
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"` + rs.status + `"}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/download/") {
			rs.downloads++
			name := strings.TrimPrefix(r.URL.Path, "/download/")
			http.ServeFile(w, r, filepath.Join(dir, name))
			return
		}
		http.NotFound(w, r)
	}))
	return rs
}

func checkerFor(srv *releaseServer, pub []byte, now time.Time) *Checker {
	return &Checker{
		Client:      srv.Client(),
		ReleaseBase: srv.URL + "/download",
		CompareBase: srv.URL + "/compare/",
		Key:         pub,
		Now:         func() time.Time { return now },
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustCache(t *testing.T, c Cache) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "m.json")
	if err := WriteCache(path, c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
