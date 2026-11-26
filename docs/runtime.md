# Coding runtime image

Every agent uses one shared image, the `runtime` target of `Dockerfile`. New agents get whatever tag `CASTBOT_IMAGE` names at create time. An existing agent stays on the image it was created with until the owner runs Replace machine.

The runtime image workflow on `main` pushes `ghcr.io/scott/castbot-runtime:<full commit>` when that commit's guest inputs differ from the newest public tag. Guest inputs are the Dockerfile, `deploy/`, the Go module files, the guest commands, and the packages those commands import. A test file is not an input. A matching public tag is left in place. The installer writes the newest matching tag into `CASTBOT_IMAGE` when a machine token and a public hostname are both set. Otherwise the machine host stays off and the service still starts. Publishing a tag does not edit a server env file, does not deploy, and does not replace a machine.

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

The four downloaded archives are checked with SHA256 during the image build. The apt packages are pinned to those versions. Their dependencies come from the Debian archive and are not hashed one by one. castlet starts as root. A separate process stays root and only starts the Cast runtime as `nobody` and runs commands as `agent`. The rest of castlet continues as `nobody`. The image has no agent and no provider key.

`CAST_AGENT_DIR=/app/agent`, so the built-in git tools look at the agent directory. `GIT_PYTHON_REFRESH=quiet` stays set. Project repositories under `workspace/` are used through `bash` and the `git` binary. Write tools in the castbot catalog stay `ask`. Read tools stay `always-allow`.

The castbot catalog does not include `write_file`, `edit_file`, or `web_fetch` yet. Those wait for a later Cast pin. Upstream, `web_fetch` now defaults to `ask` (castagent#427). Until that pin, fetching a page is `bash` plus `curl`, and `bash` stays `ask`.

## Writable prefix

`/app/.local` is owned by `nobody`, group `castwork`, and is not world-writable. The runtime account's only group is `castwork`, and `agent` is in that group. The directory is setgid, so files created there stay in that group. A command whose groups are anything else is refused. `/app/.local/bin` is last on `PATH`, after the system directories, so a program installed there cannot shadow `git`, `python`, or castlet. Git treats `/app/agent` and repositories inside it as safe directories, because the runtime and a command share that workspace. Castlet state and the permission copy of `agent.yaml` live next to the workspace, not inside it. Command temporary files use `/app/.agent-tmp`. The runtime asks castlet to start a command. Stopping that request stops the command and the processes it started.

These variables point installs at that prefix:

- `PYTHONUSERBASE=/app/.local` for `pip install --user`. `python3` and `pip` on `PATH` are the image interpreter. The Cast runtime stays at `/opt/cast/bin/python`, which is what castlet starts.
- `NPM_CONFIG_PREFIX=/app/.local` for `npm install -g`
- `GOBIN=/app/.local/bin`, `GOPATH=/app/.local/go`, `GOCACHE=/app/.local/cache/go-build`, `GOMODCACHE=/app/.local/go/pkg/mod`
- `GOTOOLCHAIN=local` so Go does not download another toolchain

The prefix is on the machine disk, so it remains across pause and resume. Replace machine streams `workspace/`, `memory/`, and `/app/.local` as one archive onto the new machine. The archive keeps file modes and stores symlinks as links. A guest that does not have the archive export yet still sends its memory tar and workspace files. That copy does not include `/app/.local` until the guest is replaced onto an image that has the archive. If the new machine cannot import an archive, the copy stays that older per-file move.

## Guest stubs

The image carries the guest side of Connections so a later control-plane change does not need a second image. With no host proxy, the stubs fail closed. They do not read `GH_TOKEN`, `GITHUB_TOKEN`, `GITHUB_PERSONAL_ACCESS_TOKEN`, `DIGITALOCEAN_ACCESS_TOKEN`, or the other names in `FilterEnv`, and they do not put a connection token in the guest.

| Program | Disconnected behavior |
| --- | --- |
| `git` | Runs the real git binary. It does not rewrite `https://github.com/`. Credential helpers and `http.extraHeader` passed with `-c` are removed. `GIT_TERMINAL_PROMPT=0`, so a missing credential does not wait for a prompt. |
| `castbot-git-credential` | Prints no username and no password. Stderr says connections are not available. |
| `gh` | Runs the real `gh` with an empty config directory and no token. `gh auth login`, `gh auth token`, `gh auth setup-git`, `gh auth refresh`, and `gh --with-token` are refused, including when flags or `--` come first. |
| `doctl` | Runs the real `doctl` with an empty config and no token. `doctl auth` is refused, including when flags or `--` come first. `--access-token` and `-t` are removed, including after `--`. |
| `castbot-mcp github` and `castbot-mcp digitalocean` | Speak MCP over stdio. `initialize` succeeds. `tools/list` and `tools/call` return the fixed not-connected sentence and do not wait. Any other well-formed catalog id fails closed the same way. A malformed server name is rejected. |
| `castbot-mcp request_secret` | Lists `request_secret` and `delete_secret`. A well-formed connection id returns the not-connected sentence when the proxy is down. A malformed connection id is rejected and is not forwarded. A named key is `request_secret` with `name` and an empty `connection`. `delete_secret` takes that name. An older image lists only `request_secret` for `github` and `digitalocean` until Replace machine. The served instructions still describe a `castbot-secret` fence for that image. |
| `castbot-mcp agents` | Lists `create_agent`, `list_agents`, `list_routines`, `create_routine`, `update_routine`, `delete_routine`, `run_routine`, `create_group`, `update_group`, `add_group_members`, `remove_group_members`, `delete_group`, `pin_group`, `unpin_group`, `create_section`, `rename_section`, `delete_section`, `move_agents`, `place_group`, `describe_me`, `account_usage`, `machine_status`, `approval_history`, `routine_runs`, `list_secret_names`, `list_files`, `list_connections`, `list_cli_tokens`, `account_overview`, and `search_account`, plus `pause_machine`, `wake_machine`, `start_machine`, `retry_machine`, `refresh_machine`, `stop_reply`, `retry_run`, `list_threads`, and `open_thread`, plus `update_name` and `set_theme`, plus `create_page`, `update_page`, `rename_page`, `delete_page`, `list_pages`, `read_page`, and `share_page`. A call is posted to local `POST /agents`. It is not a connection and does not check whether a provider is connected. An older image lists only the tools it was built with until Replace machine. The served instructions still describe the same steps. A `castbot-create` fence at the end of a reply creates the agent, a `castbot-routine` fence creates, updates, deletes, or runs one routine now, a `castbot-account` fence reads one account fact and the control plane appends the report, and a `castbot-organize` fence creates or changes a group or a sidebar section, and a `castbot-machine` fence pauses, wakes, starts, tries a failed start again, or refreshes the machine, and a `castbot-chat` fence stops a reply, retries a failed run, lists threads, or opens a thread while this turn is Chat, and a `castbot-settings` fence changes the display name or the theme, and a `castbot-page` fence creates, updates, renames, deletes, lists, reads, or shares a page, until the machine is replaced. On a new image, `read_page` may return up to 40 KiB. Other agent tool text still stops at 8000 bytes. A fence reply is still cut there, so an older image cannot return the full document. |

When the host reports a provider as live, git rewrites `https://github.com/` and `https://www.github.com/` to the local git egress, doctl sends its API client at the local upstream prefix with the placeholder `castbot-local` (the egress strips that header), and `gh api` (flags `--method`, `--header`, `--input`, `--silent`, `--verbose`, `--hostname`) is posted to the local HTTP egress. Other `gh` commands stay unauthenticated, because `gh` has no plain HTTP API base URL that preserves the GitHub API without a guest token. The MCP shims are the GitHub and DigitalOcean tool path. `request_secret` is forwarded when the proxy is enabled, including when that provider is not connected yet. HTTP, git, and MCP are not.

When any provider is live, git, gh, and doctl get `NO_PROXY` and `no_proxy` with `127.0.0.1` and `localhost` added, so they do not send the local egress through `HTTP_PROXY`. An existing list is kept.

The fixed sentences are:

- `Connections are not available on this machine.`
- `This action needs approval.`
- `The connection proxy refused a redirect.`
- `The connection request was rejected.`

A host body is not copied into the guest unless it is one of those sentences, a known card result (`saved`, `declined`, `expired`, `already_configured`, `rejected`, `deleted`, `not_found`), or an upstream response the host already accepted. Connection cards still use the first four.

## Guest and host contract

Castlet listens on `127.0.0.1:8090` inside the guest (`CASTLET_EGRESS_ADDR`, default `127.0.0.1:8090`). That port is not published. Peers that are not loopback are ignored. The stubs call `CASTLET_EGRESS_URL` (default `http://127.0.0.1:8090`) and ignore any other host.

The local routes do not take a token. Castlet uses the machine capability it already holds, the same bearer it uses for `/bundle` and `/secrets`, when it calls the control plane. That value is not a connection token and is not given to git, gh, doctl, or the shims.

Local routes:

| Method and path | Body |
| --- | --- |
| `GET /status` | `{"enabled":false,"providers":[]}` |
| `POST /http` | JSON forward below. `200` with a forward result, or `403` with a failure. |
| `POST /mcp` | `{"server":"github","message":{...json-rpc...}}`. `200` is `{"message":{...}}`. |
| `POST /secret-request` | `{"connection":"github","reason":"...","name":""}`, `{"action":"save","connection":"","name":"SERVICE_API_KEY","label":"...","reason":"..."}`, or `{"action":"delete","name":"SERVICE_API_KEY","reason":"..."}`. `200` is `{"result":"saved"}` or another known card result. A malformed connection id is refused locally and is not forwarded. A well-formed id is forwarded, and the host refuses an id that is not in the catalog. A `value` field is ignored. |
| `POST /agents` | `{"name":"create_agent","arguments":{"description":"...","name":""}}`, `{"name":"list_agents","arguments":{}}`, or the same shape for `list_routines`, `create_routine`, `update_routine`, `delete_routine`, `run_routine`, the chat tools, the group and section tools, and the account read tools. `create_group` takes `title` and `agents`, and may take `people`. `update_group` takes `group` and `title`. `add_group_members` and `remove_group_members` take `group` and at least one of `agents` or `people`. `delete_group`, `pin_group`, and `unpin_group` take `group`. `create_section` takes `name`. `rename_section` takes `section` and `name`. `delete_section` takes `section`. `move_agents` takes `agents` and either `section` or `out`. `place_group` takes `group` and an optional `section`. An empty `section`, or `groups`, puts the group back in the Groups list. `search_account` takes `query`. `approval_history` may take `query`. `routine_runs` may take `id`. `run_routine` takes `id`. `stop_reply` and `retry_run` take an optional `thread`. `list_threads` takes no arguments. `open_thread` takes `thread` and returns recent turns only while this turn is Chat. `update_name` takes `name`. `set_theme` takes `theme`. `create_page` takes `title` and `document`. `update_page` takes `page` and `document` and may take `revision`. `rename_page` takes `page` and `title`. `delete_page` and `read_page` take `page`. `list_pages` takes no arguments. `share_page` takes `page` and `visibility` (`only_you`, `workspace`, `invited`, or `link`) and may take `people` and `agents`. `pause_machine`, `wake_machine`, `start_machine`, `retry_machine`, and `refresh_machine` take no arguments. The other account reads take no arguments. `200` is `{"text":"...","error":false}`. Not gated on provider status. `read_page` may return up to 40 KiB. Other agent tool text still stops at 8000 bytes. |
| `GET` or `POST /git/{provider}/...` | Raw git smart HTTP. The path after the provider is the path on `github.com`. |
| `GET` or `POST /upstream/{provider}/...` | Raw API HTTP. doctl uses base `http://127.0.0.1:8090/upstream/digitalocean/`. |

`provider`, `server`, and `connection` are a catalog id: a lowercase letter, then up to 31 letters, digits, or underscores. `request_secret` and `agents` are not catalog ids. `POST /agents` does not take a provider. `path` is an absolute path. The guest does not choose the upstream host or address. Methods are `GET`, `HEAD`, `POST`, `PUT`, `PATCH`, and `DELETE`. Guest headers named authorization, cookie, host, and the hop-by-hop names are dropped. `X-Castbot-Approval` may carry an id of up to 80 letters, digits, `_`, or `-`. The guest buffers each request and response up to 32 MiB. A git clone whose pack is larger than that cannot pass this egress. The cap stays as it is, and it is a known limit for the host git proxy. A `3xx` from the host is not relayed and the `Location` is dropped.

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
| `POST /egress/mcp` | One JSON-RPC message for `server`. `digitalocean` is forwarded only to `https://mcp.digitalocean.com/mcp`. |
| `POST /egress/secret-request` | Card. Forwarded when liveness is enabled, including `{enabled:true, providers:[]}` and when only the other provider is listed. A disabled proxy, including `404` and a timeout, is refused at once and does not wait. A connection card may take up to 60 minutes. A delete card expires in 10 minutes. The guest HTTP wait stays 61 minutes. Connection results are `saved`, `declined`, `expired`, or `already_configured`. A named key may also return `rejected`, `deleted`, or `not_found`. |
| `POST /agents` | Create or list agents, list, create, update, delete, or run routines, change groups and sidebar sections, read account facts, and start, pause, wake, or refresh its machine, or stop, retry, and open a thread, or change the display name and the theme, or build and share a page, for the machine's account and the agent in the path. `create_agent` takes `description` and an optional `name`. `list_agents` and `list_routines` take no arguments. `create_routine` takes `name`, `cron`, and `prompt`, plus optional `timezone`, `missed`, and `approval`. `update_routine` takes `id` and any fields to change, including `enabled`. `delete_routine` takes `id`. `run_routine` takes `id`. `stop_reply` and `retry_run` take an optional `thread`. `list_threads` takes no arguments. `open_thread` takes `thread` and returns recent turns only while this turn is Chat. `update_name` takes `name`. `set_theme` takes `theme`. `create_page` takes `title` and `document`. `update_page` takes `page` and `document` and may take `revision`. `rename_page` takes `page` and `title`. `delete_page` and `read_page` take `page`. `list_pages` takes no arguments. `share_page` takes `page` and `visibility` (`only_you`, `workspace`, `invited`, or `link`) and may take `people` and `agents`. Group and section tools are `create_group`, `update_group`, `add_group_members`, `remove_group_members`, `delete_group`, `pin_group`, `unpin_group`, `create_section`, `rename_section`, `delete_section`, `move_agents`, and `place_group`. `create_group` takes `title` and `agents`, and may take `people`. `update_group` takes `group` and `title`. `add_group_members` and `remove_group_members` take `group` and at least one of `agents` or `people`. `delete_group`, `pin_group`, and `unpin_group` take `group`. `create_section` takes `name`. `rename_section` takes `section` and `name`. `delete_section` takes `section`. `move_agents` takes `agents` and either `section` or `out`. `place_group` takes `group` and an optional `section`. An empty `section`, or `groups`, puts the group back in the Groups list. The account reads are `describe_me`, `account_usage`, `machine_status`, `approval_history`, `routine_runs`, `list_secret_names`, `list_files`, `list_connections`, `list_cli_tokens`, `account_overview`, and `search_account`, plus `pause_machine`, `wake_machine`, `start_machine`, `retry_machine`, `refresh_machine`, `stop_reply`, `retry_run`, `list_threads`, and `open_thread`, plus `update_name` and `set_theme`, plus `create_page`, `update_page`, `rename_page`, `delete_page`, `list_pages`, `read_page`, and `share_page`. `search_account` takes `query`. `approval_history` may take `query`. `routine_runs` may take `id`. `pause_machine`, `wake_machine`, `start_machine`, `retry_machine`, and `refresh_machine` take no arguments. The others take no arguments. They do not return a secret value, a connection token, or a Cast CLI token. A `user_id` or `agent_id` argument is ignored. The response is `{"text":"...","error":false}`. This is not gated on a connected provider. A `read_page` reply may be up to 40 KiB. The guest still drops other agent tool text longer than 8000 bytes. |

Castlet caches liveness for 5 seconds. A forward still goes to the host, which is the place that checks the grant, the allowlist, TLS, private addresses, redirects, and approval. The guest client does not follow a redirect and does not use `HTTP_PROXY` for these calls. `request_secret` reasons are trimmed to 500 characters and token-shaped fragments are replaced with `[redacted]` before they are sent.

Example MCP config, with no environment entries, is shipped at `/opt/castbot/mcp/github.yaml`, `/opt/castbot/mcp/digitalocean.yaml`, `/opt/castbot/mcp/request_secret.yaml`, and `/opt/castbot/mcp/agents.yaml`. This repository does not retag `CASTBOT_IMAGE`. A running machine keeps the image it was created with until Replace machine. The control plane still serves the create-agent, routine, API key, account, groups, machine, chat, settings, and page instructions to that machine. A `castbot-secret` fence at the end of a reply opens the save card or the delete card until the machine is replaced. A `castbot-account` fence reads one account fact and the control plane appends the report. A `castbot-organize` fence creates or changes a group or a sidebar section until the machine is replaced. A `castbot-machine` fence pauses, wakes, starts, tries a failed start again, or refreshes the machine until the machine is replaced. A `castbot-chat` fence stops a reply, retries a failed run, lists threads, or opens a thread while this turn is Chat until the machine is replaced. A `castbot-settings` fence changes the display name or the theme until the machine is replaced. A `castbot-page` fence creates, updates, renames, deletes, lists, reads, or shares a page until the machine is replaced. A fence reply still stops at 8000 bytes, so an older image cannot return the full document.

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

S12 does not build a golden checkpoint. The measured create-plus-boot times are under the 30 second line: 8.32 s to the first HTTP 200 for the tiny image in S00, and 16.0 s to running for this image when it was already cached. A later measurement of a cold pull can revisit that choice. This tree has no checkpoint call.
