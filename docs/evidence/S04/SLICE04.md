# S04 MicroVM provisioner

`AgentHost` creates, polls, pauses, resumes, and deletes one machine per agent. The DigitalOcean client posts `POST /v2/microvms` with the shared image, size 1 vCPU and 2048 MiB, idle timeout 5m unless the agent saved another, `http_port` 8080, and tags `castbot` and `agent:<id>`. The environment on the wire is only `CASTBOT_URL`, `CASTBOT_AGENT_ID`, and a one-time bootstrap token. Status is `GET /v2/microvms/{id}`. A failed create is deleted so the owner can retry. Pause and wake are the pause and resume actions. A page load never calls the guest.

Delete asks for the agent name. A mismatch does not call the host. A match deletes the machine and the agent. Memory export is not in this version.

Each account gets a machine cap of 4, stored in `user_quotas`. The slot is reserved in SQLite before the create call. A denial is HTTP 409 and does not call the host. Another user's create, pause, wake, delete, and chat are 404.

`X-Forwarded-For` is used only when the connection address is in `CASTBOT_TRUSTED_PROXIES`. The default trusts the connection address only. A trusted hop walks the header from the right and skips other trusted hops. An entry that is not an IP stops the walk. `0.0.0.0/0` is refused.

A reload that returns 409 is retried for about 30 seconds, or until the context ends. castlet rejects an http control plane URL unless the test flag `AllowHTTP` is set. The command leaves that flag false. Guest calls require https and a `.microvm.ondigitalocean.com` host unless the test flag `InsecureEdge` is set.

Create runs after the response is sent. A disconnected browser does not cancel it. Each database has its own instance id, and every machine it creates is tagged `castbot-inst:<id>` as well as `castbot` and `agent:<id>`. Startup drops stale pending rows and deletes only this database's untracked machines. Machines that lack the tag, including another castbot database on the same token, are left alone. Demo names, `castbot-s00`, and `castbot-live` stay as well. Only `CASTBOT_DO_TOKEN` enables the machine host.

## Live timing

No `CASTBOT_DO_TOKEN` was in the environment. `TestLiveTiming` (`go test -tags live -timeout 45m ./internal/host`) can create one machine, log API time until running, and delete it. It does not measure first HTTP 200, resume-to-first-200, or a 30-second idle stream. Those need a deployed control plane. It was not run. Machines created: 0. Spend: $0. The two paused demo machines were not touched. This slice did not talk to a Cast runtime, so message-id dedupe was not checked. Cast was not changed.

## What the tests show

`TestMachineAPI` drives create, a multi-poll create, failed-create cleanup, pause, resume, and delete against a fake API. `TestStatusDoesNotTouchTheGuest` fails if status calls the guest. `TestCreateRedactsSecrets` checks the bootstrap token and the machine token stay out of errors. `TestReloadRetriesConflict` covers a 409 that later succeeds. `TestQuotaAndVMScope` checks the cap and that one user cannot read another's row. `TestMachineHandlers` and `TestVMAuthz` cover the page, quota denial, typed-name rejection, and the 404s. `TestTrustedProxyLockout` and `TestUntrustedForwardedForSharesTheConnection` cover both proxy modes.

## Evidence

Headless Chrome, paused and waking, no sideways scroll:

- `paused-1280.png`, `paused-390.png`
- `waking-1280.png`, `waking-390.png`
