#!/usr/bin/env bash
# Deploy the castbot control plane from a laptop.
# The runtime image is not built, pushed, or changed. The server env file is
# only searched for the CASTBOT_IMAGE line. Secrets are not read or printed.
if [[ $- == *x* ]]; then
  set +x
  printf 'deploy: refusing to run under xtrace\n' >&2
  exit 1
fi
set -euo pipefail

RESULT=fail
BACKUP_ID="-"
SHA_LOG="-"
LOG_WRITTEN=0
SWITCHED=0
IN_ROLLBACK=0
LOCK_FD=""
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
SCHEMA_BEFORE=""
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
                                    gate, the build, and the read-only remote
                                    checks, then print the plan. No backup,
                                    upload, or restart.
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
  if [[ -n "${LOCK_FD}" ]]; then
    eval "exec ${LOCK_FD}>&-"
    LOCK_FD=""
  fi
  if [[ -n "${LOCK_SSH}" ]] && kill -0 "$LOCK_SSH" 2>/dev/null; then
    local i=0
    while [[ "$i" -lt 30 ]] && kill -0 "$LOCK_SSH" 2>/dev/null; do
      sleep 0.1
      i=$((i + 1))
    done
    if kill -0 "$LOCK_SSH" 2>/dev/null; then
      kill "$LOCK_SSH" 2>/dev/null || true
      wait "$LOCK_SSH" 2>/dev/null || true
    else
      wait "$LOCK_SSH" 2>/dev/null || true
    fi
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
      DEPLOY_HOST|DEPLOY_USER|DEPLOY_APP_DIR|DEPLOY_ENV_FILE|DEPLOY_PUBLIC_URL|DEPLOY_KEEP_RELEASES|DEPLOY_KNOWN_HOSTS|DEPLOY_LOG|DEPLOY_LOCAL_HEALTH_URL)
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
  : "${DEPLOY_LOCAL_HEALTH_URL:=http://127.0.0.1:8080/login}"
  export DEPLOY_APP_DIR DEPLOY_ENV_FILE DEPLOY_KEEP_RELEASES DEPLOY_KNOWN_HOSTS DEPLOY_LOG DEPLOY_LOCAL_HEALTH_URL
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
  [[ "$DEPLOY_LOCAL_HEALTH_URL" =~ ^http://(127\.0\.0\.1|localhost)(:[0-9]{1,5})?(/[A-Za-z0-9._~:/@%+-]*)?$ ]] || die "DEPLOY_LOCAL_HEALTH_URL must be an http URL on 127.0.0.1 or localhost"
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

# Privileged remote work runs as root. The inner script is one bash -c
# argument so the SSH user never needs to own the app directory.
remote_root() {
  local script=$1
  remote_bash "sudo -n bash -c $(q "set -euo pipefail"$'\n'"$script")"
}

start_lock() {
  LOCK_OUT=$(mktemp)
  # The write end of this pipe stays open in this process. The remote cat
  # exits when that pipe closes, and flock exits with it. prctl makes the
  # local ssh die with this process, so a kill still closes the session.
  # flock -o keeps the lock on flock itself; cat must not inherit it.
  exec {LOCK_FD}> >(python3 -c 'import ctypes, os, sys
libc = ctypes.CDLL(None)
libc.prctl.argtypes = [ctypes.c_int, ctypes.c_ulong, ctypes.c_ulong, ctypes.c_ulong, ctypes.c_ulong]
libc.prctl.restype = ctypes.c_int
if libc.prctl(1, 9, 0, 0, 0) != 0:
    sys.exit(1)
os.execvp(sys.argv[1], sys.argv[1:])' ssh "${SSH_OPTS[@]}" "${DEPLOY_USER}@${DEPLOY_HOST}" "exec sudo -n flock -n -o /run/lock/castbot-deploy.lock sh -c 'echo LOCKED; exec cat >/dev/null'" >"$LOCK_OUT")
  LOCK_SSH=$!
  local tries=0
  while [[ "$tries" -lt 50 ]]; do
    tries=$((tries + 1))
    if [[ -f "$LOCK_OUT" ]] && grep -q LOCKED "$LOCK_OUT"; then
      return 0
    fi
    if [[ -n "$LOCK_SSH" ]] && ! kill -0 "$LOCK_SSH" 2>/dev/null; then
      die "could not take the deploy lock"
    fi
    sleep 0.1
  done
  die "could not take the deploy lock"
}

db_path() {
  printf '%s' "$DEPLOY_APP_DIR/data/castbot.db"
}

trim_spaces() {
  local v=$1
  v=${v//$'\r'/}
  v=${v#"${v%%[![:space:]]*}"}
  v=${v%"${v##*[![:space:]]}"}
  printf '%s' "$v"
}

inflight_count() {
  local db count
  db=$(db_path)
  count=$(remote_root "sqlite3 -readonly $(q "$db") $(q "SELECT COUNT(*) FROM runs WHERE status IN ('queued','streaming','waiting_approval');")") || die "in-flight query failed"
  count=$(trim_spaces "$count")
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

read_schema() {
  local db=$1 ver
  ver=$(remote_root "sqlite3 -readonly $(q "$db") $(q "SELECT COALESCE(MAX(version),0) FROM schema_migrations;")") || return 1
  ver=$(trim_spaces "$ver")
  [[ "$ver" =~ ^[0-9]+$ ]] || return 1
  printf '%s' "$ver"
}

read_image_value() {
  local raw line val n
  raw=$(remote_root "grep -E '^CASTBOT_IMAGE=' -- $(q "$DEPLOY_ENV_FILE")") || {
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

guest_rel_paths() {
  local sha=$1 wt mod out status
  wt=$(mktemp -d)
  if ! git -C "$ROOT" worktree add --detach "$wt" "$sha" >/dev/null; then
    rm -rf "$wt"
    die "could not check out a commit to list runtime packages"
  fi
  out=$(mktemp)
  set +e
  (
    cd "$wt"
    go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./cmd/castlet ./cmd/castbot-guest ./cmd/cast-standin
  ) >"$out"
  status=$?
  set -e
  mod=$(awk '$1 == "module" { print $2; exit }' "$wt/go.mod")
  git -C "$ROOT" worktree remove --force "$wt" >/dev/null 2>&1 || true
  rm -rf "$wt"
  if [[ "$status" != "0" ]]; then
    rm -f "$out"
    die "could not list runtime image packages"
  fi
  [[ -n "$mod" ]] || {
    rm -f "$out"
    die "go.mod has no module path"
  }
  awk -v mod="$mod" '
    index($0, mod "/") == 1 {
      print substr($0, length(mod) + 2)
    }
  ' "$out"
  rm -f "$out"
}

image_gate() {
  local value commit names gained paths p
  value=$(read_image_value) || die "could not read CASTBOT_IMAGE"
  IMAGE_VALUE=$value
  commit=$(image_commit "$value")
  paths=$(
    {
      printf '%s\n' Dockerfile deploy go.mod go.sum cmd/castlet cmd/castbot-guest cmd/cast-standin internal/castlet
      guest_rel_paths "$commit"
      guest_rel_paths "$SHA"
    } | awk 'NF && !seen[$0]++'
  )
  local -a path_args=()
  while IFS= read -r p; do
    [[ -n "$p" ]] || continue
    [[ "$p" =~ ^[A-Za-z0-9._/-]+$ ]] || die "runtime image package path is unexpected"
    path_args+=("$p")
  done <<<"$paths"
  names=$(git -C "$ROOT" diff --name-only "$commit" "$SHA" -- "${path_args[@]}") || die "could not diff the runtime image inputs"
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

redact_text() {
  sed -E \
    -e 's/(CASTBOT_[A-Za-z0-9_]*|GITHUB_TOKEN|DEPLOY_[A-Za-z0-9_]*)=[^[:space:]]*/\1=***/g' \
    -e 's/([A-Za-z0-9_]*(token|secret|password|key)[A-Za-z0-9_]*)[=:][^[:space:]]*/\1=***/Ig'
}

github_curl() {
  local url=$1
  local out code msg tracing=0
  if [[ $- == *x* ]]; then
    tracing=1
    set +x
  fi
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
    [[ "$tracing" == "1" ]] && set -x
    die "GitHub API request failed"
  fi
  if [[ -n "${TOKEN_HDR}" && -f "$TOKEN_HDR" ]]; then
    rm -f "$TOKEN_HDR"
  fi
  TOKEN_HDR=""
  if [[ "$tracing" == "1" ]]; then
    set -x
  fi
  if [[ "$code" != "200" ]]; then
    msg=""
    if [[ "$code" == "403" || "$code" == "401" ]]; then
      msg=$(python3 - "$out" <<'PY' || true
import json, sys
try:
    with open(sys.argv[1], encoding="utf-8") as fh:
        data = json.load(fh)
    text = data.get("message") or ""
    if not isinstance(text, str):
        text = ""
    sys.stdout.write(text)
except Exception:
    pass
PY
)
      msg=$(printf '%s' "$msg" | redact_text)
      msg=${msg//$'\n'/ }
      msg=${msg:0:200}
      msg=$(trim_spaces "$msg")
    fi
    rm -f "$out"
    if [[ -n "$msg" ]]; then
      die "GitHub API returned HTTP ${code}: ${msg}"
    fi
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

check_sudo() {
  remote_root "true" || die "sudo -n failed"
  remote_root "command -v sqlite3 >/dev/null" || die "sqlite3 is missing on the host"
}

assert_static() {
  local bin=$1
  local info ldd_out
  set +e
  info=$(readelf -d "$bin" 2>&1)
  set -e
  if printf '%s\n' "$info" | grep -q 'NEEDED'; then
    die "binary is not static"
  fi
  if ! printf '%s\n' "$info" | grep -q 'no dynamic section'; then
    die "binary is not static"
  fi
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

read_release_file() {
  local got
  got=$(remote_root "if [[ -f $(q "$DEPLOY_APP_DIR/RELEASE") ]]; then tr -d '[:space:]' < $(q "$DEPLOY_APP_DIR/RELEASE"); fi") || return 1
  trim_spaces "$got"
}

read_link_sha() {
  local link prefix name
  link=$(remote_root "if [[ -e $(q "$DEPLOY_APP_DIR/bin/castbot") || -L $(q "$DEPLOY_APP_DIR/bin/castbot") ]]; then readlink -f $(q "$DEPLOY_APP_DIR/bin/castbot"); fi") || return 1
  link=$(trim_spaces "$link")
  if [[ -z "$link" ]]; then
    printf ''
    return 0
  fi
  prefix="$DEPLOY_APP_DIR/releases/"
  [[ "$link" == "$prefix"*"/castbot" ]] || return 1
  name=${link#"$prefix"}
  name=${name%"/castbot"}
  [[ "$name" =~ ^[0-9a-f]{40}$ ]] || return 1
  printf '%s' "$name"
}

ensure_rollback_target() {
  local rel link
  rel=$(read_release_file) || die "could not read RELEASE"
  rel=$(trim_spaces "$rel")
  link=$(read_link_sha) || die "symlink is not a release binary"
  link=$(trim_spaces "$link")
  if [[ -z "$rel" && -z "$link" ]]; then
    PREV=""
    printf 'rollback none\n'
    return 0
  fi
  if [[ "$rel" != "$link" ]]; then
    die "RELEASE does not match the symlink"
  fi
  [[ "$rel" =~ ^[0-9a-f]{40}$ ]] || die "RELEASE is not a commit sha"
  remote_root "test -x $(q "$DEPLOY_APP_DIR/releases/$rel/castbot")" || die "rollback target binary is missing"
  PREV=$rel
  if [[ "$PREV" == "$SHA" ]]; then
    die "sha is already live"
  fi
  printf 'rollback %s\n' "$PREV"
}

take_backup() {
  local db dest sum ver check
  db=$(db_path)
  BACKUP_ID=$(TZ=America/Denver date '+%Y-%m-%d_%H%M')
  dest="$DEPLOY_APP_DIR/backups/$BACKUP_ID"
  remote_root "test -f $(q "$db")" || die "database is missing"
  remote_root "mkdir -p -- $(q "$DEPLOY_APP_DIR/backups") && mkdir -- $(q "$dest") && chmod 0700 $(q "$dest")" || die "backup directory already exists"
  remote_root "sqlite3 -readonly $(q "$db") $(q ".backup ${dest}/castbot.db")" || die "sqlite backup failed"
  check=$(remote_root "sqlite3 -readonly $(q "$dest/castbot.db") $(q "PRAGMA integrity_check;")") || die "integrity check failed"
  check=$(trim_spaces "$check")
  [[ "$check" == "ok" ]] || die "integrity check failed"
  ver=$(read_schema "$dest/castbot.db") || die "could not read schema_version"
  SCHEMA_BEFORE=$ver
  sum=$(remote_root "sha256sum -- $(q "$dest/castbot.db")") || die "could not hash the backup"
  sum=${sum%% *}
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || die "could not hash the backup"
  remote_root "printf '%s\n' $(q "sha256 ${sum}") $(q "schema_version ${ver}") $(q "integrity ok") > $(q "$dest/RECORD")" || die "could not record the backup"
  printf 'backup %s sha256=%s schema_version=%s\n' "$BACKUP_ID" "$sum" "$ver"
}

valid_temp() {
  local path=$1
  [[ "$path" =~ ^/(tmp|var/tmp)/[A-Za-z0-9._-]+$ ]]
}

upload_binary() {
  local tmp dest dir remote_sum mode
  dest="$DEPLOY_APP_DIR/releases/$SHA/castbot"
  dir="$DEPLOY_APP_DIR/releases/$SHA"
  tmp=$(remote_bash "mktemp -d") || die "could not create a temp directory"
  tmp=$(trim_spaces "$tmp")
  valid_temp "$tmp" || die "temp directory is unexpected"
  if ! scp "${SSH_OPTS[@]}" "$BIN" "${DEPLOY_USER}@${DEPLOY_HOST}:${tmp}/castbot"; then
    remote_bash "rm -r -- $(q "$tmp")" || true
    die "upload failed"
  fi
  if ! remote_root "mkdir -p -- $(q "$dir") && install -o castbot -g castbot -m 0755 -- $(q "$tmp/castbot") $(q "$dest")"; then
    remote_bash "rm -r -- $(q "$tmp")" || true
    die "could not install the binary"
  fi
  remote_bash "rm -r -- $(q "$tmp")" || die "could not remove the temp directory"
  mode=$(remote_root "stat -c %a -- $(q "$dest")") || die "could not read the binary mode"
  mode=$(trim_spaces "$mode")
  [[ "$mode" == "755" ]] || die "remote binary mode is not 0755"
  remote_sum=$(remote_root "sha256sum -- $(q "$dest")") || die "could not hash the remote binary"
  remote_sum=${remote_sum%% *}
  [[ "$remote_sum" == "$BIN_SHA" ]] || die "remote sha256 does not match"
}

switch_to() {
  local sha=$1
  local bin="$DEPLOY_APP_DIR/releases/$sha/castbot"
  local script
  remote_root "test -x $(q "$bin")" || return 1
  SWITCHED=1
  script=$(cat <<EOF
mkdir -p -- $(q "$DEPLOY_APP_DIR/bin")
ln -sfn -- $(q "$bin") $(q "$DEPLOY_APP_DIR/bin/castbot.next")
mv -Tf -- $(q "$DEPLOY_APP_DIR/bin/castbot.next") $(q "$DEPLOY_APP_DIR/bin/castbot")
printf '%s\n' $(q "$sha") > $(q "$DEPLOY_APP_DIR/RELEASE.next")
mv -Tf -- $(q "$DEPLOY_APP_DIR/RELEASE.next") $(q "$DEPLOY_APP_DIR/RELEASE")
EOF
)
  remote_root "$script" || return 1
}

service_active() {
  local unit=$1 got
  got=$(remote_bash "systemctl is-active $(q "$unit")" || true)
  got=$(trim_spaces "$got")
  [[ "$got" == "active" ]]
}

wait_health() {
  local deadline=$((SECONDS + 30))
  local local_ok=0 public_ok=0 code base
  base=${DEPLOY_PUBLIC_URL%/}
  while ((SECONDS < deadline)); do
    if [[ "$local_ok" == "0" ]]; then
      code=$(remote_bash "curl -sS -o /dev/null -w '%{http_code}' --max-time 5 $(q "$DEPLOY_LOCAL_HEALTH_URL")" || true)
      code=$(trim_spaces "$code")
      [[ "$code" == "200" ]] && local_ok=1
    fi
    if [[ "$public_ok" == "0" ]]; then
      code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "${base}/login" || true)
      code=$(trim_spaces "$code")
      [[ "$code" == "200" ]] && public_ok=1
    fi
    if [[ "$local_ok" == "1" && "$public_ok" == "1" ]]; then
      return 0
    fi
    sleep 1
  done
  if [[ "$local_ok" != "1" ]]; then
    printf 'local /login did not return 200\n' >&2
  fi
  if [[ "$public_ok" != "1" ]]; then
    printf 'public /login did not return 200\n' >&2
  fi
  return 1
}

verify_release() {
  local expect=$1
  local since=$2
  local got link pid exe live_sum file_sum copy meta after image logs count copy_sum remote_tmp rev mod unit
  got=$(read_release_file) || {
    printf 'could not read RELEASE\n' >&2
    return 1
  }
  got=$(trim_spaces "$got")
  [[ "$got" == "$expect" ]] || {
    printf 'RELEASE does not match\n' >&2
    return 1
  }
  link=$(read_link_sha) || {
    printf 'could not read the symlink\n' >&2
    return 1
  }
  link=$(trim_spaces "$link")
  [[ "$link" == "$expect" ]] || {
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
  exe=$(remote_root "readlink -f /proc/${pid}/exe") || {
    printf 'could not read the running binary\n' >&2
    return 1
  }
  exe=$(trim_spaces "$exe")
  [[ "$exe" == "$DEPLOY_APP_DIR/releases/$expect/castbot" ]] || {
    printf 'running binary does not match\n' >&2
    return 1
  }
  live_sum=$(remote_root "sha256sum -- /proc/${pid}/exe") || {
    printf 'could not hash the running binary\n' >&2
    return 1
  }
  live_sum=${live_sum%% *}
  file_sum=$(remote_root "sha256sum -- $(q "$DEPLOY_APP_DIR/releases/$expect/castbot")") || {
    printf 'could not hash the release\n' >&2
    return 1
  }
  file_sum=${file_sum%% *}
  [[ "$live_sum" == "$file_sum" && "$live_sum" =~ ^[0-9a-f]{64}$ ]] || {
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
  remote_tmp=$(remote_bash "mktemp") || {
    rm -f "$copy"
    printf 'could not copy the running binary\n' >&2
    return 1
  }
  remote_tmp=$(trim_spaces "$remote_tmp")
  if ! valid_temp "$remote_tmp"; then
    rm -f "$copy"
    printf 'could not copy the running binary\n' >&2
    return 1
  fi
  if ! remote_root "cp -a -- $(q "$DEPLOY_APP_DIR/releases/$expect/castbot") $(q "$remote_tmp") && chmod 0644 $(q "$remote_tmp")"; then
    rm -f "$copy"
    remote_bash "rm -f -- $(q "$remote_tmp")" || true
    printf 'could not copy the running binary\n' >&2
    return 1
  fi
  if ! scp "${SSH_OPTS[@]}" "${DEPLOY_USER}@${DEPLOY_HOST}:${remote_tmp}" "$copy"; then
    rm -f "$copy"
    remote_bash "rm -f -- $(q "$remote_tmp")" || true
    printf 'could not copy the running binary\n' >&2
    return 1
  fi
  remote_bash "rm -f -- $(q "$remote_tmp")" || true
  copy_sum=$(sha256sum "$copy" | awk '{ print $1 }')
  if [[ "$copy_sum" != "$file_sum" ]]; then
    rm -f "$copy"
    printf 'copied running binary hash does not match\n' >&2
    return 1
  fi
  if ! meta=$(go version -m "$copy"); then
    rm -f "$copy"
    printf 'go version -m failed on the running binary\n' >&2
    return 1
  fi
  rm -f "$copy"
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
  for unit in castbot caddy fail2ban; do
    if ! service_active "$unit"; then
      printf '%s is not active\n' "$unit" >&2
      return 1
    fi
  done
  if ! wait_health; then
    return 1
  fi
  [[ "$SCHEMA_BEFORE" =~ ^[0-9]+$ ]] || {
    printf 'schema_version was not recorded\n' >&2
    return 1
  }
  after=$(read_schema "$(db_path)") || {
    printf 'could not read schema_version\n' >&2
    return 1
  }
  if ((10#$after < 10#$SCHEMA_BEFORE)); then
    printf 'schema_version went from %s to %s\n' "$SCHEMA_BEFORE" "$after" >&2
    return 1
  fi
  image=$(read_image_value) || return 1
  [[ "$image" == "$IMAGE_VALUE" ]] || {
    printf 'CASTBOT_IMAGE changed\n' >&2
    return 1
  }
  logs=$(remote_root "journalctl -u castbot _PID=${pid} --since $(q "$since") --no-pager") || {
    printf 'could not read the journal\n' >&2
    return 1
  }
  logs=$(trim_spaces "$logs")
  if [[ -z "$logs" ]]; then
    printf 'journal is empty\n' >&2
    return 1
  fi
  if ! printf '%s\n' "$logs" | grep -F 'castbot listening on' >/dev/null; then
    printf 'journal has no listening line\n' >&2
    return 1
  fi
  count=$(printf '%s\n' "$logs" | grep -Eic 'error|warn|fail|panic|fatal' || true)
  count=$(trim_spaces "$count")
  [[ "$count" =~ ^[0-9]+$ ]] || count=0
  if [[ "$count" != "0" ]]; then
    printf 'journal matches: %s\n' "$count" >&2
    return 1
  fi
  printf 'journal matches: 0\n'
  return 0
}

restart_service() {
  local since
  since=$(remote_bash "date -u '+%Y-%m-%d %H:%M:%S UTC'") || return 1
  since=$(trim_spaces "$since")
  remote_root "systemctl restart castbot" || return 1
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

each_prune() {
  local mode=$1
  local -a names=()
  local -A keep=()
  local raw line name kept=0 any=0 script
  script=$(cat <<EOF
if [[ -d $(q "$DEPLOY_APP_DIR/releases") ]]; then find $(q "$DEPLOY_APP_DIR/releases") -mindepth 1 -maxdepth 1 -type d -printf '%T@ %f\n'; fi
EOF
)
  raw=$(remote_root "$script") || die "could not list releases"
  while IFS= read -r line; do
    [[ -n "$line" ]] || continue
    name=${line##* }
    [[ "$name" =~ ^[0-9a-f]{40}$ ]] || die "refusing to touch an unexpected release name"
    names+=("$name")
  done < <(printf '%s\n' "$raw" | sort -nr)
  if ((${#names[@]} > 0)); then
    for name in "${names[@]}"; do
      if ((kept < DEPLOY_KEEP_RELEASES)); then
        keep["$name"]=1
        kept=$((kept + 1))
      fi
    done
  fi
  [[ -n "$SHA" ]] && keep["$SHA"]=1
  [[ -n "$PREV" ]] && keep["$PREV"]=1
  if ((${#names[@]} == 0)); then
    if [[ "$mode" == "print" ]]; then
      printf 'would prune none\n'
    fi
    return 0
  fi
  for name in "${names[@]}"; do
    [[ -z "${keep[$name]:-}" ]] || continue
    [[ "$name" != "$SHA" && "$name" != "$PREV" ]] || die "refusing to delete the live release"
    if [[ "$mode" == "print" ]]; then
      printf 'would prune %s\n' "$name"
      any=1
    else
      remote_root "rm -rf -- $(q "$DEPLOY_APP_DIR/releases/$name")" || die "could not prune a release"
      printf 'pruned %s\n' "$name"
    fi
  done
  if [[ "$mode" == "print" && "$any" == "0" ]]; then
    printf 'would prune none\n'
  fi
}

disk_free_kb() {
  local raw kb script
  script="df -Pk $(q "$DEPLOY_APP_DIR") | awk 'NR==2 { print \$4 }'"
  raw=$(remote_root "$script") || return 1
  kb=$(trim_spaces "$raw")
  [[ "$kb" =~ ^[0-9]+$ ]] || return 1
  printf '%s' "$kb"
}

dry_run_report() {
  local count ver free
  ensure_rollback_target
  count=$(inflight_count)
  printf 'inflight %s\n' "$count"
  if [[ "$count" != "0" ]]; then
    die "abort: ${count} runs are queued, streaming, or waiting for approval"
  fi
  ver=$(read_schema "$(db_path)") || die "could not read schema_version"
  printf 'schema_version %s\n' "$ver"
  remote_root "journalctl -u castbot -n 0 --no-pager >/dev/null" || die "journalctl is not readable"
  printf 'journal readable\n'
  free=$(disk_free_kb) || die "could not read free disk"
  printf 'free_kb %s\n' "$free"
  each_prune print
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
local ${DEPLOY_LOCAL_HEALTH_URL}
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
  need git go ssh scp curl python3 readelf sha256sum ldd
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
  check_sudo
  ci_gate
  image_gate
  build_binary
  if [[ "$DRY" == "1" ]]; then
    dry_run_report
    print_plan
    RESULT=dry-run
    exit 0
  fi
  start_lock
  ensure_rollback_target
  abort_if_inflight
  take_backup
  upload_binary
  if ! switch_to "$SHA"; then
    fail_switched "could not switch the release"
  fi
  abort_if_inflight
  local since
  if ! since=$(restart_service); then
    fail_switched "restart failed"
  fi
  if ! verify_release "$SHA" "$since"; then
    fail_switched "verify failed"
  fi
  each_prune delete
  RESULT=ok
  printf 'deployed %s\n' "$SHA"
}

main "$@"
