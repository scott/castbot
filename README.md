# castbot

castbot is a web UI for Cast agents (https://castagent.dev) running on DigitalOcean MicroVMs: a sidebar of your agents, a streaming chat with each one, approval cards for risky tool calls, routines that run on a schedule, and files in and out, with every agent living in its own MicroVM that pauses when idle. It is a Go + SQLite + htmx control plane in front of the Cast runtime.

The shell signs you in, lists seeded agents, and streams a chat reply from a local echo runtime. A dropped connection resumes, and the reply is stored if the browser leaves. It does not provision a machine or call a model provider.

## Run

```
openssl rand -hex 32
```

Put that value in `castbot.env` as `CASTBOT_SESSION_KEY` (see `castbot.env.example`), then `chmod 0600 castbot.env`.

```
go run ./cmd/castbot create-user --email you@example.com --name "Your Name"
go run ./cmd/castbot seed-demo --email you@example.com
go run ./cmd/castbot
```

`create-user` reads the password from the terminal and prints the authenticator secret once. It refuses to run when any account already exists. Open the address from the log, which defaults to `127.0.0.1:8080`.
