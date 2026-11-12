# S09 routines

Cron routines live in the agent panel. Each run claims one slot, wakes the machine through the same path as chat, and keeps its own thread. A second run for the same agent waits. A run during a machine replacement stays held. Approvals on a routine wait 10 minutes, or the routine denies them at once.

No new runtime image. The guest API is unchanged.

## What the tests show

`TestMigrateV17Routines` opens a fresh database and an upgraded one and checks `routines`, `routine_runs`, the unique `(routine_id, scheduled_for)` index, both policy columns, and `vm_events.code`. Reopening is idempotent.

`TestClaimOnceAndFakeClock` overlaps two ticks and a crash mid-claim. One slot is inserted and one run starts. Advancing `SetNow` fires at the scheduled minute in the routine zone.

`TestMissedOnceAndSkipAndPause` covers two missed times with one catch-up marked Ran late and the other Missed, the same gap with both rows Skipped, and a routine that was paused across a due time with no catch-up.

`TestMinIntervalAndZones` and `TestRoutineMinIntervalCopy` save an hourly schedule and `0,15,30,45 * * * *`, and refuse `*/10 * * * *` with "Runs closer than 15 min apart keep the machine awake all day."

`TestRoutineZonesAndDSTPreview` puts the user's zone first, includes `America/Denver` and `Asia/Tokyo`, and keeps 8:00 AM on both sides of the March change.

`TestRoutineApprovalWaitTenMinutes` expires a wait-policy approval after `DefaultApprovalWait` (10 minutes). The card says "Denied automatically in 10 min if not answered. The run finishes without it."

`TestRoutineApprovalDenyPolicy` denies at once. The card says "Denied by the routine setting" and the run finishes.

`TestRunNowClaimPath` and `TestRoutineRunNowOpensThread` use the same unique insert as a schedule. A second Run now while one is open does not add a row. The thread says "Routine prompt, sent by castbot".

`TestRoutineQueueBehindReply` leaves a due routine Queued with "Waits for the reply in progress" while a chat reply is streaming, then starts it after that reply ends.

`TestRoutineClaimThenChat` claims a slot, posts a chat, then ticks. The chat stays queued and the routine starts. The chat starts after that run finishes.

`TestRoutineCrashReclaim` starts a claimed but unstarted slot once on the next tick, and leaves it queued when a chat run is already streaming.

`TestRoutineFinishedLateMarker` shows Ran late on a finished late run. `TestRoutineWakeFailedCodes` says 90 s only for `WAKE_TIMEOUT`.

`TestRoutineCSRF` returns 403 for save, toggle, run now, and delete with no token. `TestRoutineStoreInvalidIs400` returns 400 when the store rejects a schedule that parses but never fires.

`TestChatWakeKeepsRunBudget` leaves a chat wake on the run context. A routine wake still stops at the 90 s budget.

`TestFinishReplacementNotesAgent` reads the agent id before the replacement commit and runs the hook after the lock is clear.

`TestParseRangesStepsAndOr`, `TestParseRejectsBadFields`, `TestDescribeFallback`, `TestTooFrequentBoundary`, and `TestNextAcrossSpringForward` cover the cron parser.

`TestRoutineOneAtATime` starts one of two routines that are due on the same tick and starts the other after the first ends.

`TestHoldDuringReplace`, `TestHoldLateAfterOneHour`, and `TestRoutineHoldDuringReplace` keep the run Held with no Wake while a replacement is active. Within an hour of the scheduled time the start is on time. After an hour it is Ran late.

`TestRoutineWakeFailedCodes` records `wake_failed` as `WAKE_TIMEOUT`, `PROVIDER_ERROR`, or `HEALTH_FAILED`. The thread shows the code at the end of the system line. The prompt is not streamed, and the provider text is not stored.

`TestPanelHistoryCap` and `TestRoutinePanelLast30` list 30 runs and offer See all. See all opens that routine's threads.

`TestRoutineDeletedStamp` says "from a deleted routine" on an approval card after the schedule is deleted.

Marker pills are drawn when the page renders. The chat stream belongs to the open thread, and a routine marker sits on the agent chat while the run streams on its own thread, so the pill is not pushed over that stream.

`TestDeleteKeepsThreads` and `TestRoutineDeleteLabel` delete the schedule, keep the thread labeled "deleted routine", and leave an in-progress run in place.

`TestRoutineAuthzAndCSRF` returns 404 when another user opens the preview, the panel, the run events, and the run, toggle, delete, and save posts. `TestRoutineCSRF` returns 403 for a post with no token.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll, no console or CSP errors, no em or en dashes. Demo data only. 11b2 types an invalid schedule and waits for the error. 11f clicks Delete and waits for the confirm.

- `11a-dark-1280.png`, `11a-dark-390.png`, `11a-light-1280.png`, `11a-light-390.png`
- `11a2-dark-1280.png`, `11a2-dark-390.png`, `11a2-light-1280.png`, `11a2-light-390.png`
- `11b-dark-1280.png`, `11b-dark-390.png`, `11b-light-1280.png`, `11b-light-390.png`
- `11b2-dark-1280.png`, `11b2-dark-390.png`, `11b2-light-1280.png`, `11b2-light-390.png`
- `11c-dark-1280.png`, `11c-dark-390.png`, `11c-light-1280.png`, `11c-light-390.png`
- `11d-dark-1280.png`, `11d-dark-390.png`, `11d-light-1280.png`, `11d-light-390.png`
- `11d2-dark-1280.png`, `11d2-dark-390.png`, `11d2-light-1280.png`, `11d2-light-390.png`
- `11d3-dark-1280.png`, `11d3-dark-390.png`, `11d3-light-1280.png`, `11d3-light-390.png`
- `11d4-dark-1280.png`, `11d4-dark-390.png`, `11d4-light-1280.png`, `11d4-light-390.png`
- `11e-dark-1280.png`, `11e-dark-390.png`, `11e-light-1280.png`, `11e-light-390.png`
- `11f-dark-1280.png`, `11f-dark-390.png`, `11f-light-1280.png`, `11f-light-390.png`
- `11g-dark-1280.png`, `11g-dark-390.png`, `11g-light-1280.png`, `11g-light-390.png`

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.
