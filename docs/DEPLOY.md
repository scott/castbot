# Deploy

`scripts/deploy.sh` installs the castbot control plane from a laptop. It builds one static Linux binary from a commit on `main`, copies it to the host, and restarts `castbot`. It does not build or push the runtime image, and it does not edit the server env file.

Nothing in this repository names a real host. Put the real host, user, and URL in `deploy.env` on the laptop. That file stays untracked.

## Setup

The laptop needs bash, git, Go `go1.27.4` (the `toolchain` line in `go.mod`), `python3`, `ssh`, `scp`, `curl`, `readelf`, and `sha256sum`. The host needs bash, `flock`, `sqlite3`, `sha256sum`, GNU `find`, GNU `mv`, `install`, `sudo`, `systemctl`, and `journalctl`. The host also needs a `castbot` user and group.

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
| `/opt/castbot/backups/<YYYY-MM-DD_HHMM>/` | A consistent backup, mode 0700 |
| `/etc/castbot/castbot.env` | Server env file |

`/opt/castbot` is the default `DEPLOY_APP_DIR`. The database path is always `<app>/data/castbot.db`. The script does not read `CASTBOT_DB` from the server env file, so the service has to be using that file. On the reference host the unit's WorkingDirectory is `<app>/data` and the env file sets `CASTBOT_DB` to the absolute `<app>/data/castbot.db`. If your host stores the database elsewhere, the script's in-flight, backup, and schema checks will read the wrong file.

The SSH user does not own the app directory. `bin/`, `releases/`, and `RELEASE` are root-owned, `data/` is mode 0700, and the env file is not readable by the SSH user. The unit hides `/proc/<pid>`, and the SSH user is not in the group that can read the journal. The user needs passwordless `sudo -n`. The script uses it for the image line, every `sqlite3` call, the backup directory and its record, the release directory, the symlink, `RELEASE`, prune, `/proc/<pid>/exe`, `journalctl`, the lock, and `systemctl restart`. A preflight runs `sudo -n true`.

The binary is uploaded with `scp` into a temporary directory the SSH user can write, then `sudo -n install -o castbot -g castbot -m 0755` places it in `releases/<sha>/`. The schema migrates when the process starts. The script does not run a migration command of its own.

After restart the script polls `DEPLOY_LOCAL_HEALTH_URL` (default `http://127.0.0.1:8080/login`) from the host and `<DEPLOY_PUBLIC_URL>/login` from the laptop, for up to 30 seconds.

The server env file is searched with `grep` for one line, `CASTBOT_IMAGE=`. The image tag's longest hex run of 7 to 40 characters is the commit the image was built from. A short sha is enough when it is unique in the local clone, for example `example.invalid/castbot-runtime:abc1234`. Other lines in that file are not read.

## Dry run

```
scripts/deploy.sh --dry-run
```

A dry run refuses a dirty tree, resolves `origin/main`, checks that Go is `go1.27.4`, checks SSH and `sudo -n`, requires the `ci` workflow on that exact commit to be green, checks the running image tag, and builds the binary. It then reads the live release, the in-flight count, the schema version, checks that the journal is readable, reads free disk space, and prints every release directory a real run would delete. The target release is not on disk yet. The dry run still counts it as the newest kept directory, because a real run uploads it before pruning. It does not take a backup, upload, restart, or take the lock.

Run a dry run before the first real deploy on a host that already has release directories. That first run can delete every release except the newest `DEPLOY_KEEP_RELEASES`, the live binary, and the rollback target. Backups are never deleted.

## Deploy

```
scripts/deploy.sh
```

Fetch `origin` first so `origin/main` is the commit you mean. The script takes a remote lock at `/run/lock/castbot-deploy.lock`. The lock holder reads stdin, so it exits when the SSH session ends, including when the local process is killed. It refuses the deploy when `RELEASE` is already the target commit, because the upload would replace the file a rollback would restore. It refuses a target that is an ancestor of the live release unless `--allow-downgrade` is passed. It refuses when any run is `queued`, `streaming`, or `waiting_approval`, backs up the database, uploads the binary, checks those runs again, points `bin/castbot` at the new file, writes `RELEASE`, and restarts the service. After the restart it checks the running process, `castbot`, `caddy`, and `fail2ban`, both `/login` URLs, `schema_migrations`, the image line, and the journal.

The schema version is `SELECT COALESCE(MAX(version),0) FROM schema_migrations`. The script does not use `PRAGMA user_version`. After restart the version must be greater than or equal to the backup.

A line is appended to the local deploy log in America/Denver time with the commit, the backup id, and the result. The log is gitignored.

The backup directory holds `castbot.db`, a `files/` directory when `<app>/data/files` exists, and a `RECORD` file. `RECORD` has the sha256, `schema_version`, the integrity result, `files`, and `files_sha256`. `files_sha256` is the sha256 of the sorted lines `<name> <size>`. Only regular files whose names are the sha256 of their bytes are copied. A partial upload and a file whose name does not match its bytes are skipped. Backups are never deleted. The env file is never copied. Machine memory that lives only on a machine is not in this backup.

Journal output is a count only. Matching lines are not printed.

The script refuses to run under `bash -x`, because trace output would include the GitHub request header.

## What it refuses

| Check | Why |
| --- | --- |
| Dirty work tree | The binary must come from the commit that was tested. |
| Commit is not on `origin/main` | Production installs `main` only. |
| Go is not `go1.27.4` | The binary must match the `go.mod` toolchain. |
| SSH fails, `sudo -n` fails, or the known hosts file is empty | The host key has to be pinned, and the SSH user needs passwordless sudo. |
| The latest `ci` push run for that commit is missing, unfinished, or not successful on both `test` and `shots` | A red or flaky run is not waived. The newest run is the one that counts. |
| `Dockerfile`, `deploy/`, `go.mod`, `go.sum`, `cmd/castlet`, `cmd/castbot-guest`, `cmd/cast-standin`, `internal/castlet`, or a repo package those commands import differs from the commit in the image tag | The control plane would move ahead of the guest image. Imports come from `GOOS=linux GOARCH=amd64 go list -deps` at both commits. Files named `*_test.go` are not part of that diff. |
| `castbot.env.example` has a variable the image commit does not | The new process may need a setting the host file does not document yet. |
| `RELEASE` is already the target commit | The rollback copy and the running binary would be the same file. |
| The target commit is an ancestor of the live `RELEASE` | That would roll the control plane back. Pass `--allow-downgrade` to do it on purpose. The live commit has to be in this clone. |
| `RELEASE` and the symlink disagree, or the rollback binary is missing | The script will not guess which build is live. |
| Any run is queued, streaming, or waiting for approval | A restart would cut off that run. There is no warn mode. |
| `sqlite3` is missing on the host | The in-flight check and the backup cannot run. |
| The backup fails `PRAGMA integrity_check` | The copy is not a safe restore point. |
| Verify fails after restart | The previous release is put back. |
| `bash -x` or another xtrace shell | The trace would print the GitHub token header. |

`--skip-ci-check` is an explicit override. On a terminal you must type the short sha. Without a terminal you must also pass `--i-understand-ci-is-not-green`. The script does not skip CI on its own.

The script never prints `CASTBOT_DO_TOKEN`, `CASTBOT_SECRETS_KEY`, `CASTBOT_SESSION_KEY`, `CASTBOT_PROVIDER_KEY_*`, or a GitHub token. The only database reads are the in-flight count, `PRAGMA integrity_check`, and the schema version query. Journal checks print the match count and not the lines.

## Rollback

If the switch, the restart, or the verify step fails, the script points `bin/castbot` back at the previous release, writes that commit to `RELEASE`, restarts, and checks the previous release again. The previous release is the commit the symlink named before the switch. It then exits nonzero. The backup from this attempt is left in place. Release directories are pruned only after a verify that succeeds. A prune keeps the newest `DEPLOY_KEEP_RELEASES` directories (default 5) and never removes the live release, the rollback target, or anything under `backups/`.

The first install has no previous release. A failed verify on that install stops and leaves the backup. Restore that backup by hand if you need the old database.
