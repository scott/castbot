# S02 durable streaming, local

A posted chat message is stored, a run is created, and the browser gets a pending reply that follows `GET /runs/{id}/events`. The run goroutine is not tied to that browser. It calls the runtime with `Accept: text/event-stream`, `metadata.sessionId` set to the thread id and `metadata.streamId` set to the run id. A cut connection reconnects with `metadata.streamOffset` until `[DONE]`. Every event is appended to `run_events` with a monotonic `seq`, then fanned out. A browser resume sends `Last-Event-ID`, so a reload does not drop or repeat events. The finished reply is stored as a message.

The runtime client is the `runtime.Client` interface. This slice does not call DigitalOcean and does not start a machine.

## Which runtime

Python `cast` is not installed in this environment (`ModuleNotFoundError: No module named 'cast'`), so the server uses the in-process echo in `internal/runtime/echo.go`. It speaks JSON-RPC `message/send` over SSE, resumes with `streamId` and `streamOffset`, keeps a finished run for 120 seconds, and writes an SSE keepalive comment every 5 seconds. The production pace waits 30 ms after each word. The durability test slows that wait so the same reply lasts about 60 seconds. `CASTBOT_RUNTIME_URL` points the process at another server instead of the built-in echo.

`internal/runtime/proxy.go` is the test proxy. It ends each response at 15 seconds. A natural end is not counted as a cut.

## What the tests show

`TestDurableEchoSurvivesCutsAndReload` posts a reply that takes about 60 seconds. The proxy cuts the runtime connection at least 3 times, and the test closes the browser stream and opens it again with `Last-Event-ID`. The joined text matches the echo byte for byte, the sequence numbers increase with no gaps or duplicates, one producer served the whole reply, and the stored message matches.

`TestRuntimeDownShowsRetry` starts a run against a closed port. The run records an error event and the page shows "The agent is not reachable right now." with Retry. A 502, 503, 504, or a refused connection is retried with backoff for about 100 seconds, including the first open, so a five second outage still delivers the reply. `TestRetryDoesNotDuplicateTheUserMessage` retries a failed run without a second user message. Two retries at once create one new run; the other response is 409. `TestReplySurvivesBrowserDisconnect` drops the browser after the first text event; the reply is still stored and the page shows it once. `TestTwoBrowsersSeeTheSameRun` reads one run from a single connection on each browser and both see the same events once. `TestAuthzMatrix` covers another user's run, the event stream, a `Last-Event-ID` resume, and retry. Each of those is 404 and the body does not contain the other user's event.

A resume must repeat the `cast-stream` marker for the same run. After eight resumes, or when the marker is missing, the run fails with "The reply was interrupted." and the new text is not appended. A runtime error frame ends the run and the page shows that detail. Startup marks queued, streaming, and waiting runs failed in the same transaction as the error event, and a signal shuts the server down without leaving a bubble on Replying. The browser stream sends a keepalive comment every 15 seconds. A caught-up cursor on a finished run still receives the terminal event. The pending bubble remembers the last sequence and reconnects with `?after=`, which the server treats like `Last-Event-ID`. The first open also sends `metadata.history` as earlier turns of role and text.

Headless Chrome sends "hello", reloads while "Replying" is on screen, and waits until the greeting is on the page once. The same check covers the error state, a gateway 502 longer than 10 seconds, and a restart in the middle of a reply. `documentElement.scrollWidth` is not wider than the viewport at 1280x800 or 390x844.

`app.js` and `stream.js` together are 198 lines. The htmx SSE extension 2.2.3 is vendored at `internal/web/static/sse.js`. The page CSP is unchanged.

## Evidence

- `streaming-1280.png`, `streaming-390.png` mid-reply
- `reload-1280.png`, `reload-390.png` after a reload, still mid-reply
- `error-1280.png`, `error-390.png` unreachable runtime and Retry
- `502-1280.png`, `502-390.png` during a gateway 502, text not repeated
- `restart-1280.png`, `restart-390.png` interrupted reply and Retry
