package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
)

const codingFixture = "fixture-should-not-appear"

func TestRuntimeCodingImage(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := host.BuildRuntime(ctx, root); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("docker", "image", "inspect", host.RuntimeImage, "--format", "{{.Config.User}} {{.Size}}").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect: %s", out)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "nobody" {
		t.Fatalf("user %q", strings.TrimSpace(string(out)))
	}
	t.Logf("runtime image bytes %s", fields[1])
	envOut, err := exec.Command("docker", "image", "inspect", host.RuntimeImage, "--format", "{{range .Config.Env}}{{println .}}{{end}}").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	envText := string(envOut)
	for _, needle := range []string{
		"CAST_AGENT_DIR=/app/agent",
		"PYTHONUSERBASE=/app/.local",
		"NPM_CONFIG_PREFIX=/app/.local",
		"GOBIN=/app/.local/bin",
		"CASTLET_EGRESS_URL=http://127.0.0.1:8090",
		"PATH=/app/.local/bin:",
	} {
		if !strings.Contains(envText, needle) {
			t.Fatalf("missing %s", needle)
		}
	}
	for _, line := range strings.Split(envText, "\n") {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "TOKEN=") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "API_KEY") {
			t.Fatal("image env has a secret name")
		}
	}

	dir := t.TempDir()
	t.Cleanup(func() {
		owner := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
		_ = exec.Command("docker", "run", "--rm", "--user", "0:0", "-v", dir+":/work", "--entrypoint", "/bin/chown", host.RuntimeImage, "-R", owner, "/work").Run()
	})
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	chown := exec.Command("docker", "run", "--rm", "--user", "0:0", "-v", dir+":/work", "--entrypoint", "/bin/chown", host.RuntimeImage, "-R", "nobody:nogroup", "/work")
	if out, err := chown.CombinedOutput(); err != nil {
		t.Fatalf("chown: %s", out)
	}
	script := codingScript
	cmd := exec.Command("docker", "run", "--rm", "-i", "--network", "none",
		"-e", "GITHUB_TOKEN="+codingFixture,
		"-e", "GH_TOKEN="+codingFixture,
		"-e", "GITHUB_PERSONAL_ACCESS_TOKEN="+codingFixture,
		"-e", "DIGITALOCEAN_ACCESS_TOKEN="+codingFixture,
		"-e", "CAST_AGENT_DIR=/work",
		"-v", dir+":/work",
		"--entrypoint", "/bin/sh", host.RuntimeImage)
	cmd.Stdin = strings.NewReader(script)
	out, err = cmd.CombinedOutput()
	if err != nil || bytes.Contains(out, []byte(codingFixture)) {
		t.Fatalf("coding image: %v\n%s", err, out)
	}
	if !bytes.Contains(out, []byte("coding-ok")) {
		t.Fatalf("script output\n%s", out)
	}
}

func TestRuntimePrefixSurvivesRestart(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := host.BuildRuntime(ctx, root); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	name := "castbot-prefix-" + hex.EncodeToString(buf)
	run := exec.Command("docker", "run", "-d", "--name", name, "--network", "none", "--entrypoint", "sleep", host.RuntimeImage, "infinity")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("run: %s", out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	install := exec.Command("docker", "exec", "-i", name, "/bin/sh")
	install.Stdin = strings.NewReader(prefixInstall)
	if out, err := install.CombinedOutput(); err != nil {
		t.Fatalf("install: %s", out)
	}
	if out, err := exec.Command("docker", "restart", name).CombinedOutput(); err != nil {
		t.Fatalf("restart: %s", out)
	}
	check := exec.Command("docker", "exec", name, "/bin/sh", "-c", prefixCheck)
	out, err := check.CombinedOutput()
	if err != nil {
		t.Fatalf("prefix: %s", out)
	}
}

const codingScript = `set -eu
test "$(id -u)" = "65534"
test "$(id -un)" = "nobody"
for c in git curl rg node npm go gh doctl castbot-mcp castbot-git-credential; do
  command -v "$c" >/dev/null
done
git --version | grep -F 'git version'
node --version | grep -F 'v26.13.1'
npm --version >/dev/null
go version | grep -F 'go1.27.4'
gh version | grep -F '2.104.0'
doctl version | grep -F '2.4.0'
rg --version | grep -F 'ripgrep'
curl --version | grep -F curl
test -x /usr/bin/git
test -x /opt/castbot/libexec/gh
test -x /opt/castbot/libexec/doctl
test -d /app/.local/bin
test -f /opt/castbot/mcp/github.yaml
test -f /opt/castbot/mcp/digitalocean.yaml
test -f /opt/castbot/mcp/request_secret.yaml
if grep -R -E 'TOKEN|SECRET' /opt/castbot/mcp; then
  echo 'mcp file names a token' >&2
  exit 1
fi
cd /work
git init -b main >/dev/null
git config user.email fixture@example.com
git config user.name Fixture
echo hello > README.md
git add README.md
git commit -m fixture >/dev/null
if git config --get-regexp 'insteadof' >/dev/null 2>&1; then
  echo 'git rewrote a remote while disconnected' >&2
  exit 1
fi
status=$(/opt/cast/bin/python -c 'from cast.tools.git import git_status; print(git_status.invoke({}))')
printf '%s\n' "$status" | grep -F 'Current branch: main'
printf '%s\n' "$status" | grep -F 'Working tree is clean.'
printf 'protocol=https\nhost=github.com\n\n' | /usr/local/bin/castbot-git-credential get >/app/cred.out 2>/app/cred.err || true
grep -F 'Connections are not available on this machine.' /app/cred.err
if grep -F 'fixture-should-not-appear' /app/cred.out /app/cred.err; then
  echo 'credential helper echoed the env value' >&2
  exit 1
fi
set +e
ghout=$(/usr/local/bin/gh auth token 2>&1)
ghcode=$?
doout=$(/usr/local/bin/doctl auth init 2>&1)
docode=$?
set -e
test "$ghcode" != "0"
test "$docode" != "0"
printf '%s\n' "$ghout" | grep -F 'disabled'
printf '%s\n' "$doout" | grep -F 'disabled'
python3 - <<'PY'
import json, os, subprocess, sys
fixture = "fixture-should-not-appear"
env = os.environ.copy()
def run(argv, lines):
    proc = subprocess.run(argv, input="\n".join(lines)+"\n", text=True, capture_output=True, timeout=15, env=env)
    blob = proc.stdout + proc.stderr
    if fixture in blob:
        sys.exit("shim echoed the env value")
    return blob
secret = run(["/usr/local/bin/castbot-mcp", "request_secret"], [
    json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"cast","version":"0"}}}),
    json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"}),
    json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/list"}),
    json.dumps({"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_secret","arguments":{"connection":"github","reason":"need a repository"}}}),
    json.dumps({"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"request_secret","arguments":{"connection":"aws","reason":"no"}}}),
])
if "request_secret" not in secret or "Connections are not available on this machine." not in secret or "The connection request was rejected." not in secret:
    sys.exit("request_secret did not fail closed: " + secret)
github = run(["/usr/local/bin/castbot-mcp", "github"], [
    json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"cast","version":"0"}}}),
    json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/list"}),
])
if "Connections are not available on this machine." not in github:
    sys.exit("github shim did not fail closed: " + github)
print("shim-ok")
PY
echo coding-ok
`

const prefixInstall = `set -eu
mkdir -p /tmp/py /tmp/npm /tmp/castbot-go-demo
cat > /tmp/py/setup.py <<'EOF'
from setuptools import setup
setup(
    name="castbot-prefix-demo",
    version="0.0.1",
    py_modules=["castbot_prefix_demo"],
    entry_points={"console_scripts": ["castbot-prefix-demo=castbot_prefix_demo:main"]},
)
EOF
cat > /tmp/py/castbot_prefix_demo.py <<'EOF'
def main():
    print("prefix-ok")
EOF
python3 -m pip install --user --no-index --no-deps /tmp/py
cat > /tmp/npm/package.json <<'EOF'
{"name":"castbot-npm-demo","version":"1.0.0","bin":{"castbot-npm-demo":"index.js"}}
EOF
printf '%s\n' '#!/usr/bin/env node' 'console.log("prefix-ok")' > /tmp/npm/index.js
chmod +x /tmp/npm/index.js
npm install -g /tmp/npm --offline --no-audit --no-fund --ignore-scripts
cat > /tmp/castbot-go-demo/go.mod <<'EOF'
module example.com/castbot-go-demo

go 1.27
EOF
cat > /tmp/castbot-go-demo/main.go <<'EOF'
package main

import "fmt"

func main() { fmt.Println("prefix-ok") }
EOF
(cd /tmp/castbot-go-demo && GOPROXY=off go install .)
test -x /app/.local/bin/castbot-prefix-demo
test -x /app/.local/bin/castbot-npm-demo
test -x /app/.local/bin/castbot-go-demo
`

const prefixCheck = `set -eu
test "$(castbot-prefix-demo)" = "prefix-ok"
test "$(castbot-npm-demo)" = "prefix-ok"
test "$(castbot-go-demo)" = "prefix-ok"
test -x /app/.local/bin/castbot-prefix-demo
test -x /app/.local/bin/castbot-npm-demo
test -x /app/.local/bin/castbot-go-demo
`
