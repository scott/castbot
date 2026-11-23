package tooliso

import (
	"os"
	"path/filepath"
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

// SealConfig copies agent.yaml to the private directory next to the agent.
// The runtime reads that copy for its permission config. The workspace copy
// stays where the bundle wrote it.
func SealConfig(agentDir string) {
	raw, err := os.ReadFile(filepath.Join(agentDir, "agent.yaml"))
	if err != nil {
		return
	}
	parent := filepath.Dir(agentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return
	}
	dir := filepath.Join(parent, ".cast")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.Chmod(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "agent.yaml"), raw, 0o600)
}
