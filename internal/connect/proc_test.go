package connect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostProcessFramesTokenAndStopsIdle(t *testing.T) {
	const token = "test-connection-value"
	t.Setenv("CASTBOT_PARENT_MARKER", "parent-marker-value")
	dir := t.TempDir()
	names := filepath.Join(dir, "names")
	script := filepath.Join(dir, "fake-mcp")
	body := `#!/bin/sh
env | awk -F= '{print $1}' > '` + names + `'
while true; do
  len=""
  while IFS= read -r line; do
    line=$(printf '%s' "$line" | tr -d '\r')
    [ -z "$line" ] && break
    case "$line" in
      [Cc]ontent-[Ll]ength:*) len=$(printf '%s' "$line" | sed 's/.*: *//') ;;
    esac
  done
  [ -n "$len" ] || exit 1
  dd bs=1 count="$len" of=/dev/null 2>/dev/null
  hash=$(printf '%s' "$GITHUB_PERSONAL_ACCESS_TOKEN" | sha256sum | awk '{print $1}')
  msg="{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"hash\":\"$hash\"}}"
  printf 'Content-Length: %s\r\n\r\n%s' "${#msg}" "$msg"
done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	pool := NewPool(Commands{GitHub: []string{script}, Idle: 300 * time.Millisecond})
	out, err := pool.RoundTrip(context.Background(), GitHub, token, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), token) {
		t.Fatal("stdout held the token")
	}
	sum := sha256.Sum256([]byte(token))
	if !strings.Contains(string(out), hex.EncodeToString(sum[:])) {
		t.Fatalf("hash missing %s", out)
	}
	raw, err := os.ReadFile(names)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Contains(got, token) || !strings.Contains(got, "GITHUB_PERSONAL_ACCESS_TOKEN") || !strings.Contains(got, "PATH") {
		t.Fatal(got)
	}
	for _, line := range strings.Split(got, "\n") {
		switch strings.TrimSpace(line) {
		case "", "PATH", "LANG", "PWD", "GITHUB_PERSONAL_ACCESS_TOKEN":
		default:
			t.Fatalf("extra env %q", line)
		}
		if strings.Contains(line, "CASTBOT_PARENT_MARKER") || strings.Contains(line, "parent-marker-value") {
			t.Fatal("parent env was copied")
		}
	}
	time.Sleep(700 * time.Millisecond)
	pool.mu.Lock()
	s := pool.sessions[GitHub]
	pool.mu.Unlock()
	if s == nil || s.Alive() {
		t.Fatal("idle process still running")
	}

	echo := filepath.Join(dir, "echo-mcp")
	echoBody := `#!/bin/sh
while true; do
  len=""
  while IFS= read -r line; do
    line=$(printf '%s' "$line" | tr -d '\r')
    [ -z "$line" ] && break
    case "$line" in
      [Cc]ontent-[Ll]ength:*) len=$(printf '%s' "$line" | sed 's/.*: *//') ;;
    esac
  done
  dd bs=1 count="$len" of=/dev/null 2>/dev/null
  msg="{\"echo\":\"$GITHUB_PERSONAL_ACCESS_TOKEN\"}"
  printf 'Content-Length: %s\r\n\r\n%s' "${#msg}" "$msg"
done
`
	if err := os.WriteFile(echo, []byte(echoBody), 0o755); err != nil {
		t.Fatal(err)
	}
	leaky := NewPool(Commands{GitHub: []string{echo}, Idle: time.Second})
	out, err = leaky.RoundTrip(context.Background(), GitHub, token, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil || strings.Contains(string(out), token) || !strings.Contains(string(out), "redacted") {
		t.Fatalf("%s %v", out, err)
	}
	leaky.mu.Lock()
	if leaky.sessions[GitHub] != nil {
		leaky.sessions[GitHub].Stop()
	}
	leaky.mu.Unlock()
}
