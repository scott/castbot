# S06 Cast CLI access

An owner can mint a Cast CLI token for one agent. The raw token is shown once in an import blob and is not stored. The row keeps a hash, a display prefix, and revoke state. A second account gets 404 on export, the machine page, and revoke.

castlet accepts that token on the chat paths and rejects it after revoke on the next request. A CLI token cannot call the admin paths. Every accepted turn is journaled with a sequence number, the session id, the caller (`control_plane` or `cli:<token id>`), and the request, reply, permission, and decision text. A stream that ends before it is finished is marked incomplete. CLI turns are pushed to castbot. A later contact also pulls the journal, and a paused machine is not woken just to sync. Ingest is idempotent on the machine id and sequence. Each CLI session is its own Cast CLI thread. A web message in that thread reuses the session id. Approvals asked on a CLI turn are shown read only.

The manual steps for a local deployment row are in `docs/cli.md`. That note does not complete CLI chat. `cast import` is a draft pull request and is not merged.

## Carry-forwards

YAML aliases and merge keys are rejected before scrub, so a secret node cannot be kept through an alias. The summary lists leftover brain keys and other top-level keys. Secret-shaped names such as `github_token` and `openai_api_key` are dropped. A non-rate-limit 403 from the refs API uses the private repository message. A reload that was saved while the machine looked running checks the machine API again before it reloads. Expired import drafts are deleted on the machine sweep and on an hourly timer.

The live delete check waits until the machine API returns 404, treating `terminating` as still in progress, and then checks protected machines. The live tag was not run.

## What the tests show

`TestCLITokenIsHashOnly` stores a hash and proves another user cannot list or revoke. `TestJournalIngestIsIdempotent` replays one session and keeps a single Cast CLI thread, including a read-only approval. `TestExportShowsTheTokenOnce` shows the blob once, omits it later, and keeps it out of the log, the bundle, and the guest push. User B gets 404. `TestCLITokenAcceptsThenRevokes` accepts a CLI token, hides admin routes, and returns 401 after revoke. `TestIncompleteTurnIsMarked` keeps a cut stream. `TestHistoryParity` checks three CLI turns plus the control-plane turn, a replay, and a web turn on the same session id. `TestApplyPendingRechecksStatus` leaves a pending reload in place when the API says paused. `TestAliasToScrubbedSecretIsRejected`, `TestCompoundSecretNamesAreDropped`, `TestSummarizeKeptListsLeftovers`, and `TestRefsForbiddenIsPrivate` cover the review notes. `TestSweepExpiredDrafts` deletes an expired draft. `TestWaitGoneAcceptsTerminatingThen404` covers the delete wait. `TestWorkaroundDocDoesNotClaimCLIChat` reads `docs/cli.md`.

## Live

No live tag was run. The fake machine API was used. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll:

- `cli-export-dark-1280.png`, `cli-export-dark-390.png`, `cli-export-light-1280.png`, `cli-export-light-390.png`
- `cli-tokens-dark-1280.png`, `cli-tokens-dark-390.png`, `cli-tokens-light-1280.png`, `cli-tokens-light-390.png`
- `cli-thread-dark-1280.png`, `cli-thread-dark-390.png`, `cli-thread-light-1280.png`, `cli-thread-light-390.png`
