#!/usr/bin/env bash
# Deploy the castbot control plane from a laptop.
# The runtime image is not built, pushed, or changed. The server env file is
# only searched for the CASTBOT_IMAGE line. Secrets are not read or printed.
set -euo pipefail

RESULT=fail
BACKUP_ID="-"
SHA_LOG="-"
LOG_WRITTEN=0
SWITCHED=0
IN_ROLLBACK=0
LOCK_SSH=""
LOCK_OUT=""
WT=""
BUILD_DIR=""
TOKEN_HDR=""
ROOT=""
SHA=""
BIN=""
BIN_SHA=""
PREV=""
IMAGE_VALUE=""
USER_VERSION=""
DRY=0
SKIP_CI=0
UNDERSTAND=0
SHA_ARG=""

usage() {
  cat <<'EOF'
Usage: scripts/deploy.sh [options] [sha]

Build a static castbot binary from a commit on main and install it on the
host named by DEPLOY_HOST. The default commit is origin/main.

Options:
  --dry-run                         Run preflight, the CI gate, the image
                                    gate, and the build, then print the
                                    remote plan. No backup, upload, or restart.
  --skip-ci-check                   Skip the CI gate. A terminal must type
                                    the short sha. A non-interactive run also
                                    needs --i-understand-ci-is-not-green.
  --i-understand-ci-is-not-green    Confirm a non-interactive CI skip.
  -h, --help                        Show this help.

Settings come from the environment, then from deploy.env in the repo root
for any setting that is still unset. The SSH key comes from the agent or
from the local SSH config. This script never takes a key path.
EOF
}

die() {
  printf 'deploy: %s\n' "$*" >&2
  if [[ "$RESULT" != "rolled-back" ]]; then
    RESULT=fail
  fi
  exit 1
}

q() {
  printf '%q' "$1"
}

write_log() {
  if [[ "$LOG_WRITTEN" == "1" || -z "${DEPLOY_LOG:-}" ]]; then
    return 0
  fi
  mkdir -p "$(dirname "$DEPLOY_LOG")"
  local when
  when=$(TZ=America/Denver date '+%Y-%m-%d %H:%M:%S %Z') || return 0
  printf '%s sha=%s backup=%s result=%s\n' "$when" "$SHA_LOG" "$BACKUP_ID" "$RESULT" >>"$DEPLOY_LOG"
  LOG_WRITTEN=1
}

release_lock() {
  if [[ -n "${LOCK_SSH}" ]] && kill -0 "$LOCK_SSH" 2>/dev/null; then
    kill -- "-${LOCK_SSH}" 2>/dev/null || kill "$LOCK_SSH" 2>/dev/null || true
    wait "$LOCK_SSH" 2>/dev/null || true
  fi
  LOCK_SSH=""
  if [[ -n "${LOCK_OUT}" && -f "$LOCK_OUT" ]]; then
    rm -f "$LOCK_OUT"
  fi
  LOCK_OUT=""
}

remove_worktree() {
  if [[ -n "${WT}" && -n "${ROOT}" ]]; then
    git -C "$ROOT" worktree remove --force "$WT" >/dev/null 2>&1 || true
    git -C "$ROOT" worktree prune >/dev/null 2>&1 || true
  fi
  WT=""
  if [[ -n "${BUILD_DIR}" && -d "$BUILD_DIR" ]]; then
    rm -rf "$BUILD_DIR"
  fi
  BUILD_DIR=""
}

on_exit() {
  local status=$?
  release_lock || true
  remove_worktree || true
  if [[ -n "${TOKEN_HDR}" && -f "$TOKEN_HDR" ]]; then
    rm -f "$TOKEN_HDR"
  fi
  TOKEN_HDR=""
  write_log || true
  exit "$status"
}
trap on_exit EXIT

need() {
  local cmd
  for cmd in "$@"; do
    command -v "$cmd" >/dev/null 2>&1 || die "required command is missing: $cmd"
  done
}

load_deploy_env() {
  local file="$ROOT/deploy.env"
  [[ -f "$file" ]] || return 0
  local mode
  mode=$(stat -c '%a' "$file")
  [[ "$mode" == "600" ]] || die "deploy.env must be mode 0600"
  local line key val
  while IFS= read -r line || [[ -n "$line" ]]; do
    line=${line%$'\r'}
    [[ "$line" =~ ^[[:space:]]*# ]] && continue
    [[ "$line" =~ ^[[:space:]]*$ ]] && continue
    [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || die "deploy.env has a line without a setting"
    key=${BASH_REMATCH[1]}
    val=${BASH_REMATCH[2]}
    if [[ "$val" == \"*\" && "$val" == *\" ]]; then
      val=${val:1:${#val}-2}
    elif [[ "$val" == \'*\' && "$val" == *\' ]]; then
      val=${val:1:${#val}-2}
    fi
    case "$key" in
      DEPLOY_HOST|DEPLOY_USER|DEPLOY_APP_DIR|DEPLOY_ENV_FILE|DEPLOY_PUBLIC_URL|DEPLOY_KEEP_RELEASES|DEPLOY_KNOWN_HOSTS|DEPLOY_LOG)
        if [[ -z "${!key:-}" ]]; then
          printf -v "$key" '%s' "$val"
          export "${key?}"
        fi
        ;;
      *)
        die "deploy.env has an unknown setting"
        ;;
    esac
  done <"$file"
}

apply_defaults() {
  : "${DEPLOY_APP_DIR:=/opt/castbot}"
  : "${DEPLOY_ENV_FILE:=/etc/castbot/castbot.env}"
  : "${DEPLOY_KEEP_RELEASES:=5}"
  : "${DEPLOY_KNOWN_HOSTS:=$ROOT/.deploy/known_hosts}"
  : "${DEPLOY_LOG:=$ROOT/.deploy/deploy.log}"
  export DEPLOY_APP_DIR DEPLOY_ENV_FILE DEPLOY_KEEP_RELEASES DEPLOY_KNOWN_HOSTS DEPLOY_LOG
}

validate_config() {
  [[ -n "${DEPLOY_HOST:-}" ]] || die "DEPLOY_HOST is required"
  [[ -n "${DEPLOY_USER:-}" ]] || die "DEPLOY_USER is required"
  [[ -n "${DEPLOY_PUBLIC_URL:-}" ]] || die "DEPLOY_PUBLIC_URL is required"
  [[ "$DEPLOY_HOST" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || die "DEPLOY_HOST is not a host name"
  [[ "$DEPLOY_USER" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "DEPLOY_USER is not a user name"
  [[ "$DEPLOY_APP_DIR" =~ ^/([A-Za-z0-9._-]+/)*[A-Za-z0-9._-]+$ ]] || die "DEPLOY_APP_DIR is not an absolute path"
  [[ "$DEPLOY_ENV_FILE" =~ ^/([A-Za-z0-9._-]+/)*[A-Za-z0-9._-]+$ ]] || die "DEPLOY_ENV_FILE is not an absolute path"
  [[ "$DEPLOY_PUBLIC_URL" =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?(/[A-Za-z0-9._~:/@%+-]*)?$ ]] || die "DEPLOY_PUBLIC_URL must be an https URL"
  [[ "$DEPLOY_KEEP_RELEASES" =~ ^[1-9][0-9]*$ ]] || die "DEPLOY_KEEP_RELEASES must be a positive integer"
  [[ -s "$DEPLOY_KNOWN_HOSTS" ]] || die "known hosts file is missing or empty"
  [[ "$DEPLOY_KNOWN_HOSTS" != *$'\n'* ]] || die "known hosts path is invalid"
  [[ "$DEPLOY_LOG" != *$'\n'* ]] || die "deploy log path is invalid"
}

SSH_OPTS=()
setup_ssh() {
  SSH_OPTS=(
    -o BatchMode=yes
    -o StrictHostKeyChecking=yes
    -o "UserKnownHostsFile=${DEPLOY_KNOWN_HOSTS}"
    -o ConnectTimeout=15
  )
}

remote_bash() {
  local script=$1
  printf 'set -euo pipefail\n%s\n' "$script" | ssh "${SSH_OPTS[@]}" "${DEPLOY_USER}@${DEPLOY_HOST}" bash -s
}

start_lock() {
  local cmd
  cmd="mkdir -p $(q "$DEPLOY_APP_DIR/run") && flock -n $(q "$DEPLOY_APP_DIR/run/deploy.lock") sh -c 'echo LOCKED; while true; do sleep 3600; done'"
  LOCK_OUT=$(mktemp)
  set -m
  # cmd is already escaped for the remote shell. -n keeps this
  # session from reading the operator's stdin.
  # shellcheck disable=SC2029
  ssh -n "${SSH_OPTS[@]}" "${DEPLOY_USER}@${DEPLOY_HOST}" "$cmd" >"$LOCK_OUT" &
  LOCK_SSH=$!
  set +m
  local tries=0
  while [[ "$tries" -lt 50 ]]; do
    tries=$((tries + 1))
    if grep -q LOCKED "$LOCK_OUT"; then
      return 0
    fi
    if ! kill -0 "$LOCK_SSH" 2>/dev/null; then
      die "could not take the deploy lock"
    fi
    sleep 0.1
  done
  die "could not take the deploy lock"
}

db_path() {
  printf '%s' "$DEPLOY_APP_DIR/data/castbot.db"
}

inflight_count() {
  local db count
  db=$(db_path)
  count=$(remote_bash "sqlite3 -readonly $(q "$db") \"SELECT COUNT(*) FROM runs WHERE status IN ('queued','streaming','waiting_approval');\"") || die "in-flight query failed"
  count=${count//[[:space:]]/}
  [[ "$count" =~ ^[0-9]+$ ]] || die "in-flight query failed"
  printf '%s' "$count"
}

abort_if_inflight() {
  local count
  count=$(inflight_count)
  if [[ "$count" != "0" ]]; then
    die "abort: ${count} runs are queued, streaming, or waiting for approval"
  fi
}

trim_spaces() {
  local v=$1
  v=${v//$'\r'/}
  v=${v#"${v%%[![:space:]]*}"}
  v=${v%"${v##*[![:space:]]}"}
  printf '%s' "$v"
}

read_image_value() {
  local raw line val n
  raw=$(remote_bash "grep -E '^CASTBOT_IMAGE=' -- $(q "$DEPLOY_ENV_FILE")") || {
    printf 'could not read CASTBOT_IMAGE\n' >&2
    return 1
  }
  n=$(printf '%s\n' "$raw" | sed '/^$/d' | wc -l | tr -d ' ')
  [[ "$n" == "1" ]] || {
    printf 'CASTBOT_IMAGE must be a single line\n' >&2
    return 1
  }
  line=$(printf '%s\n' "$raw" | sed -n '1p')
  [[ "$line" == CASTBOT_IMAGE=* ]] || {
    printf 'CASTBOT_IMAGE was not a single assignment\n' >&2
    return 1
  }
  val=${line#CASTBOT_IMAGE=}
  val=$(trim_spaces "$val")
  if [[ "$val" == \"*\" && "$val" == *\" ]]; then
    val=${val:1:${#val}-2}
  elif [[ "$val" == \'*\' && "$val" == *\' ]]; then
    val=${val:1:${#val}-2}
  fi
  [[ -n "$val" ]] || {
    printf 'CASTBOT_IMAGE is empty\n' >&2
    return 1
  }
  printf '%s' "$val"
}

image_commit() {
  local ref=$1
  ref=${ref%@sha256:*}
  local base tag hex full
  base=${ref##*/}
  if [[ "$base" == *:* ]]; then
    tag=${base##*:}
  else
    die "runtime image tag does not name a commit"
  fi
  hex=$(printf '%s\n' "$tag" | grep -oE '[0-9a-fA-F]{7,40}' | awk 'length > n { n = length; best = $0 } END { print best }' || true)
  [[ -n "$hex" ]] || die "runtime image tag does not name a commit"
  if ! full=$(git -C "$ROOT" rev-parse --verify "${hex}^{commit}" 2>/dev/null); then
    die "runtime image tag does not match a local commit"
  fi
  printf '%s' "$full"
}

env_keys() {
  local sha=$1
  git -C "$ROOT" show "${sha}:castbot.env.example" | awk '
    /^[[:space:]]*#/ { sub(/^[[:space:]]*#[[:space:]]*/, "") }
    /^[A-Za-z_][A-Za-z0-9_]*=/ { split($0, a, "="); print a[1] }
  ' | sort -u
}

image_gate() {
  local value commit names gained
  value=$(read_image_value) || die "could not read CASTBOT_IMAGE"
  IMAGE_VALUE=$value
  commit=$(image_commit "$value")
  names=$(git -C "$ROOT" diff --name-only "$commit" "$SHA" -- Dockerfile guest internal/castlet cmd/castlet || true)
  if [[ -n "$names" ]]; then
    printf 'runtime image inputs differ:\n%s\n' "$names" >&2
    die "refusing to deploy ahead of the runtime image"
  fi
  git -C "$ROOT" cat-file -e "${commit}:castbot.env.example" 2>/dev/null || die "castbot.env.example is missing at the runtime image commit"
  git -C "$ROOT" cat-file -e "${SHA}:castbot.env.example" 2>/dev/null || die "castbot.env.example is missing at the deploy sha"
  gained=$(comm -13 <(env_keys "$commit") <(env_keys "$SHA") || true)
  if [[ -n "$gained" ]]; then
    printf 'castbot.env.example gained:\n%s\n' "$gained" >&2
    die "refusing to deploy before the server env file has the new settings"
  fi
  printf 'runtime image commit %s\n' "$commit"
}

repo_slug() {
  local url slug
  url=$(git -C "$ROOT" remote get-url origin)
  slug=$(python3 -c '
import re, sys
u = re.sub(r"\.git$", "", sys.argv[1])
m = re.search(r"github\.com[:/]([^/]+)/([^/]+)$", u)
if not m:
    sys.exit(1)
print("%s/%s" % (m.group(1), m.group(2)))
' "$url") || die "origin is not a GitHub repository"
  [[ "$slug" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "origin is not a GitHub repository"
  printf '%s' "$slug"
}

github_curl() {
  local url=$1
  local out code
  out=$(mktemp)
  local -a args
  args=(-sS --max-time 30 -o "$out" -w '%{http_code}' -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28')
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    TOKEN_HDR=$(mktemp)
    chmod 0600 "$TOKEN_HDR"
    printf 'Authorization: Bearer %s\n' "$GITHUB_TOKEN" >"$TOKEN_HDR"
    args+=(-H "@${TOKEN_HDR}")
  fi
  if ! code=$(curl "${args[@]}" "$url"); then
    rm -f "$out"
    [[ -n "${TOKEN_HDR}" && -f "$TOKEN_HDR" ]] && rm -f "$TOKEN_HDR"
    TOKEN_HDR=""
    die "GitHub API request failed"
  fi
  if [[ -n "${TOKEN_HDR}" && -f "$TOKEN_HDR" ]]; then
    rm -f "$TOKEN_HDR"
  fi
  TOKEN_HDR=""
  if [[ "$code" != "200" ]]; then
    rm -f "$out"
    die "GitHub API returned HTTP ${code}"
  fi
  printf '%s' "$out"
}

confirm_skip() {
  local short typed
  short=$(git -C "$ROOT" rev-parse --short "$SHA")
  if [[ -t 0 ]]; then
    printf 'CI check skipped. Type the short sha to continue: ' >&2
    IFS= read -r typed || die "short sha was not entered"
    [[ "$typed" == "$short" ]] || die "short sha did not match"
    printf 'CI check skipped by short sha\n'
    return 0
  fi
  [[ "$UNDERSTAND" == "1" ]] || die "non-interactive --skip-ci-check requires --i-understand-ci-is-not-green"
  printf 'CI check skipped by --i-understand-ci-is-not-green\n'
}

ci_gate() {
  if [[ "$SKIP_CI" == "1" ]]; then
    confirm_skip
    return 0
  fi
  local slug runs_url runs_file run_id jobs_url jobs_file
  slug=$(repo_slug)
  runs_url="https://api.github.com/repos/${slug}/actions/workflows/ci.yml/runs?head_sha=${SHA}&branch=main&event=push&per_page=20"
  runs_file=$(github_curl "$runs_url")
  local py_status
  set +e
  run_id=$(python3 - "$SHA" "$runs_file" <<'PY'
import json, sys
sha, path = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as fh:
    data = json.load(fh)
runs = data.get("workflow_runs") or []
if not runs:
    sys.exit(2)
run = runs[0]
path_name = run.get("path") or ""
name = run.get("name") or ""
if run.get("head_sha") != sha or run.get("head_branch") != "main" or run.get("event") != "push":
    sys.exit(3)
if not (path_name.endswith("ci.yml") or name == "ci"):
    sys.exit(4)
if run.get("status") != "completed" or run.get("conclusion") != "success":
    sys.exit(5)
print(run.get("id") or "")
PY
  )
  py_status=$?
  set -e
  rm -f "$runs_file"
  if [[ "$py_status" != "0" ]]; then
    die "ci is not green for this sha"
  fi
  [[ "$run_id" =~ ^[0-9]+$ ]] || die "ci is not green for this sha"
  jobs_url="https://api.github.com/repos/${slug}/actions/runs/${run_id}/jobs?per_page=100"
  jobs_file=$(github_curl "$jobs_url")
  set +e
  python3 - "$jobs_file" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as fh:
    data = json.load(fh)
need = {"test": "", "shots": ""}
for job in data.get("jobs") or []:
    name = job.get("name")
    if name in need:
        need[name] = job.get("conclusion") or ""
if need["test"] == "success" and need["shots"] == "success":
    sys.exit(0)
sys.stderr.write("ci is not green for this sha (test=%s shots=%s)\n" % (need["test"] or "missing", need["shots"] or "missing"))
sys.exit(1)
PY
  py_status=$?
  set -e
  rm -f "$jobs_file"
  [[ "$py_status" == "0" ]] || die "ci is not green for this sha"
  printf 'ci is green for %s\n' "$SHA"
}

preflight_tree() {
  local dirty
  dirty=$(git -C "$ROOT" status --porcelain)
  if [[ -n "$dirty" ]]; then
    printf '%s\n' "$dirty" >&2
    die "working tree is dirty"
  fi
}

resolve_sha() {
  local rev
  if [[ -n "$SHA_ARG" ]]; then
    rev=$SHA_ARG
  else
    rev='origin/main'
  fi
  SHA=$(git -C "$ROOT" rev-parse --verify "${rev}^{commit}") || die "cannot resolve the deploy sha"
  [[ "$SHA" =~ ^[0-9a-f]{40}$ ]] || die "cannot resolve the deploy sha"
  SHA_LOG=$SHA
  local main_sha
  main_sha=$(git -C "$ROOT" rev-parse --verify 'origin/main^{commit}') || die "origin/main is missing"
  git -C "$ROOT" merge-base --is-ancestor "$SHA" "$main_sha" || die "sha is not on main"
}

check_go() {
  local mod_tool got
  mod_tool=$(awk '$1 == "toolchain" { print $2; exit }' "$ROOT/go.mod")
  [[ "$mod_tool" == "go1.27.4" ]] || die "go.mod toolchain is ${mod_tool}, want go1.27.4"
  got=$(go version | awk '{ print $3 }')
  [[ "$got" == "go1.27.4" ]] || die "go is ${got}, want go1.27.4"
}

check_ssh() {
  remote_bash "true" || die "SSH to the deploy host failed"
}

assert_static() {
  local bin=$1
  local info ldd_out
  info=$(file -b "$bin")
  [[ "$info" == *"statically linked"* ]] || die "binary is not static"
  set +e
  ldd_out=$(ldd "$bin" 2>&1)
  set -e
  [[ "$ldd_out" != *"=>"* ]] || die "binary has dynamic libraries"
  [[ "$ldd_out" == *"not a dynamic executable"* || "$ldd_out" == *"statically linked"* ]] || die "binary is not static"
}

build_meta_field() {
  local meta=$1 key=$2 line
  while IFS= read -r line; do
    case "$line" in
      *"vcs.${key}="*)
        printf '%s' "${line#*"vcs.${key}="}"
        return 0
        ;;
    esac
  done <<<"$meta"
  return 1
}

assert_build_meta() {
  local bin=$1 expect=$2 meta rev mod
  meta=$(go version -m "$bin") || die "go version -m failed"
  rev=$(build_meta_field "$meta" revision) || die "binary has no vcs.revision"
  mod=$(build_meta_field "$meta" modified) || die "binary has no vcs.modified"
  [[ "$rev" == "$expect" ]] || die "vcs.revision is not the deploy sha"
  [[ "$mod" == "false" ]] || die "vcs.modified is not false"
}

build_binary() {
  BUILD_DIR=$(mktemp -d)
  WT="${BUILD_DIR}/src"
  git -C "$ROOT" worktree add --detach "$WT" "$SHA" >/dev/null
  BIN="${BUILD_DIR}/castbot"
  (
    cd "$WT"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o "$BIN" ./cmd/castbot
  )
  assert_static "$BIN"
  assert_build_meta "$BIN" "$SHA"
  BIN_SHA=$(sha256sum "$BIN" | awk '{ print $1 }')
  [[ "$BIN_SHA" =~ ^[0-9a-f]{64}$ ]] || die "sha256 of the binary failed"
  printf 'binary sha256 %s\n' "$BIN_SHA"
}

take_backup() {
  local db dest sum uv check
  db=$(db_path)
  BACKUP_ID=$(TZ=America/Denver date '+%Y-%m-%d_%H%M')
  dest="$DEPLOY_APP_DIR/backups/$BACKUP_ID"
  remote_bash "test -f $(q "$db")" || die "database is missing"
  remote_bash "mkdir -p $(q "$DEPLOY_APP_DIR/backups") && mkdir $(q "$dest")" || die "backup directory already exists"
  remote_bash "sqlite3 -readonly $(q "$db") \".backup $(q "$dest/castbot.db")\"" || die "sqlite backup failed"
  check=$(remote_bash "sqlite3 -readonly $(q "$dest/castbot.db") \"PRAGMA integrity_check;\"") || die "integrity check failed"
  check=$(trim_spaces "$check")
  [[ "$check" == "ok" ]] || die "integrity check failed"
  uv=$(remote_bash "sqlite3 -readonly $(q "$dest/castbot.db") \"PRAGMA user_version;\"") || die "could not read user_version"
  uv=$(trim_spaces "$uv")
  [[ "$uv" =~ ^[0-9]+$ ]] || die "could not read user_version"
  USER_VERSION=$uv
  sum=$(remote_bash "sha256sum $(q "$dest/castbot.db")") || die "could not hash the backup"
  sum=${sum%% *}
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || die "could not hash the backup"
  remote_bash "printf \"%s\n\" $(q "sha256 ${sum}") $(q "user_version ${uv}") $(q "integrity ok") > $(q "$dest/RECORD")" || die "could not record the backup"
  printf 'backup %s sha256=%s user_version=%s\n' "$BACKUP_ID" "$sum" "$uv"
}

upload_binary() {
  local dest dir mode remote_sum
  dest="$DEPLOY_APP_DIR/releases/$SHA/castbot"
  dir="$DEPLOY_APP_DIR/releases/$SHA"
  remote_bash "mkdir -p $(q "$dir")" || die "could not create the release directory"
  scp "${SSH_OPTS[@]}" "$BIN" "${DEPLOY_USER}@${DEPLOY_HOST}:${dest}" || die "upload failed"
  remote_bash "chmod 0755 $(q "$dest")" || die "could not set the binary mode"
  mode=$(remote_bash "stat -c %a $(q "$dest")") || die "could not read the binary mode"
  mode=$(trim_spaces "$mode")
  [[ "$mode" == "755" ]] || die "remote binary mode is not 0755"
  remote_sum=$(remote_bash "sha256sum $(q "$dest")") || die "could not hash the remote binary"
  remote_sum=${remote_sum%% *}
  [[ "$remote_sum" == "$BIN_SHA" ]] || die "remote sha256 does not match"
}

switch_to() {
  local sha=$1
  local bin="$DEPLOY_APP_DIR/releases/$sha/castbot"
  remote_bash "test -x $(q "$bin")" || return 1
  SWITCHED=1
  remote_bash "
mkdir -p $(q "$DEPLOY_APP_DIR/bin")
ln -sfn $(q "$bin") $(q "$DEPLOY_APP_DIR/bin/castbot.next")
mv -Tf $(q "$DEPLOY_APP_DIR/bin/castbot.next") $(q "$DEPLOY_APP_DIR/bin/castbot")
printf \"%s\n\" $(q "$sha") > $(q "$DEPLOY_APP_DIR/RELEASE.next")
mv -Tf $(q "$DEPLOY_APP_DIR/RELEASE.next") $(q "$DEPLOY_APP_DIR/RELEASE")
"
}

read_release() {
  local got
  got=$(remote_bash "if [[ -f $(q "$DEPLOY_APP_DIR/RELEASE") ]]; then tr -d '[:space:]' < $(q "$DEPLOY_APP_DIR/RELEASE"); fi") || die "could not read RELEASE"
  got=$(trim_spaces "$got")
  printf '%s' "$got"
}

redact_text() {
  sed -E \
    -e 's/(CASTBOT_[A-Za-z0-9_]*|GITHUB_TOKEN|DEPLOY_[A-Za-z0-9_]*)=[^[:space:]]*/\1=***/g' \
    -e 's/([A-Za-z0-9_]*(token|secret|password|key)[A-Za-z0-9_]*)[=:][^[:space:]]*/\1=***/Ig'
}

verify_release() {
  local expect=$1
  local since=$2
  local got link pid exe live_sum file_sum copy meta after image logs matches count code base
  got=$(read_release) || {
    printf 'could not read RELEASE\n' >&2
    return 1
  }
  [[ "$got" == "$expect" ]] || {
    printf 'RELEASE does not match\n' >&2
    return 1
  }
  link=$(remote_bash "readlink -f $(q "$DEPLOY_APP_DIR/bin/castbot")") || {
    printf 'could not read the symlink\n' >&2
    return 1
  }
  link=$(trim_spaces "$link")
  [[ "$link" == "$DEPLOY_APP_DIR/releases/$expect/castbot" ]] || {
    printf 'symlink does not match\n' >&2
    return 1
  }
  pid=$(remote_bash "systemctl show -p MainPID --value castbot") || {
    printf 'could not read the main process\n' >&2
    return 1
  }
  pid=$(trim_spaces "$pid")
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || {
    printf 'castbot has no main process\n' >&2
    return 1
  }
  exe=$(remote_bash "readlink -f /proc/${pid}/exe") || {
    printf 'could not read the running binary\n' >&2
    return 1
  }
  exe=$(trim_spaces "$exe")
  [[ "$exe" == "$DEPLOY_APP_DIR/releases/$expect/castbot" ]] || {
    printf 'running binary does not match\n' >&2
    return 1
  }
  live_sum=$(remote_bash "sha256sum /proc/${pid}/exe") || {
    printf 'could not hash the running binary\n' >&2
    return 1
  }
  live_sum=${live_sum%% *}
  file_sum=$(remote_bash "sha256sum $(q "$DEPLOY_APP_DIR/releases/$expect/castbot")") || {
    printf 'could not hash the release\n' >&2
    return 1
  }
  file_sum=${file_sum%% *}
  [[ "$live_sum" == "$file_sum" ]] || {
    printf 'running binary hash does not match the release\n' >&2
    return 1
  }
  if [[ "$expect" == "$SHA" ]]; then
    [[ "$file_sum" == "$BIN_SHA" ]] || {
      printf 'running binary hash does not match the build\n' >&2
      return 1
    }
  fi
  copy=$(mktemp)
  scp "${SSH_OPTS[@]}" "${DEPLOY_USER}@${DEPLOY_HOST}:${exe}" "$copy" || {
    printf 'could not copy the running binary\n' >&2
    return 1
  }
  local copy_sum
  copy_sum=$(sha256sum "$copy" | awk '{ print $1 }')
  [[ "$copy_sum" == "$file_sum" ]] || {
    rm -f "$copy"
    printf 'copied running binary hash does not match\n' >&2
    return 1
  }
  if ! meta=$(go version -m "$copy"); then
    rm -f "$copy"
    printf 'go version -m failed on the running binary\n' >&2
    return 1
  fi
  rm -f "$copy"
  local rev mod
  rev=$(build_meta_field "$meta" revision) || {
    printf 'running binary has no vcs.revision\n' >&2
    return 1
  }
  mod=$(build_meta_field "$meta" modified) || {
    printf 'running binary has no vcs.modified\n' >&2
    return 1
  }
  [[ "$rev" == "$expect" && "$mod" == "false" ]] || {
    printf 'running binary build info does not match\n' >&2
    return 1
  }
  got=$(remote_bash "systemctl is-active castbot") || return 1
  got=$(trim_spaces "$got")
  [[ "$got" == "active" ]] || {
    printf 'castbot.service is not active\n' >&2
    return 1
  }
  code=$(remote_bash "curl -sS -o /dev/null -w '%{http_code}' --max-time 15 http://127.0.0.1:8080/login") || return 1
  code=$(trim_spaces "$code")
  [[ "$code" == "200" ]] || {
    printf 'local /login returned %s\n' "$code" >&2
    return 1
  }
  base=${DEPLOY_PUBLIC_URL%/}
  code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "${base}/login") || return 1
  code=$(trim_spaces "$code")
  [[ "$code" == "200" ]] || {
    printf 'public /login returned %s\n' "$code" >&2
    return 1
  }
  after=$(remote_bash "sqlite3 -readonly $(q "$(db_path)") \"PRAGMA user_version;\"") || return 1
  after=$(trim_spaces "$after")
  [[ "$after" =~ ^[0-9]+$ ]] || {
    printf 'could not read user_version\n' >&2
    return 1
  }
  if ((10#$after < 10#$USER_VERSION)); then
    printf 'user_version went from %s to %s\n' "$USER_VERSION" "$after" >&2
    return 1
  fi
  image=$(read_image_value) || return 1
  [[ "$image" == "$IMAGE_VALUE" ]] || {
    printf 'CASTBOT_IMAGE changed\n' >&2
    return 1
  }
  logs=$(remote_bash "journalctl -u castbot --since $(q "$since") -o cat --no-pager") || return 1
  matches=$(printf '%s\n' "$logs" | grep -Ei 'panic|error|fatal' || true)
  if [[ -n "$matches" ]]; then
    count=$(printf '%s\n' "$matches" | wc -l | tr -d ' ')
    printf 'journal matches: %s\n' "$count" >&2
    printf '%s\n' "$matches" | redact_text >&2
    return 1
  fi
  printf 'journal matches: 0\n'
  return 0
}

restart_service() {
  local since
  since=$(remote_bash "date -u '+%Y-%m-%d %H:%M:%S UTC'") || return 1
  since=$(trim_spaces "$since")
  remote_bash "sudo -n systemctl restart castbot" || return 1
  printf '%s' "$since"
}

rollback() {
  local why=$1
  printf 'deploy: %s\n' "$why" >&2
  if [[ "$IN_ROLLBACK" == "1" ]]; then
    die "rollback did not verify"
  fi
  if [[ -z "$PREV" ]]; then
    die "verify failed and there is no previous release"
  fi
  IN_ROLLBACK=1
  SWITCHED=0
  if ! switch_to "$PREV"; then
    die "could not restore the previous release"
  fi
  local since
  if ! since=$(restart_service); then
    die "rollback restart failed"
  fi
  if ! verify_release "$PREV" "$since"; then
    die "rollback did not verify"
  fi
  RESULT=rolled-back
  printf 'deploy: rolled back to %s\n' "$PREV" >&2
  exit 1
}

fail_switched() {
  local why=$1
  if [[ "$SWITCHED" == "1" ]]; then
    rollback "$why"
  fi
  die "$why"
}

prune_releases() {
  local list line name kept
  list=$(remote_bash "ls -1dt -- $(q "$DEPLOY_APP_DIR/releases")/*/") || die "could not list releases"
  local -a names=()
  local -A keep=()
  while IFS= read -r line; do
    line=${line%/}
    [[ -n "$line" ]] || continue
    name=${line##*/}
    [[ "$name" =~ ^[0-9a-f]{40}$ ]] || die "refusing to touch an unexpected release name"
    names+=("$name")
  done <<<"$list"
  kept=0
  for name in "${names[@]}"; do
    if ((kept < DEPLOY_KEEP_RELEASES)); then
      keep["$name"]=1
      kept=$((kept + 1))
    fi
  done
  keep["$SHA"]=1
  if [[ -n "$PREV" ]]; then
    keep["$PREV"]=1
  fi
  for name in "${names[@]}"; do
    if [[ -n "${keep[$name]:-}" ]]; then
      continue
    fi
    [[ "$name" != "$SHA" && "$name" != "$PREV" ]] || die "refusing to delete the live release"
    remote_bash "rm -rf -- $(q "$DEPLOY_APP_DIR/releases/$name")" || die "could not prune a release"
    printf 'pruned %s\n' "$name"
  done
}

print_plan() {
  local when
  when=$(TZ=America/Denver date '+%Y-%m-%d_%H%M')
  cat <<EOF
dry-run
sha ${SHA}
host ${DEPLOY_HOST}
user ${DEPLOY_USER}
app ${DEPLOY_APP_DIR}
public ${DEPLOY_PUBLIC_URL%/}/login
binary ${BIN_SHA}
would backup ${DEPLOY_APP_DIR}/backups/${when}
would install ${DEPLOY_APP_DIR}/releases/${SHA}/castbot
would link ${DEPLOY_APP_DIR}/bin/castbot
would write ${DEPLOY_APP_DIR}/RELEASE
would restart castbot
would keep ${DEPLOY_KEEP_RELEASES} releases
no backup, upload, or restart
EOF
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --dry-run) DRY=1 ;;
      --skip-ci-check) SKIP_CI=1 ;;
      --i-understand-ci-is-not-green) UNDERSTAND=1 ;;
      -h | --help)
        usage
        exit 0
        ;;
      --)
        shift
        break
        ;;
      -*)
        die "unknown option: $1"
        ;;
      *)
        [[ -z "$SHA_ARG" ]] || die "unexpected argument"
        SHA_ARG=$1
        ;;
    esac
    shift
  done
  if [[ $# -gt 0 ]]; then
    [[ -z "$SHA_ARG" ]] || die "unexpected argument"
    SHA_ARG=$1
    shift
  fi
  [[ $# -eq 0 ]] || die "unexpected argument"
}

main() {
  parse_args "$@"
  need git go ssh scp curl python3 file sha256sum ldd
  ROOT=$(git rev-parse --show-toplevel)
  cd "$ROOT"
  load_deploy_env
  apply_defaults
  validate_config
  setup_ssh
  preflight_tree
  resolve_sha
  check_go
  check_ssh
  ci_gate
  image_gate
  build_binary
  if [[ "$DRY" == "1" ]]; then
    print_plan
    RESULT=dry-run
    exit 0
  fi
  start_lock
  abort_if_inflight
  take_backup
  upload_binary
  abort_if_inflight
  PREV=$(read_release)
  if [[ -n "$PREV" && ! "$PREV" =~ ^[0-9a-f]{40}$ ]]; then
    die "RELEASE is not a commit sha"
  fi
  if ! switch_to "$SHA"; then
    fail_switched "could not switch the release"
  fi
  local since
  if ! since=$(restart_service); then
    fail_switched "restart failed"
  fi
  if ! verify_release "$SHA" "$since"; then
    fail_switched "verify failed"
  fi
  prune_releases
  RESULT=ok
  printf 'deployed %s\n' "$SHA"
}

main "$@"
