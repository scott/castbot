# Connections

A connection lets an agent call GitHub or DigitalOcean without holding the token. The token stays in the vault on the control plane. The guest receives a grant, never the secret.

This is the host half of the design. The guest shim, the image, and the `gh` and `doctl` wrappers belong to the image slice. Until that slice lands, the control plane still refuses to put a connection token in the bundle, the guest environment, or the transcript.

## What is stored

Two catalog entries exist: GitHub and DigitalOcean. There is no custom MCP server in the product.

| Connection | Vault name | Hosts |
| --- | --- | --- |
| GitHub | `GITHUB_CONNECTION` | `api.github.com`, `github.com` |
| DigitalOcean | `DIGITALOCEAN_CONNECTION` | `api.digitalocean.com` |

The vault name is not an environment grant. `GrantSecret` rejects it. The guest pull rejects it. A hand-written grant row still does not leave the control plane: the pull keeps `kind = env` and also drops `GITHUB_CONNECTION` and `DIGITALOCEAN_CONNECTION` if a row is forced into that kind. castlet refuses the whole secrets response if either name appears.

The owner chooses the token's own scopes. castbot does not add a service list on top.

Settings, Connections owns GitHub and DigitalOcean, including the account switch. Connecting stores the token in the vault and turns the connection on for every agent on the account. The password field is blank on every page load. Saving a new value replaces the vault row. Leaving the field blank keeps the saved token. Disconnect turns the connection off for every agent and reloads a running agent. The token and the tool choices stay. No new machine is created. A connection can be on before any agent exists. The next agent you create inherits it.

Each tool has a level: Ask, Always allow, or Deny. Deny keeps a named tool listed and blocks it. Always allow on a GitHub or DigitalOcean write tool skips the host approval card. Ask keeps that card for a write. A read tool set to Ask does not open a second host card. A raw git or HTTP call that is not one of those named tools still asks before a mutating request.

Shell Actions is the builtin tool policy: bash, read, glob, and the git tools. It is not an egress provider, it has no token, and `request_secret` does not accept it. While it is on, every agent you own gets those tools at the levels you chose. While it is off, those tools are removed. A missing Shell Actions row stays on, so an existing agent keeps shell access until you disconnect it. The first save replaces a per-agent subset with the account levels.

## How a call is made

The guest does not talk to GitHub or DigitalOcean with the real token. The image slice runs castlet on `127.0.0.1:8090` inside the guest. That listener is loopback only and takes no bearer. castlet calls this control plane with the machine capability, the same bearer it uses for the bundle and for env secrets. That value is not a connection token.

`GET /api/castlet/agents/{id}/egress` returns the status document:

```json
{"enabled": true, "providers": ["github"]}
```

`enabled` is true for a live machine even when `providers` is empty. `providers` lists only connections that are turned on for that agent. Anything other than 200 means the guest treats connections as off.

Forward routes, all `POST`, with the machine bearer:

| Route | Body | Success |
| --- | --- | --- |
| `/egress/http` | `provider`, `method`, `path`, `query`, `headers`, `body_b64`, `approval_id` | `{"status":200,"headers":{},"body_b64":""}` |
| `/egress/git` | same document | same result. GitHub only. The host uses `https://github.com` plus `path`. |
| `/egress/mcp` | `{"server":"github","message":{}}` | `{"message":{}}` |
| `/egress/secret-request` | `{"connection":"github","reason":"...","name":""}` | `{"result":"saved"}` |

The guest does not choose the upstream host. `github` on `/egress/http` is `api.github.com`. `digitalocean` is `api.digitalocean.com`. `/egress/git` accepts GitHub only. `path` is absolute. Methods are GET, HEAD, POST, PUT, PATCH, and DELETE.

A refusal is HTTP 403:

```json
{"error": "not_connected", "message": "Connections are not available on this machine."}
```

`error` is `not_connected`, `approval_required`, `redirect_refused`, or `bad_request`. The messages are fixed sentences. The guest copies those four codes and the four card results. It does not copy any other host text.

`/egress/http`, `/egress/git`, and `/egress/mcp` run only when that provider is in the live list. `/egress/secret-request` runs whenever the status says `enabled`, including when `providers` is empty, so an agent can ask for a token it does not have yet.

The proxy allows HTTPS only, on the hosts above, with port 443. It refuses userinfo, IP literals, and any resolved address that is loopback, private, link-local, multicast, or carrier-grade NAT. Redirects are checked again, up to three, and the token is attached again after a redirect. A redirect that leaves the allowlist is `redirect_refused` and is not returned to the guest. The guest's `Authorization` and `Cookie` headers are dropped. A response body that echoes the token is rewritten before it is returned.

The decoded request body may be up to 32 MiB, which is the guest buffer. The JSON result the guest reads is also capped at 32 MiB, so the relayed upstream body is smaller once base64 and the envelope are counted. A larger upstream body is refused rather than cut off.

A mutating call (POST, PUT, PATCH, DELETE) needs a host approval card. A git fetch is a read only when it is real git smart HTTP on `github.com`: `GET` of a path ending in `/info/refs` with the query exactly `service=git-upload-pack`, or `POST` of `/<owner>/<repo>(.git)/git-upload-pack`. Mentioning `git-upload-pack` in an API query or path does not make the call a read. `git-receive-pack` stays a write, including `GET` discovery on `info/refs`.

The card shows a short summary of the request. Secret-shaped text in that summary is removed. The approval is stored with a hash of the exact call (method, path, query, and body; for a tool call, the tool name and canonical arguments). A later call is forwarded only when that hash matches, and the approval is consumed once. A different body or different arguments is rejected and does not reach the upstream. An approved card that is not used expires on the same clock as a pending card. The handler returns `approval_required` as soon as the card exists. It does not hold the request open: the guest's forward budget for HTTP, git, and MCP is 60 seconds. A denied, expired, or mismatched card does not reach the upstream. The card lasts 10 minutes. A runtime approval for a tool name does not authorize a connection write on its own.

`/egress/secret-request` may stay open for the 60 minute card. Its result is only `saved`, `declined`, `expired`, or `already_configured`.

Audit rows store the connection, method, path, and outcome. They do not store the token, the request body, or the response body.

## MCP

The shipped path is an in-process adapter. It exposes a small read and write set (current user, repos, issues, pull requests, and file contents for GitHub; account, droplets, and apps for DigitalOcean). Writes go through the same approval wait and the same allowlisted proxy.

Official MCP processes are not started on the 512 MB control plane. A Node server and a second native server, multiplied by concurrent agents, do not fit next to the control plane and the operating system. Set `CASTBOT_MCP_GITHUB` or `CASTBOT_MCP_DIGITALOCEAN` only on a host that has the memory. Those processes get a clean environment with one token variable, stop after 45 seconds idle, and their stdout is checked for the token. That path is optional and off by default.

The guest still sees a normal stdio MCP command: `/usr/local/bin/castbot-mcp` with one argument, `github`, `digitalocean`, or `request_secret`. The command file has no `env` block and no token. The image slice installs that binary and the loopback egress. This repo writes the command file into the bundle so a reload can pick it up.

If that binary is not in the image yet, the agent cannot start the shim. The token still does not enter the guest. The documented fallback is the same castlet egress: git, gh, and doctl in the guest call loopback, and the host adds the token. The guest environment still has no real token.

## request_secret

`request_secret(connection, reason)` renders a masked card in the chat. The typed value is posted to the control plane and stored in the vault. The tool's result is only `saved`, `declined`, `expired`, or `already_configured`. The card expires after 60 minutes. One pending card per connection is reused. More than five cards an hour return `declined`.

If a vault row already exists, the card asks for confirmation and does not require typing the token again. An optional field can replace it. The page never shows the saved value. Saving the card turns that connection on for every agent you own.

Enabling the connection updates the bundle (shim file and permissions) and reloads the guest after the current turn. The new tools show up on the next turn.

`request_secret` is `always-allow`. The card is the consent. Tool levels come from Settings, Connections. When a connection is on and a tool has no saved level, `mcp` is `ask`, read tools are `always-allow`, and write tools are `ask`.

## Limits

- The adapter is not the full official tool list. Reads and writes outside that list need the optional host process, which is not on by default and was not measured on a 512 MB control plane.
- The loopback egress, the `castbot-mcp` binary, and the git, gh, and doctl wrappers belong to the image slice. This slice is the control plane those stubs call. A rebase onto that slice should keep these host routes and drop any second guest listener.
- A person who can approve a card can cause the host to use the token. That is the point of the card. The guest process still never receives the token.
- An approval that is still pending when the guest's 60 second forward ends does not run later by itself. The person approves the card, and a following call uses it.
- Revoke stops new proxy calls. A request already inside the upstream timeout can finish.
- The optional host MCP process is not put through the allowlist. The default adapter is. Leave `CASTBOT_MCP_GITHUB` and `CASTBOT_MCP_DIGITALOCEAN` unset on the small control plane.
- Screenshots and logs in this repo use fake tokens. Do not put a real token in either.
