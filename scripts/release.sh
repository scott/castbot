#!/usr/bin/env bash
# Cut a signed castbot release from a green main commit.
# The minisign private key is CASTBOT_MINISIGN_SECRET. It is not printed.
if [[ $- == *x* ]]; then
  set +x
  printf 'release: refusing to run under xtrace\n' >&2
  exit 1
fi
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)

die() {
  printf 'release: %s\n' "$*" >&2
  exit 1
}

RELEASE_WORK=""
cleanup_release() {
  if [[ -n "$RELEASE_WORK" ]]; then
    rm -rf "$RELEASE_WORK"
    RELEASE_WORK=""
  fi
}

redact_text() {
  sed -E \
    -e 's/(CASTBOT_[A-Za-z0-9_]*|GITHUB_TOKEN|DEPLOY_[A-Za-z0-9_]*)=[^[:space:]]*/\1=***/g' \
    -e 's/([A-Za-z0-9_]*(token|secret|password|key)[A-Za-z0-9_]*)[=:][^[:space:]]*/\1=***/Ig'
}

next_version() {
  local major=${1:-0} sha existing date n
  [[ "$major" =~ ^[0-9]+$ ]] || die "major must be a number"
  sha=$(git rev-parse HEAD)
  existing=$(git tag --points-at "$sha" | sed -n 's/^v\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$/\1/p' | sort -t. -k1,1n -k2,2n -k3,3n | tail -n 1)
  existing=$(printf '%s' "$existing" | tr -d '[:space:]')
  if [[ -n "$existing" ]]; then
    printf 'already %s\n' "$existing"
    return 0
  fi
  date=$(date -u +%Y%m%d)
  n=1
  while git rev-parse -q --verify "refs/tags/v${major}.${date}.${n}" >/dev/null; do
    n=$((n + 1))
  done
  printf '%s.%s.%s\n' "$major" "$date" "$n"
}

notes() {
  local prev log
  prev=$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-creatordate | head -n 1 || true)
  prev=$(printf '%s' "$prev" | tr -d '[:space:]')
  if [[ -n "$prev" ]]; then
    log=$(git log --pretty=%s "${prev}..HEAD")
  else
    log=$(git log --pretty=%s -n 20)
  fi
  printf '%s\n' "$log" | redact_text
}

require_ci() {
  local file=$1
  [[ -n "$file" && -f "$file" ]] || die "ci jobs file is missing"
  python3 - "$file" <<'PY'
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
sys.stderr.write("ci is not green (test=%s shots=%s)\n" % (need["test"] or "missing", need["shots"] or "missing"))
sys.exit(1)
PY
}

fetch_ci_jobs() {
  local sha=$1 out run_id
  out=$(mktemp)
  gh api "repos/scott/castbot/actions/workflows/ci.yml/runs?head_sha=${sha}&branch=main&event=push&per_page=5" >"$out"
  run_id=$(python3 - "$sha" "$out" <<'PY'
import json, sys
sha, path = sys.argv[1], sys.argv[2]
with open(path, encoding="utf-8") as fh:
    data = json.load(fh)
runs = data.get("workflow_runs") or []
if not runs:
    sys.exit(2)
run = runs[0]
if run.get("head_sha") != sha or run.get("head_branch") != "main" or run.get("event") != "push":
    sys.exit(3)
if run.get("status") != "completed" or run.get("conclusion") != "success":
    sys.exit(4)
print(run.get("id") or "")
PY
  ) || {
    rm -f "$out"
    die "ci is not green for this sha"
  }
  rm -f "$out"
  [[ "$run_id" =~ ^[0-9]+$ ]] || die "ci is not green for this sha"
  CASTBOT_CI_JOBS_FILE=$(mktemp)
  gh api "repos/scott/castbot/actions/runs/${run_id}/jobs?per_page=100" >"$CASTBOT_CI_JOBS_FILE"
  export CASTBOT_CI_JOBS_FILE
}

wait_image() {
  local sha=$1 i ref wait
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || die "commit must be 40 hex characters"
  wait=${CASTBOT_RELEASE_IMAGE_WAIT:-10}
  [[ "$wait" =~ ^[0-9]+$ ]] || die "image wait must be a number of seconds"
  i=0
  while [[ "$i" -lt 12 ]]; do
    i=$((i + 1))
    if ref=$(bash "$ROOT/scripts/runtime-image.sh" select "$sha"); then
      printf '%s\n' "$ref"
      return 0
    fi
    sleep "$wait"
  done
  die "public runtime image is not available"
}

build_binary() {
  local out=$1 arch=$2 version=$3 sha=$4 guest=$5 ldflags
  ldflags="-s -w -X github.com/scott/castbot/cmd/castbot.version=${version} -X github.com/scott/castbot/cmd/castbot.commit=${sha} -X github.com/scott/castbot/cmd/castbot.guestID=${guest}"
  (
    cd "$ROOT"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="$ldflags" -o "$out" ./cmd/castbot
  )
  if readelf -d "$out" 2>/dev/null | grep -q 'NEEDED'; then
    die "binary has dynamic libraries"
  fi
}

publish() {
  local major sha line version tag guest image image_commit dir notes_file key
  cd "$ROOT" || die "could not enter the checkout"
  major=${CASTBOT_RELEASE_MAJOR:-0}
  sha=$(git rev-parse HEAD)
  [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || die "commit must be 40 hex characters"
  git fetch --tags --quiet origin
  sha=$(git rev-parse HEAD)
  if [[ -n "${GITHUB_SHA:-}" && "$GITHUB_SHA" != "$sha" ]]; then
    die "checkout does not match GITHUB_SHA"
  fi
  if [[ -z "${CASTBOT_CI_JOBS_FILE:-}" ]]; then
    fetch_ci_jobs "$sha"
  fi
  require_ci "$CASTBOT_CI_JOBS_FILE"
  line=$(next_version "$major")
  if [[ "$line" == already\ * ]]; then
    version=${line#already }
    tag="v${version}"
    if gh release view "$tag" >/dev/null 2>&1; then
      printf 'already published %s\n' "$tag"
      return 0
    fi
  else
    version=$line
    tag="v${version}"
  fi
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version is unexpected"
  [[ -n "${CASTBOT_MINISIGN_SECRET:-}" ]] || die "CASTBOT_MINISIGN_SECRET is not set"
  guest=$(bash "$ROOT/scripts/guest-id.sh")
  [[ "$guest" =~ ^[0-9a-f]{64}$ ]] || die "guest id is unexpected"
  image=$(wait_image "$sha")
  image=$(printf '%s' "$image" | tr -d '[:space:]')
  image_commit=${image##*:}
  [[ "$image" == "ghcr.io/scott/castbot-runtime:${image_commit}" ]] || die "runtime image is unexpected"
  [[ "$image_commit" =~ ^[0-9a-f]{40}$ ]] || die "runtime image commit is unexpected"
  dir=$(mktemp -d)
  RELEASE_WORK=$dir
  trap cleanup_release EXIT
  key="$dir/minisign.key"
  (umask 077; printf '%s\n' "$CASTBOT_MINISIGN_SECRET" >"$key")
  build_binary "$dir/castbot_linux_amd64" amd64 "$version" "$sha" "$guest"
  build_binary "$dir/castbot_linux_arm64" arm64 "$version" "$sha" "$guest"
  notes_file="$dir/notes.txt"
  notes >"$notes_file"
  (
    cd "$ROOT"
    go run ./cmd/castbot release pack \
      -version "$version" \
      -commit "$sha" \
      -guest-id "$guest" \
      -runtime-image "$image" \
      -runtime-image-commit "$image_commit" \
      -notes "https://github.com/scott/castbot/releases/tag/${tag}" \
      -amd64 "$dir/castbot_linux_amd64" \
      -arm64 "$dir/castbot_linux_arm64" \
      -key "$key" \
      -dir "$dir/out"
  )
  rm -f "$key"
  if [[ "${CASTBOT_RELEASE_DRY_RUN:-}" == "1" ]]; then
    printf 'dry-run %s\n' "$tag"
    return 0
  fi
  if ! git -C "$ROOT" rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
    git -C "$ROOT" -c user.name='castbot' -c user.email='14369+scott@users.noreply.github.com' \
      tag -a "$tag" -m "castbot ${tag}" "$sha"
    git -C "$ROOT" push origin "refs/tags/${tag}"
  fi
  gh release create "$tag" --title "$tag" --notes-file "$notes_file" \
    "$dir/out/release.json" \
    "$dir/out/SHA256SUMS" \
    "$dir/out/SHA256SUMS.minisig" \
    "$dir/out/castbot_linux_amd64" \
    "$dir/out/castbot_linux_arm64"
  printf 'published %s\n' "$tag"
}

usage() {
  cat <<'EOF'
Usage: scripts/release.sh <command>

  next-version [major]   Print the next MAJOR.YYYYMMDD.N, or "already <version>"
  notes                  Print redacted subjects since the previous release tag
  require-ci <jobs.json> Exit 0 when test and shots both succeeded
  wait-image <sha>       Print the public runtime image for the commit
  publish                Build, sign, tag, and publish the release
EOF
}

main() {
  local cmd=${1:-}
  case "$cmd" in
    -h | --help)
      usage
      ;;
    next-version)
      next_version "${2:-0}"
      ;;
    notes)
      notes
      ;;
    require-ci)
      [[ $# -eq 2 ]] || die "require-ci needs a jobs file"
      require_ci "$2"
      ;;
    wait-image)
      [[ $# -eq 2 ]] || die "wait-image needs a commit"
      wait_image "$2"
      ;;
    publish)
      publish
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

main "$@"
