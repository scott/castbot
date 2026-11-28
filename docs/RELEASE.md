# Releases

A green push to `main` cuts a release. The tag is `v0.YYYYMMDD.N` in UTC. The first successful `ci` run on 24 November 2026 publishes `v0.20261124.1`. The next one that day publishes `v0.20261124.2`. The version recorded in the binary drops the leading `v`.

The workflow is `.github/workflows/release.yml`. It runs after the `ci` workflow succeeds on `main`, and only when both the `test` and `shots` jobs succeeded. A failed shots run publishes nothing. A second run for a commit that already has a release does nothing. Runs queue, so two pushes cannot take the same number.

A person can run the workflow by hand to retry a commit whose runtime image was not public yet, or to bump the major number. The major stays `0` until that hand run sets it.

## Assets

Each release attaches:

| File | Role |
| --- | --- |
| `castbot_linux_amd64` | Static control-plane binary |
| `castbot_linux_arm64` | Static control-plane binary |
| `release.json` | Version, commit, schema, guest id, and the runtime image |
| `SHA256SUMS` | sha256 of `release.json` and both binaries |
| `SHA256SUMS.minisig` | minisign signature over `SHA256SUMS` |

The public key is `internal/update/minisign.pub`. The same line is `CASTBOT_MINISIGN_PUBLIC_KEY` in `scripts/install.sh`. The private key is the Actions secret `CASTBOT_MINISIGN_SECRET`. The workflow fails closed when that secret is missing. It does not print the key.

`release.json` names `ghcr.io/scott/castbot-runtime:<commit>`, the public image whose guest inputs match this commit. The job waits about two minutes for that tag. If the image build is still going, the release fails and publishes nothing. Run the workflow again after the tag is public.

The notes are the commit subjects since the previous `v*` tag. A subject that contains a token, secret, password, or key is redacted.

## What a release does not do

The workflow does not deploy a host. `scripts/deploy.sh` remains the laptop path.

## How an installed host checks

The server asks for the latest release about six hours after a successful check, and once shortly after startup. The request is a GET with user agent `castbot-update`. It does not send the hostname, the account, or an instance id.

A verified newer release is recorded in `/opt/castbot/update.json` (mode 0600). A failed check leaves that file as it was. `CASTBOT_UPDATE_MODE=off` skips the request. The key is optional, so an env file without it still starts. The check records the release. Ask me does not install it. Install updates installs it when no agent is working.

## What the owner sees

Settings has an Update section for the workspace owner. It shows this copy's version, whether a release is ready, and a choice of Ask me, Install updates, or Don't check. A host file value of `CASTBOT_UPDATE_MODE` wins, and the page cannot change it. Members do not see the section. Check now runs the same check immediately. A newer release then shows on the card, and Update now is offered when the machine image is published. Check now does not install by itself. Don't check and a checkout build do not offer the button.

Ask me also shows a banner on the agent list when a release is ready. Update now stays on the page while an agent is working and asks the owner to try again when the agent is idle. Saving the choice does not install a release. Install updates installs on a later check, when no agent is working.

## How an update is applied

Update now, when a release is ready and no agent is working, starts one root helper through sudo. The helper downloads that release again, checks the signature, the version, and the machine image policy, and backs up the database. It then switches the running binary and restarts. If the new process does not answer on this machine, the previous binary is put back. The backup stays on disk. The page says castbot is restarting and comes back at Settings.

Choosing Install updates installs the newest verified release when no agent is working. If an agent is working, the check waits for the next pass. An agent that is already running keeps its machine until Replace machine, including when the release changes the machine image.
