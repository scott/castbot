package tooliso

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scott/castbot/internal/bundle"
)

func TestSealUsesTheBundleBytes(t *testing.T) {
	agent := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _, err := bundle.ArchiveFiles("name: Atlas\npermissions:\n  bash: ask\n", "Be brief.\n", map[string]string{
		"mcp/notes.yaml": "command: /bin/true\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Install(agent, raw); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "agent.yaml"), []byte("name: Hacked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agent, "mcp", "notes.yaml"), []byte("command: /bin/false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	SealFromBundle(agent, raw)
	got, err := os.ReadFile(filepath.Join(filepath.Dir(agent), ".cast", "agent.yaml"))
	if err != nil || !strings.Contains(string(got), "bash: ask") || strings.Contains(string(got), "Hacked") {
		t.Fatalf("config %q %v", got, err)
	}
	mcp, err := os.ReadFile(filepath.Join(filepath.Dir(agent), ".cast", "mcp", "notes.yaml"))
	if err != nil || string(mcp) != "command: /bin/true\n" {
		t.Fatalf("mcp %q %v", mcp, err)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(agent), ".cast", "agent.yaml"))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode %v %v", info, err)
	}
}
