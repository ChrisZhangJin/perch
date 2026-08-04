# perch

> Your agent, perched on your inbox.

**perch** is a tiny Go daemon that turns an ordinary email mailbox into a trigger for a
CLI coding agent. It watches an inbox and, **for whitelisted senders only**, runs an
agent (Claude Code by default, but any CLI works) on each incoming email and replies
in-thread.

It exists because a CLI agent like Claude Code has no native "wake on email" mechanism —
it's a stateless request→response tool. perch is the always-on **messenger + security
gate** that listens, authorizes, spawns the agent as a worker, and mails the result back.

> Inspired by the [AAMP](https://github.com/larksuite/aamp) protocol's idea of agent
> collaboration over email, but deliberately minimal and independent: no protocol
> headers, no pairing, no JMAP — just IMAP/SMTP + a whitelist. See [`docs/DESIGN.md`](docs/DESIGN.md).

## Why it's safe

The sender whitelist and message dedup are enforced in Go, **before** the agent is ever
spawned. The agent never sees mail from a non-whitelisted sender. This logic is
deterministic code, never a prompt — an email body cannot talk perch out of it.

## How it works

One IMAP connection, alternating:

```
ProcessUnseen (fetch + handle every unseen message)
   → IDLE, waiting up to POLL_INTERVAL
       (returns early on new mail = near-real-time; else acts as a safety poll)
   → repeat
```

Per message: parse → dedup (`\Seen` + in-memory set) → **whitelist** → map the mail
thread to a stable agent session → run `agent -p …` → threaded SMTP reply → mark `\Seen`.
Replies to the same thread resume the same agent session, so context carries across a
conversation.

## Install / build

```bash
go build -o perch ./cmd/perch
```

## Configure (environment)

| Var | Required | Default | Notes |
|---|---|---|---|
| `AGENT_EMAIL` | yes | — | the mailbox perch watches |
| `AGENT_AUTH_CODE` | yes | — | mailbox auth code (163 授权码), NOT the login password |
| `ALLOW_FROM` | yes* | — | comma-separated allowed senders; empty = deny all |
| `CLAUDE_BIN` | no | `claude` | the agent CLI to spawn |
| `CLAUDE_WORKDIR` | no | `.` | working dir for the agent |
| `CLAUDE_PERMISSION_MODE` | no | `acceptEdits` | so agent tools run non-interactively |
| `IMAP_ADDR` | no | `imap.163.com:993` | implicit TLS |
| `SMTP_ADDR` | no | `smtp.163.com:465` | implicit TLS |
| `POLL_INTERVAL` | no | `60s` | safety-poll fallback (IDLE is primary) |
| `TASK_TIMEOUT` | no | `30m` | SIGTERM→5s→SIGKILL after this |
| `MAX_PROMPT_BYTES` | no | `65536` | truncate huge email bodies |
| `SESSION_STORE` | no | tmp file | thread→session UUID map (JSON) |

`*` Not hard-required, but with an empty `ALLOW_FROM` every sender is denied.

## Run

```bash
AGENT_EMAIL=agent@163.com \
AGENT_AUTH_CODE=xxxxxxxx \
ALLOW_FROM=alice@163.com \
CLAUDE_WORKDIR=/home/agent/workspace \
./perch
```

## Test

```bash
make test                          # unit tests + offline end-to-end
./scripts/localtest/demo.sh reject # real IMAP/SMTP against a local Docker mail server
```

The one-command demo spins up a throwaway [GreenMail](https://greenmail-mail-test.github.io/greenmail/)
server, sends a whitelisted task email, runs perch with a stub agent, and shows the
threaded reply — then proves a non-whitelisted sender is ignored. See
[`docs/TESTING.md`](docs/TESTING.md) for all three levels (automated → local Docker →
real 163 mailboxes).

## Manual end-to-end

1. Create two mailboxes (e.g. 163); enable IMAP/SMTP; generate an auth code for each.
2. Run perch on mailbox B with `ALLOW_FROM` set to mailbox A's address.
3. From A, email B a task → B runs the agent and replies in-thread.
4. Reply again in the same thread → same agent session (memory preserved).
5. Email B from a non-whitelisted address → silently ignored (logged, no reply).

## Provider notes (163)

163/126 require an IMAP `ID` command before login (perch sends it) and login with an
**authorization code**, not the account password. Hosts default to `imap.163.com:993` and
`smtp.163.com:465` (both implicit TLS); for 126 override `IMAP_ADDR`/`SMTP_ADDR`.

## Status

Prototype. The agent-agnostic env var names still carry a `CLAUDE_` prefix for now; the
real IMAP/SMTP paths are validated manually, not in CI.

## License

MIT — see [LICENSE](LICENSE).
