# Design: `internal/mailtest` — fast in-process email fakes for perch

Date: 2026-08-23
Status: draft

## Purpose

Fast in-process testing of perch's email-driven agent flow **without** IMAP,
SMTP, Docker, or a network. Today this is impossible outside `app_test.go`'s
private fakes; this spec promotes those fakes into a reusable package so any
test (and any future developer tool) can drop a synthetic message into a
perch-shaped inbox and observe what perch would do.

## Non-goals

- Real wire-protocol (IMAP/SMTP). The mailbox/replier code is **not** touched.
- Replacing `scripts/localtest/demo.sh` (GreenMail remains the Level 1 path).
- Any production code change. `internal/mailtest` is a `internal/` testutil
  package; nothing under `internal/app`, `internal/mailbox`, `internal/replier`,
  or `cmd/perch` moves.
- A standalone CLI. The user explicitly chose library-only — a `cmd/mocksend`
  binary is a separate ergonomic question, deferred until someone wants it.
- New behavioral coverage of perch. The point is to **reuse** what we already
  test, faster.

## Architecture

One new Go package: `internal/mailtest`. Three structs and one harness,
lifted verbatim from `internal/app/app_test.go`:

```
                       internal/mailtest
   +--------------------------------------------------+
   | FakeMailbox     satisfies mailbox.Mailbox        |
   | FakeSender      satisfies app.ReplySender        |
   | ScriptedRunner  satisfies app.TaskRunner         |
   | Mailtest        wires the three into an *app.App |
   +--------------------------------------------------+
                          ^      ^
                          |      |
              imported by |      | imported by
                          |      |
        internal/mailtest/fakes_test.go     internal/app/app_test.go
        (new: fakes + harness unit tests)   (refactored: keeps wiring smoke test)
```

The `Mailtest` harness is the user-facing surface. Tests build one with
`mailtest.New`, call `Send` to queue messages, `RunOnce` to drive
`app.ProcessUnseen`, and read `Replies` / `SeenUIDs` to assert on what perch
did. The package owns its own self-tests; `app_test.go` keeps one wiring smoke
test that confirms the fakes still plug into `app.App`.

## Components

### `internal/mailtest/fakes.go`

Three structs, each ~10–30 lines, lifted verbatim from `internal/app/app_test.go`
(they are already correct and tested):

- **`FakeMailbox`** — `mu sync.Mutex`, `msgs []mailbox.Raw`, `seen []uint32`,
  `nextUID uint32`. `FetchUnseen` drains `msgs` and returns; `MarkSeen`
  appends to `seen`; `Close` is a no-op. UID auto-assigns on `Send`.
- **`FakeSender`** — `mu sync.Mutex`, `replies []Reply`, `to []string`,
  `subjects []string`, `files [][]string`. `Reply` records into all four.
- **`ScriptedRunner`** — `outs []string` (canned stdout per call),
  `prompts []string`, `sids []string`, `isNews []bool` (recorded). `Run`
  returns `outs[i]` for call `i`; overflow reuses the last entry (defensive
  against miscount assertions). Also has a settable `Native string` field —
  when non-empty, returned as the second value (`nativeID`) of `Run`. This
  preserves the existing `TestProcessAdoptsNativeSessionID` coverage.

### `internal/mailtest/mailtest.go`

The user-facing harness. Public API:

```go
package mailtest

import (
    "context"
    "github.com/ChrisZhangJin/perch/internal/app"
    "github.com/ChrisZhangJin/perch/internal/config"
)

// Reply captures one outbound reply recorded by FakeSender.
type Reply struct {
    To, Subject, InReplyTo string
    References             []string
    Body                   string
    Attachments            []string
}

// Mailtest is an end-to-end perch harness with no network. Construct with New,
// drive with Send + RunOnce, observe with Replies / SeenUIDs. Safe for
// sequential use from a single goroutine (the same constraint app.App has).
type Mailtest struct { /* unexported */ }

// New builds a Mailtest wired to a real *app.App backed by the fakes. cfg is
// used as-is (callers typically pass &config.Config{MaxPromptBytes: …,
// AgentWorkdir: t.TempDir()}). allowFrom is forwarded to gate.New; pass ["*"]
// for allow-all or a literal/regex slice for production-equivalent gating.
// run may be nil → a ScriptedRunner that always returns ("the answer", "", nil).
func New(cfg *config.Config, allowFrom []string, run app.TaskRunner) (*Mailtest, error)

// Send composes a minimal RFC822 message (From / To / Subject / Message-ID /
// Content-Type: text/plain; charset=utf-8) and queues it into the FakeMailbox.
// UID is auto-assigned (monotonic, starts at 1). Returns the UID. from must be
// a valid email address (no whitespace, contains "@"); empty from / to errors.
// Multipart / attachment messages use SendRaw.
func (m *Mailtest) Send(from, to, subject, body string) (uint32, error)

// SendRaw queues pre-built RFC822 bytes under the given UID (must be > 0;
// UID 0 is reserved for "unassigned"). The harness does NOT enforce monotonic
// ordering across SendRaw calls — callers that mix Send (auto-assigns) and
// SendRaw (caller-chosen) are responsible for choosing UIDs that don't
// collide with auto-assigned ones. Use this for attachments, unusual
// Content-Types, or to reuse canned .eml fixtures from tests.
func (m *Mailtest) SendRaw(uid uint32, raw []byte) error

// RunOnce calls app.ProcessUnseen once. ctx-cancel aware. Errors propagate
// as-is (the same error contract app.App exposes today).
func (m *Mailtest) RunOnce(ctx context.Context) error

// Replies returns every outbound reply recorded so far, in send-order.
// Snapshot — later RunOnce calls won't retroactively change the returned slice.
func (m *Mailtest) Replies() []Reply

// SeenUIDs returns every UID passed to MarkSeen, in append-order. Includes
// both whitelisted and rejected sends (perch marks rejected messages seen too).
func (m *Mailtest) SeenUIDs() []uint32

// App exposes the underlying *app.App for tests that need to flip config
// fields (e.g. cfg.LongTaskAck = true) after construction. Read-only-by-
// convention — direct mutation of app state outside cfg is not supported.
func (m *Mailtest) App() *app.App
```

The Message-ID minted by `Send` is `<mtest-<sequence>@mailtest>` to keep it
unique and obviously synthetic. Tests that need a specific Message-ID use
`SendRaw`.

### `internal/mailtest/fakes_test.go`

New file. Owns the four behavioral tests that today live in `app_test.go`,
plus tests of the new `Mailtest` harness:

1. `TestFakeMailboxDrainsOnFetch` — `Send` then `FetchUnseen` returns the
   queue; second `FetchUnseen` returns empty.
2. `TestFakeSenderRecordsInOrder` — two `Reply` calls produce two `Reply`
   entries in order, with all fields preserved.
3. `TestScriptedRunnerReturnsPerCall` — three calls with three canned outs
   return them in order; fourth call reuses the last (defensive overflow).
4. `TestMailtestEndToEnd` — `New(cfg, ["alice@x"], nil)`,
   `Send("alice@x", "agent@x", "hi", "do the thing")`,
   `RunOnce(ctx)`, `Replies()` returns exactly one reply with body "the
   answer" (the default scripted stdout).
5. `TestMailtestRejectsNonWhitelisted` — same as above with `from: mallory@x`
   outside the whitelist → `Replies()` empty, `SeenUIDs()` non-empty.
6. `TestMailtestSendRawHonorsProvidedUID` — `SendRaw(42, …)` produces UID 42
   in `SeenUIDs` after `RunOnce`.

### `internal/app/app_test.go`

Refactored. The four private structs (`fakeMailbox`, `fakeRunner`,
`fakeSender`, `scriptedRunner`) and their methods are **deleted** — they
become `mailtest.FakeMailbox` / `FakeSender` / `ScriptedRunner` and live in
the new package. The four behavior tests stay in this file, reworked to
construct a `*mailtest.Mailtest` and call `Send` / `RunOnce` / `Replies` /
`SeenUIDs` instead of poking the fake fields directly. One small new smoke
test (`TestAppWiringWithMailtest`) confirms that `mailtest.New` returns an
`*app.App` whose `ProcessUnseen` is the same loop as before (no regression in
the integration seam).

This is a behavior-preserving refactor: no test assertions change in meaning,
only the wiring.

## Data flow

```
test code
   |
   | mailtest.New(cfg, allowFrom, run?)
   v
   Mailtest{
     mb:    *FakeMailbox,    // satisfies mailbox.Mailbox
     run:   *ScriptedRunner, // satisfies app.TaskRunner
     rep:   *FakeSender,     // satisfies app.ReplySender
     app:   *app.App,        // real perch pipeline
   }
   |
   | Send(from, to, subject, body)
   |   -> composes RFC822 bytes
   |   -> FakeMailbox.msgs = append(..., Raw{UID:nextUID, Data:bytes})
   |
   | RunOnce(ctx)
   |   -> app.App.ProcessUnseen(ctx)
   |        -> mb.FetchUnseen(ctx)        // FakeMailbox drains msgs
   |        -> parse -> dedup -> gate.Allowed
   |        -> sess.Resolve(thread root)
   |        -> run.Run(ctx, prompt, sid, isNew)   // ScriptedRunner returns canned
   |        -> rep.Reply(...)            // FakeSender records
   |        -> mb.MarkSeen(ctx, uid)
   |
   | Replies()   -> []Reply (snapshot of FakeSender.replies)
   | SeenUIDs()  -> []uint32 (snapshot of FakeMailbox.seen)
```

## Error handling

- `New` errors if `gate.New` fails (bad regex entry) — same contract as today.
- `Send` errors only on malformed input: empty `from` or `to`, or `from` that
  fails a simple "contains @" + no-whitespace check. The actual RFC822 is
  built by hand from the four fields; no MIME parsing involved.
- `SendRaw` errors only if `uid == 0` (UID 0 is reserved for "unassigned").
- `RunOnce` propagates whatever `app.ProcessUnseen` returns. No wrapping, no
  retry, no timeout — the harness is a thin wrapper. Tests that need richer
  error semantics wrap `RunOnce` themselves.
- `Replies` and `SeenUIDs` never error. They are read-only snapshots.

## Testing

- `internal/mailtest/fakes_test.go` — six tests as enumerated above. Runs as
  part of `make test`. Target runtime: <100 ms total (in-process, no I/O).
- `internal/app/app_test.go` — same behavioral coverage as today (whitelisted
  gets reply, non-whitelisted dropped, dedup skips second time, adopts
  native session id, long-task ack on/off/short, degenerate-reply retry path),
  reworked to use `mailtest.Mailtest`. **No assertion changes**, only the
  wiring. One new smoke test for the wiring.
- No CI / Level 1 / Level 2 changes. `make test`, `make vet`, `make build`
  must remain green.

## Files touched

| File | Change |
|---|---|
| `internal/mailtest/fakes.go` | NEW — three struct definitions lifted from `app_test.go` |
| `internal/mailtest/mailtest.go` | NEW — `Mailtest` harness + public API |
| `internal/mailtest/fakes_test.go` | NEW — six tests of the fakes + harness |
| `internal/app/app_test.go` | EDIT — delete the four private fakes, rework the four behavior tests to import `mailtest`, add one wiring smoke test |

No other file changes. No new binaries, no Makefile edits, no YAML / config
changes, no doc changes outside this spec.

## Why this is the right shape

- **Smallest possible change that achieves the purpose.** Three new files plus
  one refactored file. No new binary, no new protocol, no production-code
  touch.
- **Eliminates duplication.** The four private fakes in `app_test.go` become
  one source of truth under `mailtest`. Future tests in any package get them
  for free.
- **Keeps the wire-protocol escape hatch.** `scripts/localtest/demo.sh`
  (GreenMail + Python sender) is still the right answer for "test the actual
  IMAP/SMTP code." `mailtest` is for "test the perch-shaped pipeline without
  any of that." Two complementary levels.
- **Library-only.** No CLI surface to design, document, version, or maintain.
  If a future user wants `mocksend` on the command line, that's a thin CLI
  wrapper over `mailtest.Mailtest` — maybe 50 lines — but it doesn't have to
  exist now.