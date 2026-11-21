#!/usr/bin/env bash
# Choose the public runtime image whose guest inputs match a commit.
# Tags are full commits on ghcr.io/scott/castbot-runtime. A tag list file
# replaces the registry lookup in tests.
#
#   scripts/runtime-image.sh select <40-hex-sha>
#
# Prints one image reference, or exits 1 when no published tag matches.
# Operational failures print to stderr and exit 1.
set -euo pipefail

die() {
  printf 'runtime-image: %s\n' "$*" >&2
  exit 1
}

registry() {
  printf '%s' "${CASTBOT_RUNTIME_REGISTRY:-ghcr.io/scott/castbot-runtime}"
}

repo_dir() {
  if [[ -n "${CASTBOT_RUNTIME_REPO:-}" ]]; then
    printf '%s' "$CASTBOT_RUNTIME_REPO"
    return 0
  fi
  git rev-parse --show-toplevel
}

registry_path() {
  local reg host path
  reg=$(registry)
  host=${reg%%/*}
  path=${reg#*/}
  [[ "$host" == "ghcr.io" ]] || die "runtime registry host is not supported"
  [[ "$path" =~ ^[a-z0-9]+/[a-z0-9._-]+$ ]] || die "runtime registry name is not valid"
  printf '%s' "$path"
}

# Fixed inputs plus the packages imported by the guest binaries.
# Keep this list in sync with the image gate in scripts/deploy.sh.
fixed_inputs() {
  printf '%s\n' Dockerfile deploy go.mod go.sum cmd/castlet cmd/castbot-guest cmd/cast-standin internal/castlet
}

ensure_commit() {
  local repo=$1 sha=$2
  if git -C "$repo" cat-file -e "${sha}^{commit}" 2>/dev/null; then
    return 0
  fi
  if ! git -C "$repo" remote get-url origin >/dev/null 2>&1; then
    return 1
  fi
  GIT_TERMINAL_PROMPT=0 git -C "$repo" fetch --depth 1 origin "$sha" >/dev/null 2>&1 || return 1
  git -C "$repo" cat-file -e "${sha}^{commit}" 2>/dev/null
}

guest_rel_paths() {
  local repo=$1 sha=$2 wt mod out status
  wt=$(mktemp -d)
  if ! git -C "$repo" worktree add --detach "$wt" "$sha" >/dev/null 2>&1; then
    rm -rf "$wt"
    die "could not check out a commit to list runtime packages"
  fi
  out=$(mktemp)
  set +e
  (
    cd "$wt"
    GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./cmd/castlet ./cmd/castbot-guest ./cmd/cast-standin
  ) >"$out" 2>&1
  status=$?
  set -e
  mod=$(awk '$1 == "module" { print $2; exit }' "$wt/go.mod")
  git -C "$repo" worktree remove --force "$wt" >/dev/null 2>&1 || true
  rm -rf "$wt"
  if [[ "$status" != "0" ]]; then
    cat "$out" >&2
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

inputs_match() {
  local repo=$1 image=$2 head=$3
  local paths p names
  if [[ "$image" == "$head" ]]; then
    return 0
  fi
  paths=$(
    {
      fixed_inputs
      guest_rel_paths "$repo" "$image"
      guest_rel_paths "$repo" "$head"
    } | awk 'NF && !seen[$0]++'
  )
  local -a path_args=()
  while IFS= read -r p; do
    [[ -n "$p" ]] || continue
    [[ "$p" =~ ^[A-Za-z0-9._/-]+$ ]] || die "runtime image package path is unexpected"
    path_args+=("$p")
  done <<<"$paths"
  names=$(git -C "$repo" diff --name-only "$image" "$head" -- "${path_args[@]}" ':(exclude,glob)**/*_test.go') || die "could not diff the runtime image inputs"
  [[ -z "$names" ]]
}

next_page() {
  local hdr=$1
  awk '
    BEGIN { IGNORECASE = 1 }
    /^link:/ {
      if (match($0, /<[^>]+>;[[:space:]]*rel="next"/)) {
        s = substr($0, RSTART + 1, RLENGTH - 1)
        sub(/>;[[:space:]]*rel="next"$/, "", s)
        print s
        exit
      }
    }
  ' "$hdr"
}

list_shas_registry() {
  local path body hdr code token url page
  path=$(registry_path)
  body=$(mktemp)
  hdr=$(mktemp)
  code=$(curl -sS -o "$body" -w '%{http_code}' \
    "https://ghcr.io/token?service=ghcr.io&scope=repository:${path}:pull") || {
    rm -f "$body" "$hdr"
    die "could not reach the runtime image registry"
  }
  # A missing package and a private package both deny an anonymous token.
  # That is an empty public tag list, so the publish job can push the first image.
  if [[ "$code" == "401" || "$code" == "403" ]]; then
    rm -f "$body" "$hdr"
    return 0
  fi
  if [[ "$code" != "200" ]]; then
    rm -f "$body" "$hdr"
    die "could not reach the runtime image registry"
  fi
  token=$(sed -n 's/.*"token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$body")
  : >"$body"
  if [[ -z "$token" ]]; then
    rm -f "$body" "$hdr"
    die "could not reach the runtime image registry"
  fi
  url="https://ghcr.io/v2/${path}/tags/list?n=100"
  page=0
  while [[ -n "$url" ]]; do
    page=$((page + 1))
    if [[ "$page" -gt 50 ]]; then
      rm -f "$body" "$hdr"
      unset token
      die "runtime image tag list is too long"
    fi
    : >"$hdr"
    code=$(curl -sS -o "$body" -D "$hdr" -w '%{http_code}' \
      -H "Authorization: Bearer ${token}" \
      "$url") || {
      rm -f "$body" "$hdr"
      unset token
      die "could not list runtime images"
    }
    if [[ "$code" == "404" ]]; then
      break
    fi
    if [[ "$code" == "401" || "$code" == "403" ]]; then
      rm -f "$body" "$hdr"
      unset token
      die "runtime image registry is not public"
    fi
    if [[ "$code" != "200" ]]; then
      rm -f "$body" "$hdr"
      unset token
      die "could not list runtime images"
    fi
    grep -oE '"[0-9a-f]{40}"' "$body" | tr -d '"' || true
    url=$(next_page "$hdr")
    if [[ -n "$url" && "$url" != https://* ]]; then
      url="https://ghcr.io${url}"
    fi
  done
  rm -f "$body" "$hdr"
  unset token
}

list_shas() {
  if [[ -n "${CASTBOT_RUNTIME_TAGS_FILE:-}" ]]; then
    [[ -f "$CASTBOT_RUNTIME_TAGS_FILE" ]] || die "runtime image tag list is missing"
    grep -E '^[0-9a-f]{40}$' "$CASTBOT_RUNTIME_TAGS_FILE" || true
    return 0
  fi
  list_shas_registry
}

select_image() {
  local head=$1 repo listed scored sha ts best
  [[ "$head" =~ ^[0-9a-f]{40}$ ]] || die "commit must be 40 hex characters"
  repo=$(repo_dir)
  [[ -d "$repo/.git" || -f "$repo/.git" ]] || die "runtime image checkout is missing"
  ensure_commit "$repo" "$head" || die "commit is not in the checkout"
  listed=$(mktemp)
  scored=$(mktemp)
  list_shas >"$listed"
  while IFS= read -r sha || [[ -n "${sha:-}" ]]; do
    [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || continue
    if ! ensure_commit "$repo" "$sha"; then
      continue
    fi
    ts=$(git -C "$repo" log -1 --format=%ct "$sha")
    printf '%s %s\n' "$ts" "$sha"
  done <"$listed" | sort -nr >"$scored"
  rm -f "$listed"
  best=""
  while read -r _ sha; do
    [[ -n "$sha" ]] || continue
    if inputs_match "$repo" "$sha" "$head"; then
      best=$sha
      break
    fi
  done <"$scored"
  rm -f "$scored"
  [[ -n "$best" ]] || return 1
  printf '%s:%s\n' "$(registry)" "$best"
}

usage() {
  cat <<'EOF'
Usage: runtime-image.sh select <40-hex-sha>

Print the newest published runtime image whose guest inputs match the commit.
EOF
}

main() {
  local cmd=${1:-}
  case "$cmd" in
    -h|--help)
      usage
      ;;
    select)
      [[ $# -eq 2 ]] || die "select needs one commit"
      select_image "$2"
      ;;
    *)
      usage >&2
      exit 2
      ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
