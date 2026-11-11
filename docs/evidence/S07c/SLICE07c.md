# S07c replace machine

Replace machine starts a new machine on the current runtime, copies memory and workspace files, and deletes the old machine only after the new one passes its health check. The Machine tab and `castbot admin replace-machine` call the same function. The new machine stays out of `agent_vms` until one transaction switches the live row, revokes the old machine token, and writes the new token. If the process stops before that switch, startup deletes the new machine and leaves the old one. If it stops after the switch, startup finishes the delete.

If the old machine cannot be read, the replace stops and keeps it. `--no-copy` is a CLI-only override. It writes a `replace_no_copy` event and is not shown in the UI. A copy that is over 500 files, 64 MiB total, or 32 MiB for memory or one file fails the same way and keeps the old machine.

The guest HTTP API is unchanged, so this slice does not need a new runtime image.

## What the tests show

`TestReplaceMigration` opens a database twice and checks schema version 14, `vm_replacements`, `vm_replacement_tokens`, `agent_tokens.agent_vm_id`, and `agent_tokens.revoked_at`. `TestReplaceLockAndTokenBinding` keeps a second replace out, leaves the live token unchanged while the new token is staged, and shows the switch revokes the old token. `TestReplacePrecheckLines` covers the reply, approval, starting, and already-running lines. `TestDeleteRevokesAgentToken` shows an ordinary delete revokes the machine token.

`TestReplaceCopiesMemoryAndDeletesOldAfterHealth` copies memory and files, reloads, and deletes the old machine only after health. It also checks every owned row count stays equal, except `audit_log`, which must grow. `TestReplaceWakesPausedMachineOnce` wakes a paused machine once, pushes CLI hashes, then pauses the new machine, and leaves it paused at the host. The fake resumes a paused machine on any guest call, so a push after the pause fails that test. A later failure does not change the agent status.

Guest calls send the machine token in `Authorization` and the agent token in `X-Cast-Authorization`. `TestGuestCallsUseEdgeAuth` checks both headers on every guest call. A failed replace whose new machine is still there does not lock the agent. Chat, pause, and wake keep working, and the page does not say the machine is being replaced. `castbot admin machines` is the only place that says cleanup pending. Retries wait one minute, then double up to one hour, and stop after 24 hours with one event and one audit line. The line stays. `TestReplaceFailureKeepsOldMachine` covers create, boot, read_old, copy, and verify. `TestReplaceCrashRecovery` stops after each step and recovers: before the switch the new machine is deleted, and after it the old delete finishes. `TestReplaceNoCopySkipsFiles` skips the copy and writes `replace_no_copy`. `TestReplaceCaps` refuses 501 files. `TestReplaceDryRunMakesNoHostCall` and `TestReplaceBlockedWhileRunning` cover the plan and the lock.

`TestReplaceCopiesRealCastletBytes` copies a real memory tar and a workspace file through two in-process guests and records the reload. `TestReplaceTruncatedTarKeepsOld` sends a truncated tar, fails at copy, and keeps the old memory.

`TestReplacePageCopyAndRefusals` checks the update line, the confirm copy, a bad CSRF token (403), a second user's 404 on the page, the post, and the card, the success copy, and a 404 from the old token on the bundle, secrets, CLI token, and journal routes. `TestReplaceRefusesWhileRunning` refuses a message, pause, wake, delete, Add to Cast CLI, approve, and deny with "This machine is being replaced." and keeps the progress poll on the card. `TestReplaceFailureCopy` shows the read failure and Try again.

`TestAdminMachinesAndDryRunMakeNoAPICall` lists machines and prints a dry run without a host client. `TestAdminReplaceExitCodes` prints one line per step and exits 0, and the output has no token.

`TestLiveReplaceMachine` is built with `-tags live` and skips unless `CASTBOT_DO_TOKEN` is set. It was not run here.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll. Demo data only. 09b opens the confirm from the Replace machine link. 09d clicks Replace machine on that confirm and waits until the page says the machine was replaced.

- `09a-dark-1280.png`, `09a-dark-390.png`, `09a-light-1280.png`, `09a-light-390.png`
- `09a2-dark-1280.png`, `09a2-dark-390.png`, `09a2-light-1280.png`, `09a2-light-390.png`
- `09b-dark-1280.png`, `09b-dark-390.png`, `09b-light-1280.png`, `09b-light-390.png`
- `09c-dark-1280.png`, `09c-dark-390.png`, `09c-light-1280.png`, `09c-light-390.png`
- `09c2-dark-1280.png`, `09c2-dark-390.png`, `09c2-light-1280.png`, `09c2-light-390.png`
- `09d-dark-1280.png`, `09d-dark-390.png`, `09d-light-1280.png`, `09d-light-390.png`
- `09d2-dark-1280.png`, `09d2-dark-390.png`, `09d2-light-1280.png`, `09d2-light-390.png`
- `09e-dark-1280.png`, `09e-dark-390.png`, `09e-light-1280.png`, `09e-light-390.png`
- `09f-dark-1280.png`, `09f-dark-390.png`, `09f-light-1280.png`, `09f-light-390.png`
- `09f2-dark-1280.png`, `09f2-dark-390.png`, `09f2-light-1280.png`, `09f2-light-390.png`
- `09f3-dark-1280.png`, `09f3-dark-390.png`, `09f3-light-1280.png`, `09f3-light-390.png`
- `09f4-dark-1280.png`, `09f4-dark-390.png`, `09f4-light-1280.png`, `09f4-light-390.png`
- `09g-dark-1280.png`, `09g-dark-390.png`, `09g-light-1280.png`, `09g-light-390.png`

## No machine yet

A draft agent with no machine shows the composer and a Start button. The "Enter to send" hint stays off until a machine is running. Send stays off with it. The note says "No machine yet. Start it to chat."

- `s07c-nomachine-dark-1280.png`, `s07c-nomachine-dark-390.png`
- `s07c-nomachine-light-1280.png`, `s07c-nomachine-light-390.png`
