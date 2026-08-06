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
- 🔒 **Security gate in code** — sender whitelist + dedup enforced in Go *before* the agent runs; never in a prompt. Whitelist accepts literals **and** regexes (`s"..."`).
- 🧵 **Conversation memory** — replies in a thread resume the same agent session (`--resume`).
- 📡 **Push or poll, automatic** — uses IMAP `IDLE` for near-real-time when available, falls back to polling (e.g. on 163).
- 🤖 **Agent-agnostic** — defaults to `claude`, but any `-p "<prompt>"` CLI works.
- ⚙️ **Layered config** — YAML file, env vars, built-in defaults. Precedence: env > yaml > default. Secrets only from env.
- 🪶 **Tiny & static** — one small Go binary, `CGO_ENABLED=0`, no runtime deps. Auto-compressed with [UPX](https://upx.github.io/) when available (~2.8 MB on linux/amd64).

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

**Important:** replies in the same email thread share one agent session, and perch
dedups by `Message-ID`. If you send an email and then reply to it a few seconds later,
both messages arrive in one IMAP fetch — perch processes the **latest** one (which has
your most recent intent) and silently dedups the earlier one. Logged at INFO with the
subject so you can tell this from "perch ignored my email."

## 🔒 Security

The sender whitelist and message dedup are enforced in Go, **before** the agent is ever
spawned. The agent never sees mail from a non-whitelisted sender. This logic is
deterministic code, never a prompt — an email body cannot talk perch out of it.

## 📦 Install

```bash
go build -o perch ./cmd/perch      # or: make build  (static binary in ./bin, UPX-compressed if available)
```

`make build` runs [`upx --best`](https://upx.github.io/) on the freshly-linked binary if UPX
is on `PATH` (~7 MB → ~2.8 MB on linux/amd64). Set `PERCH_NO_UPX=1` to skip. Install UPX
via `apt-get install upx-ucl` or equivalent.

## ⚙️ Configuration

perch loads its settings from three layers, in this order of precedence
(higher overrides lower):

1. **Environment variables** — same names as before (`AGENT_EMAIL`, `POLL_INTERVAL`, …).
2. **YAML file** — `perch.yaml` in the working directory, or
   `~/.config/perch/perch.yaml`. Override with `--config <path>` /
   `PERCH_CONFIG=<path>`. A starter file lives at the repo root.
3. **Built-in defaults** — sensible values for 163; override via YAML or env.

Secrets (`AGENT_AUTH_CODE`) **must** come from the env and are never read
from the YAML file.

| Var / YAML key | Required | Default | Notes |
|---|:---:|---|---|
| `AGENT_EMAIL` | ✅ | — | the mailbox perch watches (env only — secret-ish) |
| `AGENT_AUTH_CODE` | ✅ | — | mailbox auth code (163 授权码), **not** the login password (env only) |
| `allow_from` / `ALLOW_FROM` | ⚠️ | — | list / comma-separated allowed senders; empty = **deny all**. YAML entries can be literals (`alice@163.com`) or regexes wrapped in `s"..."` (e.g. `s".+@(foo\|bar)\.example\.com"`, `s".*agent.*@qq\.com"`, `s"(?i).+@trusted\.org"`). Regexes are compiled at startup; a bad pattern fails the gate immediately (perch never starts in a fail-open state). `ALLOW_FROM` env var carries only literals. |
| `imap_addr` / `IMAP_ADDR` | | `imap.163.com:993` | implicit TLS |
| `smtp_addr` / `SMTP_ADDR` | | `smtp.163.com:465` | implicit TLS |
| `agent_bin` / `CLAUDE_BIN` | | `claude` | the agent CLI to spawn |
| `agent_workdir` / `CLAUDE_WORKDIR` | | `.` | working dir for the agent |
| `agent_permission_mode` / `CLAUDE_PERMISSION_MODE` | | `acceptEdits` | so agent tools run non-interactively |
| `poll_interval` / `POLL_INTERVAL` | | `60s` | poll interval / IDLE keepalive |
| `task_timeout` / `TASK_TIMEOUT` | | `30m` | SIGTERM→5s→SIGKILL after this |
| `max_prompt_bytes` / `MAX_PROMPT_BYTES` | | `65536` | truncate huge email bodies |
| `session_store` / `SESSION_STORE` | | tmp file | thread→session UUID map (JSON) |
| `tls_insecure_skip_verify` / `TLS_INSECURE_SKIP_VERIFY` | | `false` | **dev/test only** — accept self-signed certs |

## 🚀 Run

```bash
AGENT_EMAIL=agent@163.com \
AGENT_AUTH_CODE=xxxxxxxx \
ALLOW_FROM=alice@163.com \
CLAUDE_WORKDIR=/home/agent/workspace \
./perch
```

Or keep non-secret defaults in `perch.yaml` and inject only credentials via env:

```yaml
# perch.yaml
poll_interval: 5s
allow_from:
  - alice@163.com
  - s".+@(foo|bar)\.example\.com"      # regex: any user on foo/bar .example.com
agent_workdir: /home/agent/workspace
```

```bash
AGENT_EMAIL=agent@163.com AGENT_AUTH_CODE=xxxxxxxx ./perch
```

**Tip:** keep the auth code in a gitignored file (the repo ships one called `grant.code`)
rather than typing it on the command line:

```bash
AGENT_EMAIL=agent@163.com \
AGENT_AUTH_CODE="$(tr -d '\n' < ./grant.code)" \
./perch
```

A starter config with every field explained lives at [`perch.yaml.example`](perch.yaml.example).

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

**Known sharp edge:** if `replier.Reply(...)` fails (e.g. SMTP transient error), the
message is left `UNSEEN` so a later poll retries — but dedup is by `Message-ID`, so a
re-fetch of the same RFC822 bytes hits `FirstSight()=false` and is silently dropped. In
practice the SMTP path is reliable enough that this hasn't reproduced, but it's the
documented correctness gap.

## 📄 License

MIT — see [LICENSE](LICENSE).
