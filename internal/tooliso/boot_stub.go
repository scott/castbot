//go:build !linux

package tooliso

import "errors"

// PrepareApp is refused off Linux.
func PrepareApp(agentDir string) error { return errors.New("setup") }

// NoteLinkSysctl is a no-op off Linux.
func NoteLinkSysctl() {}

// DropToRuntime is a no-op off Linux.
func DropToRuntime() error { return nil }
