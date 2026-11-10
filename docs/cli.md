# Cast CLI access

castbot can mint a token for the Cast CLI. The token is shown once, on the machine page, inside an import blob. castbot stores only a hash. It does not hand out a cloud token. The CLI uses the owner's own cloud token from the environment.

The blob has the agent name, the castbot agent id, the machine URL, provider `microvm`, and the CLI token.

## Until import is merged

`cast import` is in a draft pull request on the Cast CLI. That pull request is not merged. This fallback does not complete CLI chat. Chat from the CLI counts as done only after that pull request is merged.

Until then, an owner can write the local row by hand.

1. Copy the blob when it is shown. It is not stored.
2. Hash the token with SHA-256 and keep the hex digest.
3. Pick a 6 character deployment id made of `a-z` and `0-9`.
4. In the local database, add a row to `deployments` with that id, the agent name, provider `microvm`, the machine URL, the hash, and `managed_by` set to `castbot`. The agent name has to match the CLI's agent name rules.
5. Save the raw token at `~/.cast/tokens/deployments/<id>.token` with mode `0600`, or through the CLI keychain under that deployment id.
6. Put `{"authMode":"dual","managedBy":"castbot","agentId":"<agent id>"}` in `volume_config` when the local row has that column.

`cast connect <agent>` then uses that row. The cloud token still comes from the environment. castbot never writes it.

`cast shutdown` and `cast upgrade` must not be used on a castbot-managed agent. castbot owns the machine lifecycle. Removing the local row does not delete the machine. Delete the machine from the castbot machine page.

## After import is merged

`cast import` reads the blob from stdin, checks the URL, writes the local row, and saves the token. `cast import --remove <agent>` deletes only that local row and token. `cast shutdown` and `cast upgrade` refuse a castbot-managed row.
