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

A verified newer release is recorded in `/opt/castbot/update.json` (mode 0600). A failed check leaves that file as it was. `CASTBOT_UPDATE_MODE=off` skips the request. The key is optional, so an env file without it still starts. The server records the release and does not install it yet.
