# Install

`scripts/install.sh` installs the castbot control plane on a new host and creates the first agent. It is the first-time path. Later updates still use `scripts/deploy.sh` from a laptop.

On the host, as root:

```
curl -fsSL https://raw.githubusercontent.com/scott/castbot/main/scripts/install.sh | bash
```

The script reads prompts from the terminal, so the pipe is safe. Each question explains what it is for. It asks for an email, a display name, and a password of 12 to 1024 characters on one line. It then asks for an optional public hostname. A hostname installs a reverse proxy, opens ports 80 and 443, and sets `CASTBOT_PUBLIC_URL` to `https://` that name. Do not include a scheme or a port. Point the DNS A record at the host, and leave that record unproxied, before the proxy can obtain a certificate. The script reloads the proxy after writing its config. It prints success only after `https://<hostname>/login` answers with a valid certificate. Without a hostname, castbot listens on `127.0.0.1:8080` only. Tunnel that port and open `http://127.0.0.1:8080/login`.

The machine host is optional. The prompt says Machine host. It is where agent machines run. DigitalOcean is supported today. Choosing it asks for a DigitalOcean API token, which is a personal access token that can manage machines. The token is stored in the env file and is not printed. Press Enter to skip. The site still runs, and agent machines stay off. Sign-in uses the email and password. Two-factor can be turned on later in Settings.

The model provider is optional. The prompt says Model provider. It lists OpenRouter, DigitalOcean, OpenAI, Anthropic, Google Gemini, and xAI. Paste a key for the one you choose. Choosing DigitalOcean there asks for a model access key. That key is separate from the API token on the machine host screen. After the program is installed, the installer lists the models that key can use. Press Enter to use the first model, or enter a number or a model id. The key is stored in the vault and is not printed. It is not written to the env file. Press Enter at the model provider prompt to skip and add a key later in Settings.

## What it creates

| Path | Role |
| --- | --- |
| `/opt/castbot/bin/castbot` | Symlink to the live release |
| `/opt/castbot/releases/<sha>/castbot` | The control-plane binary |
| `/opt/castbot/RELEASE` | The live commit |
| `/opt/castbot/VERSION` | The live version. A tagged release is `MAJOR.MINOR.PATCH`. Any other commit is `0.0.0` plus that commit. |
| `/opt/castbot/data/castbot.db` | The SQLite database |
| `/etc/castbot/castbot.env` | Server env file, mode 0600, owner `castbot` |
| `/etc/systemd/system/castbot.service` | The service |
| `/etc/caddy/Caddyfile` | Present only when a public hostname is set |

The service runs as `castbot` and reads `CASTBOT_ENV_FILE=/etc/castbot/castbot.env`. The session key and the secrets key are generated in that file. They are not printed. When a machine token and a public hostname are both set, the first run writes `CASTBOT_IMAGE` to the newest public tag whose guest inputs match the installed commit. Set `CASTBOT_IMAGE` before that run to use a different image. Without both, the image is left unset and the machine host stays off. The service still answers on `/login`. A public hostname without a token still installs the proxy and sets `CASTBOT_PUBLIC_URL`. A second run keeps the session keys. If that run adds a hostname, a token, or an image that the file does not have yet, it appends those lines and does not print them. If a token and a public hostname are both set and no published image matches, the script stops before it writes the env file.

The script also installs `sqlite3`, `fail2ban`, and, when a hostname is set, `caddy`. When a hostname is set, it allows the current SSH port plus ports 80 and 443, then enables the firewall. It writes `/etc/caddy/Caddyfile`, validates that file when `caddy` is installed, enables caddy, and reloads it. The package starts caddy with its default config, so a reload is required on the first run and on a later run. The install then checks `http://127.0.0.1:8080/login` and `https://<hostname>/login`. The https check retries for about 90 seconds and requires a valid certificate. Set `CASTBOT_HTTPS_WAIT_SECONDS` to change that wait. If https does not come up, the script exits with an error and does not print the success line. Check that the DNS A record points at this host and is not proxied, that ports 80 and 443 are open, and run `journalctl -u caddy`. It installs Go `go1.27.4` under `/usr/local/go` only when `go version` is not already that release, then builds a static binary from `CASTBOT_REF` (default `main`). A later run replaces that binary and keeps the env file. It reloads systemd, stops the service for the account commands, then starts it again.

## The first agent

After the account exists, the script runs `castbot create-assistant`. That command creates one agent named CastBot in the pinned section when the account has no agents yet.

CastBot is a personal assistant for the owner and an expert on this product. Delegation is on, so CastBot can message the other agents on the account and hand them work. Write tools ask before they run. Read tools are allowed. The agent stays in draft until its machine is started. When the installer saved a model key, that model is stored on CastBot and the first chat line says to start the machine. Otherwise the first chat line says to add a key and start the machine. The machine says hello once, on the first healthy start. A later run does not change the model on an existing CastBot.

A second run does not rewrite CastBot. If the account already has other agents and no CastBot, the command stops and leaves those agents alone.

## Non-interactive

Set these in the environment when there is no terminal. `CASTBOT_INSTALL_NONINTERACTIVE=1` forces that path even on a terminal.

| Setting | Role |
| --- | --- |
| `CASTBOT_ADMIN_EMAIL` | First account email |
| `CASTBOT_ADMIN_NAME` | Display name |
| `CASTBOT_ADMIN_PASSWORD` | Password, 12 to 1024 characters |
| `CASTBOT_PUBLIC_HOST` | Optional hostname, without a scheme |
| `CASTBOT_DO_TOKEN` | Optional DigitalOcean API token for agent machines |
| `CASTBOT_MODEL_PROVIDER` | Optional inference provider id. Set with the key and the model, or leave all three unset |
| `CASTBOT_MODEL_KEY` | Optional model key for that provider. Stored in the vault. Not printed and not written to the env file |
| `CASTBOT_MODEL` | Model id from that provider, such as `xai/grok-4.7` |
| `CASTBOT_IMAGE` | Runtime image override. Unset chooses the public tag when a token and a hostname are both set |
| `CASTBOT_REF` | Branch, tag, or commit. The default is `main` |
| `CASTBOT_WORKSPACE` | Workspace name. The default is `Personal` |
| `CASTBOT_HTTPS_WAIT_SECONDS` | Seconds to wait for https when a hostname is set. The default is 90 |

The script refuses to run under `bash -x`, because a trace would print the password and the generated keys. It does not print `CASTBOT_DO_TOKEN`, `CASTBOT_MODEL_KEY`, `CASTBOT_SESSION_KEY`, or `CASTBOT_SECRETS_KEY`. A partial model setting stops the install before it changes the host. When all three model settings are present, the script checks the provider and that the model id starts with that provider. It does not ask the provider for a list.

The runtime image is not built on the host. `docs/runtime.md` names the public tag, and `docs/DEPLOY.md` covers later updates.
