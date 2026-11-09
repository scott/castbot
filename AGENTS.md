# AGENTS.md

Rules for anyone (human or agent) working in this repository.

## Stack
- Go for the server and the in-VM supervisor (castlet).
- SQLite for storage (WAL mode). No other database without an approved plan change.
- htmx and server-rendered HTML for the UI, with SSE for streaming. Keep client JS small and plain; no SPA framework or JS build chain without an approved plan change.
- Agents are CastAgent agents run by the unmodified Cast runtime on DigitalOcean MicroVMs. Talk to them only through the `AgentHost` interface.

## Workflow
- One PR per slice from the plan. Keep each PR scoped to its slice.
- Open PRs as drafts only. A reviewer merges; authors do not merge their own PRs.
- Tests before merge: `gofmt`, `go vet`, and `go test -race ./...` must pass, plus any slice-specific evidence (screenshots at 1280 px and 390 px with no sideways scroll for UI work).
- Every table that holds user data carries `user_id` (and `workspace_id`) and every query is scoped by it. Add an authorization test for each new handler that proves one user cannot read or change another user's rows.

## Secrets
- Never commit secrets, tokens, API keys, passwords, private keys, or `.env` files. Use demo data only in tests, fixtures, and screenshots.
- Never print or log tokens or credentials, including in test output and CI logs.
- Secret scanning (gitleaks) will run in CI; a finding blocks merge.

## Commits
- Use a noreply commit identity. No `Co-authored-by` trailers.

## Writing
- No em dashes in code comments, docs, UI copy, commit messages, or PR text.
- Product copy uses the castbot brand only; no third-party product names or marks in the UI.
