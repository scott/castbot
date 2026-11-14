# S19 Navigation and the agent panel

The workspace nav is gone. Approvals, Routines, Files, and Usage no longer sit in the upper left, and the phone no longer has a bottom nav. Chat is the main column. The right side is the agent panel, with Agent, Machine, Secrets, Approvals, Routines, and Files, in that order. Opening a tab swaps the panel and leaves the composer, the draft, and the thread in place.

Settings is the account avatar. Usage is a section of Settings. Old paths redirect to the matching panel on an agent, or home when the person has no agents.

## What the tests show

`TestWorkspaceNavGone` removes the workspace nav and the phone bottom nav, and the home column says "Pick an agent". `TestAgentPageHasNoTopTabs` has no chat top tabs, and the panel tabs put Secrets before Approvals. `TestPanelScopedToAgent` names only that agent in the Approvals, Routines, and Files fine print.

`TestPanelSwapKeepsComposer` returns the machine panel without the composer, and the full page still has the composer. The script keeps a `draft:` value in sessionStorage. `TestPanelCloseAndHistoryHooks` has the desktop Close control and the history helpers. `TestPhonePanelClose` has the visible Chat control and the peek strip. `TestSettingsCloseReturns` closes Settings back to the previous castbot view.

`TestUsageSectionEstimate` shows this month's estimate, `$0.00` when nothing has run, and redirects `/usage` to `/settings#usage`. `TestLegacyRoutesRedirect` sends `/approvals`, `/routines`, `/files`, and the old agent paths to the panel targets with status 302. `TestApprovalsRedirectOldest` sends `/approvals` to the agent whose pending approval is oldest.

`TestSameAgentApprovalAlerts` shows the header pill, the pending bar, and the Approvals tab badge on that agent, and does not toast. `TestOtherAgentApprovalToastOnce` toasts once for another agent's approval, names the agent and the tool, and leaves the arguments out. `TestPanelAuthz` returns 404 when another person opens those panels. `TestPanelCSRFUnchanged` still returns 403 for a bad token.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll. Demo data only. Alex Rivera, workspace Personal, agents Atlas, Pip, Ledger, Juniper, Moss, and Wren. Phone shots keep tap targets at least 44 px. No em dash or en dash in the pages.

- `13a-dark-1280.png`, `13a-dark-390.png`, `13a-light-1280.png`, `13a-light-390.png` chat with the Agent panel open
- `13a2-dark-1280.png`, `13a2-dark-390.png`, `13a2-light-1280.png`, `13a2-light-390.png` panel closed, composer draft kept
- `13b-dark-1280.png`, `13b-dark-390.png`, `13b-light-1280.png`, `13b-light-390.png` Machine panel, update available
- `13b2-dark-1280.png`, `13b2-dark-390.png`, `13b2-light-1280.png`, `13b2-light-390.png` replace confirm over the Machine panel
- `13c-dark-1280.png`, `13c-dark-390.png`, `13c-light-1280.png`, `13c-light-390.png` Approvals for this agent
- `13d-dark-1280.png`, `13d-dark-390.png`, `13d-light-1280.png`, `13d-light-390.png` Routines panel
- `13e-dark-1280.png`, `13e-dark-390.png`, `13e-light-1280.png`, `13e-light-390.png` Files panel
- `13f-dark-1280.png`, `13f-dark-390.png`, `13f-light-1280.png`, `13f-light-390.png` Usage inside Settings
- `13g-dark-1280.png`, `13g-dark-390.png`, `13g-light-1280.png`, `13g-light-390.png` toast for another agent's approval
- `13h-dark-1280.png`, `13h-dark-390.png`, `13h-light-1280.png`, `13h-light-390.png` home, nothing open
