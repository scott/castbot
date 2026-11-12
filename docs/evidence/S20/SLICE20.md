# S20 Chat-first new agent

New agent opens a chat with castbot. One description becomes a setup card. Create on that card saves the agent, starts its machine, and lands in the agent's chat. There is no separate machine step and no create button outside the card.

The describe step uses a fake client in tests and screenshots. It does not call a model provider. The agent's own key stays in the user's vault. castbot does not copy a server key into the agent.

## What the tests show

`TestMigrateV19GreetedAtAndDrafts` adds `greeted_at` and `create_drafts` at schema version 19, backfills existing agents only when the column is new, and refuses a second greeting.

`TestDescribeUsesTheFake` reads the description with the injected client. `TestDescribeDoesNotDial` fails the setup step when no server key is set and does not open a connection. `TestAutoName` picks a short unused name. `TestNameFromText` keeps a name the person wrote. `TestNameConflict` disables Create when the name is taken and still allows Edit.

`TestConfirmOnlyOnTheCard` creates only from the card's confirm. `TestAdvancedCreateStarts` and `TestCreateRedirectsWithoutAMachineStep` start a Small machine (1 vCPU, 2 GiB, pause after 15 minutes) and redirect to `/agents/{id}`. `TestStartingUpHoldsTheWaitingMessage` stores a message typed during boot and does not start a run for it.

`TestGreetOnce` greets once across a reload. `TestGreetingWithAJob` starts the job in that same turn and mentions a message that was waiting. `TestGreetingWithoutAJob` asks for a job and offers three replies. `TestBootFailureAndTryAgain` shows the 4 minute failure, and Try again makes a new machine without a second hello.

`TestNoSecretsInInstructionsOrLogs` strips a pasted token before the client sees it, stores the redacted text, and keeps it out of instructions and logs. `TestNoServerKeyLent` blocks Create until the user adds a vault key. `TestRepromptUnclear` answers "hi" with examples and no card. `TestDraftExpiresAfter24Hours` drops a draft after a day. `TestCreateAuthzAndCSRF` returns 404 for another user's draft and 403 for a bad CSRF token.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390. No sideways scroll, no console or content-security errors, and phone controls are at least 44 px. Demo data is Alex Rivera, alex-style example accounts, Atlas, Scout, Juno, and Relay. The pasted token in 14j is built in the test and does not appear in the page.

Real clicks cover a suggestion chip, Send, Edit, Advanced, Create, the disabled Create buttons, and Try again.

- `14a-dark-1280.png`, `14a-dark-390.png`, `14a-light-1280.png`, `14a-light-390.png` empty create chat
- `14b-dark-1280.png`, `14b-dark-390.png`, `14b-light-1280.png`, `14b-light-390.png` setup card for Scout
- `14b2-dark-1280.png`, `14b2-dark-390.png`, `14b2-light-1280.png`, `14b2-light-390.png` editing the card
- `14c-dark-1280.png`, `14c-dark-390.png`, `14c-light-1280.png`, `14c-light-390.png` named agent with no job
- `14d-dark-1280.png`, `14d-dark-390.png`, `14d-light-1280.png`, `14d-light-390.png` auto-named Relay
- `14e-dark-1280.png`, `14e-dark-390.png`, `14e-light-1280.png`, `14e-light-390.png` full form from the card
- `14f-dark-1280.png`, `14f-dark-390.png`, `14f-light-1280.png`, `14f-light-390.png` starting up, message waiting
- `14f2-dark-1280.png`, `14f2-dark-390.png`, `14f2-light-1280.png`, `14f2-light-390.png` machine panel while starting
- `14g-dark-1280.png`, `14g-dark-390.png`, `14g-light-1280.png`, `14g-light-390.png` greeting that starts the job
- `14h-dark-1280.png`, `14h-dark-390.png`, `14h-light-1280.png`, `14h-light-390.png` greeting that asks for a job
- `14i-dark-1280.png`, `14i-dark-390.png`, `14i-light-1280.png`, `14i-light-390.png` re-prompt for "hi"
- `14j-dark-1280.png`, `14j-dark-390.png`, `14j-light-1280.png`, `14j-light-390.png` token left out of the card
- `14k-dark-1280.png`, `14k-dark-390.png`, `14k-light-1280.png`, `14k-light-390.png` Create blocked, no model key
- `14l-dark-1280.png`, `14l-dark-390.png`, `14l-light-1280.png`, `14l-light-390.png` name already in use
- `14m-dark-1280.png`, `14m-dark-390.png`, `14m-light-1280.png`, `14m-light-390.png` boot failed, Try again
