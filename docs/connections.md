# Connections

A connection lets an agent call a catalog service without holding the token. The token stays in the vault on the control plane. The guest receives a grant, never the secret.

This is the host half of the design. The guest shim, the image, and the `gh` and `doctl` wrappers belong to the image slice. Until that slice lands, the control plane still refuses to put a connection token in the bundle, the guest environment, or the transcript.

## What is stored

The catalog ships the connections in the table. Each connection is one catalog entry, registered from its own file. There is no custom MCP server in the product. The guest accepts any well-formed catalog id the host lists. The host refuses an id that is not in the catalog.

| Connection | Vault name | Hosts |
| --- | --- | --- |
| GitHub | `GITHUB_CONNECTION` | `api.github.com`, `github.com` |
| DigitalOcean | `DIGITALOCEAN_CONNECTION` | `mcp.digitalocean.com`, `api.digitalocean.com` |
| Web | `WEB_CONNECTION` | `api.search.brave.com` |
| Linear | `LINEAR_CONNECTION` | `api.linear.app` |
| Google | `GOOGLE_CONNECTION` | `imap.gmail.com`, `smtp.gmail.com`, `apidata.googleusercontent.com` |
| Slack | `SLACK_CONNECTION` | `slack.com` |
| Notion | `NOTION_CONNECTION` | `api.notion.com` |

The vault name is not an environment grant. `GrantSecret` rejects it. The guest pull rejects it. A hand-written grant row still does not leave the control plane: the pull keeps `kind = env` and also drops `GITHUB_CONNECTION` and `DIGITALOCEAN_CONNECTION` if a row is forced into that kind. A later connection's vault name is refused the same way. castlet refuses the whole secrets response if a connection vault name appears.

A catalog id is a lowercase letter followed by up to 31 letters, digits, or underscores. `request_secret` and `agents` are not catalog ids. A connection may store a second field in the same vault row, separated from the token by a tab. The agent cannot choose that field. A later connection adds `docs/connections/<id>.md` and one link in the list below.

## Catalog pages

- GitHub, on this page
- DigitalOcean, on this page
- [Web](connections/web.md)
- [Linear](connections/linear.md)
- [Google](connections/google.md)
- [Slack](connections/slack.md)
- [Notion](connections/notion.md)

The owner chooses the token's own scopes. castbot does not add a service list on top.

Settings, Connections owns each catalog connection, including the account switch. Connecting stores the token in the vault and turns the connection on for every agent on the account. After a token is saved, the page shows the first characters and xxxxxxx. Change opens a blank field. A new value replaces the vault row. Leaving that field blank keeps the saved token. Save returns to the connections list. A connected card on that list says Configure. Disconnect turns the connection off for every agent and reloads a running agent. The token and the tool choices stay. No new machine is created. A connection can be on before any agent exists. The next agent you create inherits it.

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

The guest does not choose the upstream host. `github` on `/egress/http` is `api.github.com`. `digitalocean` on `/egress/http` is `api.digitalocean.com`. DigitalOcean MCP is only `https://mcp.digitalocean.com/mcp`. `/egress/git` accepts GitHub only. `path` is absolute. Methods are GET, HEAD, POST, PUT, PATCH, and DELETE.

HTTP and git refusals are HTTP 403:

```json
{"error": "not_connected", "message": "Connections are not available on this machine."}
```

`error` is `not_connected`, `approval_required`, `redirect_refused`, or `bad_request`. The messages are fixed sentences. HTTP and git copy those four codes and the four card results. They do not copy any other host text.

An MCP refusal, once the JSON-RPC message has been read, is HTTP 200. The `message` is a JSON-RPC error whose text is one of those four sentences. The guest shim turns any non-200 into the not-connected sentence, so the real sentence has to travel inside a 200. A body that is not a JSON-RPC object stays HTTP 403.

`/egress/http`, `/egress/git`, and `/egress/mcp` run only when that provider is in the live list. `/egress/secret-request` runs whenever the status says `enabled`, including when `providers` is empty, so an agent can ask for a token it does not have yet.

The proxy allows HTTPS only, on the hosts above, with port 443. It refuses userinfo and IP literals. It dials a public address for that host. Loopback, private, link-local, multicast, and carrier-grade NAT addresses are not dialed. A private address in the same answer does not reject a public one. When the system resolver returns no public IPv4, the proxy resolves the name again through a public resolver and dials a public address from that answer. A name that resolves only to a non-public address is `off_allowlist`. Redirects are checked again, up to three, and the token is attached again after a redirect. A redirect that leaves the allowlist is `redirect_refused` and is not returned to the guest. The guest's `Authorization` and `Cookie` headers are dropped. A response body that echoes the token is rewritten before it is returned.

The decoded request body may be up to 32 MiB, which is the guest buffer. The JSON result the guest reads is also capped at 32 MiB, so the relayed upstream body is smaller once base64 and the envelope are counted. A larger upstream body is refused rather than cut off.

A mutating call (POST, PUT, PATCH, DELETE) needs a host approval card. A git fetch is a read only when it is real git smart HTTP on `github.com`: `GET` of a path ending in `/info/refs` with the query exactly `service=git-upload-pack`, or `POST` of `/<owner>/<repo>(.git)/git-upload-pack`. Mentioning `git-upload-pack` in an API query or path does not make the call a read. `git-receive-pack` stays a write, including `GET` discovery on `info/refs`.

The card shows a short summary of the request. Secret-shaped text in that summary is removed. The approval is stored with a hash of the exact call (method, path, query, and body; for a tool call, the tool name and canonical arguments). A later call is forwarded only when that hash matches, and the approval is consumed once. A different body or different arguments is rejected and does not reach the upstream. An approved card that is not used expires on the same clock as a pending card. The handler returns the approval sentence as soon as the card exists. It does not hold the request open: the guest's forward budget for HTTP, git, and MCP is 60 seconds. When Always approve for this prompt is already on, that card is approved and consumed in the same request and the call is forwarded. A denied, expired, or mismatched card does not reach the upstream. The card lasts 10 minutes. A runtime approval for a tool name does not authorize a connection write on its own.

`/egress/secret-request` may stay open for the 60 minute card. A connection card's result is only `saved`, `declined`, `expired`, or `already_configured`. A named API key uses the same route. See [secrets.md](secrets.md).

Audit rows store the connection, method, path, and outcome. They do not store the token, the request body, or the response body.

## MCP

GitHub uses an in-process adapter. It exposes a small read and write set (current user, repos, issues, pull requests, and file contents). Writes go through the same approval wait and the same allowlisted proxy.

DigitalOcean does not use that adapter. Every DigitalOcean MCP message is posted to `https://mcp.digitalocean.com/mcp` with the vault token. The guest still speaks to `castbot-mcp digitalocean`. The control plane opens the hosted session, then forwards `tools/list` and `tools/call`. `find_tools` and `retrieve_schema` are reads. `invoke_tool` is a write, so it asks unless the owner set Always allow. An `invoke_tool` that still needs a card is HTTP 200 with the approval sentence, and the hosted server is not called. An active Always approve grant for this prompt forwards that call in the same request. The older droplet and app tool names are refused. `CASTBOT_MCP_DIGITALOCEAN` is ignored, so a local process is not started for this connection.

A connected DigitalOcean account gets an SSH key named castbot. New droplets use that key when the create call does not already name one. The private key stays in the vault. Disconnect leaves the key in place. If the key is not registered yet, the create call is HTTP 200 with the sentence "The SSH key is not registered yet." and the hosted server is not called.

A local GitHub MCP process is not started on the 512 MB control plane. Set `CASTBOT_MCP_GITHUB` only on a host that has the memory. That process gets a clean environment with one token variable, stops after 45 seconds idle, and its stdout is checked for the token. That path is optional and off by default. It is not used for DigitalOcean.

The guest still sees a normal stdio MCP command: `/usr/local/bin/castbot-mcp` with one argument. The argument is `request_secret`, `agents`, or a catalog id such as `github` or `digitalocean`. A malformed id is rejected. The command file has no `env` block and no token. The image slice installs that binary and the loopback egress. This repo writes the command file into the bundle so a reload can pick it up.

If that binary is not in the image yet, the agent cannot start the shim. The token still does not enter the guest. The documented fallback is the same castlet egress: git, gh, and doctl in the guest call loopback, and the host adds the token. The guest environment still has no real token.

## request_secret

`request_secret(connection, reason)` renders a masked card in the chat for a catalog connection. The host accepts GitHub and DigitalOcean today. The typed value is posted to the control plane and stored in the vault. The tool's result is only `saved`, `declined`, `expired`, or `already_configured`. The card expires after 60 minutes. One pending card per connection is reused. More than five cards an hour return `declined`.

A named API key is not a connection token. `request_secret` can take `name`, `reason`, and an optional `label` with an empty `connection`. `delete_secret` asks before that key is removed. An image built before catalog ids were generic only accepts `github` and `digitalocean` and uses a `castbot-secret` fence until Replace machine. See [secrets.md](secrets.md).

If a vault row already exists, the card asks for confirmation and does not require typing the token again. An optional field can replace it. The page never shows the saved value. Saving the card turns that connection on for every agent you own.

Enabling the connection updates the bundle (shim file and permissions) and reloads the guest after the current turn. The new tools show up on the next turn.

`request_secret` is `always-allow`. The card is the consent. Tool levels come from Settings, Connections. When a connection is on and a tool has no saved level, `mcp` is `ask`, read tools are `always-allow`, and write tools are `ask`.

## Limits

- The GitHub adapter is not the full official tool list. Reads and writes outside that list need the optional host process, which is not on by default and was not measured on a 512 MB control plane. DigitalOcean tools come from the hosted MCP server.
- The loopback egress, the `castbot-mcp` binary, and the git, gh, and doctl wrappers belong to the image slice. This slice is the control plane those stubs call. A rebase onto that slice should keep these host routes and drop any second guest listener.
- A person who can approve a card can cause the host to use the token. That is the point of the card. The guest process still never receives the token.
- An approval that is still pending when the guest's 60 second forward ends does not run later by itself. The person approves the card, and a following call uses it.
- Revoke stops new proxy calls. A request already inside the upstream timeout can finish.
- The optional GitHub host MCP process is not put through the allowlist. The GitHub adapter and the DigitalOcean MCP server are. Leave `CASTBOT_MCP_GITHUB` unset on the small control plane. `CASTBOT_MCP_DIGITALOCEAN` is ignored.
- Screenshots and logs in this repo use fake tokens. Do not put a real token in either.
