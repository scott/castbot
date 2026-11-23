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
	stale := filepath.Join(filepath.Dir(agent), ".cast", "mcp", "old.yaml")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("command: /bin/false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SealFromBundle(agent, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatal("removed config was left sealed")
	}
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
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, ".cast"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedAgent := filepath.Join(blocked, "agent")
	if err := os.MkdirAll(blockedAgent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SealFromBundle(blockedAgent, raw); err == nil {
		t.Fatal("seal succeeded on a blocked path")
	}
}

func TestSealReplacesAnExtraLink(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "agent")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _, err := bundle.Archive("name: Atlas\n", "Be brief.\n")
	if err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(root, "kept")
	if err := os.WriteFile(kept, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".cast"), 0o700); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(root, ".cast", "agent.yaml")
	if err := os.Link(kept, sealed); err != nil {
		t.Fatal(err)
	}
	if err := SealFromBundle(agent, raw); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(kept)
	if err != nil || string(got) != "kept" {
		t.Fatalf("kept %q %v", got, err)
	}
	body, err := os.ReadFile(sealed)
	if err != nil || !strings.Contains(string(body), "name: Atlas") {
		t.Fatalf("sealed %q %v", body, err)
	}
}
