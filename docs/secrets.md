# Secrets vault

Each person has a vault. A secret is an environment variable for the agents they give it to. A model key is a vault row for one keyed provider. Settings lists every keyed provider in the same row layout. The newest vault row for a provider is the one agents use. An older row says Not in use and can be deleted. Values are encrypted with NaCl secretbox. The 32-byte key is `CASTBOT_SECRETS_KEY` in the host env file (mode 0600). It is not the session key, it is not in the repo, and startup refuses a missing, short, all-zero, or identical key.

The guest receives a granted secret, and the model key for that agent's model, only when it starts or reloads. The value stays in memory and in the runtime environment. It is not written into the bundle, the page, the audit log, or a screenshot.

A connection token is not one of these grants. GitHub and DigitalOcean tokens stay in the vault and are used only by the host proxy. See [connections.md](connections.md).

Replacing, granting, revoking, or deleting reloads each running agent that has the secret. A paused agent is not woken. An agent that is replying, or waiting on an approval, reloads after that finishes. If a reload fails, the new value stays saved and that agent keeps the previous value until a retry works.

`CASTBOT_PROVIDER_KEY_*` is still the fallback when the vault has no row for that provider. The newest vault row wins. A deploy that already sets the process environment keeps working until someone saves a key in Settings.

The first start that copies old `secret_fixtures` rows into the vault also runs a WAL checkpoint and VACUUM, so the old plaintext is not left in free pages or the WAL. A later deploy has to set `CASTBOT_SECRETS_KEY` on the host before that start. This slice does not deploy.
