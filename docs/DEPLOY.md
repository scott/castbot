# Deploy

`scripts/deploy.sh` installs the castbot control plane from a laptop. It builds one static Linux binary from a commit on `main`, copies it to the host, and restarts `castbot`. It does not build or push the runtime image, and it does not edit the server env file.

Nothing in this repository names a real host. Put the real host, user, and URL in `deploy.env` on the laptop. That file stays untracked.

## Setup

The laptop needs bash, git, Go `go1.27.4` (the `toolchain` line in `go.mod`), `python3`, `ssh`, `scp`, `curl`, `file`, and `sha256sum`. The host needs bash, `flock`, `sqlite3`, `sha256sum`, GNU `mv`, `sudo`, `systemctl`, and `journalctl`.

```
cp deploy.env.example deploy.env
chmod 0600 deploy.env
```

Edit `deploy.env` with the host name, the SSH user, and the public `https` URL. Settings already present in the environment win over the file. The SSH key comes from the agent or from `~/.ssh/config`. Do not put a key path in the file or the repo.

Create `.deploy/known_hosts` and put the server host key in it before the first run. The script sets `StrictHostKeyChecking=yes` and will not add a key by itself.

The host layout the script expects:

| Path | Role |
| --- | --- |
| `/opt/castbot/bin/castbot` | Symlink to the live release |
| `/opt/castbot/releases/<sha>/castbot` | One binary per commit |
| `/opt/castbot/RELEASE` | The live commit |
| `/opt/castbot/data/castbot.db` | The SQLite database |
| `/opt/castbot/backups/<YYYY-MM-DD_HHMM>/` | A consistent backup |
| `/etc/castbot/castbot.env` | Server env file |

`/opt/castbot` is the default `DEPLOY_APP_DIR`. The database path is `<app>/data/castbot.db`, which matches the default `CASTBOT_DB` when the process working directory is the app directory. The local health check is `http://127.0.0.1:8080/login`, the default listen address. The public check is `<DEPLOY_PUBLIC_URL>/login`.

`sudo systemctl restart castbot` uses `sudo -n`, so the SSH user needs passwordless sudo for that restart. The schema migrates when the process starts. The script does not run a migration command of its own.

The server env file is searched with `grep` for one line, `CASTBOT_IMAGE=`. The image tag must contain the git commit the image was built from, for example `example.invalid/castbot-runtime:<full-sha>`. Other lines in that file are not read.

## Dry run

```
scripts/deploy.sh --dry-run
```

A dry run refuses a dirty tree, resolves `origin/main`, checks that Go is `go1.27.4`, checks SSH, requires the `ci` workflow on that exact commit to be green, checks the running image tag, and builds the binary. It then prints the remote plan. It does not take a backup, upload, or restart.

## Deploy

```
scripts/deploy.sh
```

Fetch `origin` first so `origin/main` is the commit you mean. The script takes a remote lock, refuses the deploy when any run is `queued`, `streaming`, or `waiting_approval`, backs up the database, uploads the binary, rechecks those runs, points `bin/castbot` at the new file, writes `RELEASE`, and restarts the service. After the restart it checks the running process, both `/login` URLs, `PRAGMA user_version`, the image line, and the journal.

A line is appended to the local deploy log in America/Denver time with the commit, the backup id, and the result. The log is gitignored.

The backup directory holds `castbot.db` plus a `RECORD` file with the sha256, `PRAGMA user_version`, and the integrity result. Backups are never deleted. The env file is never copied.

## What it refuses

| Check | Why |
| --- | --- |
| Dirty work tree | The binary must come from the commit that was tested. |
| Commit is not on `origin/main` | Production installs `main` only. |
| Go is not `go1.27.4` | The binary must match the `go.mod` toolchain. |
| SSH fails, or the known hosts file is empty | The host key has to be pinned. |
| The latest `ci` push run for that commit is missing, unfinished, or not successful on both `test` and `shots` | A red or flaky run is not waived. The newest run is the one that counts. |
| `Dockerfile`, `guest/`, `internal/castlet`, or `cmd/castlet` differ from the commit in the image tag | The control plane would move ahead of the guest image. |
| `castbot.env.example` has a variable the image commit does not | The new process may need a setting the host file does not document yet. |
| Any run is queued, streaming, or waiting for approval | A restart would cut off that run. There is no warn mode. |
| The backup fails `PRAGMA integrity_check` | The copy is not a safe restore point. |
| Verify fails after restart | The previous release is put back. |

`--skip-ci-check` is an explicit override. On a terminal you must type the short sha. Without a terminal you must also pass `--i-understand-ci-is-not-green`. The script does not skip CI on its own.

The script never prints `CASTBOT_DO_TOKEN`, `CASTBOT_SECRETS_KEY`, `CASTBOT_SESSION_KEY`, `CASTBOT_PROVIDER_KEY_*`, or a GitHub token. The only database reads are the in-flight count, `PRAGMA integrity_check`, and `PRAGMA user_version`.

## Rollback

If the switch, the restart, or the verify step fails, the script points `bin/castbot` back at the previous release, writes that commit to `RELEASE`, restarts, and checks the previous release again. It then exits nonzero. The backup from this attempt is left in place. Release directories are pruned only after a verify that succeeds. A prune keeps the newest `DEPLOY_KEEP_RELEASES` directories (default 5) and never removes the live release, the rollback target, or anything under `backups/`.

The first install has no previous release. A failed verify on that install stops and leaves the backup. Restore that backup by hand if you need the old database.
