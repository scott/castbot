package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerMachineHostMenu(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "# machine-hosts:start\n")
	end := strings.Index(text, "# machine-hosts:end\n")
	if start < 0 || end < start {
		t.Fatal("machine host block is missing")
	}
	block := text[start:end]
	var got []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "}" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "machine_host") || line == "EOF" || line == "cat <<'EOF'" {
			continue
		}
		got = append(got, line)
	}
	want := []string{"digitalocean\tDigitalOcean\ttoken\tcloud machines, needs an API token"}
	if len(got) != len(want) {
		t.Fatalf("menu %q", got)
	}
	for i, row := range want {
		if got[i] != row {
			t.Fatalf("line %d %q want %q", i, got[i], row)
		}
	}
	lower := strings.ToLower(block)
	for _, banned := range []string{"sprite", "docker", "ollama", "bedrock"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("menu offers %s", banned)
		}
	}
}
