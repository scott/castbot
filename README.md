# castbot

castbot is a web UI for Cast agents (https://castagent.dev) running on DigitalOcean MicroVMs: a sidebar of your agents, a streaming chat with each one, approval cards for risky tool calls, routines that run on a schedule, and files in and out, with every agent living in its own MicroVM that pauses when idle. It is a Go + SQLite + htmx control plane in front of the Cast runtime and is in early planning; nothing here is ready to run yet.
