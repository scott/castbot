//go:build !linux

package tooliso

import "errors"

// DropToToolUser is refused off Linux. Commands run on the Linux image.
func DropToToolUser() error { return errors.New("tool user is linux only") }
