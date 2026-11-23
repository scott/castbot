package castlet

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/scott/castbot/internal/tooliso"
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
	f, err := tooliso.CreateUnder(agentDir, rel, flags, openMode)
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
