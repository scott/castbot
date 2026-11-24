#!/usr/bin/env bash
# Version numbers, redacted notes, the CI job check, and the image wait.
set -euo pipefail

fail() {
  printf 'FAIL %s\n' "$*" >&2
  exit 1
}

ROOT=$(cd "$(dirname "$0")/.." && pwd)
REL="$ROOT/scripts/release.sh"

pub=$(awk 'NR==2' "$ROOT/internal/update/minisign.pub")
grep -F "$pub" "$ROOT/scripts/install.sh" >/dev/null || fail "install key"

repo=$(mktemp -d)
git -C "$repo" init -q
git -C "$repo" config user.email 'test@example.com'
git -C "$repo" config user.name 'test'
printf 'a\n' >"$repo/a"
git -C "$repo" add a
git -C "$repo" commit -q -m 'note TOKEN=secretvalue'

note_out=$(cd "$repo" && bash "$REL" notes)
printf '%s\n' "$note_out" | grep -q 'TOKEN=\*\*\*' || fail "token not redacted"
if printf '%s\n' "$note_out" | grep -q 'secretvalue'; then
  fail "secret left in notes"
fi

first=$(cd "$repo" && bash "$REL" next-version 0)
[[ "$first" =~ ^0\.[0-9]{8}\.1$ ]] || fail "first version $first"
git -C "$repo" tag "v${first}"
again=$(cd "$repo" && bash "$REL" next-version 0)
[[ "$again" == "already ${first}" ]] || fail "already $again"

printf 'b\n' >>"$repo/a"
git -C "$repo" add a
git -C "$repo" commit -q -m 'second'
second=$(cd "$repo" && bash "$REL" next-version 0)
[[ "$second" =~ ^0\.[0-9]{8}\.2$ ]] || fail "second version $second"
major=$(cd "$repo" && bash "$REL" next-version 1)
[[ "$major" =~ ^1\.[0-9]{8}\.1$ ]] || fail "major version $major"

ok=$(mktemp)
bad=$(mktemp)
printf '%s\n' '{"jobs":[{"name":"test","conclusion":"success"},{"name":"shots","conclusion":"success"}]}' >"$ok"
printf '%s\n' '{"jobs":[{"name":"test","conclusion":"success"},{"name":"shots","conclusion":"failure"}]}' >"$bad"
bash "$REL" require-ci "$ok" || fail "green ci"
if bash "$REL" require-ci "$bad" >/dev/null 2>&1; then
  fail "red ci accepted"
fi

sha=$(git -C "$ROOT" rev-parse HEAD)
tags=$(mktemp)
printf '%s\n' "$sha" >"$tags"
got=$(CASTBOT_RUNTIME_TAGS_FILE="$tags" CASTBOT_RELEASE_IMAGE_WAIT=0 bash "$REL" wait-image "$sha")
[[ "$got" == "ghcr.io/scott/castbot-runtime:${sha}" ]] || fail "image $got"

empty=$(mktemp)
: >"$empty"
if CASTBOT_RUNTIME_TAGS_FILE="$empty" CASTBOT_RELEASE_IMAGE_WAIT=0 bash "$REL" wait-image "$sha" >/dev/null 2>&1; then
  fail "missing image accepted"
fi

if bash -x "$REL" next-version >/dev/null 2>&1; then
  fail "xtrace accepted"
fi

printf 'release tests passed\n'
