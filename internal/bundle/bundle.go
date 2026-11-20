// Package bundle is the agent directory castlet writes at boot.
// The bytes are a tar of agent.yaml, instructions.md, and optional mcp shim
// files. The sha256 is of those bytes, so a reload can tell a new version
// from the one on disk. Shim files name a command. They do not hold a token.
package bundle

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/scott/castbot/internal/agentskill"
	"github.com/scott/castbot/internal/chattool"
	"github.com/scott/castbot/internal/grouptool"
	"github.com/scott/castbot/internal/machinetool"
	"github.com/scott/castbot/internal/routinetool"
	"github.com/scott/castbot/internal/secretask"
	"github.com/scott/castbot/internal/selftool"
)

const (
	yamlName = "agent.yaml"
	instName = "instructions.md"
)

// Archive builds a deterministic tar and its sha256.
func Archive(yaml, instructions string) ([]byte, string, error) {
	return ArchiveFiles(yaml, instructions, nil)
}

// ArchiveFiles adds mcp shim files and the served skill files.
// A shim name must be mcp/<name>.yaml. Skill paths are agentskill.SkillFile,
// routinetool.SkillFile, secretask.SkillFile, selftool.SkillFile,
// grouptool.SkillFile, machinetool.SkillFile, and chattool.SkillFile.
// A nil or empty map matches
// Archive of the two main files.
func ArchiveFiles(yaml, instructions string, extras map[string]string) ([]byte, string, error) {
	if yaml == "" || instructions == "" {
		return nil, "", errors.New("bundle is missing agent.yaml or instructions.md")
	}
	var names []string
	for name, body := range extras {
		if !safeExtra(name) || strings.Contains(body, "\x00") {
			return nil, "", errors.New("bundle extra")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := write(tw, yamlName, []byte(yaml)); err != nil {
		return nil, "", err
	}
	if err := write(tw, instName, []byte(instructions)); err != nil {
		return nil, "", err
	}
	for _, name := range names {
		if err := write(tw, name, []byte(extras[name])); err != nil {
			return nil, "", err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:]), nil
}

func safeExtra(name string) bool {
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	if name == agentskill.SkillFile || name == routinetool.SkillFile || name == secretask.SkillFile || name == selftool.SkillFile || name == grouptool.SkillFile || name == machinetool.SkillFile || name == chattool.SkillFile {
		return true
	}
	rest, ok := strings.CutPrefix(name, "mcp/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return false
	}
	return strings.HasSuffix(rest, ".yaml")
}

func write(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(body)),
		ModTime:  time.Unix(0, 0).UTC(),
		Typeflag: tar.TypeReg,
		Format:   tar.FormatUSTAR,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

// Read pulls agent.yaml and instructions.md out of a bundle tar.
func Read(raw []byte) (string, string, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	var yaml, instructions string
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", "", err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != '\x00' {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return "", "", err
		}
		switch hdr.Name {
		case yamlName:
			yaml = string(body)
		case instName:
			instructions = string(body)
		}
	}
	if yaml == "" || instructions == "" {
		return "", "", errors.New("bundle is missing agent.yaml or instructions.md")
	}
	return yaml, instructions, nil
}

// Install writes the bundle into dir and creates workspace and memory.
// It leaves files already in those two directories alone. A symlink at a
// bundle path is replaced rather than followed.
func Install(dir string, raw []byte) error {
	files, err := Files(raw)
	if err != nil {
		return err
	}
	if files[yamlName] == nil || files[instName] == nil {
		return errors.New("bundle is missing agent.yaml or instructions.md")
	}
	info, err := os.Lstat(dir)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("bundle path")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, body := range files {
		if err := writeUnder(dir, name, body, 0o640); err != nil {
			return err
		}
	}
	for _, sub := range []string{"workspace", "memory"} {
		path := filepath.Join(dir, sub)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			if err := os.Mkdir(path, 0o770); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("bundle path")
		}
		if err := fchmodOwned(path, 0o2770); err != nil {
			return err
		}
	}
	return nil
}

// Files returns the regular files in a bundle, keyed by their tar names.
func Files(raw []byte) (map[string][]byte, error) {
	tr := tar.NewReader(bytes.NewReader(raw))
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != '\x00' {
			continue
		}
		name := hdr.Name
		if name != yamlName && name != instName && !safeExtra(name) {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return nil, err
		}
		out[name] = append([]byte(nil), body...)
	}
}

func writeUnder(root, name string, body []byte, mode os.FileMode) error {
	name = filepath.Clean(name)
	if name == "." || name == "" || filepath.IsAbs(name) || strings.HasPrefix(name, "..") {
		return errors.New("bundle path")
	}
	cur := root
	parts := strings.Split(name, string(os.PathSeparator))
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return errors.New("bundle path")
		}
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		mode := bundleDirMode(root, next)
		if os.IsNotExist(err) {
			if err := os.Mkdir(next, mode); err != nil {
				return err
			}
			if err := fchmodOwned(next, uint32(mode)); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("bundle path")
		} else if err := fchmodOwned(next, uint32(mode)); err != nil {
			return err
		}
		cur = next
	}
	part := parts[len(parts)-1]
	if part == "" || part == "." || part == ".." {
		return errors.New("bundle path")
	}
	return writeNoFollow(filepath.Join(cur, part), body, mode)
}

func bundleDirMode(root, path string) os.FileMode {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 0o700
	}
	if rel == "skills" || strings.HasPrefix(rel, "skills"+string(os.PathSeparator)) {
		return 0o750
	}
	return 0o700
}

var writeSeq atomic.Uint64

// writeNoFollow writes body to a new inode and renames it onto path.
// An existing inode is not modified. The final name is not followed.
func writeNoFollow(path string, body []byte, mode os.FileMode) error {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return errors.New("path")
	}
	parent, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer parent.Close()
	tmp := fmt.Sprintf(".castbot-%d-%d", os.Getpid(), writeSeq.Add(1))
	fd, err := unix.Openat(int(parent.Fd()), tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	if _, werr := f.Write(body); werr != nil {
		f.Close()
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return werr
	}
	if err := syscall.Fchmod(int(f.Fd()), uint32(mode.Perm())); err != nil {
		f.Close()
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return err
	}
	shareCastwork(int(f.Fd()))
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return err
	}
	if err := unix.Renameat(int(parent.Fd()), tmp, int(parent.Fd()), name); err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return err
	}
	return nil
}

// fchmodOwned changes a directory this process owns. A directory owned by
// someone else, or a path that cannot be opened without following a link,
// is left as it is. That does not fail the install.
func fchmodOwned(path string, mode uint32) error {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return nil
		}
		return err
	}
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if int(st.Uid) != os.Getuid() {
		return nil
	}
	shareCastwork(int(f.Fd()))
	if uint32(st.Mode)&0o7777 == mode&0o7777 {
		return nil
	}
	if err := syscall.Fchmod(int(f.Fd()), mode); err != nil && !errors.Is(err, syscall.EPERM) {
		return err
	}
	return nil
}

// shareCastwork sets the group to castwork when this process is root.
// The runtime image's tool account is in that group. A non-root process
// keeps the group it already has.
func shareCastwork(fd int) {
	if os.Geteuid() != 0 || fd < 0 {
		return
	}
	grp, err := user.LookupGroup("castwork")
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil || gid < 0 {
		return
	}
	_ = syscall.Fchown(fd, -1, gid)
}

// Sum is the sha256 of the bundle bytes.
func Sum(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
