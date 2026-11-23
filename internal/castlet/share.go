package castlet

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/scott/castbot/internal/tooliso"
	"golang.org/x/sys/unix"
)

// Files castlet writes under the agent directory are group-readable by
// castwork. Private state outside that directory stays mode 0700.
const (
	agentFileMode = 0o660
	agentDirMode  = 0o2770
)

func agentSharedMode(archived os.FileMode) os.FileMode {
	mode := os.FileMode(agentFileMode)
	if archived&0o111 != 0 {
		mode |= 0o110
	}
	return mode
}

func agentRel(agentDir, full string) (string, bool) {
	agentDir = filepath.Clean(agentDir)
	full = filepath.Clean(full)
	if agentDir == "." || agentDir == "" || agentDir == string(filepath.Separator) {
		return "", false
	}
	if full == agentDir {
		return ".", true
	}
	if !strings.HasPrefix(full, agentDir+string(os.PathSeparator)) {
		return "", false
	}
	return full[len(agentDir)+1:], true
}

// openShared opens a file under the agent directory for writing.
// O_TRUNC is applied with ftruncate only after the mode check succeeds.
// mode is passed to open only when the file may be created.
func openShared(agentDir, rel string, flags int, mode uint32) (*os.File, error) {
	trunc := flags&os.O_TRUNC != 0
	flags &^= os.O_TRUNC
	openMode := mode
	if flags&os.O_CREATE == 0 {
		openMode = 0
	}
	f, err := openOrReplace(agentDir, rel, flags, openMode)
	if err != nil {
		return nil, err
	}
	if err := finishShared(f, mode); err != nil {
		f.Close()
		return nil, err
	}
	if trunc {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// finishShared sets the mode on a descriptor this process owns.
// A file owned by someone else is left unchanged when it is already
// group-writable by the shared group. The path is not used, so a swapped
// link cannot receive the mode.
func finishShared(f *os.File, mode uint32) error {
	if f == nil {
		return errors.New("path")
	}
	if mode == 0 {
		mode = uint32(agentFileMode)
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Nlink > 1 {
		return syscall.EPERM
	}
	if int(st.Uid) != os.Getuid() {
		if groupWritableShared(st) {
			return nil
		}
		return syscall.EPERM
	}
	if err := syscall.Fchmod(int(f.Fd()), mode); err != nil {
		return err
	}
	gid, ok := castworkGID()
	if !ok {
		return nil
	}
	if err := syscall.Fchown(int(f.Fd()), -1, gid); err != nil && !errors.Is(err, syscall.EPERM) {
		return err
	}
	return nil
}

// openOrReplace returns a descriptor for a new inode when the current
// name has extra links or cannot be opened. A single link we do not own
// is left for finishShared.
func openOrReplace(agentDir, rel string, flags int, mode uint32) (*os.File, error) {
	f, err := tooliso.CreateUnder(agentDir, rel, flags, mode)
	if err != nil {
		if (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)) && linkCount(agentDir, rel) > 1 {
			if rm := tooliso.RemoveUnder(agentDir, rel); rm != nil {
				return nil, err
			}
			return tooliso.CreateUnder(agentDir, rel, flags|os.O_EXCL, mode)
		}
		return nil, err
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		f.Close()
		return nil, err
	}
	if st.Mode&syscall.S_IFMT == syscall.S_IFDIR {
		f.Close()
		return nil, syscall.EISDIR
	}
	if st.Nlink > 1 {
		f.Close()
		if err := tooliso.RemoveUnder(agentDir, rel); err != nil {
			return nil, err
		}
		return tooliso.CreateUnder(agentDir, rel, flags|os.O_EXCL, mode)
	}
	return f, nil
}

func linkCount(agentDir, rel string) uint64 {
	full := filepath.Join(agentDir, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil {
		return 0
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0
	}
	return st.Nlink
}

var streamSeq atomic.Uint64

// writeFreshStream writes r to a new inode and renames it onto name in dir.
// An existing inode is not modified. name is not followed.
func writeFreshStream(dir, name string, r io.Reader, mode os.FileMode, limit int64) (int64, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return 0, errors.New("path")
	}
	parent, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	defer parent.Close()
	tmp := fmt.Sprintf(".castbot-%d-%d", os.Getpid(), streamSeq.Add(1))
	fd, err := unix.Openat(int(parent.Fd()), tmp, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return 0, err
	}
	f := os.NewFile(uintptr(fd), tmp)
	n, copyErr := io.Copy(f, io.LimitReader(r, limit))
	if copyErr != nil {
		f.Close()
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return n, copyErr
	}
	if err := syscall.Fchmod(int(f.Fd()), uint32(mode.Perm())); err != nil {
		f.Close()
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return n, err
	}
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return n, err
	}
	if err := unix.Renameat(int(parent.Fd()), tmp, int(parent.Fd()), name); err != nil {
		_ = unix.Unlinkat(int(parent.Fd()), tmp, 0)
		return n, err
	}
	return n, nil
}

func groupWritableShared(st syscall.Stat_t) bool {
	if st.Mode&0o020 == 0 {
		return false
	}
	if gid, ok := castworkGID(); ok {
		return int(st.Gid) == gid
	}
	return int(st.Gid) == os.Getgid()
}

func castworkGID() (int, bool) {
	grp, err := user.LookupGroup("castwork")
	if err != nil {
		return 0, false
	}
	id, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return 0, false
	}
	return id, true
}
