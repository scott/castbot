//go:build linux

package tooliso

import (
	"errors"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"syscall"
)

// DropToToolUser makes the current process the tool account and nothing else.
// A setuid execution always drops. An ordinary test process drops only when
// CASTBOT_TOOLRUN_TEST=1, and then it stays the caller so unit tests can
// check the environment without a second account.
func DropToToolUser() error {
	runtime.LockOSThread()
	if os.Getenv("CASTBOT_TOOLRUN_TEST") == "1" && os.Getuid() == os.Geteuid() && os.Geteuid() != 0 {
		syscall.Umask(0o007)
		return nil
	}
	account, err := user.Lookup(ToolUser)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 && os.Geteuid() != uid {
		return errors.New("tool user is not available")
	}
	_ = syscall.Setgroups([]int{gid})
	if err := syscall.Setresgid(gid, gid, gid); err != nil && syscall.Getegid() != gid {
		return err
	}
	if err := syscall.Setresuid(uid, uid, uid); err != nil {
		return err
	}
	if syscall.Getuid() != uid || syscall.Geteuid() != uid {
		return errors.New("tool user drop did not stick")
	}
	syscall.Umask(0o007)
	_ = SetNoNewPrivs()
	_ = SetNonDumpable()
	return nil
}
