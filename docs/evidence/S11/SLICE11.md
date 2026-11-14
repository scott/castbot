# S11 Machine panel

The Machine panel shows the saved state, Pause and Wake, a log tail, the workspace files, and this month's estimated machine time. The estimate is running time at the machine size, plus disk while the machine is paused. It comes from `vm_usage`. It is an estimate, not a bill. Settings shows the same total for the account, and one line for each agent that has time or a live machine.

Opening the panel does not wake a paused machine. Logs and the file list are read only while the saved state is already running. A paused panel says the check does not wake the machine. A console over the provider console socket is later work. Replace, Pause, Wake, and the Files tab stay as they were.

## What the tests show

`TestVMUsageIntervals` records schema 24, keeps one open interval when running is written twice, splits that interval when the size changes, and counts compute and disk inside the window. Another user cannot read the rows.

`TestVMUsageMonthBoundaryAndClose` counts only the part of an interval that falls inside the month. Deleting the machine closes the interval. Deleting the agent closes it and hides it from that user.

`TestVMUsageReplaceSplitsAndRelease` closes the old machine and opens the new one in the same replace. Releasing the machine closes its interval.

`TestEnsureUsageBackfillsFromNow` starts an interval at the backfill time. It does not invent earlier time, and it does not call the guest.

`TestFormatUSD` rounds half up to cents. One hour on the small size is $0.04. Ten hours is $0.39. One day of paused disk on the small size is $0.01. One hour on the large size displays $0.31.

`TestMachinePanelRunning` shows the estimate, the scrubbed tail, and `notes.md`. The chat page does not read logs or files. The log fragment has no page shell. A granted secret and the machine token are not in the HTML.

`TestMachinePanelDoesNotWake` leaves a paused machine paused. The panel does not poll logs, and the log fragment does not call the guest.

`TestMachinePanelQuietStates` skips the guest while the machine is failed or starting. A failed read says the machine was not woken.

`TestMachinePanelAuthz` returns 404 when another user opens the panel or the log fragment.

`TestSettingsMachineEstimate` shows $0.39 after ten hours, and $0.00 on another account.

`TestUsageSectionEstimate` keeps the Settings usage section, shows this month as $0.00 when nothing has run, and drops the unmetered banner.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll. Demo data only. Alex Rivera, workspace Personal, agent Atlas. The log tail is `machine ready`. The file row is `notes.md`.

- `running-dark-1280.png`, `running-dark-390.png`, `running-light-1280.png`, `running-light-390.png` running machine, $0.04, log tail, and `notes.md`
- `paused-dark-1280.png`, `paused-dark-390.png`, `paused-light-1280.png`, `paused-light-390.png` paused machine, Wake, and the note that the panel does not wake it
- `usage-dark-1280.png`, `usage-dark-390.png`, `usage-light-1280.png`, `usage-light-390.png` Settings usage, this month $0.39, and the Atlas line

The S19 machine and usage shots (`13b`, `13b2`, `13f`) were taken again so they match the panel and the estimate copy.
