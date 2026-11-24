package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/backup"
	"github.com/scott/castbot/internal/store"
)

const (
	// DefaultOwner and DefaultGroup own the installed binary and the env file.
	DefaultOwner = "castbot"
	DefaultGroup = "castbot"
	// WrapperPath is the only command the service account may run as root.
	WrapperPath = "/usr/local/sbin/castbot-update"
	healthURL   = "http://127.0.0.1:8080/login"
	healthWait  = 30 * time.Second
)

// ApplyConfig is one attempt to install the latest signed release.
// A nil function uses the production check. Tests replace Restart, Exe, and Health.
type ApplyConfig struct {
	Root      string
	EnvFile   string
	Version   string
	Commit    string
	Schema    int
	Arch      string
	Owner     string
	Group     string
	HealthURL string
	HealthFor time.Duration
	Unit      string
	Checker   Checker
	Now       func() time.Time
	Inflight  func(context.Context) (int, error)
	Backup    func(context.Context) (string, error)
	Free      func(string) (uint64, error)
	Restart   func(context.Context) error
	Exe       func(context.Context) (string, error)
	SchemaOf  func(context.Context) (int, error)
	Health    func(context.Context) error
}

// Apply downloads the latest signed release and switches the host to it.
// It refuses a dev build, a release that is not newer, a bad compare, a lower
// schema, a busy agent, and a machine image the policy will not move.
// A failed health check puts the previous binary back and leaves the backup.
func Apply(ctx context.Context, cfg ApplyConfig) error {
	cfg = cfg.defaults()
	if cfg.Version == "" || cfg.Version == "dev" {
		return failApply("", "refused")
	}
	envBody, image, err := readEnvImage(cfg.EnvFile)
	if err != nil {
		return failApply("", "refused")
	}
	prevSHA, prevVersion, err := readCurrent(cfg.Root)
	if err != nil {
		return failApply("", "refused")
	}
	fetched, err := cfg.Checker.Fetch(ctx)
	if err != nil {
		return failApply("", "refused")
	}
	m := fetched.Manifest
	if err := m.Validate(); err != nil || !Newer(cfg.Version, m.Version) || m.Schema < cfg.Schema {
		return failApply("", "refused")
	}
	status, err := cfg.Checker.ancestor(ctx, cfg.Commit, m.Commit)
	if err != nil || (status != "ahead" && status != "identical") {
		return failApply("", "refused")
	}
	plan, err := DecideImage(ctx, image, m.Commit, m.RuntimeImage, m.RuntimeImageCommit, cfg.Checker.imageVisible)
	if err != nil || plan.Block {
		return failApply("", "refused")
	}
	asset, err := assetForArch(m, cfg.Arch)
	if err != nil {
		return failApply("", "refused")
	}
	dbPath := dbPathFrom(envBody, cfg.Root)
	dataDir := filepath.Dir(dbPath)
	need, err := spaceNeed(dbPath, dataDir, asset.Size)
	if err != nil {
		return failApply("", "refused")
	}
	freeRoot, err := cfg.Free(cfg.Root)
	if err != nil || freeRoot < need {
		return failApply("", "refused")
	}
	freeData, err := cfg.Free(dataDir)
	if err != nil || freeData < need {
		return failApply("", "refused")
	}
	st, err := store.Open(dbPath, store.Options{Now: cfg.Now})
	if err != nil {
		return failApply("", "refused")
	}
	closeStore := true
	defer func() {
		if closeStore {
			st.Close()
		}
	}()
	n, err := inflightOf(ctx, cfg, st)
	if err != nil || n > 0 {
		return failApply("", "refused")
	}
	backupID, err := backupOf(ctx, cfg, st, dataDir)
	if err != nil {
		return failApply("", "backup failed")
	}
	uid, gid, err := lookupOwner(cfg.Owner, cfg.Group)
	if err != nil {
		return failApply(backupID, "refused")
	}
	dest := filepath.Join(cfg.Root, "releases", m.Commit, "castbot")
	tmp, err := os.CreateTemp("", "castbot-asset-*")
	if err != nil {
		return failApply(backupID, "refused")
	}
	tmpName := tmp.Name()
	tmp.Close()
	os.Remove(tmpName)
	if err := cfg.Checker.Download(ctx, asset.Name, tmpName, asset.Size); err != nil {
		os.Remove(tmpName)
		return failApply(backupID, "refused")
	}
	defer os.Remove(tmpName)
	if err := matchAsset(tmpName, fetched.Sums, asset); err != nil {
		return failApply(backupID, "refused")
	}
	if err := installBinary(tmpName, dest, uid, gid); err != nil {
		return failApply(backupID, "refused")
	}
	if err := writeNext(filepath.Join(cfg.Root, "releases", m.Commit, "VERSION"), m.Version+"\n", 0o644); err != nil {
		return failApply(backupID, "refused")
	}
	n, err = inflightOf(ctx, cfg, st)
	if err != nil || n > 0 {
		return failApply(backupID, "refused")
	}
	st.Close()
	closeStore = false

	var moved bool
	var previousLine string
	if plan.Move {
		previousLine = image
		if err := writeImage(cfg.EnvFile, envBody, plan.Next, uid, gid); err != nil {
			return failApply(backupID, "refused")
		}
		moved = true
	}
	if err := switchTo(cfg.Root, m.Commit, m.Version); err != nil {
		return restoreApply(ctx, cfg, backupID, prevSHA, prevVersion, moved, previousLine, uid, gid)
	}
	if err := bringUp(ctx, cfg, dest); err != nil {
		return restoreApply(ctx, cfg, backupID, prevSHA, prevVersion, moved, previousLine, uid, gid)
	}
	if err := pruneReleases(cfg.Root, m.Commit, prevSHA); err != nil {
		log.Printf("update apply: backup %s: installed", backupID)
		log.Printf("update apply: prune failed")
		return nil
	}
	log.Printf("update apply: backup %s: installed", backupID)
	return nil
}

func (cfg ApplyConfig) defaults() ApplyConfig {
	if cfg.Root == "" {
		cfg.Root = "/opt/castbot"
	}
	if cfg.EnvFile == "" {
		cfg.EnvFile = "/etc/castbot/castbot.env"
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.Owner == "" {
		cfg.Owner = DefaultOwner
	}
	if cfg.Group == "" {
		cfg.Group = DefaultGroup
	}
	if cfg.HealthURL == "" {
		cfg.HealthURL = healthURL
	}
	if cfg.HealthFor <= 0 {
		cfg.HealthFor = healthWait
	}
	if cfg.Unit == "" {
		cfg.Unit = "castbot"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Free == nil {
		cfg.Free = statFree
	}
	if cfg.Restart == nil {
		cfg.Restart = restartUnit
	}
	if cfg.Exe == nil {
		cfg.Exe = func(ctx context.Context) (string, error) { return serviceExe(ctx, cfg.Unit) }
	}
	if cfg.SchemaOf == nil {
		cfg.SchemaOf = func(ctx context.Context) (int, error) {
			return store.InspectDB(ctx, dbPathFrom(mustRead(cfg.EnvFile), cfg.Root))
		}
	}
	if cfg.Health == nil {
		cfg.Health = func(ctx context.Context) error { return pollHealth(ctx, cfg.HealthURL, cfg.HealthFor) }
	}
	return cfg
}

func failApply(id, result string) error {
	if id == "" {
		log.Printf("update apply: %s", result)
	} else {
		log.Printf("update apply: backup %s: %s", id, result)
	}
	return errors.New(result)
}

func restoreApply(ctx context.Context, cfg ApplyConfig, id, sha, version string, moved bool, previous string, uid, gid int) error {
	restored := true
	if err := switchTo(cfg.Root, sha, version); err != nil {
		restored = false
	}
	if moved {
		body, _, err := readEnvImage(cfg.EnvFile)
		if err != nil || writeImage(cfg.EnvFile, body, previous, uid, gid) != nil {
			restored = false
		}
	}
	if err := cfg.Restart(ctx); err != nil {
		restored = false
	} else if err := cfg.Health(ctx); err != nil {
		restored = false
	}
	if restored {
		return failApply(id, "rolled back")
	}
	return failApply(id, "restore failed")
}

func bringUp(ctx context.Context, cfg ApplyConfig, dest string) error {
	if err := cfg.Restart(ctx); err != nil {
		return err
	}
	if err := cfg.Health(ctx); err != nil {
		return err
	}
	got, err := cfg.Exe(ctx)
	if err != nil {
		return err
	}
	want, err := filepath.EvalSymlinks(dest)
	if err != nil {
		want = dest
	}
	got = strings.TrimSuffix(strings.TrimSpace(got), " (deleted)")
	if resolved, err := filepath.EvalSymlinks(got); err == nil {
		got = resolved
	}
	if got != want && got != dest {
		return errors.New("running binary was unexpected")
	}
	schema, err := cfg.SchemaOf(ctx)
	if err != nil || schema < cfg.Schema {
		return errors.New("schema check failed")
	}
	return nil
}

func inflightOf(ctx context.Context, cfg ApplyConfig, st *store.Store) (int, error) {
	if cfg.Inflight != nil {
		return cfg.Inflight(ctx)
	}
	return st.InflightRuns(ctx)
}

func backupOf(ctx context.Context, cfg ApplyConfig, st *store.Store, dataDir string) (string, error) {
	if cfg.Backup != nil {
		return cfg.Backup(ctx)
	}
	res, err := backup.Create(ctx, st, dataDir, cfg.Now())
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

func assetForArch(m Manifest, arch string) (Asset, error) {
	var key, name string
	switch arch {
	case "amd64":
		key, name = "linux-amd64", "castbot_linux_amd64"
	case "arm64":
		key, name = "linux-arm64", "castbot_linux_arm64"
	default:
		return Asset{}, errors.New("update refused")
	}
	asset := m.Assets[key]
	if !assetOK(asset, name) {
		return Asset{}, errors.New("update refused")
	}
	return asset, nil
}

func readCurrent(root string) (string, string, error) {
	shaRaw, err := os.ReadFile(filepath.Join(root, "RELEASE"))
	if err != nil {
		return "", "", err
	}
	sha := strings.TrimSpace(string(shaRaw))
	if !shaRE.MatchString(sha) {
		return "", "", errors.New("release is unexpected")
	}
	bin := filepath.Join(root, "releases", sha, "castbot")
	info, err := os.Stat(bin)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", "", errors.New("release is unexpected")
	}
	verRaw, err := os.ReadFile(filepath.Join(root, "VERSION"))
	ver := ""
	if err == nil {
		ver = strings.TrimSpace(string(verRaw))
	}
	if ver == "" {
		ver = "0.0.0+" + sha
	}
	return sha, ver, nil
}

func readEnvImage(path string) ([]byte, string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, "", errors.New("env file is unexpected")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	image, _, err := imageLine(body)
	if err != nil {
		return nil, "", err
	}
	return body, image, nil
}

func imageLine(body []byte) (string, int, error) {
	lines := strings.Split(string(body), "\n")
	found := -1
	value := ""
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "CASTBOT_IMAGE=") {
			if found >= 0 {
				return "", 0, errors.New("env file is unexpected")
			}
			found = i
			value = strings.TrimPrefix(line, "CASTBOT_IMAGE=")
		}
	}
	return value, found, nil
}

func dbPathFrom(body []byte, root string) string {
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "CASTBOT_DB=") {
			path := strings.TrimPrefix(line, "CASTBOT_DB=")
			if path != "" && !strings.ContainsAny(path, "\r\n") {
				return path
			}
		}
	}
	return filepath.Join(root, "data", "castbot.db")
}

func mustRead(path string) []byte {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return body
}

func spaceNeed(dbPath, dataDir string, asset int64) (uint64, error) {
	var need uint64
	if asset > 0 {
		need += uint64(asset)
	}
	if info, err := os.Stat(dbPath); err == nil && info.Mode().IsRegular() {
		need += uint64(info.Size())
	}
	files, err := treeSize(filepath.Join(dataDir, "files"))
	if err != nil {
		return 0, err
	}
	return need + files + (1 << 20), nil
}

func treeSize(root string) (uint64, error) {
	var n uint64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n += uint64(info.Size())
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return n, err
}

func statFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

func lookupOwner(owner, group string) (int, int, error) {
	u, err := user.Lookup(owner)
	if err != nil {
		return 0, 0, err
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func installBinary(src, dest string, uid, gid int) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	part := dest + ".part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(part)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(part)
		return err
	}
	if err := chownFile(part, uid, gid); err != nil {
		os.Remove(part)
		return err
	}
	if err := os.Chmod(part, 0o755); err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, dest)
}

func chownFile(path string, uid, gid int) error {
	err := os.Chown(path, uid, gid)
	if err == nil {
		return nil
	}
	if os.Geteuid() == 0 {
		return err
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		return err
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != uid || int(sys.Gid) != gid {
		return err
	}
	return nil
}

func matchAsset(path string, sums []byte, asset Asset) error {
	info, err := os.Stat(path)
	if err != nil || info.Size() != asset.Size || info.Size() <= 0 || info.Size() > maxAsset {
		return errors.New("sum does not match")
	}
	sum, err := fileSHA(path)
	if err != nil || sum != asset.SHA256 {
		return errors.New("sum does not match")
	}
	want, err := sumNamed(sums, asset.Name)
	if err != nil || want != sum {
		return errors.New("sum does not match")
	}
	return nil
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sumNamed(sums []byte, name string) (string, error) {
	seen := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(sums), "\n"), "\n") {
		sum, file, ok := strings.Cut(line, "  ")
		if !ok || !sumRE.MatchString(sum) || !fileNameOK(file) {
			return "", errors.New("sum line is unexpected")
		}
		if _, dup := seen[file]; dup {
			return "", errors.New("sum line is unexpected")
		}
		seen[file] = sum
	}
	sum := seen[name]
	if !sumRE.MatchString(sum) {
		return "", errors.New("sum line is unexpected")
	}
	return sum, nil
}

func writeImage(path string, body []byte, next string, uid, gid int) error {
	if next == "" || strings.ContainsAny(next, "\r\n") {
		return errors.New("env file is unexpected")
	}
	text := string(body)
	nl := "\n"
	if strings.HasSuffix(text, "\r\n") {
		nl = "\r\n"
	}
	trim := strings.TrimSuffix(text, "\n")
	trim = strings.TrimSuffix(trim, "\r")
	lines := strings.Split(trim, "\n")
	found := 0
	for i, line := range lines {
		if strings.HasPrefix(line, "CASTBOT_IMAGE=") && !strings.HasPrefix(line, "#") {
			lines[i] = "CASTBOT_IMAGE=" + next
			found++
		}
	}
	if found != 1 {
		return errors.New("env file is unexpected")
	}
	out := strings.Join(lines, "\n")
	if strings.HasSuffix(text, "\n") {
		out += nl
	}
	return writeOwned(path, []byte(out), uid, gid)
}

func writeOwned(path string, body []byte, uid, gid int) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".env-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if _, err := tmp.Write(body); err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := chownFile(name, uid, gid); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func writeNext(path, text string, mode os.FileMode) error {
	next := path + ".next"
	if err := os.WriteFile(next, []byte(text), mode); err != nil {
		return err
	}
	return os.Rename(next, path)
}

func switchTo(root, sha, version string) error {
	if !shaRE.MatchString(sha) || version == "" || strings.ContainsAny(version, "\r\n") {
		return errors.New("release is unexpected")
	}
	bin := filepath.Join(root, "releases", sha, "castbot")
	if _, err := os.Stat(bin); err != nil {
		return err
	}
	link := filepath.Join(root, "bin", "castbot")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	next := link + ".next"
	os.Remove(next)
	if err := os.Symlink(bin, next); err != nil {
		return err
	}
	if err := os.Rename(next, link); err != nil {
		return err
	}
	if err := writeNext(filepath.Join(root, "RELEASE"), sha+"\n", 0o644); err != nil {
		return err
	}
	return writeNext(filepath.Join(root, "VERSION"), version+"\n", 0o644)
}

func pruneReleases(root, live, rollback string) error {
	dir := filepath.Join(root, "releases")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type item struct {
		name string
		mod  time.Time
	}
	var items []item
	for _, entry := range entries {
		if !entry.IsDir() || !shaRE.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		items = append(items, item{name: entry.Name(), mod: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].mod.Equal(items[j].mod) {
			return items[i].name > items[j].name
		}
		return items[i].mod.After(items[j].mod)
	})
	keep := map[string]bool{}
	if shaRE.MatchString(live) {
		keep[live] = true
	}
	if shaRE.MatchString(rollback) {
		keep[rollback] = true
	}
	n := 0
	for _, it := range items {
		if n >= 5 {
			break
		}
		keep[it.name] = true
		n++
	}
	for _, it := range items {
		if keep[it.name] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, it.name)); err != nil {
			return err
		}
	}
	return nil
}

func restartUnit(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "systemctl", "restart", "castbot")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	if err := cmd.Run(); err != nil {
		return errors.New("restart failed")
	}
	return nil
}

func serviceExe(ctx context.Context, unit string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", "show", "-p", "MainPID", "--value", unit)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C"}
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("running binary was unexpected")
	}
	pid := strings.TrimSpace(string(out))
	if pid == "" || pid == "0" || strings.ContainsAny(pid, "/.") {
		return "", errors.New("running binary was unexpected")
	}
	exe, err := os.Readlink("/proc/" + pid + "/exe")
	if err != nil {
		return "", errors.New("running binary was unexpected")
	}
	return exe, nil
}

func pollHealth(ctx context.Context, raw string, budget time.Duration) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/login" || u.RawQuery != "" || u.User != nil {
		return errors.New("health check failed")
	}
	if budget <= 0 {
		budget = healthWait
	}
	deadline := time.Now().Add(budget)
	client := &http.Client{
		Timeout: 2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("health redirect")
		},
	}
	for {
		if err := oneHealth(ctx, client, raw); err == nil {
			return nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return errors.New("health check failed")
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("health check failed")
		case <-timer.C:
		}
	}
}

func oneHealth(ctx context.Context, client *http.Client, raw string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return errors.New("health check failed")
	}
	return nil
}

// Download writes one release asset. limit is the manifest size, and also the cap.
func (c *Checker) Download(ctx context.Context, name, dest string, limit int64) error {
	if !fileNameOK(name) || limit <= 0 || limit > maxAsset {
		return errors.New("release download failed")
	}
	base := strings.TrimRight(c.ReleaseBase, "/")
	if base == "" {
		base = defaultReleaseBase
	}
	raw := base + "/" + name
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("release url is unexpected")
	}
	if c.Client == nil {
		if u.Scheme != "https" || !allowedReleaseHost(u.Hostname()) {
			return errors.New("release host is unexpected")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return errors.New("release url is unexpected")
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := c.client().Do(req)
	if err != nil {
		return errors.New("release download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("release download failed")
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("release download failed")
	}
	n, copyErr := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || n != limit {
		os.Remove(dest)
		return errors.New("release download failed")
	}
	return nil
}
