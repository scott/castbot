# Coding runtime image

Every agent uses one shared image, the `runtime` target of `Dockerfile`. New agents get whatever tag `CASTBOT_IMAGE` names at create time. This repository does not change that tag, does not deploy, and does not replace any agent. An existing agent stays on the image it was created with until the owner runs Replace machine.

## What the image contains

The image is Debian bookworm with Python 3.11, castlet, and the Cast runtime from the pinned commit in the Dockerfile. It also contains:

| Tool | Pin |
| --- | --- |
| git | Debian bookworm `1:2.39.5-0+deb12u3` |
| curl | Debian bookworm `7.88.1-10+deb12u15` |
| ripgrep (`rg`) | Debian bookworm `13.0.0-4+b2` |
| build-essential | Debian bookworm `12.9` (gcc, g++, make) |
| ca-certificates | Debian bookworm `20250419~deb12u2` |
| Node.js and npm | `26.13.1` official linux-x64 tarball, SHA256 in `deploy/runtime-install.sh` |
| Go | `1.27.4` official linux-amd64 tarball, same toolchain as this repo |
| gh | `2.104.0` official linux-amd64 tarball |
| doctl | `2.4.0` official linux-amd64 tarball |

The four downloaded archives are checked with SHA256 during the image build. The apt packages are pinned to those versions. Their dependencies come from the Debian archive and are not hashed one by one. The process runs as `nobody`. The image has no agent and no provider key.

`CAST_AGENT_DIR=/app/agent`, so the built-in git tools look at the agent directory. `GIT_PYTHON_REFRESH=quiet` stays set. Project repositories under `workspace/` are used through `bash` and the `git` binary. Write tools in the castbot catalog stay `ask`. Read tools stay `always-allow`.

The castbot catalog does not include `write_file`, `edit_file`, or `web_fetch` yet. Those wait for a later Cast pin. Upstream, `web_fetch` now defaults to `ask` (castagent#427). Until that pin, fetching a page is `bash` plus `curl`, and `bash` stays `ask`.

## Writable prefix

`/app/.local` is owned by `nobody` and is not world-writable. These variables point installs there, and `/app/.local/bin` is first on `PATH`:

- `PYTHONUSERBASE=/app/.local` for `pip install --user`. `python3` and `pip` on `PATH` are the image interpreter. The Cast runtime stays at `/opt/cast/bin/python`, which is what castlet starts.
- `NPM_CONFIG_PREFIX=/app/.local` for `npm install -g`
- `GOBIN=/app/.local/bin`, `GOPATH=/app/.local/go`, `GOCACHE=/app/.local/cache/go-build`, `GOMODCACHE=/app/.local/go/pkg/mod`
- `GOTOOLCHAIN=local` so Go does not download another toolchain

The prefix is on the machine disk, so it remains across pause and resume. Replace machine copies memory and workspace files only. It does not copy `/app/.local`. A later change may copy it with a size cap. This image does not.

## Guest stubs

The image carries the guest side of Connections so a later control-plane change does not need a second image. With no host proxy, the stubs fail closed. They do not read `GH_TOKEN`, `GITHUB_TOKEN`, `GITHUB_PERSONAL_ACCESS_TOKEN`, `DIGITALOCEAN_ACCESS_TOKEN`, or the other names in `FilterEnv`, and they do not put a connection token in the guest.

| Program | Disconnected behavior |
| --- | --- |
| `git` | Runs the real git binary. It does not rewrite `https://github.com/`. Credential helpers and `http.extraHeader` passed with `-c` are removed. `GIT_TERMINAL_PROMPT=0`, so a missing credential does not wait for a prompt. |
| `castbot-git-credential` | Prints no username and no password. Stderr says connections are not available. |
| `gh` | Runs the real `gh` with an empty config directory and no token. `gh auth login`, `gh auth token`, `gh auth setup-git`, `gh auth refresh`, and `gh --with-token` are refused, including when flags or `--` come first. |
| `doctl` | Runs the real `doctl` with an empty config and no token. `doctl auth` is refused, including when flags or `--` come first. `--access-token` and `-t` are removed, including after `--`. |
| `castbot-mcp github` and `castbot-mcp digitalocean` | Speak MCP over stdio. `initialize` succeeds. `tools/list` and `tools/call` return the fixed not-connected sentence and do not wait. |
| `castbot-mcp request_secret` | Lists `request_secret`. A call returns the not-connected sentence, or rejects a connection id other than `github` or `digitalocean`. |

When the host reports a provider as live, git rewrites `https://github.com/` and `https://www.github.com/` to the local git egress, doctl sends its API client at the local upstream prefix with the placeholder `castbot-local` (the egress strips that header), and `gh api` (flags `--method`, `--header`, `--input`, `--silent`, `--verbose`, `--hostname`) is posted to the local HTTP egress. Other `gh` commands stay unauthenticated, because `gh` has no plain HTTP API base URL that preserves the GitHub API without a guest token. The MCP shims are the GitHub and DigitalOcean tool path. `request_secret` is forwarded when the proxy is enabled, including when that provider is not connected yet. HTTP, git, and MCP are not.

When any provider is live, git, gh, and doctl get `NO_PROXY` and `no_proxy` with `127.0.0.1` and `localhost` added, so they do not send the local egress through `HTTP_PROXY`. An existing list is kept.

The fixed sentences are:

- `Connections are not available on this machine.`
- `This action needs approval.`
- `The connection proxy refused a redirect.`
- `The connection request was rejected.`

A host body is not copied into the guest unless it is one of those sentences, a known card result (`saved`, `declined`, `expired`, `already_configured`), or an upstream response the host already accepted.

## Guest and host contract

Castlet listens on `127.0.0.1:8090` inside the guest (`CASTLET_EGRESS_ADDR`, default `127.0.0.1:8090`). That port is not published. Peers that are not loopback are ignored. The stubs call `CASTLET_EGRESS_URL` (default `http://127.0.0.1:8090`) and ignore any other host.

The local routes do not take a token. Castlet uses the machine capability it already holds, the same bearer it uses for `/bundle` and `/secrets`, when it calls the control plane. That value is not a connection token and is not given to git, gh, doctl, or the shims.

Local routes:

| Method and path | Body |
| --- | --- |
| `GET /status` | `{"enabled":false,"providers":[]}` |
| `POST /http` | JSON forward below. `200` with a forward result, or `403` with a failure. |
| `POST /mcp` | `{"server":"github","message":{...json-rpc...}}`. `200` is `{"message":{...}}`. |
| `POST /secret-request` | `{"connection":"github","reason":"...","name":""}`. `200` is `{"result":"saved"}`. |
| `GET` or `POST /git/{provider}/...` | Raw git smart HTTP. The path after the provider is the path on `github.com`. |
| `GET` or `POST /upstream/{provider}/...` | Raw API HTTP. doctl uses base `http://127.0.0.1:8090/upstream/digitalocean/`. |

`provider` and `server` and `connection` are `github` or `digitalocean`. `path` is an absolute path. The guest does not choose the upstream host or address. Methods are `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, and `DELETE`. Guest headers named authorization, cookie, host, and the hop-by-hop names are dropped. `X-Castbot-Approval` may carry an id of up to 80 letters, digits, `_`, or `-`. The guest buffers each request and response up to 32 MiB. A git clone whose pack is larger than that cannot pass this egress. The cap stays as it is, and it is a known limit for the host git proxy. A `3xx` from the host is not relayed and the `Location` is dropped.

Forward JSON, for `POST /http` and for the control-plane git and HTTP routes:

```json
{
  "provider": "digitalocean",
  "method": "GET",
  "path": "/v2/account",
  "query": "",
  "headers": {"accept": "application/json"},
  "body_b64": "",
  "approval_id": ""
}
```

Forward result:

```json
{"status": 200, "headers": {"content-type": "application/json"}, "body_b64": ""}
```

Failure:

```json
{"error": "not_connected", "message": "Connections are not available on this machine."}
```

`error` is `not_connected`, `approval_required`, `redirect_refused`, or `bad_request`.

Control-plane routes, all under `{CASTBOT_URL}/api/castlet/agents/{agentID}` with `Authorization: Bearer` set to the machine capability:

| Method and path | When |
| --- | --- |
| `GET /egress` | Liveness. `200` with the status document. Anything else, including `404`, is disabled. |
| `POST /egress/http` | API forward. Host maps `github` to `api.github.com` and `digitalocean` to `api.digitalocean.com`. |
| `POST /egress/git` | Git smart HTTP. Host maps `github` to `https://github.com` plus `path`. |
| `POST /egress/mcp` | One JSON-RPC message for `server`. |
| `POST /egress/secret-request` | Card. Forwarded when liveness is enabled, including `{enabled:true, providers:[]}` and when only the other provider is listed. A disabled proxy, including `404` and a timeout, is refused at once and does not wait. When it is forwarded, the card may take up to 60 minutes. The result is only `saved`, `declined`, `expired`, or `already_configured`. |

Castlet caches liveness for 5 seconds. A forward still goes to the host, which is the place that checks the grant, the allowlist, TLS, private addresses, redirects, and approval. The guest client does not follow a redirect and does not use `HTTP_PROXY` for these calls. `request_secret` reasons are trimmed to 500 characters and token-shaped fragments are replaced with `[redacted]` before they are sent.

Example MCP config, with no environment entries, is shipped at `/opt/castbot/mcp/github.yaml`, `/opt/castbot/mcp/digitalocean.yaml`, and `/opt/castbot/mcp/request_secret.yaml`.

## Size and boot

Measured with the local docker daemon on 12 November 2026. No machine was created.

`docker image inspect --format '{{.Size}}'` on this daemon:

- Previous runtime image: 90,470,954 bytes (86.3 MiB).
- This runtime image: 384,927,917 bytes (367.1 MiB).
- Added: 294,456,963 bytes (280.8 MiB), mostly Go, Node.js, and the compiler.

Time from `docker run` until `python -c 'import cast.runtime'` prints, image already present, three runs:

- Previous runtime: 1.422 s, 1.409 s, 1.371 s.
- This runtime: 1.493 s, 1.431 s, 1.406 s.

The process start is about the same. The larger image is the part that would slow a first pull.

The S04 create-to-running figure of 16.0 s is a MicroVM with the image already cached. It was not remeasured. A first pull of this larger image adds time on top of that and was not measured here.
