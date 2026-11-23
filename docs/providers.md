# Provider keys

A saved model id, such as `digitalocean/deepseek-3.2`, is written into the agent bundle as `brain.model`. The key is not written into that file.

The model picker lists a provider only when this user has a key for it, from the vault or from the server environment. It then loads that provider's current chat models. The page shows ids and labels. It does not show the key. If the provider cannot be reached, the picker uses the ids saved in the manifest. A saved id that is no longer listed stays selected, and saving that same id is allowed. A different id that is not listed is rejected.

The control plane reads one environment variable per provider. The name is `CASTBOT_PROVIDER_KEY_` plus the provider id in uppercase, with hyphens turned into underscores. For the digitalocean provider that is `CASTBOT_PROVIDER_KEY_DIGITALOCEAN`. For openai-api it is `CASTBOT_PROVIDER_KEY_OPENAI_API`.

A machine that is running the current image does not receive the provider key. castlet asks for secrets with `X-Castbot-Model-Proxy: 1`, and the response leaves the key out. After that header has been seen for a machine, later secret reads leave the key out too, until that machine is replaced. The runtime is pointed at castlet's loopback forwarder with a per-boot placeholder. The placeholder is not a provider key and is not given to commands. The runtime may present it as the bearer token or as the provider key header it already sends. The forwarder accepts either, does not send the placeholder upstream, and accepts a caller only when the placeholder matches and the caller is the runtime user. The host checks the machine token, checks that the call is for that agent's model, limits how often that agent can call and how many of its calls can be in flight, and adds the key there. The key is not written to the response, the page, the bundle, or the log. The request body is not logged either.

A machine that has not been replaced still receives the key the way it always has, until its castlet sends that header. A replaced machine receives the key again until the new castlet sends the header. Nothing forces a replacement. Replace the control plane before the image: a new image talking to an older control plane would send model calls to a route that older build does not have.

The hash of secret names and values still includes the key, so rotating it reloads the guest the way other secret changes do. The hash is not a secret. The values are still not logged. The guest reads the current key through the forwarder on each call.

If a run needs a key and the variable is empty, the run fails at once with: No key for provider DigitalOcean. Ask the owner to add it. A keyless provider, such as ollama, does not need a variable.

Settings lists every keyed provider. The newest vault row for that provider wins over the process environment. If the vault has no row, the process environment is still used, so an existing agent keeps working. See `docs/secrets.md`.

`CASTBOT_DEV_ECHO=1` turns on the local echo runtime. It is off by default. Startup refuses the flag when `CASTBOT_PUBLIC_URL` is https and the host is not localhost, 127.0.0.1, or ::1.
