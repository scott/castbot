# castbot

castbot is a web UI for Cast agents (https://castagent.dev) running on DigitalOcean MicroVMs: a sidebar of your agents, a streaming chat with each one, approval cards for risky tool calls, routines that run on a schedule, and files in and out, with every agent living in its own MicroVM that pauses when idle. It is a Go + SQLite + htmx control plane in front of the Cast runtime.

The shell signs you in, lists seeded agents, and streams a chat reply from a local echo runtime. A dropped connection resumes, and the reply is stored if the browser leaves. It does not call a model provider. A machine host is attached only when the env file has a token, one shared image, and an https control plane URL. Tests use a fake machine API and do not need a token.

`Dockerfile` builds the shared runtime image used by local tests. The image contains castlet and a protocol stand-in, and no agent. A container boots by pulling its bundle from the control plane. The image is not pushed anywhere.

## Run

```
openssl rand -hex 32
openssl rand -hex 32
```

Put the first value in `castbot.env` as `CASTBOT_SESSION_KEY` and the second as `CASTBOT_SECRETS_KEY` (see `castbot.env.example`), then `chmod 0600 castbot.env`. The secrets key encrypts the vault. The process will not start without it. A later deploy has to set `CASTBOT_SECRETS_KEY` on the host. This slice does not deploy.

```
go run ./cmd/castbot create-user --email you@example.com --name "Your Name"
go run ./cmd/castbot seed-demo --email you@example.com
go run ./cmd/castbot
```

`create-user` reads the password from the terminal and prints the authenticator secret once. It refuses to run when any account already exists. Open the address from the log, which defaults to `127.0.0.1:8080`.
