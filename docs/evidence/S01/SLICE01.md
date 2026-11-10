# S01 app shell

The shell is a Go server, SQLite in WAL mode, and server-rendered HTML. Seeded agents are local rows. Nothing in this slice calls a cloud API, provisions a machine, or spends money.

## Sign-in

Owner sign-in is a password and a 6-digit authenticator code. The session cookie is `Secure`, `HttpOnly`, and `SameSite=Strict`. Every POST checks a CSRF token. `castbot create-user` creates the first account and prints the authenticator secret once.

Five wrong passwords or five wrong codes in one streak pause sign-in for 15 minutes. The two steps share one counter. The pause is stored for the email and for the client IP, so an unknown email gets the same message and the same pause as a real account. The password page says how many tries are left only when 2 or fewer remain. The code page says how many are left after a wrong code. A recovery-code submit does not count as a try. `TestLockoutHTTP` and `TestLockout` cover this.

A code that has already been accepted is rejected, including from a second browser. The accepted 30 second step is stored on the authenticator credential and checked in the same transaction that accepts the code. `TestTOTPReplay` and `TestTOTPReplayTwoSessions` cover this.

Every POST also checks `Origin` and `Sec-Fetch-Site`. A cross-site request is rejected even when the CSRF token matches.

Times and dates render in the account timezone. New accounts default to America/Denver. `TestDenverClock` covers a 01:01 UTC stamp rendering as 6:01 PM on Nov 9.

## What is real

- Password, authenticator code, trusted device for 30 days, logout, and sign-out of other sessions. Signing out other sessions also drops every other trusted device. Changing the password drops every other session, drops every trusted device, and rotates this browser's session id. `TestTrustedDeviceAndPasswordRevocation` covers both.
- Name, password, and theme (dark, light, or system) save on the account. Theme applies immediately.
- Sidebar sections, a local draft agent, and a chat reply that echoes the message you sent.
- One user gets 404, not 403, when listing, reading, changing, or deleting another user's sections, agents, threads, and messages.

## Stubs

These match the mocks and do not persist secrets:

- Settings says "8 recovery codes left". Manage does not issue codes. A recovery code at sign-in says they are not available yet.
- Forgot password always shows: "If an account exists for that email, we sent a reset link. It expires in 30 minutes." No email is sent, and the pause counter does not change.
- Model keys are a fixed list: Provider A ending 7Q2F, and Provider B not added. Replace and Add do not save a key.
- Email change and two-factor manage are short stub pages.
- Usage shows $7.42 when the account has agents and $0.00 when it does not. It is not metered.
- The first-run line "Connected · demo-team · token ends 9K1A" is the mock sentence. No token is stored.
- The status chip says mkc1 because the mocks do. The info pane says this version does not call a cloud API.

## Evidence

Screenshots are 1280 px wide and 390 px wide. None of them scroll sideways.

- `07a-login-1280.png`, `07a-login-390.png` password step
- `07b-totp-1280.png`, `07b-totp-390.png` six cells, Verify off until the code is complete
- `07c-password-error-1280.png`, `07c-password-error-390.png` same copy for a wrong password, tries left
- `07d-totp-error-1280.png`, `07d-totp-error-390.png` wrong code
- `07e-locked-1280.png`, `07e-locked-390.png` paused for 15 minutes
- `home-dark-1280.png`, `home-dark-390.png` dark home and the phone agent list
- `home-light-1280.png`, `home-light-390.png` light theme
- `chat-390.png` phone chat
- `info-1280.png`, `info-390.png` info pane
- `settings-1280.png`, `settings-390.png` Settings
- `first-run-1280.png`, `first-run-390.png` empty account
- `echo-1280.png` a sent line and the echo reply
- `chat-long-load-1280.png`, `chat-long-load-390.png` a thread taller than the screen, opened on the newest line, with the header and composer pinned
- `chat-long-send-1280.png`, `chat-long-send-390.png` the same thread after a send, still pinned on the newest line

The message list (`.ch-body`) is the scroll container. The chat header, approval bar, and composer stay put.

## Run the checks

```
gofmt -l .
go vet ./...
go test -race ./...
```

CI runs those, plus gitleaks 8.30.1. Test session keys are built at run time. `.gitleaksignore` covers the placeholder keys that were in the first commit only.
