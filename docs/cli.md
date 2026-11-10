# Cast CLI access

castbot can mint a token for the Cast CLI. The token is shown once, on the machine page, inside an import blob. castbot stores only a hash. It does not hand out a cloud token. The CLI uses the owner's own cloud token from the environment.

The blob `name` is a CLI-safe slug (`[A-Za-z0-9][A-Za-z0-9-]*`). `display_name` is the agent name shown in castbot. The blob also has the castbot agent id, the machine URL, provider `microvm`, and the CLI token. The machine page shows the CLI name to use with `cast connect`.

## Until import is merged

`cast import` is in a draft pull request on the Cast CLI. That pull request is not merged. This fallback does not complete CLI chat. Chat from the CLI counts as done only after that pull request is merged.

Until then, an owner can write the local row by hand.

1. Copy the blob when it is shown. It is not stored.
2. Hash the token with SHA-256 and keep the hex digest.
3. Pick a 6 character deployment id made of `a-z` and `0-9`.
4. In the local database, add a row to `deployments` with that id, the blob `name`, provider `microvm`, the machine URL, and the hash. Do not set a `managed_by` column. Today's Cast does not have that column.
5. Save the raw token at `~/.cast/tokens/deployments/<id>.token` with mode `0600`, or through the CLI keychain under that deployment id.
6. Put `{"authMode":"dual","managedBy":"castbot","agentId":"<agent id>"}` in `volume_config`. On today's Cast, `managedBy` lives only there.

`cast connect <agent>` then uses that row. The cloud token still comes from the environment. castbot never writes it.

An unpatched Cast does not refuse `cast shutdown` or `cast upgrade`. That warning is the only guard. Do not run those commands on a castbot-managed agent. castbot owns the machine lifecycle. Removing the local row does not delete the machine. Delete the machine from the castbot machine page.

## After import is merged

`cast import` reads the blob from stdin, checks the URL, writes the local row, and saves the token. `cast import --remove <agent>` deletes only that local row and token. A Cast build that includes that change refuses `cast shutdown` and `cast upgrade` for a castbot-managed row. An unpatched Cast does not.
