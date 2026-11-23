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
}
