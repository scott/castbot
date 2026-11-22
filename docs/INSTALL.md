# Install

`scripts/install.sh` installs the castbot control plane on a new host and creates the first agent. It is the first-time path. Later updates still use `scripts/deploy.sh` from a laptop.

On the host, as root:

```
curl -fsSL https://raw.githubusercontent.com/scott/castbot/main/scripts/install.sh | bash
```

The script reads prompts from the terminal, so the pipe is safe. It asks for an email, a display name, and a password. It then asks for an optional public hostname. With a hostname it installs a reverse proxy, opens ports 80 and 443, and sets `CASTBOT_PUBLIC_URL` to `https://` that name. Point DNS at the host before the proxy can obtain a certificate. Without a hostname, castbot listens on `127.0.0.1:8080` only. Tunnel that port and open `http://127.0.0.1:8080/login`.

The password must be 12 to 1024 characters. The script prints an authenticator secret once and copies it to `/root/castbot-first-login.txt` (mode 0600). Add the secret to an authenticator, then delete that file.

## What it creates

| Path | Role |
| --- | --- |
| `/opt/castbot/bin/castbot` | Symlink to the live release |
| `/opt/castbot/releases/<sha>/castbot` | The control-plane binary |
| `/opt/castbot/RELEASE` | The live commit |
| `/opt/castbot/data/castbot.db` | The SQLite database |
| `/etc/castbot/castbot.env` | Server env file, mode 0600, owner `castbot` |
| `/etc/systemd/system/castbot.service` | The service |
| `/etc/caddy/Caddyfile` | Present only when a public hostname is set |

The service runs as `castbot` and reads `CASTBOT_ENV_FILE=/etc/castbot/castbot.env`. The session key and the secrets key are generated in that file. They are not printed. When a machine token and a public hostname are both set, the first run writes `CASTBOT_IMAGE` to the newest public tag whose guest inputs match the installed commit. Set `CASTBOT_IMAGE` before that run to use a different image. Without both, the image is left unset and the machine host stays off. The service still answers on `/login`. A public hostname without a token still installs the proxy and sets `CASTBOT_PUBLIC_URL`. A second run keeps the session keys. If that run adds a hostname, a token, or an image that the file does not have yet, it appends those lines and does not print them. If a token and a public hostname are both set and no published image matches, the script stops before it writes the env file.

The script also installs `sqlite3`, `fail2ban`, and, when a hostname is set, `caddy`. When a hostname is set, it allows the current SSH port plus ports 80 and 443, then enables the firewall. It installs Go `go1.27.4` under `/usr/local/go` only when `go version` is not already that release, then builds a static binary from `CASTBOT_REF` (default `main`). A later run replaces that binary and keeps the env file. It stops the service for the account commands, then starts it again.

## The first agent

After the account exists, the script runs `castbot create-assistant`. That command creates one agent named CastBot in the pinned section when the account has no agents yet.

CastBot is a personal assistant for the owner and an expert on this product. Delegation is on, so CastBot can message the other agents on the account and hand them work. Write tools ask before they run. Read tools are allowed. The agent stays in draft until a model key is saved and its machine is started. The first chat line says to add a key and start the machine. The machine says hello once, on the first healthy start.

A second run does not rewrite CastBot. If the account already has other agents and no CastBot, the command stops and leaves those agents alone.

## Non-interactive

Set these in the environment when there is no terminal. `CASTBOT_INSTALL_NONINTERACTIVE=1` forces that path even on a terminal.

| Setting | Role |
| --- | --- |
| `CASTBOT_ADMIN_EMAIL` | First account email |
| `CASTBOT_ADMIN_NAME` | Display name |
| `CASTBOT_ADMIN_PASSWORD` | Password, 12 to 1024 characters |
| `CASTBOT_PUBLIC_HOST` | Optional hostname, without a scheme |
| `CASTBOT_DO_TOKEN` | Optional machine token |
| `CASTBOT_IMAGE` | Runtime image override. Unset chooses the public tag when a token and a hostname are both set |
| `CASTBOT_REF` | Branch, tag, or commit. The default is `main` |
| `CASTBOT_WORKSPACE` | Workspace name. The default is `Personal` |

The script refuses to run under `bash -x`, because a trace would print the password and the generated keys. It does not print `CASTBOT_DO_TOKEN`, `CASTBOT_SESSION_KEY`, or `CASTBOT_SECRETS_KEY`.

The runtime image is not built on the host. `docs/runtime.md` names the public tag, and `docs/DEPLOY.md` covers later updates.
