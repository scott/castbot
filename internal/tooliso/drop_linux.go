//go:build linux

package tooliso

import (
	"errors"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// DirectRun reports that this process should run the command itself.
// A test process does. The tool account does, because it is already that user.
// The runtime account does not: it asks the supervisor, which is root.
func DirectRun() bool {
	if testBypass() {
		return true
	}
	uid, _, err := toolIDs()
	return err == nil && os.Geteuid() == uid
}

// DropToToolUser makes the current process the tool account.
// Groups are not changed here. The caller must already have only the tool
// group. Root and the tool account may drop. An ordinary test process
// continues as itself when CASTBOT_TOOLRUN_TEST=1.
func DropToToolUser() error {
	runtime.LockOSThread()
	if testBypass() {
		syscall.Umask(0o007)
		return nil
	}
	uid, gid, err := toolIDs()
	if err != nil {
		return err
	}
	if err := requireGroups(gid); err != nil {
		return err
	}
	if os.Geteuid() != 0 && os.Geteuid() != uid {
		return errors.New("tool user is not available")
	}
	if err := syscall.Setresuid(uid, uid, uid); err != nil {
		return err
	}
	if syscall.Getuid() != uid || syscall.Geteuid() != uid {
		return errors.New("tool user drop did not stick")
	}
	syscall.Umask(0o007)
	if err := SetNoNewPrivs(); err != nil {
		return err
	}
	// A user id change clears the dumpable flag. Set it again now that the
	// user id is final, before this process execs a normal program.
	return SetNonDumpable()
}

// RequireToolGroups reports whether this process's group set is exactly the
// tool group. It does not call setgroups. A mismatch refuses the command.
func RequireToolGroups() error {
	if testBypass() {
		return nil
	}
	_, gid, err := toolIDs()
	if err != nil {
		return err
	}
	return requireGroups(gid)
}

func testBypass() bool {
	return os.Getenv("CASTBOT_TOOLRUN_TEST") == "1" && os.Getuid() == os.Geteuid() && os.Geteuid() != 0
}

func toolIDs() (int, int, error) {
	account, err := user.Lookup(ToolUser)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

func requireGroups(gid int) error {
	rgid, egid, sgid := unix.Getresgid()
	if int(rgid) != gid || int(egid) != gid || int(sgid) != gid {
		return errors.New("command group is not set")
	}
	groups, err := syscall.Getgroups()
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g != gid {
			return errors.New("command group is not set")
		}
	}
	return nil
}
