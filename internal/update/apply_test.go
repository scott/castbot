package update

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"aead.dev/minisign"

	"github.com/scott/castbot/internal/store"
)

const applyToken = "fake-token-9f3a"

func TestMain(m *testing.M) {
	if os.Getenv("CASTBOT_APPLY_FIXTURE") == "1" {
		if err := writeApplyFixture(); err != nil {
			fmt.Fprintf(os.Stderr, "fixture failed: %s\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestApplyDefaults(t *testing.T) {
	if DefaultOwner != "castbot" || DefaultGroup != "castbot" {
		t.Fatalf("owner %s group %s", DefaultOwner, DefaultGroup)
	}
	if WrapperPath != "/usr/local/sbin/castbot-update" {
		t.Fatalf("wrapper %s", WrapperPath)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "write_update_helper()")
	if start < 0 {
		t.Fatal("helper missing")
	}
	rest := text[start:]
	end := strings.Index(rest, "\nmachine_host_wanted()")
	if end < 0 {
		t.Fatal("helper end missing")
	}
	body := rest[:end]
	if !strings.Contains(body, "exec ${APP_DIR}/bin/castbot update apply") {
		t.Fatal("helper exec")
	}
	if strings.Contains(body, "$@") {
		t.Fatal("helper takes arguments")
	}
	if !strings.Contains(body, "Defaults:castbot env_reset") || !strings.Contains(body, `secure_path="/usr/sbin:/usr/bin:/sbin:/bin"`) {
		t.Fatal("sudoers defaults")
	}
}

func TestApplyDevDoesNotFetch(t *testing.T) {
	hits := 0
	cfg := ApplyConfig{
		Version: "dev",
		Commit:  strings.Repeat("ab", 20),
		Schema:  store.SchemaVersion,
		Checker: Checker{Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("fetched")
		})}},
	}
	buf := captureLog(t)
	if err := Apply(context.Background(), cfg); err == nil || err.Error() != "refused" {
		t.Fatalf("dev %v", err)
	}
	cfg.Version = ""
	if err := Apply(context.Background(), cfg); err == nil || err.Error() != "refused" {
		t.Fatalf("blank %v", err)
	}
	if hits != 0 {
		t.Fatalf("fetches %d", hits)
	}
	if strings.Contains(buf.String(), applyToken) {
		t.Fatal("log contained the env token")
	}
}

func TestApplyRefusesBeforeWrite(t *testing.T) {
	t.Run("not newer", func(t *testing.T) {
		l := newLayout(t, "")
		cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
		cfg.Version = "0.20261124.9"
		assertRefused(t, l, cfg, false)
	})
	t.Run("behind", func(t *testing.T) {
		l := newLayout(t, "")
		cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "behind"))
		assertRefused(t, l, cfg, false)
	})
	t.Run("low schema", func(t *testing.T) {
		l := newLayout(t, "")
		cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), 1, "ahead"))
		assertRefused(t, l, cfg, false)
	})
	t.Run("bad arch", func(t *testing.T) {
		l := newLayout(t, "")
		cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
		cfg.Arch = "386"
		assertRefused(t, l, cfg, false)
	})
	t.Run("image blocked", func(t *testing.T) {
		l := newLayout(t, "registry.example/castbot:abcd")
		cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
		assertRefused(t, l, cfg, false)
	})
	t.Run("image lookup error", func(t *testing.T) {
		l := newLayout(t, RuntimeImagePrefix+prevSHA())
		chk := mustPack(t, "0.20261124.2", nextSHA(), imageSHA(), store.SchemaVersion, "ahead")
		chk.Visible = func(context.Context, string) (bool, error) {
			return false, errors.New("runtime image lookup failed")
		}
		assertRefused(t, l, l.base(t, chk), false)
	})
	t.Run("image not visible", func(t *testing.T) {
		l := newLayout(t, RuntimeImagePrefix+prevSHA())
		chk := mustPack(t, "0.20261124.2", nextSHA(), imageSHA(), store.SchemaVersion, "ahead")
		chk.Visible = func(context.Context, string) (bool, error) { return false, nil }
		assertRefused(t, l, l.base(t, chk), false)
	})
	t.Run("two image lines", func(t *testing.T) {
		l := newLayout(t, RuntimeImagePrefix+prevSHA())
		body, err := os.ReadFile(l.env)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(l.env, append(body, []byte("CASTBOT_IMAGE=again\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		hits := 0
		cfg := l.base(t, Checker{Client: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, errors.New("fetched")
		})}})
		assertRefused(t, l, cfg, false)
		if hits != 0 {
			t.Fatalf("fetch after a bad env %d", hits)
		}
	})
	t.Run("no space", func(t *testing.T) {
		l := newLayout(t, "")
		cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
		cfg.Free = func(string) (uint64, error) { return 1, nil }
		assertRefused(t, l, cfg, false)
	})
	t.Run("inflight", func(t *testing.T) {
		l := newLayout(t, "")
		cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
		cfg.Inflight = func(context.Context) (int, error) { return 1, nil }
		assertRefused(t, l, cfg, false)
	})
}

func TestApplyHashMismatch(t *testing.T) {
	l := newLayout(t, "")
	dir, pub := packRelease(t, "0.20261124.2", nextSHA(), store.SchemaVersion)
	flipped := t.TempDir()
	copyDir(t, dir, flipped)
	path := filepath.Join(flipped, "castbot_linux_amd64")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)-1] ^= 0xff
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	srv := serveRelease(t, flipped, "ahead")
	t.Cleanup(srv.Close)
	cfg := l.base(t, *checkerFor(srv, pub, fixedNow()))
	before := tempAssets()
	buf := captureLog(t)
	err = Apply(context.Background(), cfg)
	if err == nil || err.Error() != "refused" {
		t.Fatalf("hash %v", err)
	}
	if strings.Contains(buf.String(), applyToken) {
		t.Fatal("log contained the env token")
	}
	if len(tempAssets()) > len(before) {
		t.Fatal("asset temp left behind")
	}
	l.assertUnswitched(t)
	if _, err := os.Stat(filepath.Join(l.root, "releases", nextSHA())); !os.IsNotExist(err) {
		t.Fatal("bad asset was installed")
	}
	if !l.backupExists(t) {
		t.Fatal("backup missing after a bad asset")
	}
}

func TestApplyInflightAfterDownload(t *testing.T) {
	l := newLayout(t, "")
	cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
	calls := 0
	restarts := 0
	cfg.Inflight = func(context.Context) (int, error) {
		calls++
		if calls >= 2 {
			return 1, nil
		}
		return 0, nil
	}
	cfg.Restart = func(context.Context) error {
		restarts++
		return nil
	}
	buf := captureLog(t)
	err := Apply(context.Background(), cfg)
	if err == nil || err.Error() != "refused" {
		t.Fatalf("late inflight %v", err)
	}
	if strings.Contains(buf.String(), applyToken) {
		t.Fatal("log contained the env token")
	}
	if calls != 2 || restarts != 0 {
		t.Fatalf("calls %d restarts %d", calls, restarts)
	}
	l.assertUnswitched(t)
	ver, err := os.ReadFile(filepath.Join(l.root, "releases", nextSHA(), "VERSION"))
	if err != nil || strings.TrimSpace(string(ver)) != "0.20261124.2" {
		t.Fatalf("release version %q %v", ver, err)
	}
	if !l.backupExists(t) {
		t.Fatal("backup missing")
	}
}

func TestApplyInstalls(t *testing.T) {
	l := newLayout(t, "")
	envBefore, err := os.ReadFile(l.env)
	if err != nil {
		t.Fatal(err)
	}
	cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
	restarts := 0
	cfg.Restart = func(context.Context) error {
		restarts++
		ver, err := os.ReadFile(filepath.Join(l.root, "releases", nextSHA(), "VERSION"))
		if err != nil || strings.TrimSpace(string(ver)) != "0.20261124.2" {
			t.Fatalf("version before restart %q %v", ver, err)
		}
		return nil
	}
	cfg.Exe = func(context.Context) (string, error) {
		return filepath.Join(l.root, "releases", nextSHA(), "castbot") + " (deleted)", nil
	}
	buf := captureLog(t)
	if err := Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	logged := buf.String()
	if strings.Contains(logged, applyToken) {
		t.Fatal("log contained the env token")
	}
	if !regexp.MustCompile(`update apply: backup [0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{4}: installed`).MatchString(logged) {
		t.Fatalf("log %s", logged)
	}
	if restarts != 1 {
		t.Fatalf("restarts %d", restarts)
	}
	l.assertSwitched(t, nextSHA(), "0.20261124.2")
	envAfter, err := os.ReadFile(l.env)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(envBefore, envAfter) {
		t.Fatal("env changed")
	}
	l.assertEnvOwner(t)
	if !l.backupExists(t) {
		t.Fatal("backup missing")
	}
}

func TestApplyMovesImage(t *testing.T) {
	l := newLayout(t, RuntimeImagePrefix+prevSHA())
	chk := mustPack(t, "0.20261124.2", nextSHA(), imageSHA(), store.SchemaVersion, "ahead")
	chk.Visible = func(context.Context, string) (bool, error) { return true, nil }
	cfg := l.base(t, chk)
	buf := captureLog(t)
	if err := Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), applyToken) {
		t.Fatal("log contained the env token")
	}
	l.assertSwitched(t, nextSHA(), "0.20261124.2")
	l.assertImage(t, RuntimeImagePrefix+imageSHA())
	l.assertEnvOwner(t)
}

func TestApplyRollsBack(t *testing.T) {
	l := newLayout(t, RuntimeImagePrefix+prevSHA())
	chk := mustPack(t, "0.20261124.2", nextSHA(), imageSHA(), store.SchemaVersion, "ahead")
	chk.Visible = func(context.Context, string) (bool, error) { return true, nil }
	cfg := l.base(t, chk)
	healths := 0
	restarts := 0
	cfg.Health = func(context.Context) error {
		healths++
		if healths == 1 {
			return errors.New("down")
		}
		return nil
	}
	cfg.Restart = func(context.Context) error {
		restarts++
		return nil
	}
	buf := captureLog(t)
	err := Apply(context.Background(), cfg)
	if err == nil || err.Error() != "rolled back" {
		t.Fatalf("rollback %v", err)
	}
	if strings.Contains(buf.String(), applyToken) {
		t.Fatal("log contained the env token")
	}
	if !strings.Contains(buf.String(), "rolled back") {
		t.Fatalf("log %s", buf.String())
	}
	if restarts != 2 {
		t.Fatalf("restarts %d", restarts)
	}
	l.assertUnswitched(t)
	l.assertImage(t, RuntimeImagePrefix+prevSHA())
	l.assertEnvOwner(t)
	if !l.backupExists(t) {
		t.Fatal("backup removed")
	}
}

func TestApplyRestoreFailure(t *testing.T) {
	l := newLayout(t, "")
	cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
	cfg.Health = func(context.Context) error { return errors.New("down") }
	restarts := 0
	cfg.Restart = func(context.Context) error {
		restarts++
		return nil
	}
	buf := captureLog(t)
	err := Apply(context.Background(), cfg)
	if err == nil || err.Error() != "restore failed" {
		t.Fatalf("restore %v", err)
	}
	if strings.Contains(buf.String(), applyToken) {
		t.Fatal("log contained the env token")
	}
	if restarts != 2 {
		t.Fatalf("restarts %d", restarts)
	}
	l.assertUnswitched(t)
	if !l.backupExists(t) {
		t.Fatal("backup removed")
	}
}

func TestApplyWrongBinaryRollsBack(t *testing.T) {
	l := newLayout(t, "")
	cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
	cfg.Exe = func(context.Context) (string, error) { return "/bin/true", nil }
	if err := Apply(context.Background(), cfg); err == nil || err.Error() != "rolled back" {
		t.Fatalf("exe %v", err)
	}
	l.assertUnswitched(t)
}

func TestApplyIdenticalCommit(t *testing.T) {
	l := newLayout(t, "")
	chk := mustPack(t, "0.20261124.2", prevSHA(), prevSHA(), store.SchemaVersion, "behind")
	compares := 0
	inner := chk.Client.Transport
	chk.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/compare/") {
			compares++
		}
		return inner.RoundTrip(r)
	})}
	cfg := l.base(t, chk)
	cfg.Exe = func(context.Context) (string, error) {
		return filepath.Join(l.root, "releases", prevSHA(), "castbot"), nil
	}
	if err := Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	l.assertSwitched(t, prevSHA(), "0.20261124.2")
	if compares != 0 {
		t.Fatal("identical commit called compare")
	}
}

func TestApplyPrune(t *testing.T) {
	l := newLayout(t, "")
	now := time.Now()
	if err := os.Chtimes(filepath.Join(l.root, "releases", prevSHA()), now.Add(-240*time.Hour), now.Add(-240*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var drop []string
	for i := 1; i <= 6; i++ {
		sha := fmt.Sprintf("%040x", i)
		dir := filepath.Join(l.root, "releases", sha)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "castbot"), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		mod := now.Add(-time.Duration(i) * time.Hour)
		if err := os.Chtimes(dir, mod, mod); err != nil {
			t.Fatal(err)
		}
		if i >= 5 {
			drop = append(drop, sha)
		}
	}
	notes := filepath.Join(l.root, "releases", "keep-notes")
	if err := os.MkdirAll(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := l.base(t, mustPack(t, "0.20261124.2", nextSHA(), nextSHA(), store.SchemaVersion, "ahead"))
	if err := Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	l.assertSwitched(t, nextSHA(), "0.20261124.2")
	for _, sha := range []string{prevSHA(), nextSHA(), fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 4)} {
		if _, err := os.Stat(filepath.Join(l.root, "releases", sha)); err != nil {
			t.Fatalf("kept %s %v", sha, err)
		}
	}
	for _, sha := range drop {
		if _, err := os.Stat(filepath.Join(l.root, "releases", sha)); !os.IsNotExist(err) {
			t.Fatalf("old release %s stayed", sha)
		}
	}
	if _, err := os.Stat(notes); err != nil {
		t.Fatal("notes dir removed")
	}
	if !l.backupExists(t) {
		t.Fatal("backup pruned")
	}
}

func TestDownloadRejects(t *testing.T) {
	srv := serveRelease(t, t.TempDir(), "ahead")
	t.Cleanup(srv.Close)
	c := checkerFor(srv, nil, fixedNow())
	dest := filepath.Join(t.TempDir(), "out")
	if err := c.Download(context.Background(), "..", dest, 4); err == nil {
		t.Fatal("dotdot accepted")
	}
	if err := c.Download(context.Background(), "castbot_linux_amd64", dest, 0); err == nil {
		t.Fatal("empty limit accepted")
	}
	if err := c.Download(context.Background(), "castbot_linux_amd64", dest, maxAsset+1); err == nil {
		t.Fatal("huge limit accepted")
	}
}

type applyLayout struct {
	root  string
	env   string
	db    string
	uid   int
	gid   int
	token string
}

func newLayout(t *testing.T, image string) applyLayout {
	t.Helper()
	root := t.TempDir()
	db := filepath.Join(root, "data", "castbot.db")
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(db, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	rel := filepath.Join(root, "releases", prevSHA())
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "castbot"), []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "RELEASE"), []byte(prevSHA()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "VERSION"), []byte("0.20261124.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "VERSION"), []byte("0.20261124.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(rel, "castbot"), filepath.Join(root, "bin", "castbot")); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "CASTBOT_DB=%s\n", db)
	fmt.Fprintf(&text, "CASTBOT_DO_TOKEN=%s\n", applyToken)
	if image != "" {
		fmt.Fprintf(&text, "CASTBOT_IMAGE=%s\n", image)
	}
	envPath := filepath.Join(root, "castbot.env")
	if err := os.WriteFile(envPath, []byte(text.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	uid, gid := currentIDs(t)
	return applyLayout{root: root, env: envPath, db: db, uid: uid, gid: gid, token: applyToken}
}

func (l applyLayout) base(t *testing.T, chk Checker) ApplyConfig {
	t.Helper()
	owner, group := currentNames(t)
	return ApplyConfig{
		Root:    l.root,
		EnvFile: l.env,
		Version: "0.20261124.1",
		Commit:  prevSHA(),
		Schema:  store.SchemaVersion,
		Arch:    "amd64",
		Owner:   owner,
		Group:   group,
		Checker: chk,
		Now:     func() time.Time { return fixedNow() },
		Restart: func(context.Context) error { return nil },
		Exe: func(context.Context) (string, error) {
			return filepath.Join(l.root, "releases", nextSHA(), "castbot"), nil
		},
		Health:   func(context.Context) error { return nil },
		SchemaOf: func(context.Context) (int, error) { return store.SchemaVersion, nil },
	}
}

func (l applyLayout) assertUnswitched(t *testing.T) {
	t.Helper()
	l.assertSwitched(t, prevSHA(), "0.20261124.1")
}

func (l applyLayout) assertSwitched(t *testing.T, sha, version string) {
	t.Helper()
	got, err := os.Readlink(filepath.Join(l.root, "bin", "castbot"))
	if err != nil || got != filepath.Join(l.root, "releases", sha, "castbot") {
		t.Fatalf("symlink %s %v", got, err)
	}
	rel, err := os.ReadFile(filepath.Join(l.root, "RELEASE"))
	if err != nil || strings.TrimSpace(string(rel)) != sha {
		t.Fatalf("RELEASE %q %v", rel, err)
	}
	ver, err := os.ReadFile(filepath.Join(l.root, "VERSION"))
	if err != nil || strings.TrimSpace(string(ver)) != version {
		t.Fatalf("VERSION %q %v", ver, err)
	}
}

func (l applyLayout) assertImage(t *testing.T, image string) {
	t.Helper()
	body, err := os.ReadFile(l.env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "CASTBOT_IMAGE="+image+"\n") {
		t.Fatal("image line mismatch")
	}
	if !strings.Contains(string(body), "CASTBOT_DO_TOKEN="+l.token+"\n") {
		t.Fatal("token line missing")
	}
	if strings.Count(string(body), "CASTBOT_IMAGE=") != 1 {
		t.Fatal("image line count")
	}
}

func (l applyLayout) assertEnvOwner(t *testing.T) {
	t.Helper()
	info, err := os.Stat(l.env)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env mode %o", info.Mode().Perm())
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != l.uid || int(sys.Gid) != l.gid {
		t.Fatal("env owner changed")
	}
}

func (l applyLayout) backupExists(t *testing.T) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(l.db), "backups"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return true
		}
	}
	return false
}

func assertRefused(t *testing.T, l applyLayout, cfg ApplyConfig, backup bool) {
	t.Helper()
	buf := captureLog(t)
	err := Apply(context.Background(), cfg)
	if err == nil || err.Error() != "refused" {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(buf.String(), applyToken) {
		t.Fatal("log contained the env token")
	}
	l.assertUnswitched(t)
	if _, err := os.Stat(filepath.Join(l.root, "releases", nextSHA())); !os.IsNotExist(err) {
		t.Fatal("release dir was written")
	}
	if l.backupExists(t) != backup {
		t.Fatalf("backup present %v", l.backupExists(t))
	}
}

func mustPack(t *testing.T, version, commit, imageCommit string, schema int, status string) Checker {
	t.Helper()
	dir, pub := packReleaseImage(t, version, commit, imageCommit, schema)
	srv := serveRelease(t, dir, status)
	t.Cleanup(srv.Close)
	return *checkerFor(srv, pub, fixedNow())
}

func currentNames(t *testing.T) (string, string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username == "" || g.Name == "" {
		t.Fatal("missing account name")
	}
	return u.Username, g.Name
}

func currentIDs(t *testing.T) (int, int) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	var uid, gid int
	if _, err := fmt.Sscan(u.Uid, &uid); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscan(u.Gid, &gid); err != nil {
		t.Fatal(err)
	}
	return uid, gid
}

func prevSHA() string  { return strings.Repeat("ab", 20) }
func nextSHA() string  { return strings.Repeat("cd", 20) }
func imageSHA() string { return strings.Repeat("ef", 20) }

func fixedNow() time.Time {
	return time.Date(2026, 11, 24, 18, 0, 0, 0, time.UTC)
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func tempAssets() []string {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "castbot-asset-*"))
	return matches
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func writeApplyFixture() error {
	dir := os.Getenv("CASTBOT_APPLY_FIXTURE_DIR")
	pubPath := os.Getenv("CASTBOT_APPLY_FIXTURE_PUB")
	if !filepath.IsAbs(dir) || !filepath.IsAbs(pubPath) {
		return errors.New("fixture path")
	}
	version := os.Getenv("CASTBOT_APPLY_FIXTURE_VERSION")
	commit := os.Getenv("CASTBOT_APPLY_FIXTURE_COMMIT")
	imageCommit := os.Getenv("CASTBOT_APPLY_FIXTURE_IMAGE_COMMIT")
	if version == "" || !shaRE.MatchString(commit) || !shaRE.MatchString(imageCommit) {
		return errors.New("fixture meta")
	}
	pub, priv, err := minisign.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	public, err := pub.MarshalText()
	if err != nil {
		return err
	}
	private, err := priv.MarshalText()
	if err != nil {
		return err
	}
	stubDir, err := os.MkdirTemp("", "castbot-fixture-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stubDir)
	stub := filepath.Join(stubDir, "stub")
	if err := os.WriteFile(stub, []byte("binary-bytes"), 0o644); err != nil {
		return err
	}
	if err := Pack(dir, Input{
		Version:            version,
		Commit:             commit,
		Schema:             store.SchemaVersion,
		GuestID:            strings.Repeat("ab", 32),
		RuntimeImage:       RuntimeImagePrefix + imageCommit,
		RuntimeImageCommit: imageCommit,
		Notes:              "https://github.com/scott/castbot/releases/tag/v" + version,
		AMD64:              stub,
		ARM64:              stub,
	}, private); err != nil {
		return err
	}
	return os.WriteFile(pubPath, public, 0o600)
}
