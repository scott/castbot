package web

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scott/castbot/internal/host"
)

func TestRuntimeToolIsolation(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if err := host.BuildRuntime(ctx, root); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "run", "--rm", "-i", "--network", "none",
		"-e", "OPENROUTER_API_KEY=fixture-iso-model",
		"-e", "NOTES_TOKEN=fixture-iso-notes",
		"-e", "PUBLIC_TOKEN=fixture-public",
		"-e", "CASTBOT_TOOL_SECRET_NAMES=PUBLIC_TOKEN,OPENROUTER_API_KEY",
		"-e", "CAST_AGENT_DIR=/app/agent",
		"--entrypoint", "/bin/sh", host.RuntimeImage)
	cmd.Stdin = strings.NewReader(toolIsolationScript)
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("isolation-ok")) {
		t.Fatalf("isolation: %v\n%s", err, out)
	}
	if bytes.Contains(out, []byte("fixture-iso-model")) || bytes.Contains(out, []byte("fixture-iso-notes")) {
		t.Fatal("tool output contained a secret")
	}
}

const toolIsolationScript = `set -eu
test -x /bin/bash
test -u /usr/local/bin/castbot-toolrun
test -g /usr/local/bin/castbot-toolrun
grep -q '^agent:x:999:998:' /etc/passwd
umask 077
printf '%s\n' 'private-file' > /app/.castlet/env
chmod 600 /app/.castlet/env
mkdir -p /app/agent /app/.local/bin
printf '%s\n' 'hello' > /app/agent/note.txt
chmod 640 /app/agent/note.txt
ln -s /etc/passwd /app/agent/escape
ln -s /proc/self/environ /app/agent/envlink
printf '%s\n' '#!/bin/sh' 'echo planted' > /app/.local/bin/cat
chmod 755 /app/.local/bin/cat
python3 - <<'PY' &
import ctypes, os, time
libc = ctypes.CDLL(None)
if libc.prctl(4, 0, 0, 0, 0) != 0:
    raise SystemExit("prctl")
open("/tmp/rt.ready", "w").write(str(os.getpid()))
time.sleep(60)
PY
i=0
while [ ! -s /tmp/rt.ready ]; do
  i=$((i + 1))
  if [ "$i" -gt 50 ]; then
    echo 'runtime did not become non-dumpable' >&2
    exit 1
  fi
  sleep 0.1
done
rt=$(cat /tmp/rt.ready)
if cat /proc/"$rt"/environ >/dev/null 2>&1; then
  echo 'same user read environ' >&2
  exit 1
fi
/usr/local/bin/castbot-toolrun -- /bin/bash -c "
  set -eu
  id -u > /tmp/tool.uid
  id -un > /tmp/tool.name
  id -g > /tmp/tool.gid
  id -G > /tmp/tool.groups
  if cat /proc/$rt/environ >/dev/null 2>&1; then
    echo tool-read-environ
    exit 1
  fi
  if kill -0 $rt 2>/dev/null; then
    echo could-signal
    exit 1
  fi
  if [ -n \"\${OPENROUTER_API_KEY-}\" ]; then
    echo saw-model
    exit 1
  fi
  if [ -n \"\${NOTES_TOKEN-}\" ]; then
    echo saw-notes
    exit 1
  fi
  test \"\${PUBLIC_TOKEN-}\" = fixture-public
  if cat /app/.castlet/env >/dev/null 2>&1; then
    echo read-private
    exit 1
  fi
  command -v cat > /tmp/tool.cat
  printf '%s\n' ok > /tmp/tool.done
"
test "$(cat /tmp/tool.uid)" = "999"
test "$(cat /tmp/tool.name)" = "agent"
test "$(cat /tmp/tool.gid)" = "998"
test "$(cat /tmp/tool.groups)" = "998"
/usr/local/bin/castbot-toolrun -- /bin/sh -c 'sleep 30 & sleep 30' &
hp=$!
sleep 0.4
kill -TERM "$hp"
wait "$hp" || true
left=0
for d in /proc/[0-9]*; do
  if grep -a -q 'sleep' "$d/cmdline" 2>/dev/null && grep -a -q '30' "$d/cmdline" 2>/dev/null; then
    left=1
  fi
done
test "$left" = 0
grep -q '^/usr/bin/cat$' /tmp/tool.cat
test "$(cat /tmp/tool.done)" = "ok"
/opt/cast/bin/python - <<'PY'
import os
os.environ["CAST_AGENT_DIR"] = "/app/agent"
from cast.tools.paths import resolve_agent_file
for path in ("/proc/self/environ", "/sys/kernel", "/dev/null", "/app/.castlet/env", "/app/agent/escape", "/app/agent/envlink", "/etc/passwd"):
    try:
        resolve_agent_file(path)
    except OSError:
        continue
    raise SystemExit("allowed " + path)
got = resolve_agent_file("/app/agent/note.txt")
if not got.endswith("/note.txt"):
    raise SystemExit("note " + got)
missing = resolve_agent_file("/app/agent/missing.txt")
if not missing.endswith("/missing.txt"):
    raise SystemExit("missing " + missing)
from cast.tools.read import read_tool
body = read_tool.func("/app/agent/note.txt")
if body.strip() != "hello":
    raise SystemExit("note body " + body)
refused = read_tool.func("/proc/self/environ")
if not refused.startswith("Error"):
    raise SystemExit("proc " + refused[:40])
print("read-ok")
PY
/opt/cast/bin/python -c 'import os,time; open("/tmp/py.pid","w").write(str(os.getpid())); time.sleep(30)' &
i=0
while [ ! -s /tmp/py.pid ]; do
  i=$((i + 1))
  if [ "$i" -gt 50 ]; then
    echo 'runtime did not start' >&2
    exit 1
  fi
  sleep 0.1
done
if cat /proc/"$(cat /tmp/py.pid)"/environ >/dev/null 2>&1; then
  echo runtime-dumpable >&2
  exit 1
fi
kill "$(cat /tmp/py.pid)" 2>/dev/null || true
NOTES_TOKEN=fixture-granted PUBLIC_TOKEN=fixture-public CASTBOT_TOOL_SECRET_NAMES=PUBLIC_TOKEN /opt/cast/bin/python - <<'PY'
import os, stat, subprocess, git
env = os.environ.copy()
env["ADDED_MARKER"] = "added-ok"
out = subprocess.check_output(
    ["/bin/sh", "-c", 'printf "%s\\n%s\\n%s" "$NOTES_TOKEN" "$PUBLIC_TOKEN" "$ADDED_MARKER"'],
    env=env,
    text=True,
)
notes, public, added = out.split("\n")
if notes != "" or public != "fixture-public" or added != "added-ok":
    raise SystemExit("plain " + repr(out))
path = "/app/agent/gitfake"
with open(path, "w", encoding="utf-8") as fh:
    fh.write('#!/bin/sh\nprintf "%s\\n%s" "$NOTES_TOKEN" "$PUBLIC_TOKEN"\n')
os.chmod(path, stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP | stat.S_IROTH | stat.S_IXOTH)
git.cmd.Git.GIT_PYTHON_GIT_EXECUTABLE = path
text = git.cmd.Git("/app/agent").execute([path, "--version"])
parts = text.split("\n")
if len(parts) < 2 or parts[0] != "" or parts[1] != "fixture-public":
    raise SystemExit("git " + repr(text))
agent = "/app/agent"
os.makedirs(os.path.join(agent, "mcp"), exist_ok=True)
with open(os.path.join(agent, "mcp", "notes.yaml"), "w", encoding="utf-8") as fh:
    fh.write("command: /bin/true\nenv:\n  NOTES_TOKEN: x\n")
sealed = "/app/.cast/mcp"
os.makedirs(sealed, exist_ok=True)
with open(os.path.join(sealed, "notes.yaml"), "w", encoding="utf-8") as fh:
    fh.write("command: /bin/true\n")
import cast.mcp.loader as loader
found = loader._locate_config(agent, "notes", 0)
if found is None or not found[1].endswith("/.cast/mcp/notes.yaml"):
    raise SystemExit("mcp " + repr(found))
os.makedirs("/app/agent/workspace/made/sub", exist_ok=True)
with open("/app/agent/workspace/made/sub/note.txt", "w", encoding="utf-8") as fh:
    fh.write("from-runtime")
seen = subprocess.check_output(["/bin/cat", "/app/agent/workspace/made/sub/note.txt"], text=True)
if seen.strip() != "from-runtime":
    raise SystemExit("runtime dir " + repr(seen))
import pathlib
planted = "/app/.cast/planted"
os.makedirs("/app/.cast", exist_ok=True)
with open(planted, "w", encoding="utf-8") as fh:
    fh.write("sealed")
link = "/app/agent/memory/via-link"
os.makedirs("/app/agent/memory", exist_ok=True)
os.symlink(planted, link)
def refused(label, fn):
    try:
        fn()
    except OSError:
        return
    raise SystemExit("followed " + label)
refused("open", lambda: open(link, "w", encoding="utf-8").write("pwned"))
refused("pathlib", lambda: pathlib.Path(link).write_text("pwned"))
def os_open():
    fd = os.open(link, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o660)
    os.write(fd, b"pwned")
    os.close(fd)
refused("os.open", os_open)
with open(planted, encoding="utf-8") as fh:
    if fh.read() != "sealed":
        raise SystemExit("sealed changed")
via = pathlib.Path("/app/agent/workspace/made/sub/via-path.txt")
via.write_text("pathlib")
if (via.stat().st_mode & 0o777) != 0o660:
    raise SystemExit("pathlib mode")
fd = os.open("/app/agent/workspace/made/sub/via-os.txt", os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
os.write(fd, b"osopen")
os.close(fd)
if (os.stat("/app/agent/workspace/made/sub/via-os.txt").st_mode & 0o777) != 0o660:
    raise SystemExit("osopen mode")
print("env-ok")
PY
echo isolation-ok
`
