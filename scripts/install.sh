#!/usr/bin/env bash
# Install the castbot control plane on a new host and create the first agent.
# Prompts are read from the terminal. Secrets are not traced or logged.
#
#   curl -fsSL https://raw.githubusercontent.com/scott/castbot/main/scripts/install.sh | bash
if [[ $- == *x* ]]; then
  set +x
  printf 'install: refusing to run under xtrace\n' >&2
  exit 1
fi
set -euo pipefail

PREFIX=${CASTBOT_INSTALL_PREFIX:-}
APP_DIR="${PREFIX}/opt/castbot"
ENV_DIR="${PREFIX}/etc/castbot"
ENV_FILE="${ENV_DIR}/castbot.env"
UNIT_DIR="${PREFIX}/etc/systemd/system"
UNIT_FILE="${UNIT_DIR}/castbot.service"
CADDY_DIR="${PREFIX}/etc/caddy"
CADDY_FILE="${CADDY_DIR}/Caddyfile"
NOTE="${PREFIX}/root/castbot-first-login.txt"
REPO_URL="https://github.com/scott/castbot.git"
GO_VERSION="go1.27.4"
GO_AMD64_SUM="7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840"
GO_ARM64_SUM="3392582988a273a87ab5043ee1b2c9c17a2c3c00580bf9326c47a2e0aaaeac92"

EMAIL=""
NAME=""
PASSWORD=""
PUBLIC_HOST=""
TOKEN=""
IMAGE=""
WORKSPACE="Personal"
REF="main"
SHA=""
GOARCH=""
OWNER="root"
GROUP="root"
ACCOUNT_NEW=0
SRC_DIR=""
TMP_ENV=""
LAST_ERR=""
INTERACTIVE=0
RESTART_ON_EXIT=0

usage() {
  cat <<'EOF'
Usage: install.sh

Install castbot on this host and create the first agent, CastBot.

On a new host, as root:

  curl -fsSL https://raw.githubusercontent.com/scott/castbot/main/scripts/install.sh | bash

A terminal asks for the account email, name, and password, and for an
optional public hostname. Without a hostname, castbot listens on
127.0.0.1:8080 only. The script chooses the runtime image. Set
CASTBOT_IMAGE to use a different one.

Non-interactive settings:
  CASTBOT_ADMIN_EMAIL
  CASTBOT_ADMIN_NAME
  CASTBOT_ADMIN_PASSWORD
  CASTBOT_PUBLIC_HOST          optional hostname
  CASTBOT_DO_TOKEN             optional machine token
  CASTBOT_IMAGE                runtime image override
  CASTBOT_REF                  git branch, tag, or commit (default main)
  CASTBOT_WORKSPACE            workspace name (default Personal)

Set CASTBOT_INSTALL_NONINTERACTIVE=1 to skip prompts on a terminal.
The script refuses to run under xtrace.
EOF
}

die() {
  printf 'install: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  local status=$?
  if [[ -n "$SRC_DIR" ]]; then
    rm -rf "$SRC_DIR"
  fi
  if [[ -n "$TMP_ENV" ]]; then
    rm -f "$TMP_ENV"
  fi
  if [[ -n "$LAST_ERR" ]]; then
    rm -f "$LAST_ERR"
  fi
  if [[ "$RESTART_ON_EXIT" == "1" ]]; then
    systemctl start castbot.service || true
  fi
  exit "$status"
}
trap cleanup EXIT

valid_email() {
  local email=$1
  [[ -n "$email" && ${#email} -le 254 && "$email" == *@* && "$email" != *@ && "$email" != @* ]] || return 1
  [[ "$email" != *" "* && "$email" != *$'\t'* && "$email" != *$'\n'* && "$email" != *$'\r'* ]]
}

valid_name() {
  local name=$1
  [[ -n "$name" && ${#name} -le 80 && "$name" != *$'\n'* && "$name" != *$'\r'* ]]
}

valid_host() {
  local host=$1
  [[ -n "$host" && ${#host} -le 253 ]] || return 1
  if [[ "$host" =~ ^[0-9.]+$ ]]; then
    return 1
  fi
  [[ "$host" =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$ ]]
}

normalize_host() {
  local host=$1
  host=${host#https://}
  host=${host#http://}
  host=${host%%/*}
  host=${host%/}
  if [[ "$host" == *:* ]]; then
    return 1
  fi
  printf '%s' "$host"
}

valid_token() {
  local token=$1
  [[ -z "$token" ]] && return 0
  [[ "$token" != *" "* && "$token" != *$'\t'* && "$token" != *$'\n'* && "$token" != *$'\r'* && ${#token} -le 4096 ]]
}

valid_image() {
  local image=$1
  [[ -z "$image" ]] && return 0
  [[ "$image" =~ ^[A-Za-z0-9._:@/-]+$ && ${#image} -le 300 ]]
}

valid_ref() {
  local ref=$1
  [[ "$ref" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$ ]] || return 1
  [[ "$ref" != *".."* ]]
}

prompt() {
  local text=$1
  local value=""
  if ! IFS= read -r -p "$text" value </dev/tty; then
    die "could not read a prompt"
  fi
  printf '%s' "$value"
}

prompt_secret() {
  local text=$1
  local value=""
  if ! IFS= read -r -s -p "$text" value </dev/tty; then
    printf '\n' >&2
    die "could not read a prompt"
  fi
  printf '\n' >&2
  printf '%s' "$value"
}

read_settings() {
  if [[ -t 1 && -r /dev/tty && -z "${CASTBOT_INSTALL_NONINTERACTIVE:-}" ]]; then
    INTERACTIVE=1
  fi
  WORKSPACE=${CASTBOT_WORKSPACE:-Personal}
  REF=${CASTBOT_REF:-main}
  if [[ "$INTERACTIVE" == "1" ]]; then
    if [[ -z "${CASTBOT_ADMIN_EMAIL:-}" ]]; then
      EMAIL=$(prompt "Email: ")
    else
      EMAIL=$CASTBOT_ADMIN_EMAIL
    fi
    if [[ -z "${CASTBOT_ADMIN_NAME:-}" ]]; then
      NAME=$(prompt "Display name: ")
    else
      NAME=$CASTBOT_ADMIN_NAME
    fi
    if [[ -z "${CASTBOT_ADMIN_PASSWORD:-}" ]]; then
      PASSWORD=$(prompt_secret "Password: ")
      local again
      again=$(prompt_secret "Password again: ")
      if [[ "$PASSWORD" != "$again" ]]; then
        unset PASSWORD again
        die "passwords did not match"
      fi
      unset again
    else
      PASSWORD=$CASTBOT_ADMIN_PASSWORD
    fi
    if [[ -z "${CASTBOT_PUBLIC_HOST+x}" ]]; then
      PUBLIC_HOST=$(prompt "Public hostname (optional): ")
    else
      PUBLIC_HOST=$CASTBOT_PUBLIC_HOST
    fi
    TOKEN=${CASTBOT_DO_TOKEN:-}
    if [[ -n "$TOKEN" ]]; then
      local use
      use=$(prompt "A machine token is already set. Use it? [y/N] ")
      case "$use" in
        y|Y) ;;
        *) TOKEN="" ;;
      esac
    fi
    if [[ -z "$TOKEN" ]]; then
      TOKEN=$(prompt_secret "Machine token (optional): ")
    fi
    IMAGE=${CASTBOT_IMAGE:-}
  else
    EMAIL=${CASTBOT_ADMIN_EMAIL:-}
    NAME=${CASTBOT_ADMIN_NAME:-}
    PASSWORD=${CASTBOT_ADMIN_PASSWORD:-}
    PUBLIC_HOST=${CASTBOT_PUBLIC_HOST:-}
    TOKEN=${CASTBOT_DO_TOKEN:-}
    IMAGE=${CASTBOT_IMAGE:-}
  fi
  if ! valid_email "$EMAIL"; then
    die "email is missing or not valid"
  fi
  if ! valid_name "$NAME"; then
    die "display name is missing or too long"
  fi
  if [[ ${#PASSWORD} -lt 12 || ${#PASSWORD} -gt 1024 ]]; then
    unset PASSWORD
    die "password must be 12 to 1024 characters"
  fi
  if [[ "$PASSWORD" == *$'\n'* || "$PASSWORD" == *$'\r'* ]]; then
    unset PASSWORD
    die "password must be one line"
  fi
  if [[ -n "$PUBLIC_HOST" ]]; then
    if ! PUBLIC_HOST=$(normalize_host "$PUBLIC_HOST"); then
      die "public hostname must not include a port"
    fi
    if ! valid_host "$PUBLIC_HOST"; then
      die "public hostname is not a hostname"
    fi
  fi
  if ! valid_token "$TOKEN"; then
    unset TOKEN
    die "machine token is not a single token"
  fi
  if ! valid_image "$IMAGE"; then
    die "runtime image has a character that is not allowed"
  fi
  if [[ -z "$WORKSPACE" || ${#WORKSPACE} -gt 80 || "$WORKSPACE" == *$'\n'* ]]; then
    die "workspace name is not valid"
  fi
  if ! valid_ref "$REF"; then
    die "source ref is not valid"
  fi
  unset CASTBOT_ADMIN_PASSWORD
}

prepare_os() {
  if [[ -n "$PREFIX" ]]; then
    if ! command -v openssl >/dev/null 2>&1; then
      die "openssl is missing"
    fi
    return 0
  fi
  if ! command -v apt-get >/dev/null 2>&1; then
    die "this installer needs apt"
  fi
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y ca-certificates curl git sqlite3 openssl fail2ban ufw
  if ! id castbot >/dev/null 2>&1; then
    if ! getent group castbot >/dev/null 2>&1; then
      groupadd --system castbot
    fi
    local shell
    shell=/usr/sbin/nologin
    if [[ ! -x "$shell" ]]; then
      shell=/bin/false
    fi
    useradd --system --gid castbot --home-dir /opt/castbot --no-create-home --shell "$shell" castbot
  fi
  OWNER=castbot
  GROUP=castbot
  if [[ -n "$PUBLIC_HOST" ]] && ! command -v caddy >/dev/null 2>&1; then
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https gnupg
    install -d -m 0755 /usr/share/keyrings
    rm -f /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -fsSL --retry 3 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -fsSL --retry 3 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' -o /etc/apt/sources.list.d/caddy-stable.list
    apt-get update
    apt-get install -y caddy
  fi
  if [[ -z "${CASTBOT_BINARY:-}" ]]; then
    ensure_go
  fi
}

host_arch() {
  case "$(uname -m)" in
    x86_64|amd64)
      GOARCH=amd64
      ;;
    aarch64|arm64)
      GOARCH=arm64
      ;;
    *)
      die "this installer supports x86_64 and arm64"
      ;;
  esac
}

ensure_go() {
  host_arch
  if command -v go >/dev/null 2>&1; then
    local ver
    ver=$(go version 2>/dev/null || true)
    if [[ "$ver" == *"${GO_VERSION} "* ]]; then
      return 0
    fi
  fi
  local sum url tmp
  case "$GOARCH" in
    amd64) sum=$GO_AMD64_SUM ;;
    arm64) sum=$GO_ARM64_SUM ;;
    *) die "this installer supports x86_64 and arm64" ;;
  esac
  url="https://go.dev/dl/${GO_VERSION}.linux-${GOARCH}.tar.gz"
  tmp=$(mktemp)
  curl -fsSL --retry 3 -o "$tmp" "$url"
  echo "${sum}  ${tmp}" | sha256sum -c -
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "$tmp"
  rm -f "$tmp"
  ln -sfn /usr/local/go/bin/go /usr/local/bin/go
  export PATH="/usr/local/go/bin:${PATH}"
}

assert_static() {
  local bin=$1 ldd_out
  if command -v readelf >/dev/null 2>&1; then
    if readelf -h "$bin" | grep -q 'Type:[[:space:]]*DYN'; then
      die "binary is not static"
    fi
  fi
  if ! command -v ldd >/dev/null 2>&1; then
    return 0
  fi
  set +e
  ldd_out=$(ldd "$bin" 2>&1)
  set -e
  [[ "$ldd_out" != *"=>"* ]] || die "binary has dynamic libraries"
}

# install_owned sets the service owner. A non-root test cannot chown.
install_owned() {
  if [[ "$(id -u)" -eq 0 ]]; then
    install -o "$OWNER" -g "$GROUP" "$@"
  else
    install "$@"
  fi
}

install_binary() {
  local built
  if [[ -n "${CASTBOT_BINARY:-}" ]]; then
    SHA=${CASTBOT_SHA:-}
    if [[ ! "$SHA" =~ ^[0-9a-f]{40}$ ]]; then
      die "CASTBOT_SHA must be 40 hex characters"
    fi
    if [[ ! -f "$CASTBOT_BINARY" ]]; then
      die "CASTBOT_BINARY is missing"
    fi
    built=$CASTBOT_BINARY
  else
    if [[ -n "$PREFIX" ]]; then
      die "CASTBOT_BINARY is required when CASTBOT_INSTALL_PREFIX is set"
    fi
    host_arch
    command -v git >/dev/null 2>&1 || die "git is missing"
    command -v go >/dev/null 2>&1 || die "go is missing"
    SRC_DIR=$(mktemp -d)
    GIT_TERMINAL_PROMPT=0 git clone --depth 1 "$REPO_URL" "$SRC_DIR"
    GIT_TERMINAL_PROMPT=0 git -C "$SRC_DIR" fetch --depth 1 origin "$REF"
    git -C "$SRC_DIR" checkout --detach FETCH_HEAD
    SHA=$(git -C "$SRC_DIR" rev-parse HEAD)
    if [[ ! "$SHA" =~ ^[0-9a-f]{40}$ ]]; then
      die "source revision is not a commit"
    fi
    built=$(mktemp)
    (
      cd "$SRC_DIR" || exit 1
      CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -ldflags='-s -w' -o "$built" ./cmd/castbot
    )
    assert_static "$built"
  fi
  install -d -m 0755 "$APP_DIR" "$APP_DIR/bin" "$APP_DIR/releases/${SHA}"
  install_owned -d -m 0700 "$APP_DIR/data"
  install_owned -m 0755 "$built" "$APP_DIR/releases/${SHA}/castbot"
  ln -sfn "$APP_DIR/releases/${SHA}/castbot" "$APP_DIR/bin/castbot"
  printf '%s\n' "$SHA" > "$APP_DIR/RELEASE"
  if [[ -z "${CASTBOT_BINARY:-}" ]]; then
    rm -f "$built"
  fi
}

# resolve_image fills CASTBOT_IMAGE from the published tags when the
# operator did not set one. An existing env file is left alone.
resolve_image() {
  if [[ -e "$ENV_FILE" ]]; then
    return 0
  fi
  if [[ -z "$IMAGE" ]]; then
    local repo script err selected
    repo=${CASTBOT_RUNTIME_REPO:-${SRC_DIR:-}}
    script=${CASTBOT_RUNTIME_IMAGE_SH:-}
    if [[ -z "$script" && -n "$repo" ]]; then
      script="${repo}/scripts/runtime-image.sh"
    fi
    if [[ -n "$script" && -n "$repo" ]]; then
      if [[ ! -f "$script" ]]; then
        if [[ -n "$TOKEN" || -n "$PUBLIC_HOST" ]]; then
          die "runtime image helper is missing"
        fi
        printf 'install: runtime image helper is missing. The machine host stays off.\n' >&2
      else
        err=$(mktemp)
        if ! selected=$(CASTBOT_RUNTIME_REPO="$repo" bash "$script" select "$SHA" 2>"$err"); then
          if [[ -s "$err" ]]; then
            cat "$err" >&2
          fi
          rm -f "$err"
          if [[ -n "$TOKEN" || -n "$PUBLIC_HOST" ]]; then
            die "no published runtime image matches this commit"
          fi
          printf 'install: no published runtime image matches this commit. The machine host stays off.\n' >&2
        else
          rm -f "$err"
          IMAGE=$selected
          if ! valid_image "$IMAGE"; then
            die "runtime image has a character that is not allowed"
          fi
        fi
      fi
    fi
  fi
  if [[ -n "$IMAGE" ]]; then
    printf 'Runtime image: %s\n' "$IMAGE"
  fi
}

write_env() {
  install -d -m 0755 "$ENV_DIR"
  if [[ -e "$ENV_FILE" ]]; then
    local mode
    mode=$(stat -c '%a' "$ENV_FILE")
    if [[ "$mode" != "600" ]]; then
      die "env file is not mode 0600"
    fi
    unset TOKEN CASTBOT_DO_TOKEN
    return 0
  fi
  local session secrets
  session=$(openssl rand -hex 32)
  secrets=$(openssl rand -hex 32)
  if [[ ${#session} -ne 64 || ${#secrets} -ne 64 || "$session" == "$secrets" ]]; then
    unset session secrets
    die "could not generate keys"
  fi
  if [[ "$session" =~ ^0+$ || "$secrets" =~ ^0+$ ]]; then
    unset session secrets
    die "could not generate keys"
  fi
  TMP_ENV=$(mktemp "$ENV_DIR/castbot.env.XXXXXX")
  chmod 0600 "$TMP_ENV"
  {
    printf '%s\n' "# Generated by the castbot installer. Mode 0600."
    printf '%s\n' "# The session key and the secrets key must stay different."
    printf 'CASTBOT_SESSION_KEY=%s\n' "$session"
    printf 'CASTBOT_SECRETS_KEY=%s\n' "$secrets"
    printf 'CASTBOT_ADDR=%s\n' "127.0.0.1:8080"
    printf 'CASTBOT_DB=%s\n' "$APP_DIR/data/castbot.db"
    if [[ -n "$PUBLIC_HOST" ]]; then
      printf 'CASTBOT_PUBLIC_URL=%s\n' "https://${PUBLIC_HOST}"
      printf 'CASTBOT_TRUSTED_PROXIES=%s\n' "127.0.0.1/32"
    fi
    if [[ -n "$TOKEN" ]]; then
      printf 'CASTBOT_DO_TOKEN=%s\n' "$TOKEN"
    fi
    if [[ -n "$IMAGE" ]]; then
      printf 'CASTBOT_IMAGE=%s\n' "$IMAGE"
    fi
  } > "$TMP_ENV"
  unset session secrets TOKEN CASTBOT_DO_TOKEN
  if [[ "$(id -u)" -eq 0 ]]; then
    chown "$OWNER:$GROUP" "$TMP_ENV"
  fi
  chmod 0600 "$TMP_ENV"
  mv "$TMP_ENV" "$ENV_FILE"
  TMP_ENV=""
}

write_unit() {
  install -d -m 0755 "$UNIT_DIR"
  cat > "$UNIT_FILE" <<EOF
[Unit]
Description=castbot
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=castbot
Group=castbot
WorkingDirectory=${APP_DIR}/data
Environment=CASTBOT_ENV_FILE=${ENV_FILE}
ExecStart=${APP_DIR}/bin/castbot serve
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$UNIT_FILE"
}

write_caddy() {
  if [[ -z "$PUBLIC_HOST" ]]; then
    return 0
  fi
  install -d -m 0755 "$CADDY_DIR"
  cat > "$CADDY_FILE" <<EOF
# Managed by the castbot installer.
${PUBLIC_HOST} {
	reverse_proxy 127.0.0.1:8080
}
EOF
  chmod 0644 "$CADDY_FILE"
}

run_castbot() {
  if [[ -n "$PREFIX" ]]; then
    CASTBOT_ENV_FILE="$ENV_FILE" "$APP_DIR/bin/castbot" "$@"
    return
  fi
  command -v runuser >/dev/null 2>&1 || die "runuser is missing"
  # A clean environment so the account commands do not inherit the password,
  # the machine token, or a CASTBOT_DB that would point at another file.
  runuser -u castbot -- env -i \
    HOME=/opt/castbot \
    USER=castbot \
    LOGNAME=castbot \
    CASTBOT_ENV_FILE="$ENV_FILE" \
    "$APP_DIR/bin/castbot" "$@"
}

write_note() {
  local secret=$1 url=$2
  install -d -m 0700 "${PREFIX}/root"
  local tmp
  tmp=$(mktemp "${PREFIX}/root/castbot-first-login.XXXXXX")
  chmod 0600 "$tmp"
  {
    printf '%s\n' "CastBot first login"
    printf 'Email: %s\n' "$EMAIL"
    printf 'Authenticator secret: %s\n' "$secret"
    if [[ -n "$url" ]]; then
      printf '%s\n' "$url"
    fi
    printf '\n%s\n' "Add this secret to an authenticator, then delete this file."
  } > "$tmp"
  mv "$tmp" "$NOTE"
  chmod 0600 "$NOTE"
}

onboard() {
  if [[ -z "$PREFIX" ]] && command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet castbot; then
    systemctl stop castbot
    RESTART_ON_EXIT=1
  fi
  LAST_ERR=$(mktemp)
  local out code secret url line
  set +e
  out=$(printf '%s\n' "$PASSWORD" | run_castbot create-user --email "$EMAIL" --name "$NAME" --workspace "$WORKSPACE" 2>"$LAST_ERR")
  code=$?
  set -e
  unset PASSWORD
  if [[ "$code" -ne 0 ]]; then
    if grep -F -q 'an account already exists' "$LAST_ERR"; then
      ACCOUNT_NEW=0
    else
      cat "$LAST_ERR" >&2
      die "could not create the account"
    fi
  else
    secret=""
    url=""
    while IFS= read -r line; do
      case "$line" in
        "Authenticator secret: "*) secret=${line#"Authenticator secret: "} ;;
        otpauth://*) url=$line ;;
      esac
    done <<<"$out"
    if [[ -z "$secret" ]]; then
      install -d -m 0700 "${PREFIX}/root"
      printf '%s\n' "$out" > "$NOTE"
      chmod 0600 "$NOTE"
      die "the account command did not print an authenticator secret"
    fi
    write_note "$secret" "$url"
    unset secret url out
    ACCOUNT_NEW=1
  fi
  set +e
  out=$(run_castbot create-assistant --email "$EMAIL" 2>"$LAST_ERR")
  code=$?
  set -e
  if [[ "$code" -ne 0 ]]; then
    cat "$LAST_ERR" >&2
    die "could not create CastBot"
  fi
  printf '%s\n' "$out"
  rm -f "$LAST_ERR"
  LAST_ERR=""
}

start_services() {
  if [[ -n "$PREFIX" ]]; then
    return 0
  fi
  if [[ -n "$PUBLIC_HOST" ]]; then
    local port
    port=22
    if [[ -n "${SSH_CONNECTION:-}" ]]; then
      port=${SSH_CONNECTION##* }
    fi
    if [[ ! "$port" =~ ^[0-9]+$ ]]; then
      die "could not read the ssh port"
    fi
    ufw allow "${port}/tcp"
    ufw allow 80/tcp
    ufw allow 443/tcp
    ufw --force enable
  fi
  systemctl daemon-reload
  systemctl enable --now castbot.service
  RESTART_ON_EXIT=0
  if [[ -n "$PUBLIC_HOST" ]]; then
    systemctl enable --now caddy.service
  fi
  systemctl enable --now fail2ban.service
  local i
  i=0
  while [[ "$i" -lt 30 ]]; do
    if curl -fsS --max-time 2 http://127.0.0.1:8080/login >/dev/null; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  die "castbot did not answer on /login"
}

finish() {
  printf 'castbot is installed.\n'
  if [[ -n "$PUBLIC_HOST" ]]; then
    printf 'Sign in at https://%s/login\n' "$PUBLIC_HOST"
  else
    printf 'castbot listens on 127.0.0.1:8080.\n'
    printf 'From your computer, tunnel port 8080 and open http://127.0.0.1:8080/login\n'
  fi
  printf 'Email: %s\n' "$EMAIL"
  if [[ "$ACCOUNT_NEW" == "1" && -f "$NOTE" ]]; then
    printf 'The authenticator secret is in %s (mode 0600). Add it to an authenticator, then delete that file.\n' "$NOTE"
    printf '\n'
    cat "$NOTE"
    printf '\n'
  fi
  printf 'CastBot is the first agent. Add a model key, then start its machine.\n'
}

main() {
  if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
  fi
  if [[ $# -gt 0 ]]; then
    die "this installer takes no arguments"
  fi
  if [[ -z "$PREFIX" && "$(id -u)" -ne 0 ]]; then
    die "run as root"
  fi
  read_settings
  prepare_os
  install_binary
  resolve_image
  write_env
  write_unit
  write_caddy
  onboard
  start_services
  finish
}

main "$@"
