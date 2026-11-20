#!/usr/bin/env bash
# Exercise scripts/install.sh without touching the real host.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
install="$root/scripts/install.sh"
fail() {
  printf 'FAIL %s\n' "$*" >&2
  exit 1
}

bash -n "$install"

help=$(bash "$install" --help)
printf '%s\n' "$help" | grep -q 'curl -fsSL https://raw.githubusercontent.com/scott/castbot/main/scripts/install.sh' || fail "help missing curl"
printf '%s\n' "$help" | grep -q 'CastBot' || fail "help missing CastBot"

set +e
bash -x "$install" --help >/tmp/castbot-install-xtrace.out 2>/tmp/castbot-install-xtrace.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "xtrace should refuse"
grep -q 'refusing to run under xtrace' /tmp/castbot-install-xtrace.err || fail "xtrace message"

set +e
bash "$install" extra >/tmp/castbot-install-args.out 2>/tmp/castbot-install-args.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "extra args should refuse"
grep -q 'takes no arguments' /tmp/castbot-install-args.err || fail "args message"

copy=$(mktemp)
cp "$install" "$copy"
chmod 0755 "$copy"
set +e
su nobody -s /bin/bash -c "bash $(printf '%q' "$copy")" >/tmp/castbot-install-nobody.out 2>/tmp/castbot-install-nobody.err
code=$?
set -e
rm -f "$copy"
[[ "$code" -ne 0 ]] || fail "non-root should refuse"
grep -q 'run as root' /tmp/castbot-install-nobody.err || fail "root message"

sha=0123456789abcdef0123456789abcdef01234567
password=correct-horse-1
token='install-test-machine-token'
email=admin@example.com
name='Ada Owner'

run_install() {
  local prefix=$1
  shift
  env \
    CASTBOT_INSTALL_PREFIX="$prefix" \
    CASTBOT_INSTALL_NONINTERACTIVE=1 \
    CASTBOT_ADMIN_EMAIL="$email" \
    CASTBOT_ADMIN_NAME="$name" \
    CASTBOT_ADMIN_PASSWORD="$password" \
    CASTBOT_SHA="$sha" \
    CASTBOT_STUB_LOG="$prefix/stub.log" \
    CASTBOT_STUB_USER="$prefix/stub.user" \
    "$@" \
    bash "$install"
}

expect_fail() {
  local prefix=$1
  shift
  set +e
  run_install "$prefix" "$@" >/tmp/castbot-install-fail.out 2>/tmp/castbot-install-fail.err
  local code=$?
  set -e
  [[ "$code" -ne 0 ]] || fail "expected failure: $*"
  [[ ! -d "$prefix/opt/castbot" ]] || fail "failed run created the app dir"
}

prefix=$(mktemp -d)
expect_fail "$prefix" CASTBOT_ADMIN_EMAIL=
grep -q 'email is missing' /tmp/castbot-install-fail.err || fail "missing email"
rm -rf "$prefix"

prefix=$(mktemp -d)
expect_fail "$prefix" CASTBOT_ADMIN_EMAIL="$email" CASTBOT_ADMIN_NAME="$name" CASTBOT_ADMIN_PASSWORD=short
grep -q 'password must be 12' /tmp/castbot-install-fail.err || fail "short password"
rm -rf "$prefix"

prefix=$(mktemp -d)
expect_fail "$prefix" \
  CASTBOT_ADMIN_EMAIL="$email" \
  CASTBOT_ADMIN_NAME="$name" \
  CASTBOT_ADMIN_PASSWORD="$password" \
  CASTBOT_PUBLIC_HOST='not a host' \
  CASTBOT_BINARY=/bin/true
grep -q 'public hostname' /tmp/castbot-install-fail.err || fail "bad host"
rm -rf "$prefix"

prefix=$(mktemp -d)
expect_fail "$prefix" \
  CASTBOT_ADMIN_EMAIL="$email" \
  CASTBOT_ADMIN_NAME="$name" \
  CASTBOT_ADMIN_PASSWORD="$password" \
  CASTBOT_PUBLIC_HOST='bot.example.com:443' \
  CASTBOT_BINARY=/bin/true
grep -q 'must not include a port' /tmp/castbot-install-fail.err || fail "port host"
rm -rf "$prefix"

prefix=$(mktemp -d)
expect_fail "$prefix" \
  CASTBOT_ADMIN_EMAIL="$email" \
  CASTBOT_ADMIN_NAME="$name" \
  CASTBOT_ADMIN_PASSWORD="$password" \
  CASTBOT_REF='--upload-pack=evil' \
  CASTBOT_BINARY=/bin/true
grep -q 'source ref is not valid' /tmp/castbot-install-fail.err || fail "bad ref"
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
cat > "$stub" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CASTBOT_STUB_LOG"
if [ "$1" = "create-user" ]; then
  if [ -f "$CASTBOT_STUB_USER" ]; then
    echo "account-exists" >> "$CASTBOT_STUB_LOG"
    echo "an account already exists" >&2
    exit 1
  fi
  : > "$CASTBOT_STUB_USER"
  read -r pw || true
  printf 'pw-len %s\n' "${#pw}" >> "$CASTBOT_STUB_LOG"
  echo "created admin@example.com"
  echo "Authenticator secret: TESTSECRET234567"
  echo "otpauth://totp/castbot:admin?secret=TESTSECRET234567"
  exit 0
fi
if [ "$1" = "create-assistant" ]; then
  echo "created CastBot abcdef"
  echo "/agents/abcdef"
  exit 0
fi
echo "unknown command" >&2
exit 2
EOF
chmod 0755 "$stub"
: > "$prefix/stub.log"

out=$(run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='https://bot.example.com/login' \
  CASTBOT_DO_TOKEN="$token" \
  CASTBOT_IMAGE='example.invalid/castbot-runtime:test' \
  CASTBOT_BINARY="$stub")

printf '%s\n' "$out" | grep -q 'created CastBot abcdef' || fail "assistant output"
printf '%s\n' "$out" | grep -q 'https://bot.example.com/login' || fail "sign-in url"
printf '%s\n' "$out" | grep -q 'TESTSECRET234567' || fail "secret not shown once"
if printf '%s\n' "$out" | grep -q "$password"; then
  fail "password in output"
fi
if printf '%s\n' "$out" | grep -q "$token"; then
  fail "token in output"
fi

[[ -f "$prefix/opt/castbot/RELEASE" ]] || fail "missing RELEASE"
[[ "$(tr -d '[:space:]' < "$prefix/opt/castbot/RELEASE")" == "$sha" ]] || fail "RELEASE sha"
[[ -x "$prefix/opt/castbot/releases/$sha/castbot" ]] || fail "binary mode"
[[ "$(readlink "$prefix/opt/castbot/bin/castbot")" == "$prefix/opt/castbot/releases/$sha/castbot" ]] || fail "symlink"

envfile="$prefix/etc/castbot/castbot.env"
[[ "$(stat -c '%a' "$envfile")" == "600" ]] || fail "env mode"
grep -q '^CASTBOT_SESSION_KEY=' "$envfile" || fail "session key"
grep -q '^CASTBOT_SECRETS_KEY=' "$envfile" || fail "secrets key"
grep -q '^CASTBOT_PUBLIC_URL=https://bot.example.com$' "$envfile" || fail "public url"
grep -q '^CASTBOT_TRUSTED_PROXIES=127.0.0.1/32$' "$envfile" || fail "trusted proxies"
grep -q "^CASTBOT_DO_TOKEN=${token}$" "$envfile" || fail "token stored"
grep -q '^CASTBOT_IMAGE=example.invalid/castbot-runtime:test$' "$envfile" || fail "image"
grep -q "^CASTBOT_DB=${prefix}/opt/castbot/data/castbot.db$" "$envfile" || fail "db path"
session=$(sed -n 's/^CASTBOT_SESSION_KEY=//p' "$envfile")
secrets=$(sed -n 's/^CASTBOT_SECRETS_KEY=//p' "$envfile")
[[ ${#session} -eq 64 && ${#secrets} -eq 64 && "$session" != "$secrets" ]] || fail "keys"
[[ "$session" =~ ^[0-9a-f]{64}$ && "$secrets" =~ ^[0-9a-f]{64}$ ]] || fail "key hex"

note="$prefix/root/castbot-first-login.txt"
[[ "$(stat -c '%a' "$note")" == "600" ]] || fail "note mode"
grep -q 'TESTSECRET234567' "$note" || fail "note secret"
grep -q "Email: ${email}" "$note" || fail "note email"
if grep -q "$password" "$note"; then
  fail "password in note"
fi

unit="$prefix/etc/systemd/system/castbot.service"
grep -q '^User=castbot$' "$unit" || fail "unit user"
grep -q "^Environment=CASTBOT_ENV_FILE=${envfile}$" "$unit" || fail "unit env"
grep -q "^ExecStart=${prefix}/opt/castbot/bin/castbot serve$" "$unit" || fail "unit start"
if grep -q "$session" "$unit" || grep -q "$token" "$unit"; then
  fail "secret in unit"
fi

caddy="$prefix/etc/caddy/Caddyfile"
grep -q '^bot.example.com {$' "$caddy" || fail "caddy host"
grep -q 'reverse_proxy 127.0.0.1:8080' "$caddy" || fail "caddy proxy"
if grep -q "$session" "$caddy" || grep -q "$token" "$caddy"; then
  fail "secret in caddy"
fi

grep -q 'create-user --email admin@example.com --name Ada Owner --workspace Personal' "$prefix/stub.log" || fail "create-user args"
grep -q 'create-assistant --email admin@example.com' "$prefix/stub.log" || fail "create-assistant args"
grep -q 'pw-len 15' "$prefix/stub.log" || fail "password length"
if grep -q "$password" "$prefix/stub.log"; then
  fail "password in stub log"
fi

sum_before=$(sha256sum "$envfile")
out2=$(run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='https://bot.example.com/login' \
  CASTBOT_DO_TOKEN='install-test-other-token' \
  CASTBOT_IMAGE='example.invalid/castbot-runtime:other' \
  CASTBOT_BINARY="$stub")
if printf '%s\n' "$out2" | grep -q 'TESTSECRET234567'; then
  fail "secret printed again"
fi
if printf '%s\n' "$out" | grep -q "$session" || printf '%s\n' "$out2" | grep -q "$session"; then
  fail "session key in output"
fi
[[ "$(sha256sum "$envfile")" == "$sum_before" ]] || fail "env file changed"
grep -q 'account-exists' "$prefix/stub.log" || fail "second create-user"
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
cat > "$stub" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CASTBOT_STUB_LOG"
if [ "$1" = "create-user" ]; then
  read -r _ || true
  echo "Authenticator secret: TESTSECRET234567"
  echo "otpauth://totp/castbot:admin?secret=TESTSECRET234567"
  exit 0
fi
if [ "$1" = "create-assistant" ]; then
  echo "created CastBot abcdef"
  exit 0
fi
exit 2
EOF
chmod 0755 "$stub"
: > "$prefix/stub.log"
out=$(run_install "$prefix" CASTBOT_BINARY="$stub")
printf '%s\n' "$out" | grep -q '127.0.0.1:8080' || fail "local listen"
[[ ! -e "$prefix/etc/caddy/Caddyfile" ]] || fail "caddy without a host"
envfile="$prefix/etc/castbot/castbot.env"
if grep -q 'CASTBOT_PUBLIC_URL=' "$envfile" || grep -q 'CASTBOT_TRUSTED_PROXIES=' "$envfile" || grep -q 'CASTBOT_DO_TOKEN=' "$envfile"; then
  fail "local env has public settings"
fi
rm -rf "$prefix"

rm -f /tmp/castbot-install-xtrace.out /tmp/castbot-install-xtrace.err \
  /tmp/castbot-install-args.out /tmp/castbot-install-args.err \
  /tmp/castbot-install-nobody.out /tmp/castbot-install-nobody.err \
  /tmp/castbot-install-fail.out /tmp/castbot-install-fail.err

printf 'install tests passed\n'
