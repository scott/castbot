# S12 Upgrades and backup

A memory-copying machine move saves the runtime it left. The owner can roll back to that runtime for 7 days. The same replace path copies memory and workspace files again, then removes the machine it left. A move onto the same image does not change the offer. A move that skips the copy retires the offer and does not save a new one. After a rollback, the runtime just left becomes the new offer, so the owner can undo that rollback.

The Machine page shows the short image tag and a Roll back runtime button. The confirm step uses the same full-page layout as Replace machine on a narrow screen. The posted form does not choose the image. Another account gets 404.

`castbot admin rollback-runtime` is that same move. `--dry-run` prints the plan and does not call the machine API. `backup` copies the database and the upload blobs beside it while the server can stay up. `restore` refuses while login answers, and when the public URL is unset. The deploy backup copies the same blobs and records `files` and `files_sha256`.

## Checkpoint

S12 does not build a golden checkpoint. The measured create-plus-boot times are under the 30 second line: 8.32 s to the first HTTP 200 for the tiny image in S00, and 16.0 s to running in S04 when the runtime image was already cached. A first pull of the larger image was not measured. A later measurement of a cold pull can revisit that choice. This tree has no checkpoint call.

## What the tests show

Schema version 28 adds `runtime_rollbacks`. Opening the database twice records version 28 once. The table carries `user_id` and `workspace_id`.

`TestRuntimeRollbackOffer` keeps no offer when the new image matches the live image, saves the previous image after a copy, hides that row from another user, and drops it after 7 days.

`TestNoCopyMoveRetiresRollback` clears an open offer on a no-copy move and does not save the image that move left.

`TestRollbackPageAndAuthz` shows the button, the 7 day note, and the confirm copy. A bad form token is 403. Another user gets 404 on the confirm page and the post. The rollback boots the saved image, and the page then offers the runtime it just left. The HTML has the short tag, not the full image reference, the machine token, or the endpoint.

`TestBackupRestoresDatabaseAndBlobs` snapshots the open database, copies only a blob whose name is its sha256, and skips a symlink, a partial upload, and a 64-hex name with the wrong bytes. A second backup in the same minute fails and leaves the first. Restore puts the user and the blob back and refuses a snapshot whose record hash does not match, before it moves the live database.

`TestRollbackPlanAndRestoreBlock` prints the current tag and the rollback tag, and blocks restore when the public URL is unset or the server is up.

`scripts/deploy_test.sh` happy path copies one good blob, skips `.part-upload`, and checks `files 1` plus `files_sha256`. The env file is not in the backup.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll. Demo data only. Alex Rivera, workspace Personal, agent Atlas. The machine is on `ff6d7ea`. The offer is `7b5e414`. The log tail is `machine ready`.

- `panel-dark-1280.png`, `panel-dark-390.png`, `panel-light-1280.png`, `panel-light-390.png` Machine panel, Roll back runtime, and the 7 day note
- `confirm-dark-1280.png`, `confirm-dark-390.png`, `confirm-light-1280.png`, `confirm-light-390.png` confirm step, previous runtime `7b5e414`, and the promise that a failed read keeps the current machine
