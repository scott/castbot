#!/usr/bin/env bash
# Exercise scripts/runtime-image.sh without a registry.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
script="$root/scripts/runtime-image.sh"
if [[ -x /usr/local/go/bin/go ]]; then
  export PATH="/usr/local/go/bin:$PATH"
fi

fail() {
  printf 'FAIL %s\n' "$*" >&2
  exit 1
}

bash -n "$script"
command -v go >/dev/null 2>&1 || fail "go is missing"
command -v git >/dev/null 2>&1 || fail "git is missing"

grep -F 'Dockerfile deploy go.mod go.sum cmd/castlet cmd/castbot-guest cmd/cast-standin internal/castlet' "$script" >/dev/null || fail "fixed inputs"
grep -F 'Dockerfile deploy go.mod go.sum cmd/castlet cmd/castbot-guest cmd/cast-standin internal/castlet' "$root/scripts/deploy.sh" >/dev/null || fail "deploy inputs differ"

hdr=$(mktemp)
printf '%s\n' 'Link: </v2/scott/castbot-runtime/tags/list?n=100&last=abc>; rel="next"' >"$hdr"
page=$(
  # shellcheck disable=SC1090
  source "$script"
  next_page "$hdr"
)
[[ "$page" == "/v2/scott/castbot-runtime/tags/list?n=100&last=abc" ]] || fail "relative link"
printf '%s\n' 'Link: <https://ghcr.io/v2/scott/castbot-runtime/tags/list?n=100>; rel="next"' >"$hdr"
page=$(
  # shellcheck disable=SC1090
  source "$script"
  next_page "$hdr"
)
[[ "$page" == "https://ghcr.io/v2/scott/castbot-runtime/tags/list?n=100" ]] || fail "absolute link"
rm -f "$hdr"

commit_at() {
  local repo=$1 when=$2 msg=$3
  git -C "$repo" add -A
  GIT_AUTHOR_DATE="$when" GIT_COMMITTER_DATE="$when" \
    git -C "$repo" -c user.email=test@example.com -c user.name=Test \
    commit -q -m "$msg"
  git -C "$repo" rev-parse HEAD
}

seed_repo() {
  local repo=$1
  git init -q -b main "$repo"
  mkdir -p "$repo/cmd/castlet" "$repo/cmd/castbot-guest" "$repo/cmd/cast-standin" "$repo/internal/castlet" "$repo/deploy"
  printf 'module example.com/castbot\n\ngo 1.27.4\n' >"$repo/go.mod"
  printf 'FROM scratch\n# one\n' >"$repo/Dockerfile"
  printf 'package main\n\nfunc main() {}\n' >"$repo/cmd/castlet/main.go"
  printf 'package main\n\nfunc main() {}\n' >"$repo/cmd/castbot-guest/main.go"
  printf 'package main\n\nfunc main() {}\n' >"$repo/cmd/cast-standin/main.go"
  printf 'package castlet\n' >"$repo/internal/castlet/doc.go"
  printf 'pin\n' >"$repo/deploy/pin.txt"
}

select_ref() {
  local repo=$1 tags=$2 sha=$3
  CASTBOT_RUNTIME_REPO="$repo" \
    CASTBOT_RUNTIME_TAGS_FILE="$tags" \
    CASTBOT_RUNTIME_REGISTRY=example.invalid/castbot-runtime \
    bash "$script" select "$sha"
}

repo=$(mktemp -d)
seed_repo "$repo"
old=$(commit_at "$repo" '2020-01-01T00:00:00Z' 'base')
printf 'package castlet\n\nimport "testing"\n\nfunc TestMarker(t *testing.T) {}\n' >"$repo/internal/castlet/doc_test.go"
printf 'notes\n' >"$repo/README"
head=$(commit_at "$repo" '2020-06-01T00:00:00Z' 'readme')
git -C "$repo" checkout -q -b side "$old"
printf 'FROM scratch\n# two\n' >"$repo/Dockerfile"
mid=$(commit_at "$repo" '2021-01-01T00:00:00Z' 'dockerfile')
git -C "$repo" checkout -q main

tags=$(mktemp)
printf '%s\n' "$old" "$mid" >"$tags"
got=$(select_ref "$repo" "$tags" "$head")
[[ "$got" == "example.invalid/castbot-runtime:${old}" ]] || fail "newer image inputs differ: $got"

printf '%s\n' "$old" "$head" >"$tags"
got=$(select_ref "$repo" "$tags" "$head")
[[ "$got" == "example.invalid/castbot-runtime:${head}" ]] || fail "exact tag: $got"

git -C "$repo" config uploadpack.allowReachableSHA1InWant true
shallow=$(mktemp -d)
git clone -q --depth 1 "file://${repo}" "$shallow"
if git -C "$shallow" cat-file -e "${old}^{commit}" 2>/dev/null; then
  fail "shallow clone already has the image commit"
fi
printf '%s\n' "$old" >"$tags"
got=$(select_ref "$shallow" "$tags" "$head")
[[ "$got" == "example.invalid/castbot-runtime:${old}" ]] || fail "shallow fetch: $got"
rm -rf "$shallow"

printf 'package main\n\nimport _ "example.com/castbot/internal/guestwrap"\n\nfunc main() {}\n' >"$repo/cmd/castlet/main.go"
mkdir -p "$repo/internal/guestwrap"
printf 'package guestwrap\n\nconst Marker = 1\n' >"$repo/internal/guestwrap/doc.go"
changed=$(commit_at "$repo" '2022-01-01T00:00:00Z' 'guest import')
printf '%s\n' "$old" >"$tags"
set +e
got=$(select_ref "$repo" "$tags" "$changed" 2>/tmp/castbot-runtime-image.err)
code=$?
set -e
[[ "$code" -eq 1 ]] || fail "guest change should miss, code $code"
[[ -z "$got" ]] || fail "guest change printed $got"
[[ ! -s /tmp/castbot-runtime-image.err ]] || fail "guest change wrote an error"

set +e
bash "$script" select abc >/tmp/castbot-runtime-image.out 2>/tmp/castbot-runtime-image.err
code=$?
set -e
[[ "$code" -eq 1 ]] || fail "bad sha code"
grep -q '40 hex' /tmp/castbot-runtime-image.err || fail "bad sha message"

stub=$(mktemp -d)
cat >"$stub/curl" <<'EOF'
#!/bin/sh
printf '403'
EOF
chmod 0755 "$stub/curl"
set +e
got=$(PATH="$stub:$PATH" CASTBOT_RUNTIME_REPO="$repo" CASTBOT_RUNTIME_REGISTRY=ghcr.io/scott/castbot-runtime bash "$script" select "$head" 2>/tmp/castbot-runtime-image.err)
code=$?
set -e
[[ "$code" -eq 1 ]] || fail "denied registry code $code"
[[ -z "$got" ]] || fail "denied registry printed $got"
[[ ! -s /tmp/castbot-runtime-image.err ]] || fail "denied registry wrote an error"
printf '%s\n' '#!/bin/sh' 'exit 1' >"$stub/curl"
set +e
PATH="$stub:$PATH" \
  CASTBOT_RUNTIME_REPO="$repo" \
  CASTBOT_RUNTIME_REGISTRY=ghcr.io/scott/castbot-runtime \
  bash "$script" select "$head" >/tmp/castbot-runtime-image.out 2>/tmp/castbot-runtime-image.err
code=$?
set -e
[[ "$code" -eq 1 ]] || fail "curl failure code"
grep -q 'could not reach the runtime image registry' /tmp/castbot-runtime-image.err || fail "curl failure message"
rm -rf "$stub" "$repo"
rm -f "$tags" /tmp/castbot-runtime-image.out /tmp/castbot-runtime-image.err
printf 'runtime image tests passed\n'
