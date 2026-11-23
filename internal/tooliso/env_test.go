package tooliso

import (
	"strings"
	"testing"
)

func TestToolEnvDropsSecrets(t *testing.T) {
	parent := []string{
		"PATH=/app/.local/bin:/usr/bin:/bin",
		"HOME=/app",
		"LANG=C.UTF-8",
		"TERM=xterm",
		"TZ=UTC",
		"CAST_AGENT_DIR=/app/agent",
		"OPENROUTER_API_KEY=fixture-model-key",
		"NOTES_TOKEN=fixture-notes",
		"AUTH_TOKEN=fixture-runtime-token",
		"CASTBOT_BOOTSTRAP_TOKEN=fixture-boot",
		"CASTLET_SECRET_COUNT=2",
		"CASTBOT_TOOL_SECRET_NAMES=NOTES_TOKEN,OPENROUTER_API_KEY",
		"PUBLIC_TOKEN=fixture-public",
		"HTTP_PROXY=http://127.0.0.1:9",
	}
	got := strings.Join(ToolEnv(parent, ParseNames("OPENROUTER_API_KEY,PUBLIC_TOKEN")), "\n")
	for _, secret := range []string{"fixture-model-key", "fixture-notes", "fixture-runtime-token", "fixture-boot", "HTTP_PROXY", "CASTBOT_TOOL_SECRET_NAMES"} {
		if strings.Contains(got, secret) {
			t.Fatalf("tool env contains %s\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "PUBLIC_TOKEN=fixture-public") {
		t.Fatalf("opt-in name missing\n%s", got)
	}
	if !strings.Contains(got, "HOME=/app/agent") || !strings.Contains(got, "USER=agent") {
		t.Fatalf("home or user\n%s", got)
	}
	path := ""
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "PATH=") {
			path = strings.TrimPrefix(line, "PATH=")
		}
	}
	if !strings.HasPrefix(path, "/usr/local/sbin:") || !strings.HasSuffix(path, ":/app/.local/bin") {
		t.Fatalf("path order %s", path)
	}
	if strings.Contains(path, "/app/.local/bin:/usr") {
		t.Fatalf("writable dir shadows system path %s", path)
	}
}

func TestParseNamesDefaultDeny(t *testing.T) {
	if got := ParseNames(""); len(got) != 0 {
		t.Fatal(got)
	}
	if got := ParseNames("OPENAI_API_KEY,CASTBOT_URL,notes"); len(got) != 0 {
		t.Fatal(got)
	}
	got := ParseNames("SEARCH_API_KEY, SEARCH_API_KEY, OPENAI_API_KEY")
	if len(got) != 1 || got[0] != "SEARCH_API_KEY" {
		t.Fatal(got)
	}
}

func TestRewriteRuntimePath(t *testing.T) {
	env := RewriteRuntimePath([]string{"HOME=/app", "PATH=/app/.local/bin:/usr/bin:/bin"})
	var path string
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			path = strings.TrimPrefix(item, "PATH=")
		}
	}
	if strings.HasPrefix(path, "/app/.local") {
		t.Fatal(path)
	}
	if !strings.Contains(path, "/usr/bin") || !strings.HasSuffix(path, "/app/.local/bin") {
		t.Fatal(path)
	}
	idxBin := strings.Index(path, "/usr/bin")
	idxLocal := strings.Index(path, "/app/.local/bin")
	if idxBin < 0 || idxLocal < 0 || idxLocal < idxBin {
		t.Fatal(path)
	}
}
