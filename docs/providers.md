# Provider keys

A saved model id, such as `digitalocean/deepseek-v3.2`, is written into the agent bundle as `brain.model`. The key is not written into that file.

The control plane reads one environment variable per provider. The name is `CASTBOT_PROVIDER_KEY_` plus the provider id in uppercase, with hyphens turned into underscores. For the digitalocean provider that is `CASTBOT_PROVIDER_KEY_DIGITALOCEAN`. For openai-api it is `CASTBOT_PROVIDER_KEY_OPENAI_API`.

When a machine asks for secrets, the value is sent under the name Cast expects, for example `DIGITALOCEAN_MODEL_ACCESS_KEY`. It is injected into the runtime process. It is not stored in the bundle, not shown on a page, and not written to logs.

If a run needs a key and the variable is empty, the run fails at once with: No key for provider digitalocean. Ask the owner to add it. A keyless provider, such as ollama, does not need a variable.

The full key vault is a later slice. Until then the variables live in the control plane process environment.

`CASTBOT_DEV_ECHO=1` turns on the local echo runtime. It is off by default. Startup refuses the flag when `CASTBOT_PUBLIC_URL` is https and the host is not localhost, 127.0.0.1, or ::1.
