# S07b approval cards

A web turn that asks for a tool shows one approval card in the chat. The card names the tool, the time in the user's zone, and the arguments with secret values replaced by Hidden. Approve and Deny are the only actions. Deny is the only danger button. A second click, or a click from another tab, changes nothing: one permission response and one audit row. A card that the agent no longer has pending becomes inactive and says the request is no longer active.

The server owns the wait. The default is 10 minutes, and `CASTBOT_APPROVAL_TIMEOUT` can set it (at least 1 second, at most 24 hours). When the wait ends, the server posts a denial, marks the card expired, and writes an audit row for `system:timeout`. Closing the browser stream does not deny the card. The same card is there after a reconnect. A paused machine is woken on the click, then the decision is posted. Schema version 13 adds `approvals` and `audit_log`. Version 12 stays the real runtime migration.

An approval that arrived from a Cast CLI journal is read only. It has no buttons. Another user's card, run, and event stream are 404, and the body does not include the command or the approval id.

## What the tests show

`TestAskApproveFinishesAndDenyEnds` approves one bash ask through to done and denies another with the denial text. The stored arguments and the audit detail do not contain the secret. A bad CSRF token is 403 and posts nothing. `TestApprovalDoubleSubmitOneRespond` sends two clicks at once plus a third and keeps a single approval. `TestApprovalTimeoutPostsDeny` moves a fake clock to the deadline, posts the denial, and shows Expired after reload. `TestClosedStreamDoesNotDeny` drops the stream and still shows the pending card, including after a reconnect. `TestApprovalAuthz` is the two-user table: the other account gets 404 on the card, both decisions, the run id paired with the owner's approval id, the event stream, and the agent page. `TestCLIApprovalIsReadOnly` renders a journal decision with no Approve or Deny. `TestApprovalTimeUsesUserZone` shows 3:42 PM for America/New_York at 20:42 UTC. `TestPausedMachineWakesBeforeApproval` wakes a paused machine before the permission response. `TestUnknownApprovalGoesInactive` forgets the request and leaves the card inactive. `TestApprovalsMigration` checks schema version 13 and the two tables. `TestScrubJSONDropsSecretKeys` drops secret keys and keeps `max_tokens`.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll. Demo data only. The pending shots use a live ask so the run stays open. The secret value is not on the page.

- `pending-dark-1280.png`, `pending-dark-390.png`, `pending-light-1280.png`, `pending-light-390.png`
- `approved-dark-1280.png`, `approved-dark-390.png`, `approved-light-1280.png`, `approved-light-390.png`
- `denied-dark-1280.png`, `denied-dark-390.png`, `denied-light-1280.png`, `denied-light-390.png`
- `expired-dark-1280.png`, `expired-dark-390.png`, `expired-light-1280.png`, `expired-light-390.png`
- `cli-dark-1280.png`, `cli-dark-390.png`, `cli-light-1280.png`, `cli-light-390.png`
