//go:build linux

package tooliso

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// PrepareApp creates the private directories beside the agent directory
// and gives the app directory to root. It runs before the process drops.
// A directory entry that is a symlink is replaced. The target is not opened.
func PrepareApp(agentDir string) error {
	if os.Geteuid() != 0 {
		return errors.New("setup")
	}
	parent := filepath.Dir(agentDir)
	base := filepath.Base(agentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) || base == "" || base == "." || base == ".." {
		return errors.New("setup")
	}
	app, err := os.OpenFile(parent, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer app.Close()
	if err := unix.Fchown(int(app.Fd()), 0, 0); err != nil {
		return err
	}
	if err := unix.Fchmod(int(app.Fd()), 0o755); err != nil {
		return err
	}
	cred, err := credential(RuntimeUser)
	if err != nil {
		return err
	}
	for _, name := range []string{".cast", ".castlet"} {
		if err := ensureDir(int(app.Fd()), name, int(cred.Uid), int(cred.Gid), 0o700); err != nil {
			return err
		}
	}
	if err := ensureDir(int(app.Fd()), base, int(cred.Uid), int(cred.Gid), 0o2770); err != nil {
		return err
	}
	NoteLinkSysctl()
	return nil
}

// ensureDir creates name inside dirfd when it is missing.
// A symlink is removed and created again as a directory. The target is not used.
func ensureDir(dirfd int, name string, uid, gid int, mode uint32) error {
	var st unix.Stat_t
	err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
		if err := unix.Unlinkat(dirfd, name, 0); err != nil {
			return err
		}
		err = unix.ENOENT
	}
	if errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(dirfd, name, mode); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("setup")
	}
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return err
	}
	return unix.Fchmod(fd, mode)
}

// NoteLinkSysctl asks the kernel to enable the two link protections.
// A failed write is logged. Boot continues either way.
func NoteLinkSysctl() {
	for _, name := range []string{"fs.protected_hardlinks", "fs.protected_symlinks"} {
		path := "/proc/sys/" + name
		if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
			log.Printf("%s unchanged", name)
			continue
		}
		log.Printf("%s set", name)
	}
}

// DropToRuntime makes every thread the runtime account.
// A process that is already unprivileged is left as it is.
func DropToRuntime() error {
	if os.Geteuid() != 0 {
		return nil
	}
	cred, err := credential(RuntimeUser)
	if err != nil {
		return err
	}
	if err := syscall.Setgroups([]int{int(cred.Gid)}); err != nil {
		return err
	}
	if err := syscall.Setresgid(int(cred.Gid), int(cred.Gid), int(cred.Gid)); err != nil {
		return err
	}
	if err := syscall.Setresuid(int(cred.Uid), int(cred.Uid), int(cred.Uid)); err != nil {
		return err
	}
	if os.Geteuid() != int(cred.Uid) || os.Getuid() != int(cred.Uid) {
		return errors.New("drop")
	}
	checked := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if os.Geteuid() != int(cred.Uid) || os.Getuid() != int(cred.Uid) {
			checked <- errors.New("drop")
			return
		}
		checked <- nil
	}()
	if err := <-checked; err != nil {
		return err
	}
	if err := allThreadsPrctl(prSetNoNewPrivs, 1); err != nil {
		return err
	}
	return allThreadsPrctl(prSetDumpable, 0)
}

func allThreadsPrctl(option, arg uintptr) error {
	_, _, errno := syscall.AllThreadsSyscall6(syscall.SYS_PRCTL, option, arg, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
