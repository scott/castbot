#!/usr/bin/env bash
# Install the castbot control plane on a new host and create the first agent.
# Prompts are read from the terminal. Each question explains what it is for.
# Secrets are not traced or logged.
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
MODEL_PROVIDER=""
MODEL=""
MODEL_SAVED=0
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

A terminal explains each question. It asks for the account email, the
display name, and a password of 12 to 1024 characters. It then asks for
an optional public hostname and an optional machine host. The machine
host is where agent machines run. The prompt says Machine host.
DigitalOcean is supported and asks for an API token, which is a
personal access token. It then asks for an optional model provider and
a model key. The prompt says Model provider. That list is OpenRouter,
DigitalOcean, OpenAI, Anthropic, Google Gemini, and xAI. Choosing
DigitalOcean there asks for a model access key. After the program is
installed, the key is used to list that provider's models. The key is
stored in the vault. It is not printed. Set CASTBOT_MODEL_PROVIDER,
CASTBOT_MODEL_KEY, and CASTBOT_MODEL together, or leave all three unset.
Without a hostname, castbot listens on 127.0.0.1:8080 only. With a
hostname, the script reloads the proxy and waits until https for that
name answers on /login with a valid certificate. The script chooses
the runtime image when both the token and the hostname are set. Set
CASTBOT_IMAGE to use a different one. The service still starts when
the machine host stays off. Sign-in is the email and the password.
Two-factor can be turned on later in Settings.

Non-interactive settings:
  CASTBOT_ADMIN_EMAIL
  CASTBOT_ADMIN_NAME
  CASTBOT_ADMIN_PASSWORD
  CASTBOT_PUBLIC_HOST          optional hostname
  CASTBOT_DO_TOKEN             optional machine token
  CASTBOT_MODEL_PROVIDER       optional inference provider id
  CASTBOT_MODEL_KEY            optional model key for that provider
  CASTBOT_MODEL                model id from that provider
  CASTBOT_IMAGE                runtime image override
  CASTBOT_REF                  git branch, tag, or commit (default main)
  CASTBOT_WORKSPACE            workspace name (default Personal)
  CASTBOT_HTTPS_WAIT_SECONDS   seconds to wait for https (default 90)

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
    host_cmd systemctl start castbot.service || true
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

say() {
  if [[ "$INTERACTIVE" != "1" ]]; then
    return 0
  fi
  printf '%s\n' "$*" >/dev/tty
}

say_intro() {
  say "castbot installer"
  say ""
  say "This installs the control plane on this host and creates the first agent, CastBot."
  say "CastBot is your personal assistant, knows how to use castbot, and can hand work to other agents."
  say ""
  say "You will choose a sign-in email, a display name, and a password."
  say "A public hostname is optional."
  say "A machine host is optional. It is where agent machines run. The model provider is a separate question."
  say "You can also choose a model provider and paste a key. After the program is installed, the installer lists the models that key can use."
  say "Sign-in uses the email and the password. Two-factor can be turned on later in Settings."
}

ask_email() {
  local try=0 value
  say ""
  say "Email"
  say "This is the address you use to sign in. It stays on this host."
  while [[ "$try" -lt 5 ]]; do
    value=$(prompt "Email: ")
    if valid_email "$value"; then
      EMAIL=$value
      return 0
    fi
    say "Enter an email address, up to 254 characters, with no spaces."
    try=$((try + 1))
  done
  die "email is missing or not valid"
}

ask_name() {
  local try=0 value
  say ""
  say "Display name"
  say "This name appears in the sidebar. You can change it later in Settings."
  while [[ "$try" -lt 5 ]]; do
    value=$(prompt "Display name: ")
    if valid_name "$value"; then
      NAME=$value
      return 0
    fi
    say "Enter a name, up to 80 characters, on one line."
    try=$((try + 1))
  done
  die "display name is missing or too long"
}

ask_password() {
  local try=0 first again
  say ""
  say "Password"
  say "Use 12 to 1024 characters on one line. The characters are hidden as you type."
  while [[ "$try" -lt 5 ]]; do
    first=$(prompt_secret "Password: ")
    again=$(prompt_secret "Password again: ")
    if [[ "$first" == "$again" && ${#first} -ge 12 && ${#first} -le 1024 && "$first" != *$'\n'* && "$first" != *$'\r'* ]]; then
      PASSWORD=$first
      unset first again
      return 0
    fi
    unset first again
    say "Use 12 to 1024 characters on one line, and enter the same password twice."
    try=$((try + 1))
  done
  unset PASSWORD
  die "password must be 12 to 1024 characters"
}

ask_host() {
  local try=0 raw norm
  say ""
  say "Public hostname (optional)"
  say "Leave this blank to keep castbot on this machine only. Open it through an ssh tunnel to 127.0.0.1:8080."
  say "A hostname turns on https for that name. Do not include https:// or a port. Example: bot.example.com"
  while [[ "$try" -lt 5 ]]; do
    raw=$(prompt "Public hostname: ")
    if [[ -z "$raw" ]]; then
      PUBLIC_HOST=""
      return 0
    fi
    if norm=$(normalize_host "$raw") && valid_host "$norm"; then
      PUBLIC_HOST=$norm
      return 0
    fi
    say "Enter a hostname such as bot.example.com, with no port, or press Enter to skip."
    try=$((try + 1))
  done
  die "public hostname is not a hostname"
}

# machine-hosts:start
machine_host_lines() {
  cat <<'EOF'
digitalocean	DigitalOcean	token	cloud machines, needs an API token
EOF
}
# machine-hosts:end

match_machine_host() {
  local choice=$1 n=0 key name kind _blurb
  while IFS=$'\t' read -r key name kind _blurb; do
    [[ -z "$key" ]] && continue
    n=$((n + 1))
    if [[ "$choice" == "$n" || "$choice" == "$key" || "$choice" == "$name" ]]; then
      printf '%s\t%s\t%s' "$key" "$kind" "$name"
      return 0
    fi
  done < <(machine_host_lines)
  return 1
}

ask_token() {
  local try=0 choice picked use row key kind name n host_key host_name _host_kind host_blurb
  TOKEN=${CASTBOT_DO_TOKEN:-}
  if [[ -n "$TOKEN" ]]; then
    say ""
    say "Machine host"
    say "A DigitalOcean API token is already set in the environment."
    say "It lets castbot start, pause, and delete the machines that agents run on."
    use=$(prompt "Use the token that is already set? [y/N] ")
    case "$use" in
      y|Y) return 0 ;;
    esac
    TOKEN=""
  fi
  while [[ "$try" -lt 5 ]]; do
    say ""
    say "Machine host (optional)"
    say "This is where agent machines run. The model provider is the next question."
    say "You can skip this and add a host later. The site still runs, and agent machines stay off."
    say ""
    say "Supported machine hosts:"
    n=0
    while IFS=$'\t' read -r host_key host_name _host_kind host_blurb; do
      [[ -z "$host_key" ]] && continue
      n=$((n + 1))
      say "  ${n}) ${host_name}    ${host_blurb}"
    done < <(machine_host_lines)
    say ""
    say "Enter a number, or press Enter to skip."
    choice=$(prompt "Machine host: ")
    if [[ -z "$choice" || "$choice" == "skip" || "$choice" == "Skip" ]]; then
      TOKEN=""
      return 0
    fi
    if ! row=$(match_machine_host "$choice"); then
      say "Enter a machine host number, or press Enter to skip."
      try=$((try + 1))
      continue
    fi
    IFS=$'\t' read -r key kind name <<<"$row"
    if [[ "$kind" != "token" || "$key" != "digitalocean" ]]; then
      say "That machine host is not available yet."
      try=$((try + 1))
      continue
    fi
    say ""
    say "${name} API token"
    say "In the DigitalOcean control panel this is a personal access token that can manage machines. It is stored on this host and is not printed."
    picked=$(prompt_secret "DigitalOcean API token: ")
    if [[ -z "$picked" ]]; then
      TOKEN=""
      unset picked
      return 0
    fi
    if valid_token "$picked"; then
      TOKEN=$picked
      unset picked
      return 0
    fi
    unset picked
    say "The token must be one line, with no spaces."
    try=$((try + 1))
  done
  unset TOKEN
  die "machine host was not chosen"
}

# model-providers:start
model_provider_lines() {
  cat <<'EOF'
openrouter	OpenRouter
digitalocean	DigitalOcean
openai-api	OpenAI
anthropic-api	Anthropic
gemini	Google Gemini
xai	xAI
EOF
}
# model-providers:end

model_provider_known() {
  local want=$1 key name
  while IFS=$'\t' read -r key name; do
    if [[ "$key" == "$want" ]]; then
      return 0
    fi
  done < <(model_provider_lines)
  return 1
}

model_provider_name() {
  local want=$1 key name
  while IFS=$'\t' read -r key name; do
    if [[ "$key" == "$want" ]]; then
      printf '%s' "$name"
      return 0
    fi
  done < <(model_provider_lines)
  printf '%s' "$want"
}

match_model_provider() {
  local choice=$1 n=0 key name
  while IFS=$'\t' read -r key name; do
    [[ -z "$key" ]] && continue
    n=$((n + 1))
    if [[ "$choice" == "$n" || "$choice" == "$key" || "$choice" == "$name" ]]; then
      printf '%s' "$key"
      return 0
    fi
  done < <(model_provider_lines)
  return 1
}

ask_model() {
  local try=0 choice picked key name n
  say ""
  say "Model provider (optional)"
  say "A model key lets CastBot call a model. It is stored in your vault and is not printed."
  say "You can skip this and add a key later in Settings."
  say ""
  say "Supported providers:"
  n=0
  while IFS=$'\t' read -r key name; do
    [[ -z "$key" ]] && continue
    n=$((n + 1))
    say "  ${n}) ${name}"
  done < <(model_provider_lines)
  say ""
  say "Enter a number, or press Enter to skip."
  while [[ "$try" -lt 5 ]]; do
    choice=$(prompt "Model provider: ")
    if [[ -z "$choice" || "$choice" == "skip" || "$choice" == "Skip" ]]; then
      MODEL_PROVIDER=""
      MODEL=""
      unset MODEL_KEY
      return 0
    fi
    if key=$(match_model_provider "$choice"); then
      say ""
      say "Model key"
      if [[ "$key" == "digitalocean" ]]; then
        say "Paste the model access key for DigitalOcean. It is hidden as you type. Press Enter to skip."
        picked=$(prompt_secret "Model access key: ")
      else
        say "Paste the key for $(model_provider_name "$key"). It is hidden as you type. Press Enter to skip."
        picked=$(prompt_secret "Model key: ")
      fi
      if [[ -z "$picked" ]]; then
        say "Skipping the model key. You can add one later in Settings."
        MODEL_PROVIDER=""
        MODEL=""
        unset picked MODEL_KEY
        return 0
      fi
      if valid_token "$picked"; then
        MODEL_PROVIDER=$key
        MODEL_KEY=$picked
        MODEL=""
        unset picked
        return 0
      fi
      unset picked
      say "The key must be one line, with no spaces."
    else
      say "Enter a provider number, or press Enter to skip."
    fi
    try=$((try + 1))
  done
  unset MODEL_KEY
  die "model provider was not chosen"
}

pick_model_list() {
  local out=$1
  local -a ids labels
  local line id label n shown i try choice j
  n=0
  while IFS=$'\t' read -r id label; do
    [[ -z "$id" ]] && continue
    ids+=("$id")
    if [[ -n "$label" ]]; then
      labels+=("$label")
    else
      labels+=("$id")
    fi
    n=$((n + 1))
  done <<<"$out"
  if [[ "$n" -eq 0 ]]; then
    return 1
  fi
  shown=$n
  if [[ "$shown" -gt 40 ]]; then
    shown=40
  fi
  say ""
  say "Models for this key"
  say "These are the models the provider returned for that key. Press Enter to use the first one."
  for ((i = 0; i < shown; i++)); do
    say "  $((i + 1))) ${labels[$i]}"
  done
  if [[ "$n" -gt 40 ]]; then
    say "Showing 40 of ${n}. Enter a number, or paste a model id."
  fi
  try=0
  while [[ "$try" -lt 5 ]]; do
    choice=$(prompt "Model: ")
    if [[ -z "$choice" ]]; then
      MODEL=${ids[0]}
      return 0
    fi
    if [[ "$choice" =~ ^[0-9]+$ && "$choice" -ge 1 && "$choice" -le "$n" ]]; then
      MODEL=${ids[$((choice - 1))]}
      return 0
    fi
    for ((j = 0; j < n; j++)); do
      if [[ "$choice" == "${ids[$j]}" || "$choice" == "${labels[$j]}" ]]; then
        MODEL=${ids[$j]}
        return 0
      fi
    done
    say "Enter a number from the list, or press Enter to use the first one."
    try=$((try + 1))
  done
  return 1
}

choose_model() {
  local try=0 out code err again
  if [[ -z "$MODEL_PROVIDER" || -z "${MODEL_KEY:-}" || -n "$MODEL" ]]; then
    return 0
  fi
  if [[ "$INTERACTIVE" != "1" ]]; then
    unset MODEL_KEY
    die "model is missing"
  fi
  while [[ "$try" -lt 5 ]]; do
    err=$(mktemp)
    set +e
    out=$(printf '%s\n' "$MODEL_KEY" | run_castbot list-models --provider "$MODEL_PROVIDER" 2>"$err")
    code=$?
    set -e
    rm -f "$err"
    if [[ "$code" -eq 0 ]] && pick_model_list "$out"; then
      return 0
    fi
    say "The provider did not return a model list. Check the key, or press Enter to skip."
    again=$(prompt_secret "Model key: ")
    if [[ -z "$again" ]]; then
      say "Skipping the model. You can add a key later in Settings."
      MODEL_PROVIDER=""
      MODEL=""
      unset MODEL_KEY again
      return 0
    fi
    if valid_token "$again"; then
      MODEL_KEY=$again
    else
      say "The key must be one line, with no spaces."
    fi
    unset again
    try=$((try + 1))
  done
  say "Skipping the model. You can add a key later in Settings."
  MODEL_PROVIDER=""
  MODEL=""
  unset MODEL_KEY
}

model_choice_ok() {
  if [[ -z "$MODEL_PROVIDER" && -z "${MODEL_KEY:-}" && -z "$MODEL" ]]; then
    return 0
  fi
  if [[ "$INTERACTIVE" == "1" && -n "$MODEL_PROVIDER" && -n "${MODEL_KEY:-}" && -z "$MODEL" ]]; then
    return 0
  fi
  if [[ -z "$MODEL_PROVIDER" || -z "${MODEL_KEY:-}" || -z "$MODEL" ]]; then
    unset MODEL_KEY
    die "model provider, key, and model must be set together"
  fi
  if ! model_provider_known "$MODEL_PROVIDER"; then
    unset MODEL_KEY
    die "model provider is not supported"
  fi
  if [[ -z "$MODEL_KEY" ]] || ! valid_token "$MODEL_KEY"; then
    unset MODEL_KEY
    die "model key is not a single token"
  fi
  if [[ "$MODEL" != "$MODEL_PROVIDER/"* || ${#MODEL} -gt 200 || "$MODEL" == *" "* || "$MODEL" == *$'\t'* || "$MODEL" == *$'\n'* || "$MODEL" == *$'\r'* ]]; then
    unset MODEL_KEY
    die "model is not valid"
  fi
}

read_settings() {
  if [[ -t 1 && -r /dev/tty && -z "${CASTBOT_INSTALL_NONINTERACTIVE:-}" ]]; then
    INTERACTIVE=1
  fi
  WORKSPACE=${CASTBOT_WORKSPACE:-Personal}
  REF=${CASTBOT_REF:-main}
  if [[ "$INTERACTIVE" == "1" ]]; then
    say_intro
    if [[ -z "${CASTBOT_ADMIN_EMAIL:-}" ]]; then
      ask_email
    else
      EMAIL=$CASTBOT_ADMIN_EMAIL
    fi
    if [[ -z "${CASTBOT_ADMIN_NAME:-}" ]]; then
      ask_name
    else
      NAME=$CASTBOT_ADMIN_NAME
    fi
    if [[ -z "${CASTBOT_ADMIN_PASSWORD:-}" ]]; then
      ask_password
    else
      PASSWORD=$CASTBOT_ADMIN_PASSWORD
    fi
    if [[ -z "${CASTBOT_PUBLIC_HOST+x}" ]]; then
      ask_host
    else
      PUBLIC_HOST=$CASTBOT_PUBLIC_HOST
    fi
    ask_token
    if [[ -z "${CASTBOT_MODEL_PROVIDER+x}" ]]; then
      ask_model
    else
      MODEL_PROVIDER=$CASTBOT_MODEL_PROVIDER
      MODEL_KEY=${CASTBOT_MODEL_KEY:-}
      MODEL=${CASTBOT_MODEL:-}
    fi
    IMAGE=${CASTBOT_IMAGE:-}
  else
    EMAIL=${CASTBOT_ADMIN_EMAIL:-}
    NAME=${CASTBOT_ADMIN_NAME:-}
    PASSWORD=${CASTBOT_ADMIN_PASSWORD:-}
    PUBLIC_HOST=${CASTBOT_PUBLIC_HOST:-}
    TOKEN=${CASTBOT_DO_TOKEN:-}
    MODEL_PROVIDER=${CASTBOT_MODEL_PROVIDER:-}
    MODEL_KEY=${CASTBOT_MODEL_KEY:-}
    MODEL=${CASTBOT_MODEL:-}
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
  model_choice_ok
  unset CASTBOT_ADMIN_PASSWORD CASTBOT_MODEL_KEY
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

# release_label_for prints the version stored next to RELEASE.
# A tag ref vMAJOR.MINOR.PATCH uses that number. Any other ref uses
# 0.0.0 plus the commit.
release_label_for() {
  local sha=$1 ref=$2 ver
  if [[ ! "$sha" =~ ^[0-9a-f]{40}$ ]]; then
    die "source revision is not a commit"
  fi
  if [[ "$ref" =~ ^v([0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
    ver=${BASH_REMATCH[1]}
  else
    ver="0.0.0+${sha}"
  fi
  printf '%s' "$ver"
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
    REL_VERSION=$(release_label_for "$SHA" "$REF")
    (
      cd "$SRC_DIR" || exit 1
      ldflags="-s -w -X github.com/scott/castbot/cmd/castbot.version=${REL_VERSION} -X github.com/scott/castbot/cmd/castbot.commit=${SHA}"
      CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -ldflags="$ldflags" -o "$built" ./cmd/castbot
    )
    assert_static "$built"
  fi
  if [[ -z "${REL_VERSION:-}" ]]; then
    REL_VERSION=$(release_label_for "$SHA" "")
  fi
  install -d -m 0755 "$APP_DIR" "$APP_DIR/bin" "$APP_DIR/releases/${SHA}"
  install_owned -d -m 0700 "$APP_DIR/data"
  install_owned -m 0755 "$built" "$APP_DIR/releases/${SHA}/castbot"
  ln -sfn "$APP_DIR/releases/${SHA}/castbot" "$APP_DIR/bin/castbot"
  printf '%s\n' "$SHA" > "$APP_DIR/RELEASE"
  printf '%s\n' "$REL_VERSION" > "$APP_DIR/VERSION"
  printf '%s\n' "$REL_VERSION" > "$APP_DIR/releases/${SHA}/VERSION"
  if [[ -z "${CASTBOT_BINARY:-}" ]]; then
    rm -f "$built"
  fi
}

# machine_host_wanted is true when a token and a public host are both
# available, counting keys already stored in the env file. Values are
# not printed.
machine_host_wanted() {
  local have_token=0 have_host=0
  if [[ -n "$TOKEN" ]]; then
    have_token=1
  fi
  if [[ -n "$PUBLIC_HOST" ]]; then
    have_host=1
  fi
  if [[ -e "$ENV_FILE" ]]; then
    if grep -q '^CASTBOT_DO_TOKEN=' "$ENV_FILE"; then
      have_token=1
    fi
    if grep -q '^CASTBOT_PUBLIC_URL=' "$ENV_FILE"; then
      have_host=1
    fi
  fi
  [[ "$have_token" -eq 1 && "$have_host" -eq 1 ]]
}

# resolve_image fills CASTBOT_IMAGE only when the machine host can start.
# Without a token and a public host, the image stays unset and the host
# stays off. The service still listens.
resolve_image() {
  local have_image=0
  if [[ -e "$ENV_FILE" ]] && grep -q '^CASTBOT_IMAGE=' "$ENV_FILE"; then
    have_image=1
  fi
  if ! machine_host_wanted; then
    if [[ -n "$IMAGE" && "$have_image" -eq 0 ]]; then
      printf 'install: runtime image ignored. The machine host stays off.\n' >&2
    fi
    IMAGE=""
    return 0
  fi
  if [[ "$have_image" -eq 1 ]]; then
    IMAGE=""
    return 0
  fi
  if [[ -n "$IMAGE" ]]; then
    printf 'Runtime image: %s\n' "$IMAGE"
    return 0
  fi
  local repo script err selected
  repo=${CASTBOT_RUNTIME_REPO:-${SRC_DIR:-}}
  script=${CASTBOT_RUNTIME_IMAGE_SH:-}
  if [[ -z "$script" && -n "$repo" ]]; then
    script="${repo}/scripts/runtime-image.sh"
  fi
  if [[ -z "$script" || -z "$repo" ]]; then
    die "no published runtime image matches this commit"
  fi
  if [[ ! -f "$script" ]]; then
    die "runtime image helper is missing"
  fi
  err=$(mktemp)
  if ! selected=$(CASTBOT_RUNTIME_REPO="$repo" bash "$script" select "$SHA" 2>"$err"); then
    if [[ -s "$err" ]]; then
      cat "$err" >&2
    fi
    rm -f "$err"
    die "no published runtime image matches this commit"
  fi
  rm -f "$err"
  IMAGE=$selected
  if ! valid_image "$IMAGE"; then
    die "runtime image has a character that is not allowed"
  fi
  printf 'Runtime image: %s\n' "$IMAGE"
}

write_env() {
  install -d -m 0755 "$ENV_DIR"
  if [[ -e "$ENV_FILE" ]]; then
    local mode
    mode=$(stat -c '%a' "$ENV_FILE")
    if [[ "$mode" != "600" ]]; then
      die "env file is not mode 0600"
    fi
    # A later run can add the public URL, the proxy trust, the token, or
    # the image. Session keys stay. Values are not printed.
    if [[ -n "$PUBLIC_HOST" ]] && ! grep -q '^CASTBOT_PUBLIC_URL=' "$ENV_FILE"; then
      printf 'CASTBOT_PUBLIC_URL=%s\n' "https://${PUBLIC_HOST}" >> "$ENV_FILE"
    fi
    if [[ -n "$PUBLIC_HOST" ]] && ! grep -q '^CASTBOT_TRUSTED_PROXIES=' "$ENV_FILE"; then
      printf 'CASTBOT_TRUSTED_PROXIES=%s\n' "127.0.0.1/32" >> "$ENV_FILE"
    fi
    if [[ -n "$TOKEN" ]] && ! grep -q '^CASTBOT_DO_TOKEN=' "$ENV_FILE"; then
      printf 'CASTBOT_DO_TOKEN=%s\n' "$TOKEN" >> "$ENV_FILE"
    fi
    if [[ -n "$IMAGE" ]] && ! grep -q '^CASTBOT_IMAGE=' "$ENV_FILE"; then
      printf 'CASTBOT_IMAGE=%s\n' "$IMAGE" >> "$ENV_FILE"
    fi
    chmod 0600 "$ENV_FILE"
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
  # start_services validates this file and reloads caddy once the firewall is open.
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
  # The unit file is already written. Reload systemd before any later
  # systemctl command so a re-run does not warn about a changed unit.
  if systemd_available; then
    host_cmd systemctl daemon-reload
    if host_cmd systemctl is-active --quiet castbot; then
      RESTART_ON_EXIT=1
      host_cmd systemctl stop castbot
    fi
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
    if [[ -n "$secret" ]]; then
      write_note "$secret" "$url"
    fi
    unset secret url out
    ACCOUNT_NEW=1
  fi
  if [[ -n "$MODEL_PROVIDER" && -n "${MODEL_KEY:-}" ]]; then
    set +e
    out=$(printf '%s\n' "$MODEL_KEY" | run_castbot save-provider-key --email "$EMAIL" --provider "$MODEL_PROVIDER" 2>"$LAST_ERR")
    code=$?
    set -e
    unset MODEL_KEY
    if [[ "$code" -ne 0 ]]; then
      cat "$LAST_ERR" >&2
      die "could not save the model key"
    fi
    MODEL_SAVED=1
  fi
  unset MODEL_KEY
  set +e
  if [[ -n "$MODEL" ]]; then
    out=$(run_castbot create-assistant --email "$EMAIL" --model "$MODEL" 2>"$LAST_ERR")
  else
    out=$(run_castbot create-assistant --email "$EMAIL" 2>"$LAST_ERR")
  fi
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

# host_cmd runs a host program. CASTBOT_INSTALL_PREFIX is the test
# harness: the command is recorded and the real host is left alone.
# CASTBOT_HOST_STUB_DIR can supply a replacement program for that harness.
host_cmd() {
  if [[ -n "$PREFIX" ]]; then
    printf '%s\n' "$*" >> "${PREFIX}/host-cmd.log"
    if [[ -n "${CASTBOT_HOST_STUB_DIR:-}" ]]; then
      local name=$1
      shift
      if [[ -x "${CASTBOT_HOST_STUB_DIR}/${name}" ]]; then
        "${CASTBOT_HOST_STUB_DIR}/${name}" "$@"
        return
      fi
    fi
    return 0
  fi
  command "$@"
}

systemd_available() {
  if [[ -n "$PREFIX" ]]; then
    if [[ -n "${CASTBOT_HOST_STUB_DIR:-}" && -x "${CASTBOT_HOST_STUB_DIR}/systemctl" ]]; then
      return 0
    fi
    return 1
  fi
  command -v systemctl >/dev/null 2>&1
}

caddy_bin_available() {
  if [[ -n "$PREFIX" ]]; then
    if [[ -n "${CASTBOT_HOST_STUB_DIR:-}" && -x "${CASTBOT_HOST_STUB_DIR}/caddy" ]]; then
      return 0
    fi
    return 1
  fi
  command -v caddy >/dev/null 2>&1
}

# Package install starts caddy with its default config. enable alone does
# not load the file this script writes. Validate when caddy is installed,
# then enable and reload-or-restart. A later run uses the same path.
reload_caddy() {
  if [[ -z "$PUBLIC_HOST" ]]; then
    return 0
  fi
  if caddy_bin_available; then
    if ! host_cmd caddy validate --config "$CADDY_FILE"; then
      die "caddy config is not valid"
    fi
  fi
  host_cmd systemctl enable caddy
  host_cmd systemctl reload-or-restart caddy
}

wait_local() {
  local tries i
  tries=30
  if [[ -n "$PREFIX" ]]; then
    tries=1
  fi
  i=0
  while [[ "$i" -lt "$tries" ]]; do
    if host_cmd curl -fsS --max-time 2 http://127.0.0.1:8080/login >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    if [[ "$i" -lt "$tries" ]]; then
      sleep 1
    fi
  done
  return 1
}

# /login is the page the server answers with 200. curl checks the
# certificate and the hostname because --insecure is not set.
wait_https() {
  local budget deadline url
  url="https://${PUBLIC_HOST}/login"
  if [[ -n "${CASTBOT_HTTPS_WAIT_SECONDS:-}" ]]; then
    budget=$CASTBOT_HTTPS_WAIT_SECONDS
    if [[ ! "$budget" =~ ^[0-9]+$ ]]; then
      die "CASTBOT_HTTPS_WAIT_SECONDS must be a number of seconds"
    fi
  elif [[ -n "$PREFIX" ]]; then
    budget=0
  else
    budget=90
  fi
  if [[ -z "$PREFIX" ]]; then
    printf 'Waiting for https://%s/login\n' "$PUBLIC_HOST" >&2
  fi
  deadline=$((SECONDS + 10#$budget))
  while true; do
    if host_cmd curl -fsS --max-time 5 "$url" >/dev/null 2>&1; then
      return 0
    fi
    if ((SECONDS >= deadline)); then
      return 1
    fi
    sleep 1
  done
}

start_services() {
  if [[ -n "$PUBLIC_HOST" ]]; then
    local port
    port=22
    if [[ -n "${SSH_CONNECTION:-}" ]]; then
      port=${SSH_CONNECTION##* }
    fi
    if [[ ! "$port" =~ ^[0-9]+$ ]]; then
      die "could not read the ssh port"
    fi
    host_cmd ufw allow "${port}/tcp"
    host_cmd ufw allow 80/tcp
    host_cmd ufw allow 443/tcp
    host_cmd ufw --force enable
  fi
  host_cmd systemctl daemon-reload
  host_cmd systemctl enable --now castbot.service
  RESTART_ON_EXIT=0
  if [[ -n "$PUBLIC_HOST" ]]; then
    reload_caddy
  fi
  host_cmd systemctl enable --now fail2ban.service
  if wait_local; then
    return 0
  fi
  if [[ -z "$PREFIX" ]] && command -v journalctl >/dev/null 2>&1; then
    journalctl -u castbot -n 40 --no-pager -o cat 2>/dev/null \
      | grep -E 'machine host|castbot listening|trusted proxies' \
      | tail -n 5 >&2 || true
  fi
  die "castbot did not answer on /login"
}

finish() {
  if [[ -n "$PUBLIC_HOST" ]]; then
    if ! wait_https; then
      die "https://${PUBLIC_HOST}/login did not respond over TLS with a valid certificate. Check that the DNS A record points at this host and is not proxied, that ports 80 and 443 are open, and run: journalctl -u caddy"
    fi
  fi
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
  elif [[ "$ACCOUNT_NEW" == "1" ]]; then
    printf 'Sign in with this email and the password. Two-factor can be turned on later in Settings.\n'
  fi
  if [[ "$MODEL_SAVED" == "1" ]]; then
    printf 'CastBot is the first agent. The model key is saved. Start its machine.\n'
  else
    printf 'CastBot is the first agent. Add a model key, then start its machine.\n'
  fi
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
  choose_model
  model_choice_ok
  resolve_image
  write_env
  write_unit
  write_caddy
  onboard
  start_services
  finish
}

main "$@"
