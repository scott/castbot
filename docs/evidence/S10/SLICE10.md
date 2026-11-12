# S10 Files

The Files tab sits in the agent panel. A person can upload a file, see the workspace and their uploads, preview text, and download. Search stays in the sidebar. There is no library page and no new nav entry. Opening Files does not wake a paused agent. An upload to a paused agent is held and copied the next time that agent wakes. The droplet refuses every upload when free disk is under 2 GB.

Pinned replies are out of scope. This slice does not add a runtime image. The guest file calls use the existing list, read, and write methods.

## What the tests show

`TestFilesMigrationV18` applies schema version 18 on a version 15 database, checks `files` and `file_cache`, leaves versions 16 and 17 unused, and reopens cleanly. `TestObserveWorkspaceKeepsSeenAt` keeps the first seen time when the name and size match, moves it when the size changes, scopes a read to the owner, and counts upload bytes per workspace.

`TestWorkspaceMethodsSendEdgeAuth` sends the machine token in `Authorization` and the agent token in `X-Cast-Authorization` on list, read, and write. `TestStreamingUploadOverPostLimit` accepts a body over the 1 MiB form limit. `TestStreamingDownloadChunks` streams the bytes, sets an attachment disposition, and serves a preview as `text/plain` with `X-Content-Type-Options: nosniff` and a sandbox content security policy.

`TestUploadMidStreamAbort` drops the connection and leaves no row and no blob. `TestUploadCap32MB` refuses a body over 32 MB before any byte is stored. `TestUploadNameCollisionBeforeExtension` keeps both copies and inserts ` (2)` before the extension. `TestUploadQueueOneAtATime` holds the second upload until the first finishes.

`TestHeldUploadDoesNotWake` stores an upload for a paused agent and copies it only after the next wake, without a wake of its own. `TestOpeningFilesDoesNotWake` lists Files with no list, read, write, or wake call. `TestWorkspaceDownloadWakes` wakes, then reads. `TestWorkspaceDownloadFailedWake` records `wake_requested` and answers "The machine is paused." `TestSeenTimeSurvivesPause` keeps the cached seen time after the clock moves.

`TestNoFileDeleteRoute` has no delete route for a file. `TestUploadQuotaAndWarning` stops at 2 GB per workspace, warns at 80 percent, and gives a second workspace its own 2 GB. `TestFreeDiskRefusesUpload` answers "The server is low on space. Upload is paused." `TestPreviewIsPlainText` never returns `text/html`. `TestSearchFilesAcrossAgents` matches file names for the signed-in user only. `TestPathLineInNextMessage` adds the path line to the next message. `TestReplaceLockHoldsUpload` holds the upload while a machine is being replaced and copies it onto the new machine afterward. `TestFileAuthzAndCSRF` returns 404 for another user and 403 for a missing CSRF header.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll. Demo data only. Alex Rivera, workspace Personal, agent Atlas. Phone shots hide the drop hint. The upload wait sentence stays visible.

- `12a-dark-1280.png`, `12a-dark-390.png`, `12a-light-1280.png`, `12a-light-390.png` workspace list
- `12a2-dark-1280.png`, `12a2-dark-390.png`, `12a2-light-1280.png`, `12a2-light-390.png` uploads segment
- `12b-dark-1280.png`, `12b-dark-390.png`, `12b-light-1280.png`, `12b-light-390.png` queue, one file at a time
- `12c-dark-1280.png`, `12c-dark-390.png`, `12c-light-1280.png`, `12c-light-390.png` drop overlay on desktop, list on phone
- `12d-dark-1280.png`, `12d-dark-390.png`, `12d-light-1280.png`, `12d-light-390.png` too large and mid-stream stop
- `12e-dark-1280.png`, `12e-dark-390.png`, `12e-light-1280.png`, `12e-light-390.png` paused list and a held upload
- `12f-dark-1280.png`, `12f-dark-390.png`, `12f-light-1280.png`, `12f-light-390.png` composer chip and path line
- `12g-dark-1280.png`, `12g-dark-390.png`, `12g-light-1280.png`, `12g-light-390.png` file detail and text preview
- `12i-dark-1280.png`, `12i-dark-390.png`, `12i-light-1280.png`, `12i-light-390.png` sidebar search
- `12j-dark-1280.png`, `12j-dark-390.png`, `12j-light-1280.png`, `12j-light-390.png` empty workspace
- `12k-dark-1280.png`, `12k-dark-390.png`, `12k-light-1280.png`, `12k-light-390.png` low disk, panel open
- `12k2-dark-1280.png`, `12k2-dark-390.png`, `12k2-light-1280.png`, `12k2-light-390.png` low disk, attach in the composer
