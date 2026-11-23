package castlet

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// finishShared sets the mode on the descriptor that was just written.
// When the castwork group exists, the file's group is that group.
// The path is not used, so a swapped link cannot receive the mode.
func finishShared(f *os.File, mode uint32) error {
	if f == nil {
		return errors.New("path")
	}
	if mode == 0 {
		mode = uint32(agentFileMode)
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
