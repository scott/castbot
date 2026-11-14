#!/usr/bin/env bash
# Exercise scripts/deploy.sh with stubbed ssh, scp, curl, and go.
# No test in this file contacts a real host.
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
DEPLOY="$ROOT/scripts/deploy.sh"
CHECK="$ROOT/scripts/check-deploy-secrets.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
fails=0
CASE=""

if command -v shellcheck >/dev/null 2>&1; then
  shellcheck "$DEPLOY" "$CHECK" "$0"
fi

mkdir -p "$WORK/stubs"

cat >"$WORK/stubs/ssh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${DEPLOY_TEST_STATE}/ssh.log"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      shift 2
      ;;
    -o*)
      shift
      ;;
    -*)
      shift
      ;;
    *)
      break
      ;;
  esac
done
host=${1:-}
shift || true
[[ "$host" == "deploy@203.0.113.10" ]] || {
  printf 'ssh stub: unexpected host\n' >&2
  exit 1
}
if [[ "${1:-}" == "bash" && "${2:-}" == "-s" ]]; then
  script=$(cat)
  if [[ -n "${DEPLOY_TEST_PROC:-}" ]]; then
    proc="${DEPLOY_TEST_PROC%/}/"
    script=${script//\/proc\//${proc}}
  fi
  printf '%s\n' "$script" | bash -s
  exit "${PIPESTATUS[1]}"
fi
if [[ $# -eq 1 ]]; then
  exec bash -c "$1"
fi
printf 'ssh stub: unexpected command\n' >&2
exit 1
EOF

cat >"$WORK/stubs/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${DEPLOY_TEST_STATE}/scp.log"
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      shift 2
      ;;
    -o* | -*)
      shift
      ;;
    *)
      break
      ;;
  esac
done
src=$1
dst=$2
strip() {
  local p=$1
  if [[ "$p" == *:* ]]; then
    local host=${p%%:*}
    [[ "$host" == "deploy@203.0.113.10" ]] || {
      printf 'scp stub: unexpected host\n' >&2
      exit 1
    }
    p=${p#*:}
  fi
  printf '%s' "$p"
}
src=$(strip "$src")
dst=$(strip "$dst")
mkdir -p "$(dirname "$dst")"
cp -a "$src" "$dst"
EOF

cat >"$WORK/stubs/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${DEPLOY_TEST_STATE}/curl.log"
url=""
out=""
i=0
args=("$@")
while [[ $i -lt ${#args[@]} ]]; do
  a=${args[$i]}
  case "$a" in
    -o)
      i=$((i + 1))
      out=${args[$i]}
      ;;
    -H | -w)
      i=$((i + 1))
      ;;
    http://* | https://*)
      url=$a
      ;;
  esac
  i=$((i + 1))
done
body=""
code="200"
if [[ "$url" == *api.github.com*/actions/workflows/ci.yml/runs* ]]; then
  sha=${url#*head_sha=}
  sha=${sha%%&*}
  conclusion=success
  if [[ -f "${DEPLOY_TEST_STATE}/run_conclusion" ]]; then
    conclusion=$(cat "${DEPLOY_TEST_STATE}/run_conclusion")
  fi
  body=$(printf '{"workflow_runs":[{"id":101,"head_sha":"%s","head_branch":"main","event":"push","name":"ci","path":".github/workflows/ci.yml","status":"completed","conclusion":"%s"}]}' "$sha" "$conclusion")
elif [[ "$url" == *api.github.com*/actions/runs/*/jobs* ]]; then
  t=success
  s=success
  if [[ -f "${DEPLOY_TEST_STATE}/job_test" ]]; then
    t=$(cat "${DEPLOY_TEST_STATE}/job_test")
  fi
  if [[ -f "${DEPLOY_TEST_STATE}/job_shots" ]]; then
    s=$(cat "${DEPLOY_TEST_STATE}/job_shots")
  fi
  body=$(printf '{"jobs":[{"name":"test","conclusion":"%s"},{"name":"shots","conclusion":"%s"}]}' "$t" "$s")
elif [[ "$url" == */login ]]; then
  rel=""
  if [[ -f "${DEPLOY_APP_DIR}/RELEASE" ]]; then
    rel=$(tr -d '[:space:]' <"${DEPLOY_APP_DIR}/RELEASE")
  fi
  bad=""
  if [[ -f "${DEPLOY_TEST_STATE}/bad_sha" ]]; then
    bad=$(cat "${DEPLOY_TEST_STATE}/bad_sha")
  fi
  if [[ -n "$bad" && "$rel" == "$bad" ]]; then
    code="500"
  else
    code="200"
  fi
  body=""
else
  printf 'curl stub: unexpected url\n' >&2
  exit 1
fi
if [[ -n "$out" && "$out" != "/dev/null" ]]; then
  printf '%s' "$body" >"$out"
fi
printf '%s' "$code"
EOF

cat >"$WORK/stubs/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${DEPLOY_TEST_STATE}/go.log"
if [[ "${1:-}" == "version" && "${2:-}" == "-m" ]]; then
  file=$3
  [[ -f "$file" ]] || exit 1
  printf '%s: go1.27.4\n' "$file"
  tail -n +2 "$file"
  exit 0
fi
if [[ "${1:-}" == "version" ]]; then
  printf 'go version go1.27.4 linux/amd64\n'
  exit 0
fi
if [[ "${1:-}" == "build" ]]; then
  printf 'CGO=%s GOOS=%s GOARCH=%s\n' "${CGO_ENABLED:-}" "${GOOS:-}" "${GOARCH:-}" >>"${DEPLOY_TEST_STATE}/go.log"
  out=""
  prev=""
  for arg in "$@"; do
    if [[ "$prev" == "-o" ]]; then
      out=$arg
    fi
    prev=$arg
  done
  [[ -n "$out" ]] || exit 1
  sha=$(git rev-parse HEAD)
  {
    printf 'FAKEBINARY\n'
    printf 'build\tvcs.revision=%s\n' "$sha"
    printf 'build\tvcs.modified=false\n'
  } >"$out"
  exit 0
fi
printf 'go stub: unexpected args\n' >&2
exit 1
EOF

cat >"$WORK/stubs/sudo" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-n" ]]; then
  shift
fi
exec "$@"
EOF

cat >"$WORK/stubs/systemctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cmd=${1:-}
shift || true
case "$cmd" in
  restart)
    [[ "${1:-}" == "castbot" ]]
    pid=4242
    mkdir -p "${DEPLOY_TEST_PROC}/${pid}"
    sha=$(tr -d '[:space:]' <"${DEPLOY_APP_DIR}/RELEASE")
    ln -sfn "${DEPLOY_APP_DIR}/releases/${sha}/castbot" "${DEPLOY_TEST_PROC}/${pid}/exe"
    printf '%s\n' "$pid" >"${DEPLOY_TEST_STATE}/pid"
    printf 'active\n' >"${DEPLOY_TEST_STATE}/active"
    ;;
  is-active)
    [[ "${1:-}" == "castbot" ]]
    if [[ -f "${DEPLOY_TEST_STATE}/active" ]]; then
      cat "${DEPLOY_TEST_STATE}/active"
      exit 0
    fi
    printf 'inactive\n'
    exit 3
    ;;
  show)
    prop=""
    while [[ $# -gt 0 ]]; do
      case "$1" in
        -p)
          prop=$2
          shift 2
          ;;
        --value | castbot)
          shift
          ;;
        *)
          shift
          ;;
      esac
    done
    case "$prop" in
      MainPID)
        if [[ -f "${DEPLOY_TEST_STATE}/pid" ]]; then
          cat "${DEPLOY_TEST_STATE}/pid"
        else
          printf '0\n'
        fi
        ;;
      *)
        printf 'systemctl stub: unknown property\n' >&2
        exit 1
        ;;
    esac
    ;;
  *)
    printf 'systemctl stub: %s\n' "$cmd" >&2
    exit 1
    ;;
esac
EOF

cat >"$WORK/stubs/journalctl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
rel=""
if [[ -f "${DEPLOY_APP_DIR}/RELEASE" ]]; then
  rel=$(tr -d '[:space:]' <"${DEPLOY_APP_DIR}/RELEASE")
fi
bad=""
if [[ -f "${DEPLOY_TEST_STATE}/journal_bad_sha" ]]; then
  bad=$(cat "${DEPLOY_TEST_STATE}/journal_bad_sha")
fi
if [[ -n "$bad" && "$rel" == "$bad" ]]; then
  printf 'panic: CASTBOT_SESSION_KEY=journal_secret_MARKER\n'
  exit 0
fi
if [[ -f "${DEPLOY_TEST_STATE}/journal" ]]; then
  cat "${DEPLOY_TEST_STATE}/journal"
  exit 0
fi
printf 'castbot listening\n'
EOF

cat >"$WORK/stubs/file" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'ELF 64-bit LSB executable, x86-64, version 1 (SYSV), statically linked, stripped\n'
EOF

cat >"$WORK/stubs/ldd" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'not a dynamic executable\n' >&2
exit 1
EOF

chmod 0755 "$WORK/stubs/"*

run_case() {
  local name=$1
  shift
  CASE=$name
  printf '== %s\n' "$name"
  local status=0
  set +e
  (
    set -euo pipefail
    "$@"
  )
  status=$?
  set -e
  if [[ "$status" -eq 0 ]]; then
    printf 'ok %s\n' "$name"
    return 0
  fi
  printf 'FAIL %s\n' "$name" >&2
  local dir="$WORK/$name"
  if [[ -d "$dir" ]]; then
    printf '%s\n' '--- stdout ---' >&2
    cat "$dir/stdout" >&2 || true
    printf '%s\n' '--- stderr ---' >&2
    cat "$dir/stderr" >&2 || true
  fi
  fails=$((fails + 1))
}

case_dir() {
  local dir="$WORK/$CASE"
  mkdir -p "$dir/state" "$dir/proc" "$dir/app/data" "$dir/app/bin" "$dir/app/releases"
  printf '%s' "$dir"
}

init_repo() {
  local repo=$1
  git init -b main "$repo" >/dev/null
  git -C "$repo" config user.email "castbot-dev@users.noreply.github.com"
  git -C "$repo" config user.name "castbot-dev"
  printf 'module example.com/castbot\n\ngo 1.26.0\n\ntoolchain go1.27.4\n' >"$repo/go.mod"
  printf 'FROM scratch\n# runtime one\n' >"$repo/Dockerfile"
  mkdir -p "$repo/guest" "$repo/internal/castlet" "$repo/cmd/castlet" "$repo/cmd/castbot"
  printf 'guest\n' >"$repo/guest/README"
  printf 'package castlet\n' >"$repo/internal/castlet/doc.go"
  printf 'package main\nfunc main() {}\n' >"$repo/cmd/castlet/main.go"
  printf 'package main\nfunc main() {}\n' >"$repo/cmd/castbot/main.go"
  cat >"$repo/castbot.env.example" <<'EOF'
CASTBOT_SESSION_KEY=
CASTBOT_SECRETS_KEY=
CASTBOT_ADDR=127.0.0.1:8080
# CASTBOT_IMAGE=example.invalid/castbot-runtime:s04
EOF
  printf 'deploy.env\n.deploy/\n' >"$repo/.gitignore"
  git -C "$repo" add .
  git -C "$repo" commit -m "base" >/dev/null
  git -C "$repo" remote add origin https://github.com/example/castbot.git
  git -C "$repo" update-ref refs/remotes/origin/main HEAD
}

write_db() {
  local db=$1
  local queued=${2:-0}
  rm -f "$db"
  python3 - "$db" "$queued" <<'PY'
import sqlite3, sys
db, queued = sys.argv[1], sys.argv[2] == "1"
con = sqlite3.connect(db)
con.execute("CREATE TABLE runs (id TEXT PRIMARY KEY, status TEXT NOT NULL, user_id TEXT NOT NULL, token TEXT NOT NULL)")
con.execute("INSERT INTO runs VALUES ('r1','done','user-1','tok_FAKE_SHOULD_NOT_APPEAR')")
if queued:
    con.execute("INSERT INTO runs VALUES ('r2','queued','user-1','tok_FAKE_SHOULD_NOT_APPEAR')")
con.execute("PRAGMA user_version=4")
con.commit()
PY
}

plant_binary() {
  local path=$1
  local sha=$2
  mkdir -p "$(dirname "$path")"
  {
    printf 'FAKEBINARY\n'
    printf 'build\tvcs.revision=%s\n' "$sha"
    printf 'build\tvcs.modified=false\n'
  } >"$path"
  chmod 0755 "$path"
}

prepare() {
  local dir repo app
  dir=$(case_dir)
  repo="$dir/repo"
  app="$dir/app"
  init_repo "$repo"
  local sha
  sha=$(git -C "$repo" rev-parse HEAD)
  write_db "$app/data/castbot.db" 0
  mkdir -p "$(dirname "$dir/env")"
  cat >"$dir/server.env" <<EOF
CASTBOT_DO_TOKEN=dop_v1_FAKE_SHOULD_NOT_APPEAR
CASTBOT_IMAGE=example.invalid/castbot-runtime:${sha}
CASTBOT_SECRETS_KEY=sec_FAKE_SHOULD_NOT_APPEAR
CASTBOT_SESSION_KEY=ses_FAKE_SHOULD_NOT_APPEAR
CASTBOT_PROVIDER_KEY_OPENAI=pk_FAKE_SHOULD_NOT_APPEAR
EOF
  chmod 0600 "$dir/server.env"
  printf '203.0.113.10 ssh-ed25519 AAAA-test-only\n' >"$dir/known_hosts"
  cat >"$repo/deploy.env" <<'EOF'
DEPLOY_HOST=evil.example
DEPLOY_USER=deploy
DEPLOY_PUBLIC_URL=https://castbot.example.com
EOF
  chmod 0600 "$repo/deploy.env"
  printf '%s\n' "$dir" "$repo" "$app" "$sha"
}

run_deploy() {
  local dir=$1
  shift
  local repo="$dir/repo"
  local app="$dir/app"
  (
    cd "$repo"
    env \
      PATH="$WORK/stubs:${PATH}" \
      DEPLOY_HOST=203.0.113.10 \
      DEPLOY_USER=deploy \
      DEPLOY_APP_DIR="$app" \
      DEPLOY_ENV_FILE="$dir/server.env" \
      DEPLOY_PUBLIC_URL=https://castbot.example.com \
      DEPLOY_KNOWN_HOSTS="$dir/known_hosts" \
      DEPLOY_LOG="$dir/deploy.log" \
      DEPLOY_TEST_STATE="$dir/state" \
      DEPLOY_TEST_PROC="$dir/proc" \
      GITHUB_TOKEN=ghp_FAKE_SHOULD_NOT_APPEAR \
      bash "$DEPLOY" "$@"
  ) >"$dir/stdout" 2>"$dir/stderr"
}

assert_no_secrets() {
  local dir=$1
  local blob
  blob=$(cat "$dir/stdout" "$dir/stderr" "$dir/pty.out" "$dir/deploy.log" "$dir/state/curl.log" "$dir/state/ssh.log" 2>/dev/null || true)
  local marker
  for marker in \
    dop_v1_FAKE_SHOULD_NOT_APPEAR \
    sec_FAKE_SHOULD_NOT_APPEAR \
    ses_FAKE_SHOULD_NOT_APPEAR \
    pk_FAKE_SHOULD_NOT_APPEAR \
    ghp_FAKE_SHOULD_NOT_APPEAR \
    tok_FAKE_SHOULD_NOT_APPEAR \
    journal_secret_MARKER; do
    if printf '%s\n' "$blob" | grep -F "$marker" >/dev/null; then
      printf 'leaked %s\n' "$marker" >&2
      return 1
    fi
  done
}

assert_ssh_strict() {
  local dir=$1
  grep -q 'StrictHostKeyChecking=yes' "$dir/state/ssh.log"
  if grep -E '(^|[[:space:]])-i[[:space:]]|IdentityFile' "$dir/state/ssh.log" >/dev/null; then
    printf 'ssh used a key path\n' >&2
    return 1
  fi
}

happy() {
  local info dir repo app sha
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  repo=$(printf '%s\n' "$info" | sed -n '2p')
  app=$(printf '%s\n' "$info" | sed -n '3p')
  sha=$(printf '%s\n' "$info" | sed -n '4p')
  printf 'castbot listening\nCASTBOT_SESSION_KEY=ses_FAKE_SHOULD_NOT_APPEAR\n' >"$dir/state/journal"
  run_deploy "$dir"
  [[ "$(tr -d '[:space:]' <"$app/RELEASE")" == "$sha" ]]
  [[ "$(readlink -f "$app/bin/castbot")" == "$app/releases/$sha/castbot" ]]
  [[ "$(stat -c '%a' "$app/releases/$sha/castbot")" == "755" ]]
  local backup record
  backup=$(find "$app/backups" -mindepth 1 -maxdepth 1 -type d -print -quit)
  [[ -f "$backup/castbot.db" && -f "$backup/RECORD" ]]
  record=$(cat "$backup/RECORD")
  printf '%s\n' "$record" | grep -q '^sha256 [0-9a-f]\{64\}$'
  printf '%s\n' "$record" | grep -q '^user_version 4$'
  printf '%s\n' "$record" | grep -q '^integrity ok$'
  grep -q "sha=${sha} backup=" "$dir/deploy.log"
  grep -q 'result=ok$' "$dir/deploy.log"
  grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2} M[SD]T ' "$dir/deploy.log"
  grep -q 'journal matches: 0' "$dir/stdout"
  grep -q 'flock -n' "$dir/state/ssh.log"
  grep -q -- '-trimpath' "$dir/state/go.log"
  grep -q -- '-ldflags=-s -w' "$dir/state/go.log"
  grep -q 'CGO=0 GOOS=linux GOARCH=amd64' "$dir/state/go.log"
  grep -q '\./cmd/castbot' "$dir/state/go.log"
  assert_ssh_strict "$dir"
  assert_no_secrets "$dir"
  flock -n "$app/run/deploy.lock" true
  [[ "$repo" == "$dir/repo" ]]
}

dry_run() {
  local info dir app
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  app=$(printf '%s\n' "$info" | sed -n '3p')
  run_deploy "$dir" --dry-run
  grep -q 'no backup, upload, or restart' "$dir/stdout"
  [[ ! -d "$app/backups" ]]
  [[ ! -e "$app/RELEASE" ]]
  if [[ -f "$dir/state/ssh.log" ]] && grep -q 'flock -n' "$dir/state/ssh.log"; then
    printf 'dry-run took the lock\n' >&2
    return 1
  fi
  grep -q 'result=dry-run$' "$dir/deploy.log"
  assert_no_secrets "$dir"
}

ci_red() {
  local info dir app sha
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  app=$(printf '%s\n' "$info" | sed -n '3p')
  sha=$(printf '%s\n' "$info" | sed -n '4p')
  printf 'success\n' >"$dir/state/run_conclusion"
  printf 'success\n' >"$dir/state/job_test"
  printf 'failure\n' >"$dir/state/job_shots"
  if run_deploy "$dir"; then
    printf 'ci red did not abort\n' >&2
    return 1
  fi
  grep -q 'shots=failure' "$dir/stderr"
  [[ ! -d "$app/releases/$sha" ]]
  [[ ! -d "$app/backups" ]]
  assert_no_secrets "$dir"
}

dirty_tree() {
  local info dir
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  printf 'nope\n' >"$dir/repo/DIRTY"
  if run_deploy "$dir"; then
    printf 'dirty tree did not abort\n' >&2
    return 1
  fi
  grep -q 'working tree is dirty' "$dir/stderr"
  if [[ -f "$dir/state/ssh.log" && -s "$dir/state/ssh.log" ]]; then
    printf 'dirty tree talked to ssh\n' >&2
    return 1
  fi
}

image_change() {
  local info dir repo app sha
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  repo=$(printf '%s\n' "$info" | sed -n '2p')
  app=$(printf '%s\n' "$info" | sed -n '3p')
  sha=$(printf '%s\n' "$info" | sed -n '4p')
  printf 'FROM scratch\n# runtime two\n' >"$repo/Dockerfile"
  git -C "$repo" add Dockerfile
  git -C "$repo" commit -m "image" >/dev/null
  git -C "$repo" update-ref refs/remotes/origin/main HEAD
  local new
  new=$(git -C "$repo" rev-parse HEAD)
  if run_deploy "$dir"; then
    printf 'image change did not abort\n' >&2
    return 1
  fi
  grep -q 'refusing to deploy ahead of the runtime image' "$dir/stderr"
  [[ ! -d "$app/releases/$new" ]]
  [[ "$sha" != "$new" ]]
  assert_no_secrets "$dir"
}

env_gain() {
  local info dir repo
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  repo=$(printf '%s\n' "$info" | sed -n '2p')
  printf 'CASTBOT_NEW_SETTING=\n' >>"$repo/castbot.env.example"
  git -C "$repo" add castbot.env.example
  git -C "$repo" commit -m "env" >/dev/null
  git -C "$repo" update-ref refs/remotes/origin/main HEAD
  if run_deploy "$dir"; then
    printf 'env gain did not abort\n' >&2
    return 1
  fi
  grep -q 'CASTBOT_NEW_SETTING' "$dir/stderr"
  grep -q 'new settings' "$dir/stderr"
  assert_no_secrets "$dir"
}

inflight() {
  local info dir app
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  app=$(printf '%s\n' "$info" | sed -n '3p')
  write_db "$app/data/castbot.db" 1
  if run_deploy "$dir"; then
    printf 'in-flight runs did not abort\n' >&2
    return 1
  fi
  grep -q 'abort: 1 runs are queued, streaming, or waiting for approval' "$dir/stderr"
  [[ ! -d "$app/backups" ]]
  assert_no_secrets "$dir"
}

rollback() {
  local info dir app sha old
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  app=$(printf '%s\n' "$info" | sed -n '3p')
  sha=$(printf '%s\n' "$info" | sed -n '4p')
  old=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  plant_binary "$app/releases/$old/castbot" "$old"
  printf '%s\n' "$old" >"$app/RELEASE"
  ln -sfn "$app/releases/$old/castbot" "$app/bin/castbot"
  printf '%s\n' "$sha" >"$dir/state/journal_bad_sha"
  if run_deploy "$dir"; then
    printf 'verify failure did not abort\n' >&2
    return 1
  fi
  [[ "$(tr -d '[:space:]' <"$app/RELEASE")" == "$old" ]]
  [[ "$(readlink -f "$app/bin/castbot")" == "$app/releases/$old/castbot" ]]
  [[ -n "$(find "$app/backups" -mindepth 1 -maxdepth 1 -type d)" ]]
  grep -q 'result=rolled-back$' "$dir/deploy.log"
  grep -q 'journal matches: 1' "$dir/stderr"
  grep -q 'rolled back to' "$dir/stderr"
  assert_no_secrets "$dir"
  flock -n "$app/run/deploy.lock" true
}

skip_noninteractive_refused() {
  local info dir
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  printf 'failure\n' >"$dir/state/job_shots"
  if run_deploy "$dir" --skip-ci-check </dev/null; then
    printf 'skip without confirmation did not abort\n' >&2
    return 1
  fi
  grep -q 'requires --i-understand-ci-is-not-green' "$dir/stderr"
}

skip_flag() {
  local info dir
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  printf 'failure\n' >"$dir/state/job_shots"
  run_deploy "$dir" --skip-ci-check --i-understand-ci-is-not-green --dry-run </dev/null
  grep -q 'no backup, upload, or restart' "$dir/stdout"
  grep -q 'CI check skipped' "$dir/stdout"
  if [[ -f "$dir/state/curl.log" ]] && grep -q 'api.github.com' "$dir/state/curl.log"; then
    printf 'skip still called the CI API\n' >&2
    return 1
  fi
}

pty_skip() {
  local info dir repo short typed expect
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  repo=$(printf '%s\n' "$info" | sed -n '2p')
  short=$(git -C "$repo" rev-parse --short HEAD)
  typed=${1:-$short}
  expect=${2:-0}
  printf 'failure\n' >"$dir/state/job_shots"
  python3 - "$dir" "$repo" "$typed" "$expect" "$WORK" "$DEPLOY" <<'PY'
import os, pty, select, subprocess, sys, time
dir_path, repo, typed, expect, work, deploy = sys.argv[1:]
expect = int(expect)
env = os.environ.copy()
env.update({
    "PATH": work + "/stubs:" + env.get("PATH", ""),
    "DEPLOY_HOST": "203.0.113.10",
    "DEPLOY_USER": "deploy",
    "DEPLOY_APP_DIR": dir_path + "/app",
    "DEPLOY_ENV_FILE": dir_path + "/server.env",
    "DEPLOY_PUBLIC_URL": "https://castbot.example.com",
    "DEPLOY_KNOWN_HOSTS": dir_path + "/known_hosts",
    "DEPLOY_LOG": dir_path + "/deploy.log",
    "DEPLOY_TEST_STATE": dir_path + "/state",
    "DEPLOY_TEST_PROC": dir_path + "/proc",
    "GITHUB_TOKEN": "ghp_FAKE_SHOULD_NOT_APPEAR",
})
master, slave = pty.openpty()
proc = subprocess.Popen(
    ["bash", deploy, "--skip-ci-check", "--dry-run"],
    stdin=slave, stdout=slave, stderr=slave, cwd=repo, env=env,
)
os.close(slave)
buf = b""
sent = False
deadline = time.time() + 20
while time.time() < deadline:
    ready, _, _ = select.select([master], [], [], 0.2)
    if master in ready:
        try:
            chunk = os.read(master, 4096)
        except OSError:
            break
        if not chunk:
            break
        buf += chunk
        if (not sent) and b"Type the short sha" in buf:
            os.write(master, (typed + "\n").encode())
            sent = True
    if proc.poll() is not None:
        while True:
            ready, _, _ = select.select([master], [], [], 0.1)
            if not ready:
                break
            try:
                chunk = os.read(master, 4096)
            except OSError:
                break
            if not chunk:
                break
            buf += chunk
        break
else:
    proc.kill()
    proc.wait()
    sys.stderr.write("pty timeout\n")
    sys.exit(1)
os.close(master)
code = proc.wait()
open(dir_path + "/pty.out", "wb").write(buf)
if not sent:
    sys.stderr.write("prompt missing\n")
    sys.exit(1)
if expect == 0 and code != 0:
    sys.stderr.write(buf.decode("utf-8", "replace"))
    sys.exit(1)
if expect != 0 and code == 0:
    sys.stderr.write("wrong sha was accepted\n")
    sys.exit(1)
PY
  if [[ "$expect" == "0" ]]; then
    grep -q 'no backup, upload, or restart' "$dir/pty.out"
    assert_no_secrets "$dir"
  else
    grep -q 'short sha did not match' "$dir/pty.out"
  fi
}

skip_pty_ok() {
  pty_skip "${1:-}" 0
}

skip_pty_wrong() {
  pty_skip "not-the-sha" 1
}

prune() {
  local info dir app sha old ancient
  info=$(prepare)
  dir=$(printf '%s\n' "$info" | sed -n '1p')
  app=$(printf '%s\n' "$info" | sed -n '3p')
  sha=$(printf '%s\n' "$info" | sed -n '4p')
  old=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  ancient=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  plant_binary "$app/releases/$old/castbot" "$old"
  plant_binary "$app/releases/$ancient/castbot" "$ancient"
  touch -d '2020-01-01 00:00:00' "$app/releases/$old"
  touch -d '2024-01-01 00:00:00' "$app/releases/$ancient"
  printf '%s\n' "$old" >"$app/RELEASE"
  ln -sfn "$app/releases/$old/castbot" "$app/bin/castbot"
  mkdir -p "$app/backups/manual"
  printf 'keep\n' >"$app/backups/manual/keep.txt"
  DEPLOY_KEEP_RELEASES=1 run_deploy "$dir"
  [[ -d "$app/releases/$sha" ]]
  [[ -d "$app/releases/$old" ]]
  [[ ! -d "$app/releases/$ancient" ]]
  [[ -f "$app/backups/manual/keep.txt" ]]
  [[ -n "$(find "$app/backups" -mindepth 1 -maxdepth 1 -type d ! -name manual)" ]]
  assert_no_secrets "$dir"
}

source_guards() {
  grep -q "grep -E '\\^CASTBOT_IMAGE='" "$DEPLOY"
  if grep -nE 'docker[[:space:]]+(build|push)|IdentityFile' "$DEPLOY" >/dev/null; then
    printf 'script builds an image or pins a key\n' >&2
    return 1
  fi
  if grep -nE 'cat[[:space:]].*(castbot\.env|DEPLOY_ENV_FILE)' "$DEPLOY" >/dev/null; then
    printf 'script cats the env file\n' >&2
    return 1
  fi
  local selects
  selects=$(grep -n 'SELECT' "$DEPLOY" || true)
  printf '%s\n' "$selects" | grep -q 'SELECT COUNT(\*) FROM runs WHERE status IN'
  if printf '%s\n' "$selects" | grep -v 'SELECT COUNT(\*) FROM runs WHERE status IN' | grep -q 'SELECT'; then
    printf 'unexpected SQL\n' >&2
    return 1
  fi
  bash "$CHECK"
}

check_rejects_private_files() {
  local repo="$WORK/secret-check"
  rm -rf "$repo"
  git init -b main "$repo" >/dev/null
  git -C "$repo" config user.email "castbot-dev@users.noreply.github.com"
  git -C "$repo" config user.name "castbot-dev"
  printf 'ok\n' >"$repo/README"
  git -C "$repo" add README
  git -C "$repo" commit -m "ok" >/dev/null
  (cd "$repo" && bash "$CHECK")
  printf '%s\n' 'DEPLOY_HOST=203.0.113.10' >"$repo/deploy.env"
  git -C "$repo" add deploy.env
  if (cd "$repo" && bash "$CHECK" >/dev/null 2>&1); then
    printf 'tracked deploy.env was accepted\n' >&2
    return 1
  fi
  git -C "$repo" rm -f --cached deploy.env >/dev/null
  rm -f "$repo/deploy.env"
  mkdir -p "$repo/keys"
  printf 'not-a-real-key\n' >"$repo/keys/id_ed25519"
  git -C "$repo" add keys/id_ed25519
  if (cd "$repo" && bash "$CHECK" >/dev/null 2>&1); then
    printf 'tracked private key file was accepted\n' >&2
    return 1
  fi
  git -C "$repo" rm -rf --cached keys >/dev/null
  rm -rf "$repo/keys"
  printf '%s\n' '-----BEGIN OPENSSH PRIVATE KEY-----' >"$repo/oops.txt"
  git -C "$repo" add oops.txt
  if (cd "$repo" && bash "$CHECK" >/dev/null 2>&1); then
    printf 'tracked private key block was accepted\n' >&2
    return 1
  fi
}

run_case happy happy
run_case dry-run dry_run
run_case ci-red ci_red
run_case dirty-tree dirty_tree
run_case image-change image_change
run_case env-gain env_gain
run_case in-flight inflight
run_case rollback rollback
run_case skip-refused skip_noninteractive_refused
run_case skip-flag skip_flag
run_case skip-pty skip_pty_ok
run_case skip-pty-wrong skip_pty_wrong
run_case prune prune
run_case source-guards source_guards
run_case secret-files check_rejects_private_files

if [[ "$fails" != "0" ]]; then
  printf '%s failed\n' "$fails" >&2
  exit 1
fi
printf 'all deploy tests passed\n'
