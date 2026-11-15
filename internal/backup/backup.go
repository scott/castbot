// Package backup copies the castbot database and the upload blobs beside it.
// It does not copy the env file, the secrets key, or a machine's own disk.
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/store"
	_ "time/tzdata"
)

const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var idRE = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{4}$`)

// Result is one finished backup. SHA256 is the database file, not a secret.
type Result struct {
	ID          string
	Dir         string
	SHA256      string
	Schema      int
	Files       int
	FilesSHA256 string
}

// Summary is one backup directory the list command can print.
type Summary struct {
	ID     string
	Schema int
	Files  int
	OK     bool
}

type blob struct {
	name string
	size int64
}

// Create writes <dataDir>/backups/<id>/. The id is the Denver minute of now.
// A second backup in the same minute fails and leaves the first one in place.
func Create(ctx context.Context, st *store.Store, dataDir string, now time.Time) (Result, error) {
	dataDir = strings.TrimSpace(dataDir)
	if st == nil || dataDir == "" {
		return Result{}, store.ErrInvalid
	}
	id := backupID(now)
	if !idRE.MatchString(id) {
		return Result{}, store.ErrInvalid
	}
	root := filepath.Join(dataDir, "backups")
	dest := filepath.Join(root, id)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Result{}, err
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		return Result{}, fmt.Errorf("backup directory already exists")
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dest)
		}
	}()
	dbPath := filepath.Join(dest, "castbot.db")
	if err := st.SnapshotDB(ctx, dbPath); err != nil {
		return Result{}, err
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		return Result{}, err
	}
	sum, err := hashFile(dbPath)
	if err != nil {
		return Result{}, err
	}
	schema, err := store.InspectDB(ctx, dbPath)
	if err != nil {
		return Result{}, err
	}
	files, err := copyBlobs(filepath.Join(dataDir, "files"), filepath.Join(dest, "files"))
	if err != nil {
		return Result{}, err
	}
	fileSum := fileManifestSHA(files)
	rec := recordText(sum, schema, len(files), fileSum)
	if err := writeRecord(filepath.Join(dest, "RECORD"), rec); err != nil {
		return Result{}, err
	}
	ok = true
	return Result{ID: id, Dir: dest, SHA256: sum, Schema: schema, Files: len(files), FilesSHA256: fileSum}, nil
}

// List returns backup ids, newest first. A directory that is not a backup id is skipped.
func List(dataDir string) ([]Summary, error) {
	root := filepath.Join(strings.TrimSpace(dataDir), "backups")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, ent := range entries {
		if ent.IsDir() && idRE.MatchString(ent.Name()) {
			ids = append(ids, ent.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	out := make([]Summary, 0, len(ids))
	for _, id := range ids {
		sum := Summary{ID: id}
		rec, err := readRecord(filepath.Join(root, id, "RECORD"))
		if err == nil {
			sum.Schema = rec.schema
			sum.Files = rec.files
			sum.OK = true
		}
		out = append(out, sum)
	}
	return out, nil
}

// Restore replaces the live database and missing or changed blobs with one backup.
// The caller has to stop the server first. The previous database is moved aside
// under backups/ and is not deleted. Extra blobs that are not in the backup stay.
func Restore(dataDir, id string) error {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" || !idRE.MatchString(id) {
		return store.ErrInvalid
	}
	dest := filepath.Join(dataDir, "backups", id)
	rec, err := readRecord(filepath.Join(dest, "RECORD"))
	if err != nil {
		return err
	}
	dbPath := filepath.Join(dest, "castbot.db")
	sum, err := hashFile(dbPath)
	if err != nil {
		return err
	}
	if sum != rec.sha {
		return errors.New("backup hash does not match")
	}
	schema, err := store.InspectDB(context.Background(), dbPath)
	if err != nil {
		return err
	}
	if schema != rec.schema {
		return errors.New("backup schema does not match")
	}
	files, err := listBlobs(filepath.Join(dest, "files"))
	if err != nil {
		return err
	}
	if fileManifestSHA(files) != rec.filesSHA {
		return errors.New("backup files do not match")
	}
	if rec.files != len(files) {
		return errors.New("backup files do not match")
	}
	live := filepath.Join(dataDir, "castbot.db")
	if _, err := os.Lstat(live); err != nil {
		return err
	}
	aside := filepath.Join(dataDir, "backups", "pre-restore-"+id+"-"+strconv.FormatInt(time.Now().Unix(), 10))
	if err := os.Mkdir(aside, 0o700); err != nil {
		return err
	}
	moved := []string{}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := live + suffix
		if _, err := os.Lstat(src); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := os.Rename(src, filepath.Join(aside, "castbot.db"+suffix)); err != nil {
			putBack(aside, dataDir, moved)
			return err
		}
		moved = append(moved, suffix)
	}
	if err := copyRegular(dbPath, live, 0o600); err != nil {
		putBack(aside, dataDir, moved)
		return err
	}
	if err := restoreBlobs(filepath.Join(dest, "files"), filepath.Join(dataDir, "files"), files); err != nil {
		return err
	}
	return nil
}

func putBack(aside, dataDir string, suffixes []string) {
	live := filepath.Join(dataDir, "castbot.db")
	for i := len(suffixes) - 1; i >= 0; i-- {
		suffix := suffixes[i]
		_ = os.Rename(filepath.Join(aside, "castbot.db"+suffix), live+suffix)
	}
}

func backupID(now time.Time) string {
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		return ""
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.In(loc).Format("2006-01-02_1504")
}

type savedRecord struct {
	sha      string
	schema   int
	files    int
	filesSHA string
}

func recordText(sum string, schema, files int, filesSHA string) string {
	return fmt.Sprintf("sha256 %s\nschema_version %d\nintegrity ok\nfiles %d\nfiles_sha256 %s\n", sum, schema, files, filesSHA)
}

func writeRecord(path, text string) error {
	tmp := path + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, text); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readRecord(path string) (savedRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return savedRecord{}, err
	}
	var out savedRecord
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, " ")
		if !ok || seen[key] {
			return savedRecord{}, errors.New("backup record is unreadable")
		}
		seen[key] = true
		switch key {
		case "sha256":
			if !hashRE.MatchString(val) {
				return savedRecord{}, errors.New("backup record is unreadable")
			}
			out.sha = val
		case "schema_version":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return savedRecord{}, errors.New("backup record is unreadable")
			}
			out.schema = n
		case "integrity":
			if val != "ok" {
				return savedRecord{}, errors.New("backup record is unreadable")
			}
		case "files":
			n, err := strconv.Atoi(val)
			if err != nil || n < 0 {
				return savedRecord{}, errors.New("backup record is unreadable")
			}
			out.files = n
		case "files_sha256":
			if !hashRE.MatchString(val) {
				return savedRecord{}, errors.New("backup record is unreadable")
			}
			out.filesSHA = val
		default:
			return savedRecord{}, errors.New("backup record is unreadable")
		}
	}
	if !seen["sha256"] || !seen["schema_version"] || !seen["integrity"] || !seen["files"] || !seen["files_sha256"] {
		return savedRecord{}, errors.New("backup record is unreadable")
	}
	return out, nil
}

var hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func fileManifestSHA(files []blob) string {
	var b strings.Builder
	for _, f := range files {
		fmt.Fprintf(&b, "%s %d\n", f.name, f.size)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func copyBlobs(src, dest string) ([]blob, error) {
	files, err := listBlobs(src)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := copyRegular(filepath.Join(src, f.name), filepath.Join(dest, f.name), 0o600); err != nil {
			return nil, err
		}
	}
	return files, nil
}

func listBlobs(dir string) ([]blob, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []blob
	for _, ent := range entries {
		name := ent.Name()
		if !hashRE.MatchString(name) {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sum, err := hashFile(filepath.Join(dir, name))
		if err != nil || sum != name {
			continue
		}
		out = append(out, blob{name: name, size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func restoreBlobs(src, dest string, files []blob) error {
	if len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	for _, f := range files {
		target := filepath.Join(dest, f.name)
		if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() {
			sum, err := hashFile(target)
			if err == nil && sum == f.name && info.Size() == f.size {
				continue
			}
		}
		tmp := filepath.Join(dest, ".restore-"+f.name)
		if err := copyRegular(filepath.Join(src, f.name), tmp, 0o600); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}

func copyRegular(src, dst string, mode os.FileMode) error {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func hashFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
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
