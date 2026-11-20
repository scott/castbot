# Admin CLI

`castbot admin` runs on the host as the castbot user. It reads `/etc/castbot/castbot.env` the same way the server does. It uses the server's SQLite file. It does not print tokens, machine URLs, or file contents.

The server must be up before a command that can change a machine. `castbot admin` checks `CASTBOT_PUBLIC_URL/login` and continues only when that returns 200. `machines`, `replace-machine --dry-run`, `rollback-runtime --dry-run`, `backup`, and `backups` do not call the machine API. `backup` can run while the server is up. `restore` refuses while that login URL answers, and it refuses when the public URL is unset.

## machines

```
castbot admin machines
```

One line per live machine: agent name, state, image tag, and `update available` when that tag differs from `CASTBOT_IMAGE`. A line also says `cleanup pending` when a leftover machine from a failed replace still needs to be deleted. castbot retries that delete after one minute, then doubles the wait up to one hour. After 24 hours castbot stops retrying. The leftover MicroVM keeps running. Delete it in the machine host. It carries the tag `castbot-replace:` followed by the replacement id. The cleanup pending line stays. The agent can be used while that cleanup is pending. This command does not call the machine API.

## replace-machine

```
castbot admin replace-machine <agent id or Cast CLI slug> [--dry-run] [--yes] [--no-copy]
```

This is the same replace the Machine tab runs. It starts a new machine on `CASTBOT_IMAGE`, streams one archive of memory, workspace files, and `/app/.local`, and deletes the old machine only after the new one is healthy. The control plane proxies the archive and does not store the tree. A guest that is still on an older runtime has no archive export. The control plane then reads that guest's memory tar and workspace files and streams those into the archive. That first copy does not include `/app/.local`. The next replace does. If the new machine's runtime has no archive import yet, the copy stays the older per-file move, which also leaves `/app/.local` behind. File modes and symlinks are kept when both guests speak the archive. An archive larger than the destination disk stops the replace and leaves the agent on the old machine.

`--dry-run` prints the agent, the machine id prefix, the state, the current image, the target image, and any reason the replace would be refused. It does not call the machine API.

Without `--yes`, the command prints the plan and waits until the agent name is typed. A different name stops the command.

`--no-copy` skips the memory and file copy. It is not in the web UI. The command writes a `replace_no_copy` machine event. The history list does not show that event.

Exit 0 means the agent is on the new machine. Exit 1 means the old machine was kept. Exit 2 means the switch succeeded and the old machine still needs to be deleted. castbot retries that delete on its own. A paused agent stays paused. SIGINT or SIGTERM stops the command and rolls the replace back when the switch has not committed.

A move that copies memory saves the runtime it left. The Machine page offers that runtime for 7 days. A move onto the same image does not change the offer. `--no-copy` retires the offer and does not save a new one, because the memory was not copied.

## rollback-runtime

```
castbot admin rollback-runtime <agent id or Cast CLI slug> [--dry-run] [--yes]
```

This is the same replace, aimed at the runtime saved by the last memory-copying move. It streams the archive again. `--dry-run` prints the agent, the current image, the rollback image, and any reason the move would be refused. It does not call the machine API.

Without `--yes`, the command waits until the agent name is typed. Exit codes match `replace-machine`. When no runtime is saved, the command prints `rollback: none` and exits 1.

## backup

```
castbot admin backup
```

Writes `<database directory>/backups/<YYYY-MM-DD_HHMM>/` in America/Denver. The directory is mode 0700. It holds a consistent `castbot.db`, the upload blobs from `<database directory>/files` whose names are their sha256, and a `RECORD` file with the database sha256, the schema version, `integrity ok`, the file count, and `files_sha256`. `files_sha256` is the sha256 of the sorted lines `<name> <size>`. A second backup in the same minute fails and leaves the first one in place. Backups are not deleted. The env file and the secrets key are not copied. Machine memory and workspace files that live only on a machine are not in this backup. A move copies those.

## backups

```
castbot admin backups
```

Prints each backup id, its schema version, and its file count. A directory whose name is not a backup id is skipped.

## restore

```
castbot admin restore <backup id> [--yes]
```

Stop castbot first. The command checks the backup hash, `PRAGMA integrity_check`, the schema version, and the file manifest. It moves the live database, its WAL, and its SHM aside under `backups/pre-restore-<id>-<unix>/`, then copies the snapshot into place. Blobs from the backup replace a local blob whose bytes differ. Extra blobs stay. Without `--yes`, the command waits until the backup id is typed.

The secrets key is not in the backup. Restore needs the same key the snapshot was written with. The previous database file is left in the aside directory.
