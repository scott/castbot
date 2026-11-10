# S07b approval cards

A web turn that asks for a tool shows one approval card in the chat. The card names the tool, the time in the user's zone, and the arguments with secret values replaced by Hidden. Approve and Deny are the only actions. Deny is the only danger button. A second click, or a click from another tab, changes nothing: one permission response and one audit row. A card that the agent no longer has pending becomes inactive and says the request is no longer active.

The server owns the wait. The default is 10 minutes, and `CASTBOT_APPROVAL_TIMEOUT` can set it (at least 1 second, at most 24 hours). When the wait ends, the server posts a denial, marks the card expired, and writes an audit row for `system:timeout`. Closing the browser stream does not deny the card. The same card is there after a reconnect. A paused machine is woken on the click, then the decision is posted. An expiry or a restart denial does not wake a paused machine, and a denial that keeps failing waits longer between tries (2 seconds, then 4, 8, 16, 32, capped at 60) with its own 15 second deadline. Schema version 13 adds `approvals` and `audit_log`. Version 12 stays the real runtime migration.

The click checks the run id before it claims the row. Claim, the permission response, and the audit row run on a server context, so a tab that closes mid-click still lands the decision when the agent accepts it. If that post fails, the claim returns to pending and a later click or the timeout can try again. On startup, and every second after a 30 second grace, a row that was claimed and not confirmed is posted again and then audited. A lost request is marked inactive and does not write `approval.approved`.

A bundle publish or a secret change does not restart the guest while a web approval is pending, while another run is still open, or while this run has already started (`waiting_approval`, or a stream offset above 0). The restart waits until that run finishes. The next message applies it before the new turn, because a brand new run with offset 0 is left out of the check. Opening the agent page or saving the agent waits the same way while any run is streaming or waiting.

A run that was left open at shutdown has its pending cards marked inactive. The expiry loop posts Deny when the machine is still reachable, and it does not wake a paused one. After a decision the run returns to streaming while the tool continues. The waiting bar, the sidebar badge, and the nav count refresh with the card.

Secret names also cover camelCase (`privateKey`, `accessKey`, `sessionKey`), plus `cookie`, `passphrase`, `auth`, `session`, `pass`, and `pwd`, and a value that starts with `Bearer `. Argument text is clipped at 80 runes so a long value stays valid UTF-8.

An approval that arrived from a Cast CLI journal is read only. It has no buttons. The real journal writes the decision before the permission and leaves the tool name off the decision, so the tool is copied from the matching permission. A web turn's own journal (caller `control_plane` on the web thread) is not copied in as a second card. Another user's card, run, and event stream are 404, and the body does not include the command or the approval id.

## What the tests show

`TestAskApproveFinishesAndDenyEnds` approves one bash ask through to done and denies another with the denial text. The stored arguments and the audit detail do not contain the secret. A bad CSRF token is 403 and posts nothing. `TestApprovalDoubleSubmitOneRespond` sends two clicks at once plus a third and keeps a single approval. `TestApprovalTimeoutPostsDeny` moves a fake clock to the deadline, posts the denial, and shows Expired after reload. `TestClosedStreamDoesNotDeny` drops the stream and still shows the pending card, including after a reconnect. `TestApprovalAuthz` is the two-user table: the other account gets 404 on the card, both decisions, the run id paired with the owner's approval id, the event stream, and the agent page. `TestCLIApprovalIsReadOnly` renders a journal decision with no Approve or Deny, using the real order: a decision with no tool name, then the permission. `TestCLIDecisionPicksUpToolFromLaterPermission` rewrites that decision to name bash and does not copy a `control_plane` permission into the web thread. `TestApprovalTimeUsesUserZone` shows 3:42 PM for America/New_York at 20:42 UTC. `TestPausedMachineWakesBeforeApproval` wakes a paused machine before the permission response. `TestExpiryDoesNotWakePausedMachine` expires a card on a paused machine without a wake. `TestUnknownApprovalGoesInactive` forgets the request and leaves the card inactive. `TestMismatchedRunDoesNotClaim` returns 404 for the wrong run id and leaves the row pending. `TestCanceledClickStillDecides` covers a click whose browser gives up after the agent has the decision, and a click the agent never receives. `TestWebJournalDoesNotDuplicateCard` feeds the web journal shape and keeps a single card. `TestSecretArgNamesStayHidden` keeps the extra secret names off the page, the card, the event stream, the run events, the stored arguments, and the audit. `TestLongValueTruncatesOnRunes` clips on runes. `TestAbandonedApprovalIsDeniedWhenLive` posts Deny after a restart leaves the card inactive. `TestRunLeavesWaitingAfterDecision` returns the run to streaming and refreshes the waiting bar. `TestApprovalSurvivesCutProxy` cuts the stream twice and still approves once. `TestDeferGuestRestartWhileApprovalPending` waits out a reload during a pending card or a started run. `TestApprovalsMigration` checks schema version 13 and the two tables. `TestScrubJSONDropsSecretKeys` and `TestScrubJSONDropsCamelCaseAndBearer` drop secret keys, camelCase names, and a Bearer value, and keep `max_tokens`.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll. Demo data only. The pending shots use a live ask so the run stays open. The secret value is not on the page.

- `pending-dark-1280.png`, `pending-dark-390.png`, `pending-light-1280.png`, `pending-light-390.png`
- `approved-dark-1280.png`, `approved-dark-390.png`, `approved-light-1280.png`, `approved-light-390.png`
- `denied-dark-1280.png`, `denied-dark-390.png`, `denied-light-1280.png`, `denied-light-390.png`
- `expired-dark-1280.png`, `expired-dark-390.png`, `expired-light-1280.png`, `expired-light-390.png`
- `cli-dark-1280.png`, `cli-dark-390.png`, `cli-light-1280.png`, `cli-light-390.png`
