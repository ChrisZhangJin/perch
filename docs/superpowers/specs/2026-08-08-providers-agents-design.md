# Built-in email providers and AI agents

**Status:** draft
**Date:** 2026-08-08
**Author:** brainstorming session with the project owner

## Motivation

Perch today treats the email transport and the AI agent as two free-form
strings in `perch.yaml` (or env vars): `imap_addr` / `smtp_addr` /
`agent_bin`. That works for one provider and one agent but every new
deployment pays the same discovery tax — copying the right endpoints
from a README, picking an implicit-TLS port, deciding whether IMAP ID
is required, etc.

This change:

1. Names the supported providers and agents explicitly (so `perch.yaml`
   reads `name: 163`, not `imap_addr: imap.163.com:993`).
2. Bakes the per-provider transport quirks (IMAP ID requirement, IDLE
   support, SMTP/IMAP endpoints) into a built-in registry so they
   cannot be misconfigured.
3. Prompts the user interactively on first run when a required field is
   missing — and stops prompting once a config file exists.
4. Keeps env-var compatibility for the existing `AGENT_EMAIL` /
   `AGENT_AUTH_CODE` / `ALLOW_FROM` / `*_INTERVAL` / `*_TIMEOUT` /
   `MAX_*` family so current deployments do not break.

Out of scope: Gmail (OAuth 2.0 + App Password complexity deserves a
separate design pass). 163 / 126 / QQ are the MVP.

## Supported surface

### Email providers (MVP)

| Name  | IMAP                | SMTP                 | Needs IMAP ID | Supports IDLE |
|-------|---------------------|----------------------|---------------|---------------|
| `163` | `imap.163.com:993`  | `smtp.163.com:465`   | yes            | no             |
| `126` | `imap.126.com:993`  | `smtp.126.com:465`   | yes            | no             |
| `qq`  | `imap.qq.com:993`   | `smtp.qq.com:465`    | yes            | yes            |

**Open question (non-blocking):** 163 and 126 are sister products under
NetEase. They share the authentication flow (authorization code, not
password) and the IMAP ID requirement. Whether 126 differs in any way
that affects `IMAPClient.Login` semantics or SMTP relay behaviour
remains to be verified by an actual 126 mailbox test before the
implementation lands. The MVP does not depend on the answer.

### AI agents

| Name     | Binary   | Session / resume flags                              |
|----------|----------|------------------------------------------------------|
| `claude` | `claude` | `--session-id <uuid>` (new) / `--resume <uuid>` (resume) |
| `nanopi` | `nanopi` | `--session <uuid>` (resume) — `-c` / `--continue` is "most recent for cwd" and not a substitute |
| `pi`     | `pi`     | **to be verified** against the Pi monorepo before implementation |

The agent table is open for new adapters later, but the MVP does not
ship a "custom binary" escape hatch.

## Config schema

New YAML:

```yaml
email_provider:
  name: 163           # 163 | 126 | qq
  # name: 126
  # name: qq

ai_agent:
  name: claude        # claude | nanopi | pi
  workdir: .          # cwd for the spawned agent (default: .)
  permission_mode: acceptEdits   # claude only; ignored by others

allow_from:
  - alice@163.com
  - s".+@trusted\\.org"

poll_interval: 60s
task_timeout: 30m
max_prompt_bytes: 65536
max_attachment_bytes: 52428800
tls_insecure_skip_verify: false

# session_store is unchanged (default: $TMPDIR/perch-sessions.json)
```

### Backward compatibility

| Old key               | Status                                                                 |
|-----------------------|------------------------------------------------------------------------|
| `imap_addr`           | **removed.**  derived from `email_provider.name`.                     |
| `smtp_addr`           | **removed.**  derived from `email_provider.name`.                     |
| `agent_bin`           | **removed.**  derived from `ai_agent.name`.                            |
| `claude_workdir`      | moved to `ai_agent.workdir`.                                            |
| `claude_permission_mode` | moved to `ai_agent.permission_mode`.                                  |
| `allow_from`          | unchanged.                                                              |
| `poll_interval`, `task_timeout`, `max_prompt_bytes`, `max_attachment_bytes`, `session_store`, `tls_insecure_skip_verify` | unchanged.          |
| env: `AGENT_EMAIL`, `AGENT_AUTH_CODE`, `ALLOW_FROM`, `POLL_INTERVAL`, `TASK_TIMEOUT`, `MAX_PROMPT_BYTES`, `MAX_ATTACHMENT_BYTES`, `SESSION_STORE`, `TLS_INSECURE_SKIP_VERIFY` | unchanged. |
| env: `IMAP_ADDR`, `SMTP_ADDR`, `CLAUDE_BIN`, `CLAUDE_WORKDIR`, `CLAUDE_PERMISSION_MODE` | **removed.**         |

Old-style YAML files containing `imap_addr` / `smtp_addr` / `claude_bin`
get a one-line `WARN: imap_addr is deprecated; remove it and use
email_provider.name instead` log and the fields are ignored. The user
fixes the file at their leisure; perch keeps working until they do.

## Architecture

### New packages

```
internal/provider/
    registry.go        # Provider struct, providers map, Lookup(name)
    registry_test.go   # one test per provider, asserts endpoints + flags
internal/agent/
    registry.go        # Agent struct, agents map, Lookup(name)
    adapters.go        # BuildArgs for claude, nanopi, pi
    registry_test.go   # one test per agent, asserts arg vectors
internal/setup/
    wizard.go          # interactive + non-interactive prompt paths
    wizard_test.go     # uses os.Pipe to feed stdin
```

### Provider type

```go
type Provider struct {
    Name         string
    IMAPAddr     string
    SMTPAddr     string
    NeedsIMAPID  bool   // 163/126/QQ all require IMAP ID
    SupportsIDLE bool   // 163/126 do not
}
```

### Agent type + adapter

```go
type Args struct {
    Prompt    string
    SessionID string
    IsNew     bool   // true => create; false => resume
    Workdir   string
    PermMode  string // claude only; ignored by nanopi/pi
}

type Agent struct {
    Name      string
    Binary    string
    BuildArgs func(Args) []string
}
```

Adapter table:

| Agent    | BuildArgs (new)                                                                 | BuildArgs (resume)                                                              |
|----------|----------------------------------------------------------------------------------|----------------------------------------------------------------------------------|
| `claude` | `["-p", prompt, "--output-format", "text", "--permission-mode", perm, "--session-id", sid]` | `["-p", prompt, "--output-format", "text", "--permission-mode", perm, "--resume", sid]` |
| `nanopi` | `["-p", prompt, "--yolo", "--output", "text", "--session", sid]`                | same                                                                            |
| `pi`     | **TBD** — verify against the Pi monorepo source before implementation           | **TBD**                                                                          |

`nanopi` does not distinguish new vs resume (both use `--session
<sid>`); the `IsNew` flag is ignored. `claude` uses `--session-id`
only on first run and `--resume` thereafter — preserves today's
behaviour exactly.

### Config refactor

`internal/config/config.go` keeps the layering (env > yaml > defaults)
but the **fields** change:

- **Removed:** `IMAPAddr`, `SMTPAddr`, `ClaudeBin`, `ClaudeWorkdir`, `ClaudePermMode`.
- **Added:** `ProviderName string`, `AgentName string`, `AgentWorkdir string`, `AgentPermMode string`.

Endpoint resolution happens **after** config load, in `cmd/perch/main.go` (or
a thin helper in `internal/provider`), so config remains pure data:

```go
p, err := provider.Lookup(cfg.ProviderName) // returns (Provider, error)
if err != nil { ... }

a, err := agent.Lookup(cfg.AgentName)       // returns (Agent, error)
if err != nil { ... }
```

The mailbox dials `p.IMAPAddr`; the replier sends via `p.SMTPAddr`. The
runner builds args via `a.BuildArgs(...)`.

`mailbox.Dial` keeps its current behaviour; the only change is that it
now receives an `IMAPAddr` that came from the provider registry rather
than from config. No internal change required.

## Interactive first-run wizard

### Trigger

`cmd/perch/main.go` calls a new `setup.Ensure(cfg)` after
`config.Load`. `Ensure` looks at the fields below and decides whether
to enter the wizard:

| Field            | Where it's read                |
|------------------|---------------------------------|
| `ProviderName`   | env > yaml > defaults           |
| `AgentName`      | env > yaml > defaults           |
| `AgentWorkdir`   | env > yaml > defaults (default ".") |
| `AgentPermMode`  | env > yaml > defaults (default "acceptEdits") |
| `Email`          | env only (`AGENT_EMAIL`)        |
| `AuthCode`       | env only (`AGENT_AUTH_CODE`)    |
| `AllowFrom`      | env > yaml                      |

Defaults: `ProviderName="163"`, `AgentName="claude"` — but the wizard
**never assumes** the defaults are accepted; it prompts anyway for any
field the user might want to change. (Empty defaults fail loudly,
matching today's behaviour.)

### Decision tree

```
Ensure(cfg)
├── all fields present AND cfg source is not just defaults
│     └── return nil (silent boot)
├── stdin is TTY
│     └── run wizard, persist non-secrets to ~/.config/perch/perch.yaml
│         return nil
└── stdin not TTY
      └── print "missing fields: X, Y; to run interactively: ./perch;
              or pre-fill via env or ~/.config/perch/perch.yaml"
          return ErrMissingFields
```

`cfg source is not just defaults` means at least one of the fields
came from env or yaml — prevents the wizard from firing on every boot
just because defaults are present.

### Wizard UX

```
Welcome to perch.
Press Enter to accept the default shown in [brackets]; type to override.

Email provider: [163] (163 / 126 / qq)
AI agent:       [claude] (claude / nanopi / pi)
Agent workdir:  [/home/you/agent-workspace] (.)
Allow senders:  [alice@163.com, s".+@trusted\\.org"] (empty = deny all)
Agent email:    [] (env var AGENT_EMAIL; can't write to disk)

Mailbox authorization code (env var AGENT_AUTH_CODE; not written to disk):
Password:
```

- Plain inputs use `fmt.Fscanln` over an `io.Reader` we wrap to default
  to the bracketed value when the user hits Enter.
- The authorization code uses `golang.org/x/term.ReadPassword` so it
  does not echo.
- After the wizard the non-secret fields are written to
  `~/.config/perch/perch.yaml` with `0600` mode. The file includes a
  header comment listing the env vars that should still hold the
  secrets.

### Non-interactive failure mode

```
$ ./perch < /dev/null
err: missing required fields: provider.name, agent.email, agent.authcode
hint: run interactively (./perch) or pre-fill via:
        AGENT_EMAIL=agent@163.com AGENT_AUTH_CODE=xxxxxxxx ./perch
        or edit ~/.config/perch/perch.yaml
exit 2
```

## Migration & deprecation

1. On startup, if YAML contains any of `imap_addr`, `smtp_addr`,
   `agent_bin`, `claude_workdir`, `claude_permission_mode`, log a
   single `WARN` line listing the deprecated keys and their
   replacements. Perch keeps running — the keys are simply ignored.
2. `perch.yaml.example` is rewritten to the new schema.
3. README's config table is updated.
4. No automatic one-time migration; users fix their files at their
   pace.

## Testing strategy

### `internal/provider/registry_test.go`

- `TestProviderTableConsistent` — every provider's name equals its
  map key.
- `TestProviderEndpoints` — table-driven; for each provider, assert
  IMAP/SMTP addresses, `NeedsIMAPID`, `SupportsIDLE`.
- `TestProviderLookupUnknown` — unknown name returns a non-nil error
  listing valid names.

### `internal/agent/registry_test.go`

- `TestAgentBuildArgs_ClaudeNew` / `TestAgentBuildArgs_ClaudeResume`
  — assert the exact flag vector against today's behaviour (no
  regression).
- `TestAgentBuildArgs_Nanopi` — assert prompt + `--yolo` +
  `--session` regardless of IsNew.
- `TestAgentBuildArgs_Pi` — placeholder with TODO; skips until the Pi
  flag shape is verified.
- `TestAgentLookupUnknown`.

### `internal/setup/wizard_test.go`

- `TestEnsureSilentWhenComplete` — env + yaml fully populated ⇒ no
  stdin read, no error.
- `TestEnsureInteractiveHappyPath` — pipe a known-good script of
  answers into stdin (using a `bytes.Buffer` + `os.Pipe` swap) and
  assert the resulting config matches the answers.
- `TestEnsureInteractiveAuthcodeNotEchoed` — assert that the
  password prompt reads via `ReadPassword`, not `Fscanln`.
- `TestEnsureNonInteractiveFailsLoud` — pipe `< /dev/null`; assert
  exit-style error containing each missing field name and the
  one-line hint.

### Integration (optional, time permitting)

- A new `scripts/localtest/demo.sh provider` subcommand that runs the
  wizard against GreenMail with a piped answer script. Verifies the
  full path.

## Open questions / follow-ups

1. **126 vs 163 transport differences** — confirm by manual test on a
   126 mailbox before the v0.3.0 release. Not blocking the MVP.
2. **Pi CLI flag verification** — read `/root/workspace/pi/packages/coding-agent/src/cli.ts`
   (or run `pi --help` once it's installed) to confirm Pi's session
   flag shape; record the answer in
   `internal/agent/adapters.go`. The placeholder will be `panic("pi
   adapter not implemented yet")` so the code fails loud rather than
   silent.
3. **Gmail** — separate design pass covering OAuth 2.0 (XOAUTH2) and
   App Password. Out of scope here.
4. **Custom provider / agent escape hatch** — not in MVP. If users
   need it later, add `email_provider: { name: custom, imap_addr: X,
   smtp_addr: Y }` with a guarded validator; today, an unknown name
   is an error.

## Rollout

1. Land the registry + config refactor in one commit (tests first).
2. Land the wizard in a second commit (setup package + tests).
3. Update `perch.yaml.example` + README in a third commit.
4. Tag `v0.3.0`.