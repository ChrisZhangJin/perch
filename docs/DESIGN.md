# perch — Design

## Purpose

A minimal daemon that lets a CLI coding agent collaborate over ordinary email, without
any dedicated protocol. Inspired by [AAMP](https://github.com/larksuite/aamp)'s "agents
collaborate over email" idea, but stripped to the essentials — the "B-tier" version:

> Give an agent an ordinary mailbox; when someone needs it, they email it; the agent acts
> on the content and replies. No pairing codes, no `X-AAMP-*` headers, no JMAP.

A CLI agent (e.g. Claude Code) is stateless request→response — it has no native
"wake on email." perch is the always-on process that **listens** to a mailbox and
**triggers** the agent, and it is the **security gate** in front of it.

**Core invariant:** perch is the deterministic messenger + gate; the agent is a stateless
worker it spawns on demand. The FROM whitelist and dedup are enforced in Go and MUST
NEVER be delegated to the LLM (that would be prompt-injectable).

## Architecture & data flow

```
                     ┌────────────────────── perch (resident) ──────────────────────┐
inbox ──IMAP──▶      │ mailbox: IDLE(primary) + poll(fallback) on one connection ─┐  │
                     │                                                            ▼  │
                     │                                        message: parse MIME    │
                     │                                                            ▼  │
                     │        gate: (1) dedup(\Seen + in-mem set)  (2) FROM whitelist│
                     │                     │ non-whitelist → log + mark \Seen + drop │
                     │                     ▼ (authorized task only)                  │
                     │        session: thread-root → session UUID (create/resume)    │
                     │                     ▼                                         │
                     │        runner: agent -p <prompt> --session-id/--resume ─▶ agent
                     │                     ▼ (stdout = reply body)                   │
inbox ◀──SMTP reply──│ replier: Re:subject + In-Reply-To/References + CRLF sanitize  │
                     └───────────────────────────────────────────────────────────────┘
```

Single connection, alternating `ProcessUnseen` → IDLE-up-to-`POLL_INTERVAL` → repeat.
IDLE gives near-real-time delivery; the timeout doubles as a safety poll; there is no
concurrent use of the IMAP connection.

## Receiving: passive push vs active polling

How perch learns a new mail arrived depends on what the server supports. It probes the
IMAP `IDLE` capability (RFC 2177) after `SELECT` and picks a mode, logged at startup:

- **`mode=idle+poll` (passive push):** perch holds an IMAP `IDLE` connection open; the
  server *pushes* an untagged `EXISTS` when mail arrives and perch wakes immediately
  (near-real-time). `POLL_INTERVAL` only re-issues IDLE periodically as a keepalive/safety
  net. This is event-driven, not a busy loop.
- **`mode=poll-only` (active polling):** if the server has no IDLE, perch never sends it
  (on some servers a rejected IDLE desyncs the connection). It just runs a `UID SEARCH
  UNSEEN` every `POLL_INTERVAL`. Latency ≈ the interval; cost is one cheap SEARCH per tick.

perch chooses automatically — no config. It is IMAP-only; it does **not** implement JMAP,
JMAP Push (RFC 8887), or provider-native webhooks.

### Provider capability (as of 2026)

| Provider | IMAP IDLE (perch push) | JMAP / JMAP Push | perch mode |
|---|---|---|---|
| 网易 163 / 126 (NetEase, Coremail) | ❌ no (verified) | ❌ | `poll-only` |
| QQ 邮箱 / 其他国内 (Sina, 139, 189, Aliyun) | ⚠️ varies — check the `mode=` log | ❌ | usually `poll-only` |
| Gmail, Outlook/O365, Fastmail, self-hosted Dovecot/Cyrus | ✅ yes | Fastmail: ✅ JMAP | `idle+poll` |

**Chinese mainstream mailboxes do not support JMAP** — they are all IMAP/POP/SMTP, so
JMAP Push is not an option domestically, and 163/126 lack even IMAP IDLE. On those, active
polling is the only mechanism; lower `POLL_INTERVAL` (e.g. `10s`) to reduce latency (the
per-tick cost is negligible).

**Want true passive push?** Use an IDLE-capable mailbox (Gmail / Fastmail / self-hosted
Dovecot) and perch switches to `idle+poll` automatically, zero code change. Provider-native
webhooks (Gmail API `watch` + Pub/Sub, MS Graph subscriptions) or JMAP Push are possible
but are per-provider integrations outside perch's minimal scope — at that point the full
[AAMP](https://github.com/larksuite/aamp) stack (JMAP push) is the better fit.

## Components (`internal/`)

| Package | Responsibility |
|---|---|
| `config` | env config + defaults |
| `message` | MIME parse → From/MessageID/InReplyTo/References/Subject/Body + `ThreadRoot` |
| `gate` | sender whitelist (exact, case-insensitive) + in-memory message-id dedup |
| `session` | thread-root → agent session UUID, persisted to JSON (`--session-id`/`--resume`) |
| `runner` | build prompt + spawn `agent -p`; timeout kill SIGTERM→5s→SIGKILL |
| `replier` | threaded SMTP reply, all headers CRLF-sanitized |
| `mailbox` | IMAP: `ID` + login + fetch unseen + mark `\Seen` + IDLE (integration-tested) |
| `app` | the loop: fetch → dedup → whitelist → session → run → reply → mark seen |

## Session & agent invocation

- `sessionKey` = a stable UUID per mail thread root, persisted so a thread resumes the
  same session across restarts.
- First message in a thread → `agent -p <prompt> --session-id <uuid>` (creates the
  session). Subsequent → `--resume <uuid>`. `--output-format text`, `--permission-mode`
  from config so tools run non-interactively.

## Security

- Whitelist enforced in Go, deterministically, **before** any agent spawn.
- The agent never sees mail from a non-whitelisted sender.
- CRLF sanitization on all outbound headers (no header injection).
- Auth code from env, never committed.
- Prompt size capped. Whitelisted senders' bodies still reach the LLM — inherent; the
  whitelist is the trust anchor.

## Testing

- Unit: config, message (`.eml` fixtures), gate, session, runner (real subprocess via a
  stub agent), replier.
- Offline end-to-end (`internal/app`): mock mailbox + fake sender assert the three key
  behaviors — whitelisted → runs + replies + seen; non-whitelisted → NOT run, no reply,
  still seen; duplicate id → skipped.
- Manual e2e: two real mailboxes (see README).

## Deliberately out of scope

Multi-agent discovery / capability cards; streaming progress; pairing & sender-policy
files; protocol headers; JMAP. Those are the full AAMP feature set — if this ever needs
untrusted senders, many agents, or long-task progress, graduate to AAMP proper.
