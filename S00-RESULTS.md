# S00 platform probe results

Run Monday Nov 9, 2026, 3:35 PM to 3:44 PM MST (UTC-7) by the planning agent, with Scott's approval (a few cents of DO spend). Script: `s00/s00.py`. Raw data: `s00/raw.json` (no tokens or credentials in either file).

## Setup
- Exactly one temp MicroVM: `castbot-s00-20261109153459`, id `01a1c273-523f-7cc0-a0bd-3145c66c8df4`, region mkc1, size 1 vCPU / 2048 MiB (smallest offered), 50 GiB disk (fixed default), idle timeout 2m, http_port 8080, tag `castbot-s00`.
- Image: public `docker.io/jmalloc/echo-server:v0.3.7` (echoes request headers, serves a test SSE stream on `/.sse`). Not the Cast runtime image.
- No environment variables set on the VM. No secret of any kind in the VM.
- Client: the box, via doctl for lifecycle calls and plain HTTPS for endpoint calls. The DO token was read from the process env for request headers only and was never printed or written.
- Created 3:35:00 PM MST. Deleted 3:44 PM MST with `doctl compute microvm delete <that one id> --force` after `doctl get` confirmed the name. The MCP connector's token got 403 on delete and pause (it lacks `microvm:delete` and `microvm:update`), so the same doctl client that created it deleted it. Verified with the same client: `get` returns 404 "MicroVM not found"; `list` shows only the two `cast-demo-agent` VMs, both still paused and untouched.
- No checkpoints were created.

## Results

| Measure | Result | Method |
| --- | --- | --- |
| First create to `running` | 7.88 s (create API call returned in 0.73 s with state `creating`) | `doctl compute microvm create`, then `doctl get` every 1 s |
| First create to endpoint ACTIVE | 7.88 s (same poll that saw `running`) | `urls[].status` in the same poll |
| First create to first HTTP 200 | 8.32 s, no non-200 codes before it | `GET /` with DO bearer, retry every 0.5 s |
| Resume from pause, p50 of 10 | 0.455 s (min 0.28 s, max 0.58 s). Zero 503s or other non-200 codes in any cycle | Each cycle: `doctl compute microvm pause`, poll `get` until `paused` (0.78 s to 2.53 s), wait 5 s, then time `GET /` until 200 (auto-resume) |
| Edge stream cut | No cut observed. 3 of 3 SSE trials stayed open to the 110 s test cap, first byte 0.31 to 0.52 s, 133 events each (about one every 0.83 s) | `GET /.sse` with `Accept: text/event-stream`, read until close or 110 s |
| Team MicroVM limits | `max_total_count` 200, `max_memory_bytes` 32 GiB, `max_disk_bytes` 400 GiB, `max_idle_timeout_seconds` 14400 (4 h). Features on: microvm, exec_pty. Off: port_forward, egress_policy, egress_credentials, file_transfer, insights_opt_in | `doctl compute microvm options -o json` (reads `/v2/microvms/options`), before the run |
| Edge forwards `X-Cast-Authorization` | Yes, byte for byte | Sent `X-Cast-Authorization: Bearer castbot-s00-dummy-not-a-secret`; compared the echoed value in-process, kept only a boolean |
| Edge forwards `Authorization` (DO token) | No, the edge strips it. The guest never sees the DO token | Echoed header names only |
| Request with no token | 401 at the edge | `GET /` with no auth headers |
| Request with only `X-Cast-Authorization` | 401 at the edge | DO bearer omitted |

Header names the guest received (values not kept): Accept-Encoding, Cdn-Loop, Cell, Cf-Connecting-Ip, Cf-Ipcountry, Cf-Ray, Cf-Visitor, Do-External-Address, Host, Microvm-Resource-Id, Region, Request-Id, User-Agent, X-Cast-Authorization, X-Castbot-Probe, X-Forwarded-For, X-Forwarded-Proto, X-Gateway, X-Gateway-Service, X-Request-Id, X-Response-From. The `Cf-*` headers suggest a Cloudflare front (inference, not documented).

## What this means for castbot
- Resume is fast (about half a second) after a short explicit pause. Chat wake-up is not a UX problem at this size and image. The "most of a minute in 503" that Cast's client code allows for did not happen.
- First create is about 8 s for a tiny image. A Python Cast runtime image is much larger, so its first create is still UNKNOWN, but the provisioning layer is fast. The golden checkpoint (S11) is not needed up front; keep it as an option.
- The ~15 s edge cut that Cast's `streamId` resume was built for did not appear on a stream that sends an event about every 0.8 s. Keep the resume path (it is cheap and Cast already has it), and castlet should send an SSE comment every 5 s like Cast's StreamHub does.
- The DO edge is the first auth layer (401 without the team token) and does not leak the DO token to the guest. The per-agent token in `X-Cast-Authorization` arrives intact, so Cast's dual-auth mode works through the edge.
- Team memory cap is 32 GiB total, so about 16 agents at 1:2048 (running plus paused, ESTIMATE that the cap counts paused VMs, UNKNOWN). The 200-VM count limit is not the binding one.
- `file_transfer` and `port_forward` are off for the team, which supports serving files through castlet.

## UNKNOWN
- Edge cut on an idle stream (no bytes for 15 s or more), and cuts past 110 s. The echo server streams constantly, so neither was tested. Test in S03/S04 with castlet and a real Cast run that waits on a slow model call.
- Resume after a long pause (hours) and after auto-pause rather than explicit pause. Only 5 s explicit pauses were measured.
- First create and resume with the real Cast runtime image (Python deps, larger image). Measure in S04.
- Whether `max_memory_bytes` counts paused VMs.
- Concurrent resumes (several agents waking at once, for example routines on the hour).

## Spend (ESTIMATE)
About 9 minutes of 1:2048 at $0.039 per running hour is about $0.006, plus 50 GiB disk for 9 minutes (about $0.0005) and a few MB of egress. Total about $0.01, well under the $1 stop line. Rates: https://docs.digitalocean.com/products/microvms/details/pricing/

## S04 appendix

Measured Tuesday Nov 10, 2026. No writable machine token was present in the environment (`DIGITALOCEAN_ACCESS_TOKEN`, `DIGITALOCEAN_TOKEN`, and `DO_TOKEN` were unset). The live harness is `go test -tags live ./internal/host` and it skips without those values plus `CASTBOT_IMAGE` and an https `CASTBOT_PUBLIC_URL`. It was not run. No MicroVM was created. The two paused `cast-demo-agent` machines were not touched. Spend is $0.

| Measure | Result | Method |
| --- | --- | --- |
| First create to first HTTP 200 with the real Cast image | Not measured. Blocked on a writable token. | `TestLiveTiming` would time `POST /v2/microvms` through the first probe 200 |
| Resume after a 20-minute idle auto-pause | Not measured. Blocked on a writable token. | Status polls only, then `POST /v2/microvms/{id}/resume` |
| Idle 30-second stream | Not measured. Blocked on a writable token. | `GET` with `Accept: text/event-stream`, both auth headers, read for 30 s |
| Real agent chat through the web path | Not measured. Blocked on a writable token. | Would use the existing SSE path once a machine is running |
| Cast message-id dedupe | Not measured. This slice did not talk to a Cast runtime. | Recorded here so a later slice can check it. Cast was not changed. |

CI uses the in-memory machine host and a fake HTTP API. It does not need a token and does not create a machine.
