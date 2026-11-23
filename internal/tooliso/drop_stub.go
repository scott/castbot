//go:build !linux

package tooliso

import "errors"

// DropToToolUser is refused off Linux. Commands run on the Linux image.
func DropToToolUser() error { return errors.New("tool user is linux only") }

// RequireToolGroups is refused off Linux.
func RequireToolGroups() error { return errors.New("tool user is linux only") }

// DirectRun is false off Linux.
func DirectRun() bool { return false }
