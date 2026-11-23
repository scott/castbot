# Secrets vault

Each person has a vault. A secret is an environment variable for the agents they give it to. A model key is a vault row for one keyed provider. Settings lists every keyed provider in the same row layout. The newest vault row for a provider is the one agents use. An older row says Not in use and can be deleted. Values are encrypted with NaCl secretbox. The 32-byte key is `CASTBOT_SECRETS_KEY` in the host env file (mode 0600). It is not the session key, it is not in the repo, and startup refuses a missing, short, all-zero, or identical key.

The guest receives a granted secret, and the model key for that agent's model, only when it starts or reloads. The value stays in memory and in the runtime environment. It is not written into the bundle, the page, the audit log, or a screenshot.

A command the agent runs does not receive that value. The runtime environment is scrubbed down to an allowlist (`PATH`, `HOME`, `LANG`, `TERM`, `TZ`, the workspace directory, and the install prefixes) before the command starts. Model key names, castlet tokens, and granted secrets are left out. On the secret's page, "Available to commands" copies that one name into the command. It is off until you turn it on. A model key name is never copied, even if the box is checked. The command runs as `agent`, not as the runtime user, and it cannot read `/app/.cast` or `/app/.castlet`. Stopping the request stops that command and the processes it started. An MCP server receives that same command environment. A name in its config is passed through when the owner marked it available to commands, or when that value is not already in the runtime environment. A config the owner shipped can still place a runtime value under a different name. The agent cannot change that config. The copy next to the workspace is the one that loads, with the permission copy of agent.yaml. If that copy is missing or cannot be read, the workspace file is not used instead.

A connection token is not one of these grants. GitHub and DigitalOcean tokens stay in the vault and are used only by the host proxy. See [connections.md](connections.md).

Replacing, granting, revoking, or deleting reloads each running agent that has the secret. A paused agent is not woken. An agent that is replying, or waiting on an approval, reloads after that finishes. If a reload fails, the new value stays saved and that agent keeps the previous value until a retry works.

`CASTBOT_PROVIDER_KEY_*` is still the fallback when the vault has no row for that provider. The newest vault row wins. A deploy that already sets the process environment keeps working until someone saves a key in Settings.

The first start that copies old `secret_fixtures` rows into the vault also runs a WAL checkpoint and VACUUM, so the old plaintext is not left in free pages or the WAL. A later deploy has to set `CASTBOT_SECRETS_KEY` on the host before that start. This slice does not deploy.

## Named API keys in chat

An agent can ask for a new API key in the chat. The person types it on a masked card. The key is saved in the vault and granted to that agent only. The agent does not see the value. The result is `saved`, `declined`, `expired`, `already_configured`, or `rejected`. After `saved`, that agent receives the key on the next turn.

If this agent already has the key, the result is `already_configured` and no card is shown. If the vault already has that name and this agent does not, the card can grant the saved key without typing it again. A typed value replaces the saved key. The page never shows the saved value.

Asking to remove that key opens an approval card. Approving deletes the vault row. Every agent that had the key loses it on the next turn. Denying leaves the key. The card is denied automatically after 10 minutes. The result is `deleted`, `declined`, `expired`, or `not_found`.

The guest tool is `request_secret` with `name`, `reason`, and an optional `label`, and `connection` left empty. `delete_secret` takes the same name. A machine image built before those arguments still works: the control plane reads one `castbot-secret` fence at the end of the reply. That fence carries the name and the reason, never the key. A running machine sees the new instructions on the next bundle pull. The tool call itself waits for Replace machine.

Model keys, connection tokens, and names such as `GITHUB_TOKEN` are not saved or deleted this way. Connection tokens stay on the host. See [connections.md](connections.md). More than five cards an hour for one name return `declined`. One pending card for that name is reused.
