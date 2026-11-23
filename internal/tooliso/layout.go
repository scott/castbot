package tooliso

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/scott/castbot/internal/bundle"
)

// PrepareLayout creates the runtime-private directories next to the agent
// directory. Mode 0700 keeps the tool account out. Errors are ignored when
// the directory cannot be created, because a read-only test tree still boots.
func PrepareLayout(agentDir string) {
	parent := filepath.Dir(agentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return
	}
	for _, name := range []string{".cast", ".castlet"} {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			continue
		}
		_ = os.Chmod(dir, 0o700)
	}
	if err := os.MkdirAll(StateDir(agentDir), 0o700); err == nil {
		_ = os.Chmod(StateDir(agentDir), 0o700)
	}
}

// StateDir is castlet state for one agent. It sits outside the agent
// directory so a command cannot rename or replace it.
func StateDir(agentDir string) string {
	parent := filepath.Dir(agentDir)
	base := filepath.Base(agentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) || base == "" || base == "." || base == string(filepath.Separator) {
		return filepath.Join(agentDir, ".castlet")
	}
	return filepath.Join(parent, ".castlet", base)
}

// SealFromBundle writes the permission copy of agent.yaml and the MCP
// config files next to the agent directory. The bytes come from the bundle
// the control plane sent. The workspace copies are not read again.
func SealFromBundle(agentDir string, raw []byte) {
	parent := filepath.Dir(agentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return
	}
	files, err := bundle.Files(raw)
	if err != nil {
		return
	}
	for name, body := range files {
		if name != "agent.yaml" && !(strings.HasPrefix(name, "mcp/") && strings.HasSuffix(name, ".yaml")) {
			continue
		}
		_ = writePrivate(filepath.Join(parent, ".cast", filepath.FromSlash(name)), body)
	}
}

func writePrivate(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err != nil && (errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EEXIST)) {
		if rm := os.Remove(path); rm != nil && !os.IsNotExist(rm) {
			return rm
		}
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	}
	if err != nil {
		return err
	}
	if _, werr := f.Write(body); werr != nil {
		f.Close()
		return werr
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
