package castlet

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Files the runtime creates under the agent directory are group-readable
// by castwork. Private state outside that directory stays mode 0700.
const (
	agentFileMode = 0o660
	agentDirMode  = 0o2770
)

func shareAgentDir(agentDir, path string) error {
	return chmodAgentDirs(agentDir, path)
}

func shareAgentFile(agentDir, path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path")
	}
	if err := chmodAgentDirs(agentDir, filepath.Dir(path)); err != nil {
		return err
	}
	if mode == 0 {
		mode = agentFileMode
	}
	return os.Chmod(path, mode)
}

func agentSharedMode(archived os.FileMode) os.FileMode {
	mode := os.FileMode(agentFileMode)
	if archived&0o111 != 0 {
		mode |= 0o110
	}
	return mode
}

func chmodAgentDirs(agentDir, path string) error {
	root := filepath.Clean(agentDir)
	path = filepath.Clean(path)
	if root == "." || root == "" || root == string(filepath.Separator) {
		return errors.New("path")
	}
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return nil
	}
	var chain []string
	cur := path
	for {
		chain = append(chain, cur)
		if cur == root {
			break
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
		cur = next
	}
	for i := len(chain) - 1; i >= 0; i-- {
		info, err := os.Lstat(chain[i])
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path")
		}
		if !info.IsDir() {
			continue
		}
		if err := syscall.Chmod(chain[i], uint32(agentDirMode)); err != nil {
			return err
		}
	}
	return nil
}
