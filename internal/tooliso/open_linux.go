//go:build linux

package tooliso

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxToolFile = 4 << 20

// ReadUnder reads one regular file that stays inside root.
// The open uses RESOLVE_BENEATH, RESOLVE_NO_SYMLINKS, and
// RESOLVE_NO_MAGICLINKS, then checks the opened descriptor is still inside
// the root. A symlink swapped into the path is not followed.
func ReadUnder(root, rel string) ([]byte, error) {
	rootFile, rel, err := openRoot(root, rel)
	if err != nil {
		return nil, ErrPath
	}
	defer rootFile.Close()
	file, err := openAt(rootFile, rel, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrPath
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxToolFile {
		return nil, ErrPath
	}
	buf, err := io.ReadAll(io.LimitReader(file, maxToolFile+1))
	if err != nil || len(buf) > maxToolFile {
		return nil, ErrPath
	}
	return buf, nil
}

// ListUnder returns relative names inside root that match pattern.
// Directory walks do not follow symlinks.
func ListUnder(root, pattern string) ([]string, error) {
	pattern = strings.TrimPrefix(strings.TrimSpace(pattern), "./")
	if pattern == "" || strings.Contains(pattern, "\x00") || filepath.IsAbs(pattern) {
		return nil, ErrPath
	}
	rootFile, err := os.Open(root)
	if err != nil {
		return nil, ErrPath
	}
	defer rootFile.Close()
	info, err := rootFile.Stat()
	if err != nil || !info.IsDir() {
		return nil, ErrPath
	}
	var out []string
	if err := walkUnder(rootFile, ".", pattern, &out); err != nil {
		return nil, ErrPath
	}
	return out, nil
}

// CreateUnder opens one file inside root without following a symlink.
// flags are the open flags, such as O_WRONLY|O_CREAT. O_TRUNC is not passed
// to the kernel. Callers shorten the file with ftruncate after the mode
// check. mode is the creation mode and is zero unless O_CREAT is set.
// Callers still fchmod a descriptor they own, because the process umask is
// applied at creation.
func CreateUnder(root, rel string, flags int, mode uint32) (*os.File, error) {
	rootFile, rel, err := openRoot(root, rel)
	if err != nil {
		return nil, ErrPath
	}
	defer rootFile.Close()
	flags |= unix.O_CLOEXEC | unix.O_NOFOLLOW
	flags &^= unix.O_TRUNC
	if flags&(unix.O_CREAT|unix.O_TMPFILE) == 0 {
		mode = 0
	}
	return openAt(rootFile, rel, flags, mode)
}

// OpenDirUnder opens one directory inside root without following a symlink.
// An empty relative path opens the root itself.
func OpenDirUnder(root, rel string) (*os.File, error) {
	if rel == "" || rel == "." {
		f, err := os.Open(root)
		if err != nil {
			return nil, ErrPath
		}
		info, err := f.Stat()
		if err != nil || !info.IsDir() {
			f.Close()
			return nil, ErrPath
		}
		return f, nil
	}
	rootFile, rel, err := openRoot(root, rel)
	if err != nil {
		return nil, ErrPath
	}
	defer rootFile.Close()
	return openAt(rootFile, rel, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

// MkdirAllUnder creates missing directories inside root. A symlink anywhere
// in the relative path is not followed. A directory this process owns is
// given mode when its mode differs. A directory owned by someone else is
// left alone.
func MkdirAllUnder(root, rel string, mode uint32) error {
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return nil
	}
	rel = cleanRel(rel)
	if rel == "" {
		return ErrPath
	}
	rootFile, err := os.Open(root)
	if err != nil {
		return ErrPath
	}
	defer rootFile.Close()
	info, err := rootFile.Stat()
	if err != nil || !info.IsDir() {
		return ErrPath
	}
	parent := rootFile
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			if parent != rootFile {
				parent.Close()
			}
			return ErrPath
		}
		next, err := openAt(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			if mkErr := unix.Mkdirat(int(parent.Fd()), part, mode); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
				if parent != rootFile {
					parent.Close()
				}
				return ErrPath
			}
			next, err = openAt(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				if parent != rootFile {
					parent.Close()
				}
				return ErrPath
			}
		}
		chmodOwned(next, mode)
		if parent != rootFile {
			parent.Close()
		}
		parent = next
	}
	if parent != rootFile {
		parent.Close()
	}
	return nil
}

// RenameUnder renames one file inside a directory of root. Both paths must
// sit in that same directory. The final names are not followed.
func RenameUnder(root, oldRel, newRel string) error {
	oldDir, oldName := splitFinal(oldRel)
	newDir, newName := splitFinal(newRel)
	if oldDir != newDir || oldName == "" || newName == "" || oldName == "." || newName == "." || oldName == ".." || newName == ".." {
		return ErrPath
	}
	dir, err := OpenDirUnder(root, oldDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := unix.Renameat(int(dir.Fd()), oldName, int(dir.Fd()), newName); err != nil {
		return ErrPath
	}
	return nil
}

// RemoveUnder removes one file inside root. A symlink is removed, not followed.
func RemoveUnder(root, rel string) error {
	dirRel, name := splitFinal(rel)
	if name == "" || name == "." || name == ".." {
		return ErrPath
	}
	dir, err := OpenDirUnder(root, dirRel)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return ErrPath
	}
	return nil
}

func chmodOwned(f *os.File, mode uint32) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return
	}
	if int(st.Uid) != os.Getuid() {
		return
	}
	ShareCastwork(int(f.Fd()))
	if uint32(st.Mode)&0o7777 == mode&0o7777 {
		return
	}
	_ = unix.Fchmod(int(f.Fd()), mode)
}

func splitFinal(rel string) (string, string) {
	rel = filepath.Clean(rel)
	if rel == "." || rel == "" {
		return ".", ""
	}
	return filepath.Dir(rel), filepath.Base(rel)
}

func openRoot(root, rel string) (*os.File, string, error) {
	rel = cleanRel(rel)
	if rel == "" {
		return nil, "", ErrPath
	}
	rootFile, err := os.Open(root)
	if err != nil {
		return nil, "", err
	}
	info, err := rootFile.Stat()
	if err != nil || !info.IsDir() {
		rootFile.Close()
		return nil, "", ErrPath
	}
	return rootFile, rel, nil
}

func openAt(root *os.File, rel string, flags int, mode uint32) (*os.File, error) {
	fd, err := unix.Openat2(int(root.Fd()), rel, &unix.OpenHow{
		Flags:   uint64(flags),
		Mode:    uint64(mode),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), rel)
	if err := confirmInside(root, file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func confirmInside(root, file *os.File) error {
	rootPath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", root.Fd()))
	if err != nil {
		return err
	}
	filePath, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return err
	}
	rootPath = strings.TrimSuffix(rootPath, " (deleted)")
	filePath = strings.TrimSuffix(filePath, " (deleted)")
	if blockedFS(filePath) || !inside(rootPath, filePath) {
		return ErrPath
	}
	var rootStat, fileStat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &rootStat); err != nil {
		return err
	}
	if err := unix.Fstat(int(file.Fd()), &fileStat); err != nil {
		return err
	}
	if fileStat.Dev != rootStat.Dev {
		return ErrPath
	}
	return nil
}

func cleanRel(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.ContainsRune(rel, 0) || strings.ContainsAny(rel, "\n\r") || filepath.IsAbs(rel) {
		return ""
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

func walkUnder(dir *os.File, rel, pattern string, out *[]string) error {
	entries, err := dir.ReadDir(0)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "." || name == ".." || strings.ContainsRune(name, 0) {
			continue
		}
		child := name
		if rel != "." {
			child = rel + "/" + name
		}
		file, err := openAt(dir, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			continue
		}
		if info.IsDir() {
			if matchGlob(pattern, child) {
				*out = append(*out, child)
			}
			err = walkUnder(file, child, pattern, out)
			file.Close()
			if err != nil {
				return err
			}
			continue
		}
		file.Close()
		if info.Mode().IsRegular() && matchGlob(pattern, child) {
			*out = append(*out, child)
		}
	}
	return nil
}

var freshSeq atomic.Uint64

func freshName() string {
	return fmt.Sprintf(".castbot-%d-%d", os.Getpid(), freshSeq.Add(1))
}

// mkdirNoFollow creates path and any missing parents.
// A symlink component is an error. An existing directory is left in place.
func mkdirNoFollow(path string, mode os.FileMode) error {
	path = filepath.Clean(path)
	if path == "" || path == "." || path == string(filepath.Separator) {
		return errors.New("path")
	}
	parent := filepath.Dir(path)
	name := filepath.Base(path)
	if name == "" || name == "." || name == ".." || parent == path {
		return errors.New("path")
	}
	if parent != string(filepath.Separator) {
		if err := mkdirNoFollow(parent, mode); err != nil {
			return err
		}
	}
	dir, err := os.OpenFile(parent, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer dir.Close()
	var st unix.Stat_t
	err = unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if st.Mode&unix.S_IFMT != unix.S_IFDIR {
			return errors.New("path")
		}
		return nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Mkdirat(int(dir.Fd()), name, uint32(mode.Perm())); err != nil {
		return err
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fchmod(fd, uint32(mode.Perm()))
}

// WriteFresh writes body to a new inode in dir and renames it to name.
// An existing inode is not modified. name is not followed.
func WriteFresh(dir, name string, body []byte, mode uint32) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return ErrPath
	}
	parent, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer parent.Close()
	tmp := freshName()
	fd, err := unix.Openat(int(parent.Fd()), tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, mode)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	if _, err := f.Write(body); err != nil {
		f.Close()
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return err
	}
	if err := unix.Fchmod(int(f.Fd()), mode); err != nil {
		f.Close()
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return err
	}
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

func listDirNoFollow(path string) ([]os.DirEntry, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func removeNameNoFollow(dir, name string) error {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return ErrPath
	}
	parent, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer parent.Close()
	return unix.Unlinkat(int(parent.Fd()), name, 0)
}

func matchGlob(pattern, name string) bool {
	if ok, err := filepath.Match(pattern, name); err == nil && ok {
		return true
	}
	if !strings.Contains(pattern, "**") {
		return false
	}
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*\*`, `.*`)
	re = strings.ReplaceAll(re, `\*`, `[^/]*`)
	re = strings.ReplaceAll(re, `\?`, `[^/]`)
	matched, err := regexp.MatchString("^"+re+"$", name)
	return err == nil && matched
}
