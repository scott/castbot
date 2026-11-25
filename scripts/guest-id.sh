#!/usr/bin/env bash
# Print the guest id for this checkout.
# The id is a sha256 over the guest inputs, one sorted file per line.
# Test files are left out. The fixed path list matches scripts/runtime-image.sh
# and the image gate in scripts/deploy.sh.
set -euo pipefail

die() {
  printf 'guest-id: %s\n' "$*" >&2
  exit 1
}

fixed_inputs() {
  printf '%s\n' Dockerfile deploy go.mod go.sum cmd/castlet cmd/castbot-guest cmd/cast-standin cmd/castbot-toolrun internal/castlet
}

ROOT=$(git rev-parse --show-toplevel)
cd "$ROOT"

mod=$(awk '$1 == "module" { print $2; exit }' go.mod)
[[ -n "$mod" ]] || die "go.mod has no module path"

deps=$(
  GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local go list -deps \
    -f '{{if not .Standard}}{{.ImportPath}}{{end}}' \
    ./cmd/castlet ./cmd/castbot-guest ./cmd/cast-standin ./cmd/castbot-toolrun
) || die "could not list runtime image packages"

list=$(mktemp)
sums=$(mktemp)
trap 'rm -f "$list" "$sums"' EXIT
{
  fixed_inputs
  printf '%s\n' "$deps" | awk -v mod="$mod" 'index($0, mod "/") == 1 { print substr($0, length(mod) + 2) }'
} | awk 'NF && !seen[$0]++' | while IFS= read -r rel; do
  [[ "$rel" =~ ^[A-Za-z0-9._/-]+$ ]] || die "runtime image package path is unexpected"
  if [[ -f "$rel" ]]; then
    case "$rel" in
      *_test.go) ;;
      *) printf '%s\n' "$rel" ;;
    esac
  elif [[ -d "$rel" ]]; then
    find "$rel" -type f ! -name '*_test.go' -print
  fi
done | sort -u >"$list"

[[ -s "$list" ]] || die "guest inputs are empty"

while IFS= read -r rel; do
  [[ "$rel" =~ ^[A-Za-z0-9._/-]+$ ]] || die "runtime image package path is unexpected"
  sum=$(sha256sum -- "$rel" | awk '{ print $1 }')
  printf '%s  %s\n' "$sum" "$rel"
done <"$list" >"$sums"
sha256sum -- "$sums" | awk '{ print $1 }'
