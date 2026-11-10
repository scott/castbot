# S03 castlet and the shared image, local Docker

The shared image contains castlet and a protocol stand-in. It does not contain an agent. A container starts with an empty `/app/agent`, exchanges a one-time bootstrap token, pulls the bundle and granted fixture secrets over HTTPS, writes `/app/agent`, and starts the stand-in on that container's own loopback at `127.0.0.1:8081`. castlet listens on the only published port, checks `X-Cast-Authorization`, and proxies `POST /` and `/.well-known/agent.json`. Each guest has its own bridge network, so one guest cannot answer for another. The browser secrets route returns names only. Workspace downloads are octet-stream attachments.

This slice does not call DigitalOcean, does not push a registry, and does not create a MicroVM. CI uses the same local image. `CASTLET_RUNTIME` is the command castlet execs, so a later slice can install Cast without changing the supervisor. Fixture secrets stay plaintext in SQLite. The encrypted vault is a later slice.

## What the tests show

`TestImageHasNoAgent` runs the image with a shell entrypoint and checks that `agent.yaml` and `instructions.md` are absent and that both binaries exist.

`TestContainerBootsFromBundle` builds `castbot-runtime:s03`, starts it against a test control plane, and posts "hello". The stored reply matches the stand-in byte for byte. The same container serves a file put and get, a memory tar export and import, and a log tail of recent stand-in output. The log does not contain the fixture secret. The secret is in the runtime process environment and is not under `/app`. A request with no token is 401 and does not add a turn to the log. A bundle reload to a new version changes the reply without a new image. User B gets 404 for the bundle, the secrets, the file list, and the file bytes.

`TestOtherUserCannotPullBundleSecretsOrFiles` covers those 404s without Docker, including a token minted for user B. A bootstrap token works once.

A resume that receives the matching `cast-stream` marker and stays open for a few seconds counts as progress, so keepalive-only cuts past 135 seconds still finish byte for byte. Nine connections that close immediately after the marker still end with "The reply was interrupted." A wall-clock limit still fails a hung runtime. A server that accepts and then closes with no response is retried the same way as a 502. Retry reuses the stored user message id, and the runtime transcript contains that user turn once.

Headless Chrome checks that Retry sits inside `.ch-body` after a live error, after a reload, and in the light theme. The shots job installs Chrome 156.0.8078.47-1. `app.js` and `stream.js` together are 222 lines. The extra lines pin Retry inside the chat after the error fragment settles.

The container-backed reply below was still streaming: the page shows "Hello!" and "Replying", and not the rest of the greeting. `documentElement.scrollWidth` is not wider than the viewport at 1280x800 or 390x844.

## Evidence

- `streaming-1280.png`, `streaming-390.png` mid-reply from a local container
