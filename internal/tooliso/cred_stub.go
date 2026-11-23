//go:build !linux

package tooliso

import "os/exec"

func RuntimeUID() (int, bool) { return 0, false }

func ApplyRuntime(cmd *exec.Cmd) error { return nil }

func ApplyTool(cmd *exec.Cmd) error { return nil }

func OwnRuntime(path string) {}

func ShareCastwork(fd int) {}
