//go:build !linux

package tooliso

import "errors"

// SetNonDumpable is a no-op off Linux. The machine image is Linux.
func SetNonDumpable() error { return errors.New("dumpable is linux only") }

// SetNoNewPrivs is a no-op off Linux.
func SetNoNewPrivs() error { return errors.New("no new privs is linux only") }
