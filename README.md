<div align="center">

# 🐦 perch

**Your agent, perched on your inbox.**

Turn an ordinary email mailbox into a trigger for a CLI coding agent.

[![CI](https://github.com/ChrisZhangJin/perch/actions/workflows/ci.yml/badge.svg)](https://github.com/ChrisZhangJin/perch/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
![Status](https://img.shields.io/badge/status-prototype-orange)
![Platform](https://img.shields.io/badge/platform-linux%20%7C%20macOS-lightgrey)

[English](README.md) · [中文](README.zh-CN.md) · [Design](docs/DESIGN.md) · [Testing](docs/TESTING.md)

</div>

---

**perch** is a tiny Go daemon that watches an inbox and, **for whitelisted senders only**,
runs an agent (Claude Code by default, but any CLI works) on each incoming email and
replies in-thread.

It exists because a CLI agent like Claude Code has no native "wake on email" mechanism —
it's a stateless request→response tool. perch is the always-on **messenger + security
gate** that listens, authorizes, spawns the agent as a worker, and mails the result back.

> 💡 Inspired by the [AAMP](https://github.com/larksuite/aamp) protocol's idea of agent
> collaboration over email, but deliberately minimal and independent: no protocol headers,
> no pairing, no JMAP — just IMAP/SMTP + a whitelist.

## ✨ Features

- 📬 **Email → agent** — every whitelisted email becomes an agent task; the answer is mailed back, threaded.
- 🔒 **Security gate in code** — sender whitelist + dedup enforced in Go *before* the agent runs; never in a prompt.
- 🧵 **Conversation memory** — replies in a thread resume the same agent session (`--resume`).
- 📡 **Push or poll, automatic** — uses IMAP `IDLE` for near-real-time when available, falls back to polling (e.g. on 163).
- 🤖 **Agent-agnostic** — defaults to `claude`, but any `-p "<prompt>"` CLI works.
- 🪶 **Tiny & static** — one small Go binary, `CGO_ENABLED=0`, no runtime deps.

## 🗺️ How it works

One IMAP connection, alternating:

```text
ProcessUnseen (fetch + handle every unseen message)
   → IDLE, waiting up to POLL_INTERVAL
       (returns early on new mail = near-real-time; else acts as a safety poll)
   → repeat
```

Per message: `parse → dedup (\Seen + in-memory set) → whitelist → map thread to a stable
agent session → run agent -p … → threaded SMTP reply → mark \Seen`.

## 🔒 Security

The sender whitelist and message dedup are enforced in Go, **before** the agent is ever
spawned. The agent never sees mail from a non-whitelisted sender. This logic is
deterministic code, never a prompt — an email body cannot talk perch out of it.

## 📦 Install

```bash
go build -o perch ./cmd/perch      # or: make build  (static binary in ./bin)
```

## ⚙️ Configuration (environment)

| Var | Required | Default | Notes |
|---|:---:|---|---|
| `AGENT_EMAIL` | ✅ | — | the mailbox perch watches |
| `AGENT_AUTH_CODE` | ✅ | — | mailbox auth code (163 授权码), **not** the login password |
| `ALLOW_FROM` | ⚠️ | — | comma-separated allowed senders; empty = **deny all** |
| `CLAUDE_BIN` | | `claude` | the agent CLI to spawn |
| `CLAUDE_WORKDIR` | | `.` | working dir for the agent |
| `CLAUDE_PERMISSION_MODE` | | `acceptEdits` | so agent tools run non-interactively |
| `IMAP_ADDR` | | `imap.163.com:993` | implicit TLS |
| `SMTP_ADDR` | | `smtp.163.com:465` | implicit TLS |
| `POLL_INTERVAL` | | `60s` | poll interval / IDLE keepalive |
| `TASK_TIMEOUT` | | `30m` | SIGTERM→5s→SIGKILL after this |
| `MAX_PROMPT_BYTES` | | `65536` | truncate huge email bodies |
| `SESSION_STORE` | | tmp file | thread→session UUID map (JSON) |
| `TLS_INSECURE_SKIP_VERIFY` | | `false` | **dev/test only** — accept self-signed certs |

## 🚀 Run

```bash
AGENT_EMAIL=agent@163.com \
AGENT_AUTH_CODE=xxxxxxxx \
ALLOW_FROM=alice@163.com \
CLAUDE_WORKDIR=/home/agent/workspace \
./perch
```

## 🧪 Test

```bash
make test                          # unit tests + offline end-to-end
./scripts/localtest/demo.sh reject # real IMAP/SMTP against a local Docker mail server
```

The one-command demo spins up a throwaway [GreenMail](https://greenmail-mail-test.github.io/greenmail/)
server, sends a whitelisted task email, runs perch with a stub agent, and shows the
threaded reply — then proves a non-whitelisted sender is ignored. See
[`docs/TESTING.md`](docs/TESTING.md) for all three levels (automated → local Docker →
real 163 mailboxes).

### Manual end-to-end

1. Create two mailboxes (e.g. 163); enable IMAP/SMTP; generate an auth code for each.
2. Run perch on mailbox **B** with `ALLOW_FROM` set to mailbox **A**'s address.
3. From **A**, email **B** a task → B runs the agent and replies in-thread.
4. Reply again in the same thread → same agent session (memory preserved).
5. Email B from a non-whitelisted address → silently ignored (logged, no reply).

## 📡 Receiving: push vs polling

perch auto-detects the IMAP `IDLE` capability and logs the mode at startup:

- 🟢 **`mode=idle+poll`** — server *pushes* on new mail; perch reacts near-instantly
  (passive). Works on Gmail, Outlook, Fastmail, self-hosted Dovecot, etc.
- 🟡 **`mode=poll-only`** — no IDLE, so perch polls every `POLL_INTERVAL` (active). This is
  the case for **163/126** (NetEase has no IMAP IDLE). Lower `POLL_INTERVAL` to cut latency.

perch is IMAP-only — no JMAP / JMAP-Push or provider webhooks. **Chinese mainstream
mailboxes don't support JMAP**, and 163/126 lack IMAP IDLE too, so on them polling is the
only option. For true passive push, use an IDLE-capable mailbox (perch switches
automatically). Full provider table in
[`docs/DESIGN.md`](docs/DESIGN.md#receiving-passive-push-vs-active-polling).

## 📮 Provider notes (163 / 126)

163/126 require an IMAP `ID` command before login (perch sends it) and login with an
**authorization code**, not the account password. Hosts default to `imap.163.com:993` and
`smtp.163.com:465` (both implicit TLS); for 126 override `IMAP_ADDR`/`SMTP_ADDR`. NetEase
IMAP has no `IDLE`, so perch runs `poll-only` there.

## ⚠️ Status

Prototype. The agent-agnostic env var names still carry a `CLAUDE_` prefix for now; the
real IMAP/SMTP paths are validated manually and via the local Docker demo, not in CI.

## 📄 License

MIT — see [LICENSE](LICENSE).
