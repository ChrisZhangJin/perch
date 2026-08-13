# Inbound forward — agent-directed recipient override on the reply path

**Status:** roadmap (not scheduled)
**Date:** 2026-08-11
**Author:** brainstorming session with the project owner

## Motivation

Today the inbound path is a strict 1-to-1: the message from `m.From`
goes to the agent, and the agent's stdout goes back to `m.From` on the
same thread. The `To` on the outbound envelope is hard-coded in
`internal/app/app.go` to the parsed `From` of the inbound message.

That closes off a category of natural requests where the sender's
intent is *"handle this for me and pass it along to someone else"*:

- *"请转发给 ops@x, 主题写 'DB restart plan'."*
- *"Answer this for me and cc my manager on the reply."*
- *"帮我把这份周报发给 team@x, 不用回给我。"*

The sender is authenticated by `allow_from`; the *content* of what the
agent should emit is under the agent's control; but the *envelope* — who
receives the mail — is not something the agent can influence today.

This spec covers only the inbound path. Agent-initiated outbound with
no inbound trigger is already covered by the separate
[`perch-send`](2026-08-08-perch-send-design.md) spec.

## Scope

**In scope**

- Let the agent, when replying to an inbound message, override the
  recipient list: change the primary `To`, add `Cc`, or both.
- Keep threading intact when the override targets include the original
  sender; drop threading headers when the reply is *only* to third
  parties (it's a new conversation for them).
- Fail-closed policy: an allow-list controls which addresses the agent
  can redirect to, distinct from `allow_from`.
- Audit: every redirect leaves a structured log line with the reason
  the agent gave.

**Out of scope**

- No new binary. `perch-send` remains the tool for cold-start outbound;
  this feature only extends the existing reply path.
- No queue, no delayed send, no scheduled forward.
- No inline MIME forward of the original message with `Fwd:` semantics
  (attachments + full headers). The agent-composed body is what goes
  out; if the human wants an RFC-5322-style forward, the agent can
  paste the relevant excerpts into its body.
- No multi-message workflows ("send A then B"). One inbound message ⇒
  at most one outbound message, same as today.
- No reply-all inference. If the agent wants Cc'd parties on the
  outbound, it names them explicitly.

## Design options

Two shapes were considered. The choice below is **B** (envelope file).
Option A (inline headers) is documented for comparison because it may
resurface if B feels heavy in practice.

### Option A — inline envelope header block

Agent stdout begins with an optional RFC-822-style header block, ended
by a blank line, before the greeting:

```
To: ops@x
Cc: manager@x
X-Perch-Reason: sender asked to forward this to ops

Hi ops,
...
```

*Pros:* zero new files, agent can emit it with a single Write.
*Cons:* collides with the existing "stdout is the email body" contract
and the greeting protocol (`ExtractBodyAfterGreeting`). Parsing has to
peel the header block off before the greeting scan, and any accidental
stray `To:` line at the top of a normal reply becomes a live redirect.
That's an unsafe default.

### Option B — envelope file in the reply dir (chosen)

The reply directory already exists (`<workdir>/reply/`) and is already
scanned for attachments. Add one **reserved filename** in that
directory:

```
<workdir>/reply/.perch-envelope.yaml
```

If present, perch parses it and uses the parsed fields to override the
outbound envelope. If absent, behavior is exactly as today.

```yaml
# All fields optional. Missing fields fall back to today's behavior.
to:                        # replaces m.From as the primary recipient
  - ops@example.com
cc:                        # additional recipients
  - manager@example.com
keep_sender: false         # if true, also include the original m.From
                           # in To (default: false when `to:` is set,
                           # implicitly true when `to:` is empty)
reason: "sender asked to forward to ops"   # required whenever `to:` or
                                           # `cc:` is set — audit trail
subject_prefix: "Fwd: "    # optional; prepended to the existing Subject
                           # (perch de-dupes if Subject already starts
                           # with the prefix)
```

*Pros:*

- Structured, machine-parsed, YAML like the config. No ambiguity with
  the greeting protocol or the stdout-is-body rule.
- Opt-in per reply: the file has to *exist* for the redirect to fire.
  A malformed file fails the send with a clear error, no silent
  redirects.
- Reuses the reply-dir mechanism the agent already knows (that's where
  it stages attachments). No new prompt vocabulary.
- Trivial to test in isolation from SMTP.

*Cons:*

- Agent has to do a file Write in addition to writing stdout. Slightly
  more work than option A.

## Threading semantics

The rule perch applies after parsing the envelope file:

| `to:` set? | `keep_sender` | Effective `To`         | Effective `Cc` | Threading headers                |
|------------|---------------|------------------------|----------------|----------------------------------|
| no         | (n/a)         | `m.From`               | envelope `cc`  | keep `In-Reply-To` + `References`|
| yes        | false         | envelope `to`          | envelope `cc`  | drop `In-Reply-To` + `References`|
| yes        | true          | envelope `to` + `m.From` | envelope `cc`  | keep `In-Reply-To` + `References`|

Rationale:

- The original sender being on `To` (either as the sole recipient or
  because `keep_sender: true`) means the message is still part of the
  original thread. `In-Reply-To` / `References` are meaningful and
  should be kept.
- When `to:` is set and `keep_sender: false`, the outbound is a
  brand-new conversation for the third-party recipients — the original
  sender has no visibility into it. Keeping `In-Reply-To` in that case
  would leak the original `Message-ID` to third parties and pin them
  into a thread they never saw. Drop the threading headers; add a
  fresh `Message-ID` (same generator as `perch-send`).

## Policy: `allow_forward_to`

Fail-closed allow-list, distinct from `allow_from`:

| Field           | YAML key           | Env var             | Default | Notes |
|-----------------|--------------------|---------------------|---------|-------|
| `AllowForwardTo`| `allow_forward_to` | `ALLOW_FORWARD_TO`  | `[]`    | Same literal/regex syntax as `allow_from`. Empty ⇒ any envelope with `to:` or `cc:` set is rejected. |

Checks, in order, before opening SMTP:

1. Every address in envelope `to:` and `cc:` must pass `allow_forward_to`.
2. `cfg.Email` (the perch mailbox itself) is rejected — loop guard.
3. `m.From` in `to:` is *allowed* (it just collapses to today's
   behavior); no need to be in `allow_forward_to` because the sender
   is already authenticated via `allow_from`.

Regex compile failure at `config.Load` time fails startup, same as
`allow_from`.

Note: `allow_forward_to` and `allow_send_to` (from the `perch-send`
spec) stay separate. Inbound-triggered forwards have a live human on
the other end (the original sender) who can escalate if something goes
wrong; unattended cold-start sends via `perch-send` have no such
witness. Two allow-lists let the two postures diverge without either
having to be the strictest of both.

## Prompt changes

`runner.BuildPrompt` gets one additional paragraph, only when
`allow_forward_to` is non-empty:

> If the sender asks you to forward this reply to someone else (or to
> Cc a third party), write `<replyDir>/.perch-envelope.yaml` with the
> new recipient list and a short `reason:` field explaining why. Only
> addresses on perch's forward allow-list will be accepted; if you're
> unsure whether an address is allowed, reply to the original sender
> asking them to confirm the target instead of guessing.

When `allow_forward_to` is empty the paragraph is omitted entirely, so
agents on installs that never opted in don't learn a capability they
can't exercise.

## Audit log

Every redirect emits one INFO line at reply time, in addition to the
existing `task done` line:

```
INFO  forward from=<original> to=<envelope-to> cc=<envelope-cc> keep_sender=<bool> reason=<reason> session=<sid> message_id=<m.MessageID>
```

`reason` is required in the envelope file whenever `to:` or `cc:` is
non-empty; missing reason ⇒ envelope rejected before SMTP opens.

## Failure semantics

- Envelope YAML unparseable ⇒ send fails, notify original sender via
  the existing `NotifyFailure` path with a short "your forward request
  couldn't be parsed" body.
- Any recipient rejected by `allow_forward_to` ⇒ send fails, notify
  original sender with the rejected address list (no SMTP session
  opened).
- Loop target (`cfg.Email` in `to:`) ⇒ send fails, notify original
  sender.
- Envelope references present but `reason:` empty ⇒ send fails,
  notify original sender.
- SMTP failure after retries ⇒ same as today: `NotifyFailure` to
  original sender, message stays unseen so the next poll retries.

## Architecture

```
internal/replier/
    replier.go            # Reply signature grows a *Envelope arg (nil ⇒ today's
                          # behavior). Applies threading rules from the table above.
    envelope.go           # struct Envelope + Load(path) with validation
    envelope_test.go      # parse, validation, threading-rule matrix

internal/config/
    config.go             # +AllowForwardTo, +allow_forward_to yaml, +ALLOW_FORWARD_TO env,
                          # +regex-compile validation at Load
    config_test.go        # +env override + regex compile failure tests

internal/app/
    app.go                # after collectReplyFiles: if .perch-envelope.yaml exists,
                          # replier.LoadEnvelope, check against AllowForwardTo,
                          # pass into Reply. On any policy failure: notifyReplyFailure
                          # + continue (leave unseen so operator sees the failure email).

internal/runner/
    runner.go             # BuildPrompt gains the forward paragraph, gated on
                          # cfg.AllowForwardTo being non-empty.
```

The reserved filename `.perch-envelope.yaml` starts with a dot so the
existing `collectReplyFiles` scan (which currently returns everything
in the dir) needs one tweak: skip dotfiles. That's a one-line change
plus a test.

`replier.Reply` growing an `*Envelope` parameter is a signature change
but a small one — one caller in `app.go`, plus tests. The nil-envelope
path is the same code as today.

## Backward compatibility

- Existing installs without `allow_forward_to` set behave identically
  to today. The envelope file is only read when `allow_forward_to` is
  non-empty (checked before file stat), so a stray `.perch-envelope.yaml`
  in a workdir on a legacy install is ignored, not an error.
- Existing `Reply` callers pass `nil` for envelope; no behavior change.
- No wire-format change on the inbound side.

## Open questions

- **Does the original sender get a bcc-style receipt?** Right now the
  proposal is no — if `keep_sender: false`, the original sender sees
  nothing. That matches "forward it and don't reply to me", but it
  also means the sender has no confirmation the forward happened.
  Option: always send a short "forwarded to X" confirmation to the
  original sender when `keep_sender: false`. Decide before
  implementation.
- **Should `allow_forward_to` regexes be scoped by domain by default?**
  A very common footgun is a literal `.*@.*` regex that permits
  outbound to anywhere. Config validation could warn (not fail) on
  regexes that would match a huge address space. Punt to
  implementation.
- **What about `Reply-To`?** If the forwarded recipient hits Reply,
  the reply goes to `cfg.Email` (perch), which will then feed it back
  into the agent as a new inbound. This is probably fine — that's the
  whole point of perch — but should be documented in the prompt so
  agents know their forwarded messages become new inbound tasks when
  someone hits Reply.

## Tech stack

No new third-party deps. `envelope.go` uses the existing
`gopkg.in/yaml.v3` for parsing. Everything else is stdlib.

## Relation to `perch-send`

`perch-send` is the cold-start outbound path (no inbound message,
no thread, no session). This spec is the warm-start redirect path
(existing inbound, existing session, agent decided the reply should
go somewhere other than the sender). The two share the same SMTP
plumbing in `internal/replier` but not the same policy or config
surface — they're intentionally separate features so an operator can
enable one without the other.

If `perch-send` lands first, this spec's `envelope.go` will likely
reuse `perch-send`'s `ComposeNew` helper for the `keep_sender: false`
case (fresh Message-ID, no threading headers). If this spec lands
first, `perch-send` can reuse the same compose helper in reverse.
Either ordering works.
