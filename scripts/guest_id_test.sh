#!/usr/bin/env bash
# guest-id is stable and uses the same fixed inputs as the image gate.
set -euo pipefail

fail() {
  printf 'FAIL %s\n' "$*" >&2
  exit 1
}

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

id1=$(bash scripts/guest-id.sh)
id2=$(bash scripts/guest-id.sh)
[[ "$id1" =~ ^[0-9a-f]{64}$ ]] || fail "guest id shape"
[[ "$id1" == "$id2" ]] || fail "guest id changed"

line="printf '%s\n' Dockerfile deploy go.mod go.sum cmd/castlet cmd/castbot-guest cmd/cast-standin cmd/castbot-toolrun internal/castlet"
grep -F "$line" scripts/guest-id.sh >/dev/null || fail "guest-id inputs"
grep -F "$line" scripts/runtime-image.sh >/dev/null || fail "runtime-image inputs"
grep -F "$line" scripts/deploy.sh >/dev/null || fail "deploy inputs"

printf 'guest-id tests passed\n'
