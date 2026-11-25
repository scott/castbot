#!/usr/bin/env bash
# Exercise castbot update apply against a local release fixture.
# No test in this file contacts a real host.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
fail() {
  printf 'FAIL %s\n' "$*" >&2
  exit 1
}

command -v python3 >/dev/null 2>&1 || fail "python3 is required"
command -v go >/dev/null 2>&1 || fail "go is required"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

old=$(python3 - <<'PY'
print("ab" * 20)
PY
)
new=$(python3 - <<'PY'
print("cd" * 20)
PY
)
image=$(python3 - <<'PY'
print("ef" * 20)
PY
)
token='fake-token-9f3a'
version='0.20261124.2'

(
  cd "$ROOT"
  CASTBOT_APPLY_FIXTURE=1 \
  CASTBOT_APPLY_FIXTURE_DIR="$work/release" \
  CASTBOT_APPLY_FIXTURE_PUB="$work/minisign.pub" \
  CASTBOT_APPLY_FIXTURE_VERSION="$version" \
  CASTBOT_APPLY_FIXTURE_COMMIT="$new" \
  CASTBOT_APPLY_FIXTURE_IMAGE_COMMIT="$image" \
  go test -count=1 -timeout 180s ./internal/update
) >/dev/null || fail "fixture"

[[ -f "$work/minisign.pub" && -f "$work/release/SHA256SUMS.minisig" ]] || fail "fixture files"

cat >"$work/srv.py" <<'PY'
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

fixture = sys.argv[1]
portfile = sys.argv[2]


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        query = "" if "?" not in self.path else self.path.split("?", 1)[1]
        if query and path != "/token":
            self.send_error(400)
            return
        if path.startswith("/download/"):
            name = path[len("/download/"):]
            if "/" in name or name in ("", ".", ".."):
                self.send_error(404)
                return
            target = os.path.join(fixture, name)
            if not os.path.isfile(target):
                self.send_error(404)
                return
            with open(target, "rb") as fh:
                body = fh.read()
            self._send(200, body, "application/octet-stream")
            return
        if path.startswith("/compare/"):
            self._send(200, b'{"status":"ahead"}', "application/json")
            return
        if path == "/token":
            self._send(200, b'{"token":"t"}', "application/json")
            return
        if path.startswith("/v2/scott/castbot-runtime/manifests/"):
            self._send(200, b"{}", "application/json")
            return
        if path == "/login":
            code = 200
            health = os.environ.get("CASTBOT_APPLY_HEALTH_FILE", "")
            if health and os.path.exists(health):
                text = open(health, "r", encoding="utf-8").read().strip()
                if text:
                    code = int(text)
            body = b"ok" if code == 200 else b"down"
            self._send(code, body, "text/plain")
            return
        self.send_error(404)

    def _send(self, code, body, ctype):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(portfile, "w", encoding="utf-8") as fh:
    fh.write(str(server.server_address[1]))
server.serve_forever()
PY

portfile="$work/port"
CASTBOT_APPLY_HEALTH_FILE="$work/health" python3 "$work/srv.py" "$work/release" "$portfile" &
srv_pid=$!
cleanup() {
  kill "$srv_pid" 2>/dev/null || true
  wait "$srv_pid" 2>/dev/null || true
}
trap 'cleanup; rm -rf "$work"' EXIT

for _ in 1 2 3 4 5 6 7 8 9 10; do
  [[ -s "$portfile" ]] && break
  sleep 0.1
done
[[ -s "$portfile" ]] || fail "server did not start"
port=$(cat "$portfile")

owner=$(id -un)
group=$(id -gn)
if [[ "$(id -u)" -eq 0 ]] && id castbot >/dev/null 2>&1; then
  owner=castbot
  group=castbot
fi

cat >"$work/systemctl" <<'EOF'
#!/bin/sh
set -eu
root=${CASTBOT_APPLY_ROOT:?}
if [ "${1:-}" != "restart" ] || [ "${2:-}" != "castbot" ]; then
  exit 1
fi
n=0
if [ -f "$root/run/restarts" ]; then
  n=$(cat "$root/run/restarts")
fi
n=$((n + 1))
printf '%s\n' "$n" > "$root/run/restarts"
old=$(cat "$root/run/oldsha")
new=$(cat "$root/run/newsha")
if [ -f "$root/run/fail-health" ] && [ "$n" -eq 1 ]; then
  printf '500\n' > "$root/run/health-code"
  printf '%s\n' "$root/opt/castbot/releases/$new/castbot" > "$root/run/exe"
  exit 0
fi
printf '200\n' > "$root/run/health-code"
if [ -f "$root/run/fail-health" ]; then
  printf '%s\n' "$root/opt/castbot/releases/$old/castbot" > "$root/run/exe"
else
  printf '%s\n' "$root/opt/castbot/releases/$new/castbot" > "$root/run/exe"
fi
exit 0
EOF
chmod 0755 "$work/systemctl"

prepare_root() {
  local root=$1
  mkdir -p "$root/opt/castbot/releases/$old" "$root/opt/castbot/bin" "$root/opt/castbot/data" \
    "$root/etc/castbot" "$root/run" "$root/usr/bin"
  printf 'old\n' > "$root/opt/castbot/releases/$old/castbot"
  chmod 0755 "$root/opt/castbot/releases/$old/castbot"
  ln -s "$root/opt/castbot/releases/$old/castbot" "$root/opt/castbot/bin/castbot"
  printf '%s\n' "$old" > "$root/opt/castbot/RELEASE"
  printf '%s\n' '0.20261124.1' > "$root/opt/castbot/VERSION"
  printf '%s\n' '0.20261124.1' > "$root/opt/castbot/releases/$old/VERSION"
  printf '%s\n' "$old" > "$root/run/oldsha"
  printf '%s\n' "$new" > "$root/run/newsha"
  ln -s "$work/systemctl" "$root/usr/bin/systemctl"
}

write_env() {
  local root=$1 image=$2
  local env="$root/etc/castbot/castbot.env"
  {
    printf 'CASTBOT_DB=%s\n' "$root/opt/castbot/data/castbot.db"
    printf 'CASTBOT_DO_TOKEN=%s\n' "$token"
    if [[ -n "$image" ]]; then
      printf 'CASTBOT_IMAGE=%s\n' "$image"
    fi
  } > "$env"
  chmod 0600 "$env"
  if [[ "$(id -u)" -eq 0 ]]; then
    chown "$owner:$group" "$env"
  fi
}

point_health() {
  local root=$1
  printf '200\n' > "$root/run/health-code"
  ln -sfn "$root/run/health-code" "$work/health"
}

run_apply() {
  local root=$1 wait=$2 now=$3
  local log="$root/apply.log"
  point_health "$root"
  PATH="$root/usr/bin:$PATH" \
  CASTBOT_APPLY_ROOT="$root" \
  CASTBOT_APPLY_RELEASE_BASE="http://127.0.0.1:${port}/download" \
  CASTBOT_APPLY_COMPARE_BASE="http://127.0.0.1:${port}/compare/" \
  CASTBOT_APPLY_REGISTRY="http://127.0.0.1:${port}" \
  CASTBOT_APPLY_PUBKEY="$work/minisign.pub" \
  CASTBOT_APPLY_HEALTH="http://127.0.0.1:${port}/login" \
  CASTBOT_APPLY_HEALTH_WAIT="$wait" \
  CASTBOT_APPLY_NOW="$now" \
  CASTBOT_APPLY_OWNER="$owner" \
  CASTBOT_APPLY_GROUP="$group" \
  "$bin" update apply >"$root/apply.out" 2>"$log"
}

assert_quiet_log() {
  local log=$1
  local out
  out="$(dirname "$log")/apply.out"
  if grep -F -q "$token" "$log" || { [[ -f "$out" ]] && grep -F -q "$token" "$out"; }; then
    fail "log contained the env token"
  fi
  if [[ -s "$out" ]]; then
    fail "apply wrote to stdout"
  fi
}

bin="$work/castbot"
(
  cd "$ROOT"
  CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=0.20261124.1 -X main.commit=${old}" -o "$bin" ./cmd/castbot
) || fail "build"

# Happy path. Six older release dirs, two of which are past the newest five.
happy="$work/happy"
prepare_root "$happy"
write_env "$happy" ""
for i in 1 2 3 4 5 6; do
  sha=$(printf '%040x' "$i")
  mkdir -p "$happy/opt/castbot/releases/$sha"
  printf 'x\n' > "$happy/opt/castbot/releases/$sha/castbot"
  chmod 0755 "$happy/opt/castbot/releases/$sha/castbot"
  touch -d "$i hours ago" "$happy/opt/castbot/releases/$sha"
done
touch -d '10 days ago' "$happy/opt/castbot/releases/$old"
mkdir -p "$happy/opt/castbot/releases/keep-notes"
uid_before=$(stat -c '%u' "$happy/etc/castbot/castbot.env")
set +e
run_apply "$happy" 5s "2026-11-24T18:00:00Z"
happy_status=$?
set -e
if [[ "$happy_status" -ne 0 ]]; then
  sed "s/${token}/***/g" "$happy/apply.log" >&2 || true
  fail "happy status $happy_status"
fi
assert_quiet_log "$happy/apply.log"
grep -E -q 'update apply: backup [0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{4}: installed' "$happy/apply.log" || fail "happy log"
[[ "$(readlink "$happy/opt/castbot/bin/castbot")" == "$happy/opt/castbot/releases/$new/castbot" ]] || fail "happy symlink"
[[ "$(tr -d '[:space:]' < "$happy/opt/castbot/RELEASE")" == "$new" ]] || fail "happy RELEASE"
[[ "$(tr -d '[:space:]' < "$happy/opt/castbot/VERSION")" == "$version" ]] || fail "happy VERSION"
[[ "$(tr -d '[:space:]' < "$happy/opt/castbot/releases/$new/VERSION")" == "$version" ]] || fail "happy release VERSION"
[[ -d "$happy/opt/castbot/releases/$old" ]] || fail "rollback target removed"
[[ -d "$happy/opt/castbot/releases/$(printf '%040x' 1)" ]] || fail "newest extra removed"
[[ -d "$happy/opt/castbot/releases/$(printf '%040x' 4)" ]] || fail "kept extra removed"
[[ ! -d "$happy/opt/castbot/releases/$(printf '%040x' 5)" ]] || fail "old extra kept"
[[ ! -d "$happy/opt/castbot/releases/$(printf '%040x' 6)" ]] || fail "oldest extra kept"
[[ -d "$happy/opt/castbot/releases/keep-notes" ]] || fail "notes removed"
find "$happy/opt/castbot/data/backups" -mindepth 1 -maxdepth 1 -type d | grep -q . || fail "backup missing"
[[ "$(stat -c '%a' "$happy/etc/castbot/castbot.env")" == "600" ]] || fail "env mode"
[[ "$(stat -c '%u' "$happy/etc/castbot/castbot.env")" == "$uid_before" ]] || fail "env owner changed"
if grep -q '^CASTBOT_IMAGE=' "$happy/etc/castbot/castbot.env"; then
  fail "image line appeared"
fi

# Health failure restores the previous binary and the image line.
roll="$work/roll"
prepare_root "$roll"
old_image="ghcr.io/scott/castbot-runtime:${old}"
write_env "$roll" "$old_image"
touch "$roll/run/fail-health"
uid_before=$(stat -c '%u' "$roll/etc/castbot/castbot.env")
set +e
run_apply "$roll" 1s "2026-11-24T18:04:00Z"
roll_status=$?
set -e
if [[ "$roll_status" -eq 0 ]]; then
  sed "s/${token}/***/g" "$roll/apply.log" >&2 || true
  fail "rollback succeeded"
fi
assert_quiet_log "$roll/apply.log"
grep -q 'rolled back' "$roll/apply.log" || fail "rollback log"
[[ "$(readlink "$roll/opt/castbot/bin/castbot")" == "$roll/opt/castbot/releases/$old/castbot" ]] || fail "rollback symlink"
[[ "$(tr -d '[:space:]' < "$roll/opt/castbot/RELEASE")" == "$old" ]] || fail "rollback RELEASE"
[[ "$(tr -d '[:space:]' < "$roll/opt/castbot/VERSION")" == "0.20261124.1" ]] || fail "rollback VERSION"
grep -F -q "CASTBOT_IMAGE=${old_image}" "$roll/etc/castbot/castbot.env" || fail "image not restored"
if grep -F -q "ghcr.io/scott/castbot-runtime:${image}" "$roll/etc/castbot/castbot.env"; then
  fail "new image left in place"
fi
find "$roll/opt/castbot/data/backups" -mindepth 1 -maxdepth 1 -type d | grep -q . || fail "rollback backup missing"
[[ "$(stat -c '%a' "$roll/etc/castbot/castbot.env")" == "600" ]] || fail "rollback env mode"
[[ "$(stat -c '%u' "$roll/etc/castbot/castbot.env")" == "$uid_before" ]] || fail "rollback env owner changed"
[[ "$(tr -d '[:space:]' < "$roll/run/restarts")" == "2" ]] || fail "rollback restarts"

printf 'apply tests passed\n'
