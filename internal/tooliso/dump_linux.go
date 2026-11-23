//go:build linux

package tooliso

import "syscall"

const (
	prSetDumpable   = 4
	prSetNoNewPrivs = 38
)

// SetNonDumpable hides this process's memory and environment from other
// processes, including ones that share its user. A child keeps the flag
// until its user id changes.
func SetNonDumpable() error {
	return prctl(prSetDumpable, 0)
}

// SetNoNewPrivs stops the process from gaining privileges later.
func SetNoNewPrivs() error {
	return prctl(prSetNoNewPrivs, 1)
}

func prctl(option, arg uintptr) error {
	_, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, option, arg, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
