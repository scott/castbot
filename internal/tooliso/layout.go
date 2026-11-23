package tooliso

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

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
		_ = mkdirNoFollow(filepath.Join(parent, name), 0o700)
	}
	_ = mkdirNoFollow(StateDir(agentDir), 0o700)
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
// A config file that is no longer in the bundle is removed.
func SealFromBundle(agentDir string, raw []byte) error {
	parent := filepath.Dir(agentDir)
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return errors.New("seal path")
	}
	files, err := bundle.Files(raw)
	if err != nil {
		return err
	}
	wanted := map[string][]byte{}
	for name, body := range files {
		if name == "agent.yaml" || (strings.HasPrefix(name, "mcp/") && strings.HasSuffix(name, ".yaml") && strings.Count(name, "/") == 1) {
			wanted[name] = body
		}
	}
	if _, ok := wanted["agent.yaml"]; !ok {
		return errors.New("seal config: agent.yaml")
	}
	for name, body := range wanted {
		if err := writePrivate(filepath.Join(parent, ".cast", filepath.FromSlash(name)), body); err != nil {
			return err
		}
	}
	return removeStaleSeal(filepath.Join(parent, ".cast"), wanted)
}

func removeStaleSeal(dir string, wanted map[string][]byte) error {
	mcpDir := filepath.Join(dir, "mcp")
	entries, err := listDirNoFollow(mcpDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		name := "mcp/" + ent.Name()
		if _, ok := wanted[name]; ok {
			continue
		}
		if err := removeNameNoFollow(mcpDir, ent.Name()); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func writePrivate(path string, body []byte) error {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if err := mkdirNoFollow(dir, 0o700); err != nil {
		return err
	}
	return WriteFresh(dir, name, body, 0o600)
}
