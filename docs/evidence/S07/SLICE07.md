# S07 real Cast runtime

The default image target runs Cast from scott/castagent at `5fc2bdb46c4041831fc51c9f909d59e54f887d31`. The command was checked in that tree:

```
/opt/cast/bin/python -m cast.runtime --agent-path /app/agent --port 8081 --host 127.0.0.1
```

`install.sh` is not used. The image is built in stages, runs as nobody, and does not contain an agent or a provider key. The stand-in binary is only on `docker build --target standin`. The default target is `castbot-runtime:s07`. The measured image size is 89,793,655 bytes.

A saved model is written into the bundle as `brain.model`. The provider key stays in the control plane environment (`CASTBOT_PROVIDER_KEY_<PROVIDER>`) and is sent to the guest under the name Cast reads, for example `DIGITALOCEAN_MODEL_ACCESS_KEY`. See `docs/providers.md`. A missing key fails the run at once with: No key for provider digitalocean. Ask the owner to add it.

`CASTBOT_DEV_ECHO=1` is off by default. Startup refuses it when `CASTBOT_PUBLIC_URL` is https and the host is not localhost, 127.0.0.1, or ::1. A draft agent with no machine shows "No machine yet. Start it to chat." and a Start button.

Machine timings are slog lines and `vm_events` rows. `agent_vms.created_running_at` is the first time the machine was running and is not overwritten. A reconciler lists machines about once a minute and marks rows paused when the list says so. It does not call the guest and it does not wake one. The first message after that shows Waking.

A run that stays quiet for 30 seconds keeps the browser stream up through keepalives, including a 15 second cut and a reload. A keepalive gap over 20 seconds is logged at debug with the run id and the gap.

## S06 leftovers

A journal that ends on a partial line drops that tail when the file is opened, so the next append stays readable. A CLI token that was minted but never shown is revoked if the export page does not go out, and the list says Not shown.

## Live tests

No `CASTBOT_DO_TOKEN` was in this environment. The live tests were not run. Machines created: 0. Spend: $0. Guest clock after resume was not measured. The tests create and delete only names that start with `castbot-s07-test-`, and they read no other DigitalOcean token variable.

```
CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... go test -tags live -timeout 45m -count=1 -run TestLiveIdleStream ./internal/host
CASTBOT_DO_TOKEN=... CASTBOT_IMAGE=... CASTBOT_PUBLIC_URL=https://... go test -tags live -timeout 45m -count=1 -run TestLiveGuestClock ./internal/host
```

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll:

- `s07-start-dark-1280.png`, `s07-start-dark-390.png`, `s07-start-light-1280.png`, `s07-start-light-390.png`
- `s07-paused-dark-1280.png`, `s07-paused-dark-390.png`, `s07-paused-light-1280.png`, `s07-paused-light-390.png`
- `s07-waking-dark-1280.png`, `s07-waking-dark-390.png`, `s07-waking-light-1280.png`, `s07-waking-light-390.png`
- `s07-reply-dark-1280.png`, `s07-reply-dark-390.png`, `s07-reply-light-1280.png`, `s07-reply-light-390.png`
- `s07-nokey-dark-1280.png`, `s07-nokey-dark-390.png`, `s07-nokey-light-1280.png`, `s07-nokey-light-390.png`
