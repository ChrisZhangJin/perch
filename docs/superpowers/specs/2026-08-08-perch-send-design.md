# `perch-send` — one-shot CLI for agent-initiated outbound mail

**Status:** draft
**Date:** 2026-08-08
**Author:** brainstorming session with the project owner (follows the providers-and-agents plan of the same date)

## Motivation

Today perch is a **passive** daemon: it polls/IDLEs an IMAP mailbox, hands
each new message to an AI agent, and replies on the same thread. There is
no way for the agent — or any other automation calling perch as a tool —
to **start a new email thread** on its own initiative.

Workflows that need perch as a notification channel (CI reporting a build
result, a long-running agent sending an end-of-day summary, a cron job
firing an alert) currently have no path through perch. They either skip
perch entirely and shell out to `sendmail`, or build their own SMTP
plumbing.

This change adds a separate `perch-send` binary that loads the same
config the daemon uses and sends one outbound email over SMTP, then
exits. It is fire-and-forget from the caller's perspective — no queue,
no socket, no mailbox watcher.

## Use cases

1. CI pipeline: `perch-send --to dev@x --subject "build #${BUILD}" --body @build.log --reason "build finished"` after a job step.
2. Cron / systemd timer: `perch-send --to ops@x --subject "..." --body @status.json` on a schedule.
3. Inside an `agent -p` invocation: agent writes its report to a temp path, then shells out to `perch-send --to <reporter> --body @report.md`.

The sender is always `cfg.Email` (the same mailbox the daemon watches).
The recipient is one or more `--to` addresses.

## Non-goals

- **No inbox / mailbox side at all.** `perch-send` does not connect to IMAP. It only opens an outbound SMTP session and closes.
- **No queueing or retry orchestration.** A single `perch-send` invocation makes one send attempt with the same retry/backoff the daemon's reply path uses (3 attempts, 1s base). If you want bulk, call `perch-send` in a loop from your workflow.
- **No rate-limit table in perch.** 163 / 126 / qq throttle at the provider; the assumption is "provider has its own limit".
- **No reply-all / bounce handling.** One outbound message per invocation; no automatic follow-up.
- **No templating / Markdown rendering.** The body is sent as `text/plain` exactly as given.
- **No MCP / socket / RPC.** A separate process, fire-and-forget.

## Surface

```
perch-send --to <addr> [--to <addr> ...] \
           --subject <text> \
           --body "literal text" | --body @path/to/file \
           [--attach <path> ...] \
           [--reason "why I'm sending this"]

  -to         recipient (repeatable for multiple). All must pass
              allow_send_to. Loop block: --to cfg.Email is rejected.
  -subject    subject line. Required.
  -body       literal text or @<file> (reads up to MAX_PROMPT_BYTES).
  -attach     file path (repeatable). Per-file cap: MAX_ATTACHMENT_BYTES.
  -reason     audit string. Landed in `X-Perch-Reason:` header and in the
              slog log line. Recommended for agent-initiated sends.

  exit 0   sent (SMTP 250)
  exit 1   send failed after retries
  exit 2   config/auth missing (no TTY → ErrMissingFields-style hint)
```

The binary is built from a new `cmd/perch-send/main.go` and shares every
internal package the daemon already uses: `internal/config`,
`internal/provider`, `internal/replier`. It does **not** import
`internal/mailbox`, `internal/runner`, `internal/agent`, `internal/app`,
`internal/setup`, `internal/session`. Result: small binary, fast startup,
zero mailbox / agent / wizard dependencies.

## Wire format

- New `Message-ID:` header, generated server-side (random hex +
  `@<provider-domain>`).
- No `In-Reply-To:` / `References:` headers — this is a brand-new thread.
- `X-Perch-Reason: <reason>` if `--reason` is supplied.
- `X-Perch-Sender: perch-send/<version>` constant.
- Existing `Compose` / `ComposeWithAttachments` already support omitting
  threading headers by passing empty `inReplyTo` and `nil` references.
  `perch-send` adds Message-ID + X-Perch-Reason and otherwise reuses the
  same wire code.

## Config additions

| Field          | YAML key      | Env var          | Default | Notes |
|----------------|---------------|------------------|---------|-------|
| `AllowSendTo`  | `allow_send_to` | `ALLOW_SEND_TO` | `[]`    | list/regex allow-list. **Empty = deny all outbound.** Distinct from `allow_from` so the two policies can diverge. |

`allow_send_to` accepts the same literal/regex syntax as `allow_from`
(literal address or `s"..."` regex). Regex compilation failures fail
the binary immediately — same fail-closed posture as `allow_from`.

Loop block: `--to` values are checked against `cfg.Email` after
lower-casing; a match returns `exit 1` with a clear message.

## Audit log

Every send produces one INFO line:

```
INFO  send  to=<recipients> subject=<...> size=<bytes> reason=<reason or "-"> session=<n/a>
```

`session` is `n/a` because `perch-send` does not load sessions. If
the user wants per-agent tracking they can encode the agent / workflow
identity in `--reason`.

## Failure semantics

- SMTP error after 3 attempts → `exit 1`, stderr carries the last error.
- Recipient not in `allow_send_to` → `exit 1`, no SMTP session opened.
- `--to cfg.Email` → `exit 1`, no SMTP session opened.
- Config / auth missing → `exit 2`, hint to run `perch` interactively first or set env vars.
- Attachment file missing or oversized → `exit 1` before opening SMTP.

## Architecture

```
cmd/perch-send/
    main.go              # CLI flag parse + dispatch (no subcommand layer; this IS the only mode)

internal/replier/
    compose_new.go       # ComposeNew(fromAddr, to []string, subject, reason, body, attachments) ([]byte, error)
    compose_new_test.go  # asserts: fresh Message-ID, no In-Reply-To/References, X-Perch-Reason present/absent

internal/config/
    config.go            # +AllowSendTo on Config, +allow_send_to yaml field, +ALLOW_SEND_TO env var,
                         # +allowSendTo validation at Load (regex compile, fail closed)
    config_test.go       # +test for env override, +test for regex compile failure
    deprecation.go       # (unchanged — no deprecated keys here)
```

`cmd/perch-send/main.go` (~80 lines) wires in this exact order:

1. `flag.Parse()` — see Surface above.
2. `cfg, err := config.Load(*configPath)` — same search path as the daemon.
3. Validate `--to` against `cfg.AllowSendTo` and `cfg.Email`.
4. Read `--body` (literal or `@file`).
5. Validate each `--attach` exists + under `MAX_ATTACHMENT_BYTES`.
6. `p, _ := provider.Lookup(cfg.ProviderName)`.
7. `r := replier.New(cfg, p.SMTPAddr)`.
8. `msg, _ := replier.ComposeNew(cfg.Email, toAddrs, subject, reason, body, attachments)`.
9. `err := r.SendRaw(msg)`.
10. Log + exit.

`r.SendRaw` is a thin new method on `*Replier` that takes pre-composed
bytes instead of (to, subject, body) — avoids re-running the existing
`Reply` plumbing which assumes threading headers. It still uses the
same retry / backoff / `defaultDial` / `dial` injection points the tests
already rely on.

## Backward compatibility

None of the existing YAML keys change behavior. `allow_send_to` is new
and defaults to `[]`, which means **a freshly-upgraded perch that does
not set it will reject all `perch-send` calls** with a clear error
("no recipients in allow_send_to; add yours to perch.yaml"). This is
intentional — outbound mail is a new capability, fail-closed by
default.

`perch` (the daemon) is unchanged. It does not gain a `send`
subcommand.

## Tech stack

No new third-party deps. Same Go 1.25 / `gopkg.in/yaml.v3` /
`golang.org/x/term` set as today.