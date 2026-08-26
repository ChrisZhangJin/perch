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
- 🎩 **Standing role** — `append_system_prompt` layers a persistent role (support desk, triager) onto the agent's own system prompt, from a file that's re-read every email. See [Standing role](#-standing-role-append_system_prompt).
- 🪝 **On-email script hook** — run your own script for every accepted email (`<email_id> <subject> <body> <sender> <new_thread|reply_thread>`); log threads, ping a phone, bump a counter. See [Script hook](#-script-hook).
- ⚙️ **Layered config** — YAML file, env vars, built-in defaults. Precedence: env > yaml > default. Secrets only from env.
- 🪶 **Tiny & static** — one small Go binary, `CGO_ENABLED=0`, no runtime deps. Auto-compressed with [UPX](https://upx.github.io/) when available (~2.8 MB on linux/amd64).

## 🗺️ How it works

Perch picks one of two transport strategies at startup, driven by the
provider's `Capabilities` in `internal/provider/registry.go`:

| Provider | `SupportsIDLE` | Strategy | Behaviour |
|---|---|---|---|
| 163, 126 | `false` | **P** — Poller (short-conn) | Every `FetchUnseen` / `MarkSeen` is a fresh IMAP dial → op → logout → close. A dead or restarted server simply means the next dial fails — no stuck connection. |
| qq (and any future provider that advertises IDLE) | `true` | **L** — IMAPMailbox (long-conn + IDLE) | One connection, alternating `FetchUnseen` and `IDLE` (capped by `POLL_INTERVAL` as a safety poll). New mail arrives in 1-3 seconds instead of waiting for the next poll tick. |

Future `S` mode (server-push webhook, e.g. Gmail Pub/Sub) is a one-line addition to `BuildStrategy` once a provider needs it.

```text
P mode:  for { sleep(POLL_INTERVAL); fetch(dial→UIDSearch→Fetch→Logout→Close); mark seen }
L mode:  for { IDLE up to POLL_INTERVAL (early-exit on EXISTS); fetch; }   // shared connection
```

Per message: `parse → dedup (\Seen + in-memory set) → whitelist → map thread to a stable
agent session → run agent -p … → threaded SMTP reply → mark \Seen`.

**When the agent can't run** (binary missing, crash, timeout, quota exhausted), the
sender gets a short out-of-office notice asking them to send again later — never the
Go error. The error itself goes to the log at ERROR, where the operator needs it:
`exec: "nanopi": executable file not found in $PATH` is meaningless to the person who
emailed in, and it leaks binary names, paths and stderr. Same for a run that produces
nothing usable twice: the sender is asked to rephrase, in plain language. Both notices
are written by perch (no agent in the loop), so perch picks the language from the
inbound email — Chinese in, Chinese out.

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
   `~/.perch/perch.yaml`. Override with `--config <path>` /
   `PERCH_CONFIG=<path>`. A starter file lives at the repo root.
3. **Built-in defaults** — sensible values for 163; override via YAML or env.

Secrets (`AGENT_AUTH_CODE`) **must** come from the env and are never read
from the YAML file.

| Var / YAML key | Required | Default | Notes |
|---|:---:|---|---|
| `AGENT_EMAIL` | ✅ | — | the mailbox perch watches. Env wins; otherwise read from `email:` in YAML (written by setup wizard). |
| `AGENT_AUTH_CODE` | ✅ | — | mailbox auth code (163 授权码), **not** the login password. Env-only — never written to disk. |
| `allow_from` / `ALLOW_FROM` | ⚠️ | — | list / comma-separated allowed senders. A literal `*` entry accepts everyone (this is the onboarding default the setup wizard writes — tighten before going live); otherwise empty list = **deny all** (fail-closed, the safe default for env-only setups). YAML entries can be literals (`alice@163.com`) or regexes wrapped in `s"..."` (e.g. `s".+@(foo\|bar)\.example\.com"`, `s".*agent.*@qq\.com"`, `s"(?i).+@trusted\.org"`). Regexes are compiled at startup; a bad pattern fails the gate immediately (perch never starts in a fail-open state). `ALLOW_FROM` env var carries only literals. |
| `email_provider.name`       | —                 | `163`            | `163` / `126` / `qq` — endpoints derived |
| `ai_agent.name`             | —                 | `claude`         | `claude` / `nanopi` / `pi` — binary derived |
| `ai_agent.workdir`          | —                 | `.`              | cwd for the spawned agent |
| `ai_agent.permission_mode`  | —                 | `acceptEdits`    | claude only; ignored by nanopi/pi |
| `ai_agent.append_system_prompt` / `APPEND_SYSTEM_PROMPT` | — | — | standing role appended to the agent's own system prompt. Text or file path. See [Standing role](#-standing-role-append_system_prompt) |
| `poll_interval` / `POLL_INTERVAL` | | `60s` | poll interval / IDLE keepalive |
| `task_timeout` / `TASK_TIMEOUT` | | `30m` | SIGTERM→5s→SIGKILL after this |
| `max_prompt_bytes` / `MAX_PROMPT_BYTES` | | `65536` | truncate huge email bodies |
| `max_attachment_bytes` / `MAX_ATTACHMENT_BYTES` | | `52428800` (50 MB) | per-attachment size cap; oversized attachments are dropped (parse survives) |
| `session_store` / `SESSION_STORE` | | tmp file | thread→session UUID map (JSON) |
| `hooks.on_email` / `ON_EMAIL_HOOK` | | — | script run for every accepted email, before the agent. See [Script hook](#-script-hook) |
| `hooks.timeout` / `HOOK_TIMEOUT` | | `30s` | per-run bound on the hook (SIGTERM→5s→SIGKILL) |
| `tls_insecure_skip_verify` / `TLS_INSECURE_SKIP_VERIFY` | | `false` | **dev/test only** — accept self-signed certs |

## 🎩 Standing role (`append_system_prompt`)

The prompt perch builds per email is about *this* message. `append_system_prompt`
is the other axis: text layered onto the **agent's own system prompt** on every
run, for a role that outlives any single email — a support desk, a build
babysitter, an on-call triager.

```yaml
# perch.yaml
ai_agent:
  name: claude
  workdir: /root/perch
  append_system_prompt: /root/perch/helpdesk.md   # a path...
  # append_system_prompt: |                       # ...or inline text
  #   You are the kulink support desk.
  #   Task definitions live in ./tasks/.
```

The value is **text-or-path** (the same rule pi's own flag follows): a
single-line value naming a readable file is read as a file, anything else is
used literally. A file is **re-read on every email**, so editing the role takes
effect on the next message with no restart. perch logs which reading won at
startup, so a path typo is visible:

```
INFO "append_system_prompt enabled" agent=claude source="file /root/perch/helpdesk.md" bytes=1683
```

**Agents that don't have the flag are detected, not broken.** perch runs
`<agent> --help` once at startup and looks for `--append-system-prompt`. claude
and pi have it; nanopi is adding it. If it's missing, perch logs a WARN naming
what it dropped and runs without it, rather than killing every email on an
unknown flag — and starts using it automatically once the agent ships it, with
no perch upgrade or config change.

### Help-desk template

[`helpdesk.md.example`](helpdesk.md.example) is a fill-in-the-blanks support-desk
role: read task definitions from `tasks/`, ask for missing information instead of
guessing, invent no policy or prices, escalate rather than promise, and keep
internals out of replies. [`tasks.example/complaint-intake.md`](tasks.example/complaint-intake.md)
shows the shape of one task file.

```bash
cp helpdesk.md.example helpdesk.md          # then replace every [[PLACEHOLDER]]
grep -n '\[\[' helpdesk.md                  # should print nothing
cp -r tasks.example /root/perch/tasks       # <workdir>/tasks
```

```yaml
ai_agent:
  workdir: /root/perch
  append_system_prompt: /root/perch/helpdesk.md
```

The file is sent to the agent **verbatim**. Two consequences worth internalising:

- **Fill it in.** perch WARNs at startup if the loaded prompt still contains
  `[[PLACEHOLDER]]` slots or the example's setup comment, because an unedited
  template tells the agent to escalate to a literal `[[ESCALATION CONTACT]]`.
  Fix the file and the next email clears the warning — no restart.
- **Keep it outside the agent's workdir.** Inside `workdir`, the SAFETY
  PROTOCOL treats the file as freely writable, so the agent can rewrite its own
  role definition. `~/.perch/helpdesk.md` or `/etc/perch/helpdesk.md` stays
  readable (read-only access anywhere is always allowed) without being editable.

Keep the five task-file headings the role definition tells the agent to rely on.

Keep the role short, and don't restate the per-email contracts in it (reply
format, language, safety, grounding) — perch already sends those, and a role
that contradicts them is a fight the model has to pick a side in.

## 🪝 Script hook

`hooks.on_email` points at a script perch runs for **every inbound email it
accepts**, just before the agent runs. It's the seam for side effects perch
itself doesn't do — record every new thread, notify a phone, feed a metric.

```yaml
# perch.yaml
hooks:
  on_email: /home/agent/record.sh
  timeout: 30s
```

The script is called with five positional arguments:

| Arg | Value |
|---|---|
| `$1` | `email_id` — the `Message-Id` header (e.g. `<abc@163.com>`), assigned by the sender's mail provider. **Empty string** when the email carries none — the argument is still passed, so `$2`…`$5` never shift. |
| `$2` | `subject` |
| `$3` | `body` — `text/plain`, as received (before quote-stripping), truncated at 64 KiB |
| `$4` | `sender` — lowercased address, e.g. `alice@163.com` |
| `$5` | `new_thread` if this email opened a thread perch had no session for, else `reply_thread` |

```bash
#!/usr/bin/env bash
# record.sh — append one line per new thread
[ "$5" = new_thread ] || exit 0
printf '%s\t%s\t%s\n' "$(date -Is)" "$4" "$2" >> "$HOME/perch-threads.tsv"
```

```bash
chmod +x /home/agent/record.sh
```

Contract:

- **Observer, never a gate.** A missing script, a non-zero exit, or a run that
  outlives `hooks.timeout` is logged at WARN and the email is handled exactly
  as it would have been anyway. The hook cannot stop perch from replying.
- **Trusted senders only.** It fires *after* `allow_from` and the loop guards,
  so mail perch drops never reaches your script.
- Runs synchronously with `cwd` = `ai_agent.workdir`, inheriting perch's
  environment. Keep it quick — the email behind it waits.

### Example: log every email to a Lark (Feishu) Bitable

[`scripts/hooks/lark_bitable.py`](scripts/hooks/lark_bitable.py) appends one
row per email (`SendTime` / `Subject` / `Content` / `TicketNo` / `sender`) to a
Bitable table. Python 3 stdlib only — nothing to install on the perch host.

```bash
# credentials for your Lark custom app (never committed; .gitignore covers .env)
cat > .env <<'EOF'
APP_ID=cli_xxxxxxxx
APP_SECRET=xxxxxxxx
EOF
```

```yaml
# perch.yaml — both ids come from the Bitable URL:
#   https://<host>/base/<APP_TOKEN>?table=<TABLE_ID>&view=...
hooks:
  on_email: /path/to/perch/scripts/hooks/lark_bitable.py
```

Set `LARK_APP_TOKEN` / `LARK_TABLE_ID` for your own table, and
`LARK_ONLY_NEW_THREADS=1` to record only the first email of each thread. The
script's docstring lists the rest (`LARK_BASE_URL` for a Lark-global tenant,
`LARK_MAX_CONTENT`, `LARK_TIMEOUT`, `LARK_USE_ENV_PROXY`, …). Test it without
any email traffic by calling it the way perch does:

```bash
./scripts/hooks/lark_bitable.py "<t1@163.com>" "test subject" "body text" alice@163.com new_thread
```

## 🚀 Run

```bash
AGENT_EMAIL=agent@163.com \
AGENT_AUTH_CODE=xxxxxxxx \
ALLOW_FROM=alice@163.com \
./perch
```

Or keep non-secret defaults in `perch.yaml` and inject only credentials via env:

```yaml
# perch.yaml
poll_interval: 5s
allow_from:
  - alice@163.com
  - s".+@(foo|bar)\.example\.com"      # regex: any user on foo/bar .example.com
ai_agent:
  workdir: /home/agent/workspace
```

```bash
AGENT_EMAIL=agent@163.com AGENT_AUTH_CODE=xxxxxxxx ./perch
```

### First-run wizard vs unattended restart

On the very first run, if stdin is a TTY and any required field is missing, perch
runs an interactive wizard, writes non-secret fields (provider, agent, workdir,
permission_mode, allow_from, email) to `~/.perch/perch.yaml` (mode 0600),
and prompts for the auth code via `ReadPassword` (never echoed, never persisted).

From then on, restarting is just:

```bash
AGENT_AUTH_CODE=xxxxxxxx ./perch    # everything else is in the YAML
```

For systemd / launchd / cron, put the secrets in an env file (mode 0600) and
point the unit at it — perch will read the YAML for the rest:

```ini
# /etc/systemd/system/perch.service
[Service]
EnvironmentFile=/etc/perch/env
ExecStart=/usr/local/bin/perch
Restart=on-failure
```

```bash
# /etc/perch/env (chmod 0600, chown root:root)
AGENT_EMAIL=agent@163.com
AGENT_AUTH_CODE=xxxxxxxx
```

Without `AGENT_AUTH_CODE` set, a non-interactive run exits with a hint pointing
back at the wizard or the env file.

### Running detached (`--daemon`)

If you don't want to write a systemd/launchd unit, `./perch --daemon`
re-execs itself as a detached process: it breaks the link to your
controlling terminal (so closing the SSH session won't SIGHUP it) and
writes its PID to `~/.perch/perch.pid`. To stop it, send SIGTERM to
the recorded PID (or `pkill -TERM perch`). The pidfile is removed on
clean shutdown; a stale pidfile is detected and cleaned up on the next
launch.

```bash
./perch --daemon           # alias: -D
kill $(cat ~/.perch/perch.pid)
```

In daemon mode, all post-handoff logs go to `/dev/null` — there is no
log file yet. A log-file option with rotation is on the roadmap.

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
