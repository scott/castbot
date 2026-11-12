# S08 secrets vault

A signed-in person can add, replace, grant, revoke, and delete their own secrets. Values are stored with NaCl secretbox. The master key is `CASTBOT_SECRETS_KEY` in the host env file (mode 0600). It is not the session key and it is not in the repo. Startup refuses a missing, short, or all-zero key.

Granted secrets, and the model key for that agent's model, reach the guest only when it starts or reloads. They stay in memory and in the runtime environment. They are not written into the bundle. A paused agent is not woken. An agent that is replying, or waiting on an approval, reloads after that finishes. If a reload fails, the new value stays saved and that agent keeps the previous value until a retry works.

Schema version 16 adds `secrets` and copies any `secret_fixtures` row into it under the same id, then deletes the plaintext. Opening the database twice records version 16 once. S09 owns version 17 and S10 owns version 18.

The guest HTTP API is unchanged, so this slice does not need a new runtime image. A later deploy has to set `CASTBOT_SECRETS_KEY` on the host. This slice does not deploy.

## What the tests show

`TestMigrateEncryptsFixtures` opens a version 15 database, inserts a plaintext fixture and a grant, and reopens it. The grant id is unchanged, the plaintext is gone from the database file after a checkpoint and VACUUM, and a second open does not record version 16 again. `TestSecretNameRuleAndCap` accepts `SEARCH_API_KEY`, rejects a lowercase name, `PATH`, `HOME`, `CASTBOT_FOO`, `CASTLET_FOO`, `HOSTNAME`, and a name that starts with a digit. The name is unique per person. Another person can reuse it. 8192 bytes save. 8193 bytes, an empty value, and a newline are rejected. Another person's `SecretMeta` is not found.

`TestVaultPageHidesValues` renders the list and the detail with a mask and no password value. The invalid-name and name-in-use copy is present. An 8 KB value is not echoed. A bad CSRF token is 403. `TestVaultAuthzIsNotFound` is user B on user A's detail, replace, delete, grant, revoke, agent grant, and reload, all 404. `TestPausedReplaceDoesNotWake` replaces a secret on a paused agent with no wake and no reload, and the next guest pull returns the new value. `TestReloadWaitsForReplyThenRetries` holds the reload until the open run finishes, keeps the new ciphertext when the reload fails, shows the failure copy, and retries that agent. `TestModelKeyDeleteAndEnvFallback` uses the process environment until a vault row exists, then the vault wins. Deleting the key that agents still use returns 409 and the in-use page. After those agents change model, the delete succeeds. `TestSecretFingerprintIsVaultHMAC` matches the HMAC of the decrypted pair and differs from SHA-256. A matching fingerprint does not reload. `TestReplaceLockRefusesVaultReload` returns 409 with "This machine is being replaced." and does not write the new value. `TestMachinePullIsGrantPlusModelKey` returns the granted secret and the model key to the guest, leaves an ungranted secret out, and keeps every value out of the bundle.

## Live

No live tag was run. No machine API was called. Machines created: 0. Spend: $0.

## Evidence

Headless Chrome, dark and light, 1280 and 390, no sideways scroll, no console errors. Phone controls are at least 44px. Demo data only (Alex Rivera, alex@example.com). The stored value is not in the page or these files.

- `10a-dark-1280.png`, `10a-dark-390.png`, `10a-light-1280.png`, `10a-light-390.png`
- `10a2-dark-1280.png`, `10a2-dark-390.png`, `10a2-light-1280.png`, `10a2-light-390.png`
- `10b-dark-1280.png`, `10b-dark-390.png`, `10b-light-1280.png`, `10b-light-390.png`
- `10b2-dark-1280.png`, `10b2-dark-390.png`, `10b2-light-1280.png`, `10b2-light-390.png`
- `10b3-dark-1280.png`, `10b3-dark-390.png`, `10b3-light-1280.png`, `10b3-light-390.png`
- `10c-dark-1280.png`, `10c-dark-390.png`, `10c-light-1280.png`, `10c-light-390.png`
- `10d-dark-1280.png`, `10d-dark-390.png`, `10d-light-1280.png`, `10d-light-390.png`
- `10d2-dark-1280.png`, `10d2-dark-390.png`, `10d2-light-1280.png`, `10d2-light-390.png`
- `10d3-dark-1280.png`, `10d3-dark-390.png`, `10d3-light-1280.png`, `10d3-light-390.png`
- `10e-dark-1280.png`, `10e-dark-390.png`, `10e-light-1280.png`, `10e-light-390.png`
- `10f-dark-1280.png`, `10f-dark-390.png`, `10f-light-1280.png`, `10f-light-390.png`
- `10f2-dark-1280.png`, `10f2-dark-390.png`, `10f2-light-1280.png`, `10f2-light-390.png`
- `10f3-dark-1280.png`, `10f3-dark-390.png`, `10f3-light-1280.png`, `10f3-light-390.png`
- `10g-dark-1280.png`, `10g-dark-390.png`, `10g-light-1280.png`, `10g-light-390.png`
- `10g2-dark-1280.png`, `10g2-dark-390.png`, `10g2-light-1280.png`, `10g2-light-390.png`
