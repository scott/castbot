#!/usr/bin/env bash
# Exercise scripts/install.sh without touching the real host.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
install="$root/scripts/install.sh"
fail() {
  printf 'FAIL %s\n' "$*" >&2
  exit 1
}

assert_cmd_order() {
  local file=$1
  local prev=0 line needle
  shift
  for needle in "$@"; do
    line=$(grep -n -F -m 1 -- "$needle" "$file" | cut -d: -f1 || true)
    [[ -n "$line" ]] || fail "missing command: $needle"
    [[ "$line" -gt "$prev" ]] || fail "command out of order: $needle"
    prev=$line
  done
}

cmd_count() {
  local n
  n=$(grep -c -F -- "$1" "$2" || true)
  printf '%s\n' "$n"
}

write_user_stub() {
  local stub=$1
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
echo "unknown command" >&2
exit 2
EOF
  chmod 0755 "$stub"
}

bash -n "$install"

awk '
  /systemctl daemon-reload/ { seen = 1 }
  /systemctl stop castbot/ && !seen { exit 1 }
' "$install" || fail "daemon-reload before stop"
awk '
  /caddy validate --config/ { v = NR }
  /systemctl enable caddy$/ { e = NR }
  /systemctl reload-or-restart caddy$/ { r = NR }
  END { if (v == 0 || e == 0 || r == 0 || v > e || e > r) exit 1 }
' "$install" || fail "caddy command order"

help=$(bash "$install" --help)
printf '%s\n' "$help" | grep -q 'curl -fsSL https://raw.githubusercontent.com/scott/castbot/main/scripts/install.sh' || fail "help missing curl"
printf '%s\n' "$help" | grep -q 'CastBot' || fail "help missing CastBot"
printf '%s\n' "$help" | grep -q 'DigitalOcean' || fail "help missing provider"
printf '%s\n' "$help" | grep -q 'Machine host' || fail "help missing machine host"
printf '%s\n' "$help" | grep -q 'API token' || fail "help missing API token"
printf '%s\n' "$help" | grep -q 'Model provider' || fail "help missing model provider"
printf '%s\n' "$help" | grep -q 'model access key' || fail "help missing model access key"
if printf '%s\n' "$help" | grep -Eiq 'sprite|docker'; then
  fail "help offers an unfinished machine host"
fi
printf '%s\n' "$help" | grep -q 'OpenRouter' || fail "help missing OpenRouter"
printf '%s\n' "$help" | grep -q 'OpenAI' || fail "help missing OpenAI"
printf '%s\n' "$help" | grep -q 'Anthropic' || fail "help missing Anthropic"
printf '%s\n' "$help" | grep -q 'Google Gemini' || fail "help missing Gemini"
printf '%s\n' "$help" | grep -q 'xAI' || fail "help missing xAI"
printf '%s\n' "$help" | grep -q 'model key' || fail "help missing model key"
printf '%s\n' "$help" | grep -q '12 to 1024' || fail "help missing password length"
printf '%s\n' "$help" | grep -q 'Two-factor can be turned on later in Settings' || fail "help missing two-factor"

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

# GitHub Actions is not root, so su cannot drop privileges there.
# Run the installer as the current user in that case. As root, drop to nobody.
if [[ "$(id -u)" -eq 0 ]]; then
  copy=$(mktemp)
  cp "$install" "$copy"
  chmod 0755 "$copy"
  set +e
  su nobody -s /bin/bash -c "bash $(printf '%q' "$copy")" >/tmp/castbot-install-nobody.out 2>/tmp/castbot-install-nobody.err
  code=$?
  set -e
  rm -f "$copy"
else
  set +e
  bash "$install" >/tmp/castbot-install-nobody.out 2>/tmp/castbot-install-nobody.err
  code=$?
  set -e
fi
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
[[ "$(tr -d '[:space:]' < "$prefix/opt/castbot/VERSION")" == "0.0.0+$sha" ]] || fail "VERSION"
[[ "$(tr -d '[:space:]' < "$prefix/opt/castbot/releases/$sha/VERSION")" == "0.0.0+$sha" ]] || fail "release VERSION"
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
if grep -q 'CASTBOT_MODEL' "$envfile" || grep -q 'CASTBOT_PROVIDER_KEY_' "$envfile"; then
  fail "env has a model setting"
fi
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

printf '%s\n' "$out" | grep -q 'castbot is installed.' || fail "success banner"
cmdlog="$prefix/host-cmd.log"
assert_cmd_order "$cmdlog" \
  'systemctl daemon-reload' \
  'systemctl enable --now castbot.service' \
  'systemctl enable caddy' \
  'systemctl reload-or-restart caddy' \
  'curl -fsS --max-time 2 http://127.0.0.1:8080/login' \
  'curl -fsS --max-time 5 https://bot.example.com/login'
if grep -q 'caddy validate' "$cmdlog"; then
  fail "validated without caddy"
fi
if grep -E -q '(^|[[:space:]])(-k|--insecure)([[:space:]]|$)' "$cmdlog"; then
  fail "https check skips certificate verification"
fi
[[ "$(cmd_count 'systemctl reload-or-restart caddy' "$cmdlog")" -eq 1 ]] || fail "caddy reload count"

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
printf '%s\n' "$out2" | grep -q 'castbot is installed.' || fail "re-run success banner"
[[ "$(cmd_count 'systemctl reload-or-restart caddy' "$prefix/host-cmd.log")" -eq 2 ]] || fail "caddy not reloaded on re-run"
assert_cmd_order "$prefix/host-cmd.log" \
  'systemctl daemon-reload' \
  'systemctl reload-or-restart caddy'
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
printf '%s\n' "$out" | grep -q 'castbot is installed.' || fail "local success banner"
[[ ! -e "$prefix/etc/caddy/Caddyfile" ]] || fail "caddy without a host"
if grep -q 'reload-or-restart caddy' "$prefix/host-cmd.log" || grep -q 'https://' "$prefix/host-cmd.log"; then
  fail "public proxy without a host"
fi
grep -q 'curl -fsS --max-time 2 http://127.0.0.1:8080/login' "$prefix/host-cmd.log" || fail "local health check"
envfile="$prefix/etc/castbot/castbot.env"
if grep -q 'CASTBOT_PUBLIC_URL=' "$envfile" || grep -q 'CASTBOT_TRUSTED_PROXIES=' "$envfile" || grep -q 'CASTBOT_DO_TOKEN=' "$envfile"; then
  fail "local env has public settings"
fi
if grep -q 'CASTBOT_IMAGE=' "$envfile"; then
  fail "local env has an image"
fi
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
selector="$prefix/select.sh"
cat > "$selector" <<'EOF'
#!/bin/sh
printf '%s\n' "example.invalid/castbot-runtime:0123456789abcdef0123456789abcdef01234567"
EOF
chmod 0755 "$selector"
mkdir -p "$prefix/repo"
: > "$prefix/stub.log"
out=$(run_install "$prefix" \
  CASTBOT_BINARY="$stub" \
  CASTBOT_RUNTIME_REPO="$prefix/repo" \
  CASTBOT_RUNTIME_IMAGE_SH="$selector")
printf '%s\n' "$out" | grep -q '127.0.0.1:8080' || fail "local listen"
if printf '%s\n' "$out" | grep -q 'Runtime image:'; then
  fail "local install printed an image"
fi
envfile="$prefix/etc/castbot/castbot.env"
if grep -q 'CASTBOT_IMAGE=' "$envfile"; then
  fail "local install stored an image"
fi
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
selector="$prefix/select.sh"
printf '%s\n' '#!/bin/sh' 'printf "%s\n" "example.invalid/castbot-runtime:0123456789abcdef0123456789abcdef01234567"' > "$selector"
chmod 0755 "$selector"
mkdir -p "$prefix/repo"
: > "$prefix/stub.log"
out=$(run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_DO_TOKEN="$token" \
  CASTBOT_BINARY="$stub" \
  CASTBOT_RUNTIME_REPO="$prefix/repo" \
  CASTBOT_RUNTIME_IMAGE_SH="$selector")
printf '%s\n' "$out" | grep -q 'Runtime image: example.invalid/castbot-runtime:0123456789abcdef0123456789abcdef01234567' || fail "selected image output"
envfile="$prefix/etc/castbot/castbot.env"
grep -q '^CASTBOT_IMAGE=example.invalid/castbot-runtime:0123456789abcdef0123456789abcdef01234567$' "$envfile" || fail "selected image"
grep -q '^CASTBOT_PUBLIC_URL=https://bot.example.com$' "$envfile" || fail "selected public url"
if printf '%s\n' "$out" | grep -q "$token"; then
  fail "token in selected output"
fi
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
printf '%s\n' '#!/bin/sh' 'exit 0' > "$stub"
chmod 0755 "$stub"
selector="$prefix/select.sh"
printf '%s\n' '#!/bin/sh' 'exit 1' > "$selector"
chmod 0755 "$selector"
mkdir -p "$prefix/repo"
set +e
run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_DO_TOKEN="$token" \
  CASTBOT_BINARY="$stub" \
  CASTBOT_RUNTIME_REPO="$prefix/repo" \
  CASTBOT_RUNTIME_IMAGE_SH="$selector" >/tmp/castbot-install-fail.out 2>/tmp/castbot-install-fail.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "missing image should refuse"
grep -q 'no published runtime image matches this commit' /tmp/castbot-install-fail.err || fail "missing image message"
[[ ! -e "$prefix/etc/castbot/castbot.env" ]] || fail "missing image wrote an env file"
if grep -q "$token" /tmp/castbot-install-fail.err || grep -q "$token" /tmp/castbot-install-fail.out; then
  fail "token in missing image output"
fi
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
envfile="$prefix/etc/castbot/castbot.env"
session=$(sed -n 's/^CASTBOT_SESSION_KEY=//p' "$envfile")
out2=$(run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_BINARY="$stub")
grep -q '^CASTBOT_PUBLIC_URL=https://bot.example.com$' "$envfile" || fail "second run public url"
grep -q '^CASTBOT_TRUSTED_PROXIES=127.0.0.1/32$' "$envfile" || fail "second run proxies"
if grep -q 'CASTBOT_IMAGE=' "$envfile" || grep -q 'CASTBOT_DO_TOKEN=' "$envfile"; then
  fail "second run stored a machine host"
fi
[[ "$(sed -n 's/^CASTBOT_SESSION_KEY=//p' "$envfile")" == "$session" ]] || fail "second run session key"
if printf '%s\n' "$out2" | grep -q "$session"; then
  fail "session key in second output"
fi
printf '%s\n' "$out2" | grep -q 'https://bot.example.com/login' || fail "second run sign-in url"
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
selector="$prefix/select.sh"
printf '%s\n' '#!/bin/sh' 'exit 1' > "$selector"
chmod 0755 "$selector"
mkdir -p "$prefix/repo"
: > "$prefix/stub.log"
out=$(run_install "$prefix" \
  CASTBOT_BINARY="$stub" \
  CASTBOT_RUNTIME_REPO="$prefix/repo" \
  CASTBOT_RUNTIME_IMAGE_SH="$selector" 2>/tmp/castbot-install-fail.err)
printf '%s\n' "$out" | grep -q '127.0.0.1:8080' || fail "local listen without an image"
if grep -q 'no published runtime image' /tmp/castbot-install-fail.err; then
  fail "local miss treated a missing image as fatal"
fi
envfile="$prefix/etc/castbot/castbot.env"
if grep -q 'CASTBOT_IMAGE=' "$envfile"; then
  fail "local miss stored an image"
fi
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
cat > "$stub" <<'EOF'
#!/bin/sh
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
out=$(run_install "$prefix" \
  CASTBOT_IMAGE='example.invalid/castbot-runtime:test' \
  CASTBOT_BINARY="$stub" 2>/tmp/castbot-install-fail.err)
printf '%s\n' "$out" | grep -q '127.0.0.1:8080' || fail "ignored image listen"
grep -q 'runtime image ignored' /tmp/castbot-install-fail.err || fail "ignored image message"
envfile="$prefix/etc/castbot/castbot.env"
if grep -q 'CASTBOT_IMAGE=' "$envfile"; then
  fail "ignored image was stored"
fi
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
cat > "$stub" <<'EOF'
#!/bin/sh
if [ "$1" = "create-user" ]; then
  read -r _ || true
  echo "created admin@example.com"
  echo "Sign in with this email and the password. Two-factor can be turned on later in Settings."
  exit 0
fi
if [ "$1" = "create-assistant" ]; then
  echo "created CastBot abcdef"
  exit 0
fi
exit 2
EOF
chmod 0755 "$stub"
out=$(run_install "$prefix" CASTBOT_BINARY="$stub")
printf '%s\n' "$out" | grep -q 'Two-factor can be turned on later in Settings' || fail "optional two-factor"
if printf '%s\n' "$out" | grep -q 'Authenticator secret'; then
  fail "secret heading without a secret"
fi
[[ ! -e "$prefix/root/castbot-first-login.txt" ]] || fail "note without a secret"
rm -rf "$prefix"

model_key='fixture-install-model-key'

prefix=$(mktemp -d)
expect_fail "$prefix" CASTBOT_MODEL_PROVIDER=xai
grep -q 'must be set together' /tmp/castbot-install-fail.err || fail "provider alone"
rm -rf "$prefix"

prefix=$(mktemp -d)
expect_fail "$prefix" \
  CASTBOT_MODEL_PROVIDER=xai \
  CASTBOT_MODEL_KEY="$model_key" \
  CASTBOT_MODEL='openai-api/gpt-4o'
grep -q 'model is not valid' /tmp/castbot-install-fail.err || fail "wrong model prefix"
if grep -q "$model_key" /tmp/castbot-install-fail.err || grep -q "$model_key" /tmp/castbot-install-fail.out; then
  fail "model key in prefix failure"
fi
rm -rf "$prefix"

prefix=$(mktemp -d)
expect_fail "$prefix" \
  CASTBOT_MODEL_PROVIDER=bedrock \
  CASTBOT_MODEL_KEY="$model_key" \
  CASTBOT_MODEL='bedrock/claude'
grep -q 'model provider is not supported' /tmp/castbot-install-fail.err || fail "bedrock provider"
rm -rf "$prefix"

prefix=$(mktemp -d)
expect_fail "$prefix" \
  CASTBOT_MODEL_PROVIDER=xai \
  CASTBOT_MODEL_KEY='bad key' \
  CASTBOT_MODEL='xai/grok-4.7'
grep -q 'model key is not a single token' /tmp/castbot-install-fail.err || fail "model key spaces"
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
cat > "$stub" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CASTBOT_STUB_LOG"
if [ "$1" = "create-user" ]; then
  read -r pw || true
  printf 'pw-len %s\n' "${#pw}" >> "$CASTBOT_STUB_LOG"
  echo "created admin@example.com"
  echo "Sign in with this email and the password. Two-factor can be turned on later in Settings."
  exit 0
fi
if [ "$1" = "save-provider-key" ]; then
  read -r key || true
  printf 'save-len %s\n' "${#key}" >> "$CASTBOT_STUB_LOG"
  echo "saved model key"
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
  CASTBOT_MODEL_PROVIDER=xai \
  CASTBOT_MODEL_KEY="$model_key" \
  CASTBOT_MODEL='xai/grok-4.7' \
  CASTBOT_BINARY="$stub")
printf '%s\n' "$out" | grep -q 'The model key is saved. Start its machine.' || fail "saved model finish"
printf '%s\n' "$out" | grep -q 'created CastBot abcdef' || fail "model assistant output"
if printf '%s\n' "$out" | grep -q "$model_key"; then
  fail "model key in output"
fi
if printf '%s\n' "$out" | grep -q "$password"; then
  fail "password in model output"
fi
grep -q 'save-provider-key --email admin@example.com --provider xai' "$prefix/stub.log" || fail "save-provider-key args"
grep -q 'create-assistant --email admin@example.com --model xai/grok-4.7' "$prefix/stub.log" || fail "create-assistant model"
grep -q 'save-len 25' "$prefix/stub.log" || fail "model key length"
if grep -q 'list-models' "$prefix/stub.log"; then
  fail "noninteractive listed models"
fi
if grep -q "$model_key" "$prefix/stub.log"; then
  fail "model key in stub log"
fi
envfile="$prefix/etc/castbot/castbot.env"
if grep -q "$model_key" "$envfile" || grep -q 'CASTBOT_MODEL' "$envfile" || grep -q 'CASTBOT_PROVIDER_KEY_' "$envfile"; then
  fail "model key stored in env"
fi
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
cat > "$stub" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CASTBOT_STUB_LOG"
if [ "$1" = "list-models" ]; then
  read -r key || true
  printf 'list-len %s\n' "${#key}" >> "$CASTBOT_STUB_LOG"
  printf 'xai/grok-4.7\tgrok-4.7\n'
  exit 0
fi
if [ "$1" = "create-user" ]; then
  read -r pw || true
  printf 'pw-len %s\n' "${#pw}" >> "$CASTBOT_STUB_LOG"
  echo "created admin@example.com"
  echo "Sign in with this email and the password. Two-factor can be turned on later in Settings."
  exit 0
fi
if [ "$1" = "save-provider-key" ]; then
  read -r key || true
  printf 'save-len %s\n' "${#key}" >> "$CASTBOT_STUB_LOG"
  echo "saved model key"
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
python3 - "$install" "$prefix" "$stub" "$sha" "$email" "$name" "$password" "$model_key" <<'PY'
import errno, os, pty, select, sys, time

install, prefix, stub, sha, email, name, password, key = sys.argv[1:9]
os.environ["CASTBOT_INSTALL_PREFIX"] = prefix
os.environ["CASTBOT_ADMIN_EMAIL"] = email
os.environ["CASTBOT_ADMIN_NAME"] = name
os.environ["CASTBOT_ADMIN_PASSWORD"] = password
os.environ["CASTBOT_PUBLIC_HOST"] = ""
os.environ["CASTBOT_SHA"] = sha
os.environ["CASTBOT_BINARY"] = stub
os.environ["CASTBOT_STUB_LOG"] = prefix + "/stub.log"
os.environ["CASTBOT_STUB_USER"] = prefix + "/stub.user"
for name_var in (
    "CASTBOT_INSTALL_NONINTERACTIVE",
    "CASTBOT_MODEL_PROVIDER",
    "CASTBOT_MODEL_KEY",
    "CASTBOT_MODEL",
    "CASTBOT_DO_TOKEN",
    "CASTBOT_IMAGE",
):
    os.environ.pop(name_var, None)

pid, fd = pty.fork()
if pid == 0:
    os.execv("/bin/bash", ["bash", install])

buf = b""
status = None

def redact(raw):
    text = raw.decode("utf-8", "replace")
    return text.replace(key, "[redacted]").replace(password, "[redacted]")

def reap(block=False):
    global status
    if status is not None:
        return
    try:
        wpid, got = os.waitpid(pid, 0 if block else os.WNOHANG)
    except ChildProcessError:
        return
    if wpid != 0:
        status = got

def pump_once(timeout):
    global buf
    readable, _, _ = select.select([fd], [], [], timeout)
    if readable:
        try:
            chunk = os.read(fd, 4096)
        except OSError as err:
            if err.errno == errno.EIO:
                reap(block=True)
                return False
            raise
        if not chunk:
            reap(block=True)
            return False
        buf += chunk
        return True
    reap()
    return status is None

def wait_count(token, count, timeout=30):
    deadline = time.time() + timeout
    while buf.count(token) < count:
        if time.time() > deadline or not pump_once(0.2):
            if buf.count(token) >= count:
                return
            sys.stderr.write("timeout waiting for installer prompt\n")
            sys.stderr.write(redact(buf))
            sys.exit(1)

def send(raw):
    os.write(fd, raw)

wait_count(b"Machine host: ", 1)
send(b"\n")
wait_count(b"Model provider: ", 1)
send(b"6\n")
wait_count(b"Model key: ", 1)
send(key.encode() + b"\n")
wait_count(b"Model: ", 1)
send(b"1\n")

deadline = time.time() + 30
while status is None:
    if time.time() > deadline:
        sys.stderr.write("timeout waiting for installer exit\n")
        sys.stderr.write(redact(buf))
        sys.exit(1)
    pump_once(0.2)
while True:
    readable, _, _ = select.select([fd], [], [], 0.2)
    if not readable:
        break
    try:
        chunk = os.read(fd, 4096)
    except OSError:
        break
    if not chunk:
        break
    buf += chunk

if os.WIFEXITED(status):
    code = os.WEXITSTATUS(status)
else:
    code = 1
text = buf.decode("utf-8", "replace")
if code != 0 or key in text or password in text:
    sys.stderr.write("interactive installer failed\n")
    sys.stderr.write(redact(buf))
    sys.exit(1)
for needle in (
    "OpenRouter",
    "OpenAI",
    "Anthropic",
    "Google Gemini",
    "xAI",
    "DigitalOcean",
    "cloud machines, needs an API token",
    "where agent machines run",
    "Model provider",
    "grok-4.7",
    "The model key is saved.",
):
    if needle not in text:
        sys.stderr.write("missing installer text\n")
        sys.stderr.write(redact(buf))
        sys.exit(1)
if "Provider: " in text:
    sys.stderr.write("old provider prompt is still shown\n")
    sys.exit(1)
for banned in ("Ollama", "Bedrock", "ollama", "bedrock", "sprite", "Sprite", "docker", "Docker"):
    if banned in text:
        sys.stderr.write("unexpected provider in installer text\n")
        sys.exit(1)
PY
grep -q 'list-models --provider xai' "$prefix/stub.log" || fail "interactive list-models"
grep -q 'list-len 25' "$prefix/stub.log" || fail "interactive list length"
grep -q 'save-provider-key --email admin@example.com --provider xai' "$prefix/stub.log" || fail "interactive save"
grep -q 'save-len 25' "$prefix/stub.log" || fail "interactive save length"
grep -q 'create-assistant --email admin@example.com --model xai/grok-4.7' "$prefix/stub.log" || fail "interactive model"
if grep -q "$model_key" "$prefix/stub.log" || grep -q "$password" "$prefix/stub.log"; then
  fail "secret in interactive stub log"
fi
envfile="$prefix/etc/castbot/castbot.env"
if grep -q "$model_key" "$envfile" || grep -q 'CASTBOT_MODEL' "$envfile" || grep -q 'CASTBOT_PROVIDER_KEY_' "$envfile"; then
  fail "interactive model key stored in env"
fi
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
write_user_stub "$stub"
: > "$prefix/stub.log"
mkdir -p "$prefix/stubs"
cat > "$prefix/stubs/caddy" <<'EOF'
#!/bin/sh
if [ "$1" = "validate" ] && [ "$2" = "--config" ]; then
  exit 0
fi
echo "unexpected caddy args" >&2
exit 2
EOF
chmod 0755 "$prefix/stubs/caddy"
out=$(run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_BINARY="$stub" \
  CASTBOT_HOST_STUB_DIR="$prefix/stubs")
printf '%s\n' "$out" | grep -q 'castbot is installed.' || fail "validate success banner"
assert_cmd_order "$prefix/host-cmd.log" \
  "caddy validate --config ${prefix}/etc/caddy/Caddyfile" \
  'systemctl enable caddy' \
  'systemctl reload-or-restart caddy' \
  'curl -fsS --max-time 5 https://bot.example.com/login'
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
write_user_stub "$stub"
: > "$prefix/stub.log"
mkdir -p "$prefix/stubs"
printf '%s\n' '#!/bin/sh' 'exit 1' > "$prefix/stubs/caddy"
chmod 0755 "$prefix/stubs/caddy"
set +e
run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_BINARY="$stub" \
  CASTBOT_HOST_STUB_DIR="$prefix/stubs" >/tmp/castbot-install-fail.out 2>/tmp/castbot-install-fail.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "invalid caddy config should refuse"
grep -q 'caddy config is not valid' /tmp/castbot-install-fail.err || fail "invalid caddy message"
if grep -q 'castbot is installed.' /tmp/castbot-install-fail.out; then
  fail "success banner after invalid caddy config"
fi
if grep -q 'systemctl reload-or-restart caddy' "$prefix/host-cmd.log" || grep -q 'systemctl enable caddy' "$prefix/host-cmd.log"; then
  fail "caddy reloaded after an invalid config"
fi
grep -q "caddy validate --config ${prefix}/etc/caddy/Caddyfile" "$prefix/host-cmd.log" || fail "caddy was not validated"
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
write_user_stub "$stub"
: > "$prefix/stub.log"
mkdir -p "$prefix/stubs"
cat > "$prefix/stubs/curl" <<'EOF'
#!/bin/sh
url=
for arg in "$@"; do
  case "$arg" in
    http://*|https://*) url=$arg ;;
  esac
done
case "$url" in
  http://127.0.0.1:8080/login) exit 0 ;;
esac
exit 1
EOF
chmod 0755 "$prefix/stubs/curl"
set +e
run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_HTTPS_WAIT_SECONDS=0 \
  CASTBOT_BINARY="$stub" \
  CASTBOT_HOST_STUB_DIR="$prefix/stubs" >/tmp/castbot-install-fail.out 2>/tmp/castbot-install-fail.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "https failure should be non-zero"
grep -q 'DNS A record points at this host and is not proxied' /tmp/castbot-install-fail.err || fail "https dns hint"
grep -q 'ports 80 and 443 are open' /tmp/castbot-install-fail.err || fail "https port hint"
grep -q 'journalctl -u caddy' /tmp/castbot-install-fail.err || fail "https journal hint"
if grep -q 'castbot is installed.' /tmp/castbot-install-fail.out || grep -q 'Sign in at https://bot.example.com/login' /tmp/castbot-install-fail.out; then
  fail "success banner when https is down"
fi
if grep -q 'TESTSECRET234567' /tmp/castbot-install-fail.out; then
  fail "secret printed when https is down"
fi
assert_cmd_order "$prefix/host-cmd.log" \
  'systemctl reload-or-restart caddy' \
  'curl -fsS --max-time 2 http://127.0.0.1:8080/login' \
  'curl -fsS --max-time 5 https://bot.example.com/login'
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
write_user_stub "$stub"
: > "$prefix/stub.log"
mkdir -p "$prefix/stubs"
printf '%s\n' '#!/bin/sh' 'exit 1' > "$prefix/stubs/curl"
chmod 0755 "$prefix/stubs/curl"
set +e
run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_BINARY="$stub" \
  CASTBOT_HOST_STUB_DIR="$prefix/stubs" >/tmp/castbot-install-fail.out 2>/tmp/castbot-install-fail.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "local health failure should be non-zero"
grep -q 'castbot did not answer on /login' /tmp/castbot-install-fail.err || fail "local health message"
if grep -q 'castbot is installed.' /tmp/castbot-install-fail.out; then
  fail "success banner when local health fails"
fi
if grep -q 'https://bot.example.com/login' "$prefix/host-cmd.log"; then
  fail "https check ran before local health passed"
fi
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
write_user_stub "$stub"
: > "$prefix/stub.log"
set +e
run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_HTTPS_WAIT_SECONDS=nope \
  CASTBOT_BINARY="$stub" >/tmp/castbot-install-fail.out 2>/tmp/castbot-install-fail.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "bad https wait should refuse"
grep -q 'CASTBOT_HTTPS_WAIT_SECONDS must be a number of seconds' /tmp/castbot-install-fail.err || fail "bad https wait message"
if grep -q 'castbot is installed.' /tmp/castbot-install-fail.out; then
  fail "success banner after a bad https wait"
fi
rm -rf "$prefix"

prefix=$(mktemp -d)
stub="$prefix/castbot"
write_user_stub "$stub"
: > "$prefix/stub.log"
mkdir -p "$prefix/stubs"
printf '%s\n' '#!/bin/sh' 'exit 0' > "$prefix/stubs/systemctl"
chmod 0755 "$prefix/stubs/systemctl"
out=$(run_install "$prefix" \
  CASTBOT_PUBLIC_HOST='bot.example.com' \
  CASTBOT_BINARY="$stub" \
  CASTBOT_HOST_STUB_DIR="$prefix/stubs")
printf '%s\n' "$out" | grep -q 'castbot is installed.' || fail "active service success banner"
assert_cmd_order "$prefix/host-cmd.log" \
  'systemctl daemon-reload' \
  'systemctl is-active --quiet castbot' \
  'systemctl stop castbot' \
  'systemctl enable --now castbot.service' \
  'systemctl enable caddy' \
  'systemctl reload-or-restart caddy'
if grep -q 'systemctl start castbot.service' "$prefix/host-cmd.log"; then
  fail "restarted castbot after a successful start"
fi
rm -rf "$prefix"

# A signed fixture is installed without the Go toolchain. The production
# key rejects that fixture. A flipped byte and a mismatched tag do too.
fixture=$(mktemp -d)
cat >"$fixture/stub.go" <<'EOF'
package main

import (
	"fmt"
	"os"
)

func main() {
	if log := os.Getenv("CASTBOT_STUB_LOG"); log != "" {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintln(f, os.Args[1:])
			f.Close()
		}
	}
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "create-user":
		buf := make([]byte, 1)
		_, _ = os.Stdin.Read(buf)
		fmt.Println("Authenticator secret: TESTSECRET234567")
		fmt.Println("otpauth://totp/castbot:admin?secret=TESTSECRET234567")
	case "create-assistant":
		fmt.Println("created CastBot abcdef")
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		os.Exit(2)
	}
}
EOF
(
  cd "$fixture"
  go mod init stub >/dev/null
  CGO_ENABLED=0 go build -o "$fixture/stub" .
)
(cd "$root" && CASTBOT_FIXTURE_DIR="$fixture" go test -count=1 -timeout 120s -run TestWriteInstallFixture ./internal/update)
relcommit=fedcba9876543210fedcba9876543210fedcba98
keyline=$(awk 'NR==2' "$fixture/key.pub")
copy=$(mktemp)
python3 - "$install" "$copy" "$keyline" <<'PY'
import pathlib, sys
src, dst, key = sys.argv[1:]
text = pathlib.Path(src).read_text()
needle = 'CASTBOT_MINISIGN_PUBLIC_KEY="'
start = text.find(needle)
end = text.find('"', start + len(needle))
if start < 0 or end < 0:
    raise SystemExit("key line missing")
text = text[: start + len(needle)] + key + text[end:]
pathlib.Path(dst).write_text(text)
PY
chmod 0755 "$copy"

run_release() {
  local prefix=$1 script=$2
  shift 2
  env \
    CASTBOT_INSTALL_PREFIX="$prefix" \
    CASTBOT_INSTALL_NONINTERACTIVE=1 \
    CASTBOT_ADMIN_EMAIL="$email" \
    CASTBOT_ADMIN_NAME="$name" \
    CASTBOT_ADMIN_PASSWORD="$password" \
    CASTBOT_STUB_LOG="$prefix/stub.log" \
    CASTBOT_RELEASE_FIXTURE="$fixture/out" \
    "$@" \
    bash "$script"
}

prefix=$(mktemp -d)
out=$(run_release "$prefix" "$copy")
printf '%s\n' "$out" | grep -q 'created CastBot abcdef' || fail "release assistant"
[[ "$(tr -d '[:space:]' <"$prefix/opt/castbot/RELEASE")" == "$relcommit" ]] || fail "release commit"
[[ "$(tr -d '[:space:]' <"$prefix/opt/castbot/VERSION")" == "0.20261124.1" ]] || fail "release version"
[[ -x "$prefix/opt/castbot/releases/$relcommit/castbot" ]] || fail "release binary"
grep -F 'release_kind)" == "source"' "$install" >/dev/null || fail "go stays off the release path"
rm -rf "$prefix"

prefix=$(mktemp -d)
out=$(run_release "$prefix" "$copy" CASTBOT_REF=v0.20261124.1)
[[ "$(tr -d '[:space:]' <"$prefix/opt/castbot/VERSION")" == "0.20261124.1" ]] || fail "tagged release version"
rm -rf "$prefix"

prefix=$(mktemp -d)
set +e
run_release "$prefix" "$copy" CASTBOT_REF=v0.20261124.2 >/tmp/castbot-install-fail.out 2>/tmp/castbot-install-fail.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "mismatched tag was accepted"
grep -q 'release version does not match' /tmp/castbot-install-fail.err || fail "mismatch message"
[[ ! -d "$prefix/opt/castbot" ]] || fail "mismatch created the app dir"
rm -rf "$prefix"

flipped="$fixture/flipped"
rm -rf "$flipped"
cp -a "$fixture/out" "$flipped"
case "$(uname -m)" in
  aarch64 | arm64) flip_arch=arm64 ;;
  *) flip_arch=amd64 ;;
esac
python3 - "$flipped/castbot_linux_${flip_arch}" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
raw = bytearray(p.read_bytes())
raw[0] ^= 0xFF
p.write_bytes(raw)
PY
prefix=$(mktemp -d)
set +e
env \
  CASTBOT_INSTALL_PREFIX="$prefix" \
  CASTBOT_INSTALL_NONINTERACTIVE=1 \
  CASTBOT_ADMIN_EMAIL="$email" \
  CASTBOT_ADMIN_NAME="$name" \
  CASTBOT_ADMIN_PASSWORD="$password" \
  CASTBOT_RELEASE_FIXTURE="$flipped" \
  bash "$copy" >/tmp/castbot-install-fail.out 2>/tmp/castbot-install-fail.err
code=$?
set -e
[[ "$code" -ne 0 ]] || fail "flipped release was accepted"
grep -q 'release hash was rejected' /tmp/castbot-install-fail.err || fail "flip message"
[[ ! -d "$prefix/opt/castbot" ]] || fail "flipped release created the app dir"
rm -rf "$prefix"

prefix=$(mktemp -d)
expect_fail "$prefix" CASTBOT_RELEASE_FIXTURE="$fixture/out"
grep -q 'release signature was rejected' /tmp/castbot-install-fail.err || fail "foreign signature"
rm -rf "$prefix" "$fixture" "$copy"

rm -f /tmp/castbot-install-xtrace.out /tmp/castbot-install-xtrace.err \
  /tmp/castbot-install-args.out /tmp/castbot-install-args.err \
  /tmp/castbot-install-nobody.out /tmp/castbot-install-nobody.err \
  /tmp/castbot-install-fail.out /tmp/castbot-install-fail.err

printf 'install tests passed\n'
