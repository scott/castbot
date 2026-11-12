# Admin CLI

`castbot admin` runs on the host as the castbot user. It reads `/etc/castbot/castbot.env` the same way the server does. It uses the server's SQLite file. It does not print tokens, machine URLs, or file contents.

The server must be up before a command that can change a machine. `castbot admin` checks `CASTBOT_PUBLIC_URL/login` and continues only when that returns 200. `machines` and `replace-machine --dry-run` only read the database, so they do not check the server and they do not call the machine API.

## machines

```
castbot admin machines
```

One line per live machine: agent name, state, image tag, and `update available` when that tag differs from `CASTBOT_IMAGE`. A line also says `cleanup pending` when a leftover machine from a failed replace still needs to be deleted. castbot retries that delete after one minute, then doubles the wait up to one hour. After 24 hours castbot stops retrying. The leftover MicroVM keeps running. Delete it in the machine host. It carries the tag `castbot-replace:` followed by the replacement id. The cleanup pending line stays. The agent can be used while that cleanup is pending. This command does not call the machine API.

## replace-machine

```
castbot admin replace-machine <agent id or Cast CLI slug> [--dry-run] [--yes] [--no-copy]
```

This is the same replace the Machine tab runs. It starts a new machine on `CASTBOT_IMAGE`, copies memory and workspace files, and deletes the old machine only after the new one is healthy.

`--dry-run` prints the agent, the machine id prefix, the state, the current image, the target image, and any reason the replace would be refused. It does not call the machine API.

Without `--yes`, the command prints the plan and waits until the agent name is typed. A different name stops the command.

`--no-copy` skips the memory and file copy. It is not in the web UI. The command writes a `replace_no_copy` machine event. The history list does not show that event.

Exit 0 means the agent is on the new machine. Exit 1 means the old machine was kept. Exit 2 means the switch succeeded and the old machine still needs to be deleted. castbot retries that delete on its own. A paused agent stays paused. SIGINT or SIGTERM stops the command and rolls the replace back when the switch has not committed.
