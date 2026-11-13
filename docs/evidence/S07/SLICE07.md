# S07 real Cast runtime

The default image target runs Cast from scott/castagent at `5fc2bdb46c4041831fc51c9f909d59e54f887d31`. The command was checked in that tree:

```
/opt/cast/bin/python -m cast.runtime --agent-path /app/agent --port 8081 --host 127.0.0.1
```

`install.sh` is not used. The image is built in stages, runs as nobody, and does not contain an agent or a provider key. The stand-in binary is only on `docker build --target standin`. The default target is `castbot-runtime:s07`. Python dependencies are pinned in `deploy/cast-constraints.txt`. The castagent tarball is checked against sha256 `f6df23078ffd4e4a92bb7c77c1445c7d42d1b4e60d0551a1ff2f7f90e41539e9`. The python base image already has CA certificates, so those stages do not install packages with apt. A later image adds coding tools on this same runtime target. See docs/runtime.md.

Image size is `docker image inspect --format '{{.Size}}'`. That field is the size the local daemon reports for the image content, not a compressed registry blob. On this machine's containerd snapshotter the Size field is 89,794,747 bytes after the constraint and checksum change (it was 89,793,655 bytes before that change). A classic overlay store measured the earlier image at 278,958,566 bytes uncompressed. Those two numbers are different store accounting for the same image, not a compressed and uncompressed pair from one daemon.

A saved model is written into the bundle as `brain.model`. The provider key stays in the control plane environment (`CASTBOT_PROVIDER_KEY_<PROVIDER>`) and is sent to the guest under the name Cast reads, for example `DIGITALOCEAN_MODEL_ACCESS_KEY`. See `docs/providers.md`. A missing key fails the run at once with: No key for provider digitalocean. Ask the owner to add it. A key added or rotated after boot is reloaded into the guest on the next message. Cast inside the guest listens with a per-boot token that castlet generates and does not log.

`CASTBOT_DEV_ECHO=1` is off by default. Startup refuses it when `CASTBOT_PUBLIC_URL` is https and the host is not localhost, 127.0.0.1, or ::1. A draft agent with no machine shows "No machine yet. Start it to chat." and a Start button.

Machine timings are slog lines and `vm_events` rows. `agent_vms.created_running_at` is the first time a machine created by this version was running, and it is not overwritten. Rows that already existed keep it empty, and the machine page shows that as unknown. A reconciler lists machines about once a minute and marks a row paused only when it is still running with the same last wake time. It does not call the guest and it does not wake one. An edge resume that this process did not request is logged as edge_resume. The first message after a pause shows Waking. `create_accepted` is logged when the machine API accepts the create, before the client polls for running.

A run that stays quiet for 30 seconds keeps the browser stream up through keepalives, including a 15 second cut and reloads during the quiet period. A keepalive gap over 20 seconds is logged at debug with the run id and the gap. The Start button is its own form, so the machine page is not drawn inside the chat.

## S06 leftovers

A journal that ends on a partial line drops that tail when the file is opened, so the next append stays readable. A CLI token that was minted but never shown is revoked if the export page does not go out, and that revoke is pushed to the machine. The list says Not shown. Tokens that existed before `viewed_at` was added are marked shown, so they stay Active.

## Live tests

No `CASTBOT_DO_TOKEN` was in this environment. The live tests were not run. Machines created: 0. Spend: $0. Guest clock after resume was not measured.

The tests provision through the control plane, which issues a real castlet token. `CASTBOT_PUBLIC_URL` must be https and must forward to `CASTBOT_LISTEN` (default `127.0.0.1:18080`), including `/s07-infer/`. They create and delete only names that start with `castbot-s07-test-`, including on failure, signal, or when the test deadline is close. They read no other DigitalOcean token variable.

```
CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... go test -tags live -timeout 45m -count=1 -run TestLiveIdleStream ./internal/web
CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... go test -tags live -timeout 45m -count=1 -run TestLiveGuestClock ./internal/web
```

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll:

- `s07-start-dark-1280.png`, `s07-start-dark-390.png`, `s07-start-light-1280.png`, `s07-start-light-390.png`
- `s07-paused-dark-1280.png`, `s07-paused-dark-390.png`, `s07-paused-light-1280.png`, `s07-paused-light-390.png`
- `s07-waking-dark-1280.png`, `s07-waking-dark-390.png`, `s07-waking-light-1280.png`, `s07-waking-light-390.png`
- `s07-reply-dark-1280.png`, `s07-reply-dark-390.png`, `s07-reply-light-1280.png`, `s07-reply-light-390.png`
- `s07-nokey-dark-1280.png`, `s07-nokey-dark-390.png`, `s07-nokey-light-1280.png`, `s07-nokey-light-390.png`
