# Secrets vault

Each person has a vault. A secret is an environment variable for the agents they give it to. A model key is a vault row with a provider set. Values are encrypted with NaCl secretbox. The 32-byte key is `CASTBOT_SECRETS_KEY` in the host env file (mode 0600). It is not the session key, it is not in the repo, and startup refuses a missing, short, or all-zero key.

The guest receives a granted secret, and the model key for that agent's model, only when it starts or reloads. The value stays in memory and in the runtime environment. It is not written into the bundle, the page, the audit log, or a screenshot.

Replacing, granting, revoking, or deleting reloads each running agent that has the secret. A paused agent is not woken. An agent that is replying, or waiting on an approval, reloads after that finishes. If a reload fails, the new value stays saved and that agent keeps the previous value until a retry works.

`CASTBOT_PROVIDER_KEY_*` is still the fallback when the vault has no row for that provider. A vault row wins. A deploy that already sets the process environment keeps working until someone saves a key in Settings.
