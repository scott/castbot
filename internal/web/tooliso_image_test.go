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
		"--security-opt", "no-new-privileges",
		"-e", "OPENROUTER_API_KEY=fixture-iso-model",
		"-e", "NOTES_TOKEN=fixture-iso-notes",
		"-e", "PUBLIC_TOKEN=fixture-public",
		"-e", "CASTBOT_TOOL_SECRET_NAMES=PUBLIC_TOKEN,OPENROUTER_API_KEY",
		"-e", "CAST_AGENT_DIR=/app/agent",
		"--entrypoint", "/bin/sh", host.RuntimeImage, "-c", toolBootWrapper)
	cmd.Stdin = strings.NewReader(toolIsolationScript)
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("isolation-ok")) {
		t.Fatalf("isolation: %v\n%s", err, out)
	}
	if bytes.Contains(out, []byte("fixture-iso-model")) || bytes.Contains(out, []byte("fixture-iso-notes")) {
		t.Fatal("tool output contained a secret")
	}
}

const toolBootWrapper = `set -eu
test ! -u /usr/local/bin/castbot-toolrun
test ! -g /usr/local/bin/castbot-toolrun
cat >/tmp/castbot-test.sh
/usr/local/bin/castbot-toolrun --serve >/tmp/tool-serve.log 2>&1 &
i=0
while [ ! -S /run/castbot-tool.sock ]; do
  i=$((i + 1))
  if [ "$i" -gt 50 ]; then
    echo 'tool server did not start' >&2
    cat /tmp/tool-serve.log >&2 || true
    exit 1
  fi
  sleep 0.1
done
exec /usr/local/bin/castbot-toolrun --as-runtime -- /bin/sh /tmp/castbot-test.sh
`

const toolIsolationScript = `set -eu
test -x /bin/bash
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
run_stopped() {
  /usr/local/bin/castbot-toolrun -- "$@" > /tmp/stop.out 2>/tmp/stop.err &
  sp=$!
  sleep 0.4
  kill -TERM "$sp" 2>/dev/null || true
  i=0
  while kill -0 "$sp" 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -gt 30 ]; then
      echo 'stopped command did not return' >&2
      exit 1
    fi
    sleep 0.1
  done
  wait "$sp" || true
}
run_stopped /bin/sh -c 'echo before-timeout; setsid sleep 600'
grep -q before-timeout /tmp/stop.out
run_stopped setsid sleep 600
for d in /proc/[0-9]*; do
  if grep -a -q 'sleep' "$d/cmdline" 2>/dev/null && grep -a -q '600' "$d/cmdline" 2>/dev/null; then
    kill -KILL "${d#/proc/}" 2>/dev/null || true
  fi
done
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
mem = "/app/agent/memory"
os.makedirs(mem, exist_ok=True)
os.chmod(mem, 0o2770)
subprocess.check_call(["/bin/sh", "-c", "printf '%s\\n' 'agent notes' > /app/agent/memory/notes.md && chmod 660 /app/agent/memory/notes.md"])
path = "/app/agent/memory/notes.md"
with open(path, "a", encoding="utf-8") as fh:
    fh.write("more\n")
with open(path, encoding="utf-8") as fh:
    if fh.read() != "agent notes\nmore\n":
        raise SystemExit("append")
with open(path, "r+", encoding="utf-8") as fh:
    if fh.read() != "agent notes\nmore\n":
        raise SystemExit("r+")
fd = os.open(path, os.O_RDWR)
os.close(fd)
with open(path, "w", encoding="utf-8") as fh:
    fh.write("rewritten\n")
with open(path, encoding="utf-8") as fh:
    if fh.read() != "rewritten\n":
        raise SystemExit("rewrite")
st = os.stat(path)
if st.st_uid != 999 or (st.st_mode & 0o777) != 0o660 or st.st_size == 0:
    raise SystemExit("notes " + repr((st.st_uid, oct(st.st_mode), st.st_size)))
own = "/app/agent/memory/own.md"
with open(own, "w", encoding="utf-8") as fh:
    fh.write("own")
with open(own, "r+", encoding="utf-8") as fh:
    if fh.read() != "own":
        raise SystemExit("own r+")
print("env-ok")
PY
umask 007
mkdir -p /app/agent/workspace
chmod 2770 /app/agent/workspace
cd /app/agent
git init -b main >/dev/null
git config user.email fixture@example.com
git config user.name Fixture
echo hello > README.md
git add README.md
git commit -m fixture >/dev/null
if ! status=$(/opt/cast/bin/python -c 'from cast.tools.git import git_status; print(git_status.invoke({}))'); then
  printf '%s\n' "$status" >&2
  echo 'git_status failed' >&2
  exit 1
fi
printf '%s\n' "$status" | grep -F 'Current branch: main' || {
  printf '%s\n' "$status" >&2
  echo 'git_status output' >&2
  exit 1
}
printf '%s\n' 'boot file' > /app/agent/workspace/boot.txt
chmod 660 /app/agent/workspace/boot.txt
if ! seen=$(/usr/local/bin/castbot-toolrun -- /bin/cat /app/agent/workspace/boot.txt); then
  echo 'file read failed' >&2
  exit 1
fi
test "$seen" = "boot file" || {
  printf '%s\n' "$seen" >&2
  echo 'file body' >&2
  exit 1
}
mkdir -p /app/.cast
cat > /app/.cast/agent.yaml <<'YAML'
name: Atlas
version: "1"
brain:
  instructions_file: instructions.md
tools:
  builtin: []
  mcp: []
  custom: []
YAML
cp /app/.cast/agent.yaml /app/agent/agent.yaml
printf '%s\n' 'Reply briefly.' > /app/agent/instructions.md
/opt/cast/bin/python -m cast.runtime --agent-path /app/agent --port 8081 --host 127.0.0.1 >/tmp/runtime.log 2>&1 &
echo $! >/tmp/runtime.pid
i=0
while ! /opt/cast/bin/python -c 'import socket;s=socket.create_connection(("127.0.0.1",8081),2);s.close()' >/dev/null 2>&1; do
  if ! kill -0 "$(cat /tmp/runtime.pid)" 2>/dev/null; then
    echo 'runtime exited' >&2
    cat /tmp/runtime.log >&2
    exit 1
  fi
  i=$((i + 1))
  if [ "$i" -gt 150 ]; then
    echo 'runtime did not listen' >&2
    cat /tmp/runtime.log >&2
    exit 1
  fi
  sleep 0.2
done
rtuid=$(awk '/^Uid:/{print $2}' /proc/"$(cat /tmp/runtime.pid)"/status)
test "$rtuid" = "65534"
echo isolation-ok
`
