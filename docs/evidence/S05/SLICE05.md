# S05 Agent create and edit

The new-agent and edit pages are one form: name, title, sidebar section, avatar, instructions, model, tools, and permissions. The model list is the provider manifest shipped with this slice. An unknown model is an error and is not replaced with a default. Write and shell tools default to ask. `git_push` defaults to ask. Read tools default to always allow.

Import reads a zip or tar, or a public https github.com URL, and fills the same form for review before save. Private repositories are out of scope. The reader rejects path traversal, links, absolute paths, oversized archives, and too many files, and it does not execute anything. Files that can hold secrets, including `.env`, are skipped and listed. The saved bundle is `agent.yaml` plus `instructions.md`. Secret values are not written into it.

Saving an existing agent updates that bundle. A running machine is reloaded through the existing reload path, including its 409 backoff, and no new machine is created. A paused machine stays paused. A machine that is still starting keeps the bundle for the next boot. Creating an agent does not take a machine slot. The cap of 4 still applies when a machine is created.

Another account gets 404 for edit, import, and avatar, and for a create that names a section it does not own.

## Carry-forwards

The create poll keeps waiting, up to 60 seconds, while a running machine has no hostname yet. A published hostname is rejected only when it fails the https and `.microvm.ondigitalocean.com` check. An unpublished hostname is not that error.

The orphan sweep deletes a machine only when this database has a failed, deleted, or stale pending row for the `agent:<id>` tag. A copied database keeps the same instance id, and a machine whose agent id is unknown here is left alone.

A machine that is running but has not finished sign-in says "The machine is starting." The chip and the lead agree.

Trusted proxy prefixes broader than /8 for IPv4 or /32 for IPv6 are refused unless `CASTBOT_ALLOW_WIDE_PROXIES=1`. `0.0.0.0/0` and `::/0` stay refused with that flag set.

## What the tests show

`TestAgentFormRoundTrip` covers create, inline validation, a kept value, CSRF, and the bundle round trip. `TestImportReviewAndRejects` covers a valid folder, the skipped `.env`, traversal, a link, an absolute path, size, file count, and GitHub URL checks against a fake server. `TestSaveReloadsRunningAndLeavesPaused` asserts the fake host sees reload and not create or wake. `TestAgentFormAuthz` covers the 404s. `TestModelsComeFromTheManifest` reads the embedded manifest. `TestPollWaitsForHostnameThenAcceptsIt`, `TestPublishedBadHostnameIsRejected`, and `TestUnpublishedHostnameIsNotRejected` cover the address wait. `TestSweepCopiedDatabaseKeepsUnknownAgents` copies the database file. `TestStartingMachineIsNotReady` checks the starting sentence. `TestTrustedProxies` covers the wide prefixes and the override.

## Live

No live tag was run. The fake machine API was used. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll:

- `empty-dark-1280.png`, `empty-dark-390.png`, `empty-light-1280.png`, `empty-light-390.png`
- `filled-dark-1280.png`, `filled-dark-390.png`, `filled-light-1280.png`, `filled-light-390.png`
- `validation-dark-1280.png`, `validation-dark-390.png`, `validation-light-1280.png`, `validation-light-390.png`
- `import-dark-1280.png`, `import-dark-390.png`, `import-light-1280.png`, `import-light-390.png`
- `edit-dark-1280.png`, `edit-dark-390.png`, `edit-light-1280.png`, `edit-light-390.png`
