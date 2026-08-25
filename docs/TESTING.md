# Testing perch

Three levels, cheapest first — these cover the **transport** (offline → GreenMail → a real
mailbox). Prompt and session behaviour is a separate axis with no transport at all; see
[Prompt & session behaviour](#prompt--session-behaviour-no-mailbox).

## Level 0 — automated tests (no setup, ~1s)

Proves all the pure logic and the offline pipeline (mock mailbox + stub agent + fake sender):

```bash
make test        # unit tests + offline end-to-end
make vet
```

## Level 1 — local end-to-end with Docker (no real mailbox, ~30s)

Runs the **real** IMAP + SMTP code against a throwaway [GreenMail](https://greenmail-mail-test.github.io/greenmail/)
test server, with a stub agent (no tokens, no Claude auth). One command:

```bash
./scripts/localtest/demo.sh          # happy path: whitelisted sender -> threaded reply
./scripts/localtest/demo.sh reject   # also proves a non-whitelisted sender is ignored
docker rm -f perch-greenmail         # tear down
```

Expected happy-path output: perch logs `task done`, and `alice@perch.test`'s inbox shows a
`Re:` reply with `In-Reply-To`/`References` set. The `reject` run logs `rejected sender`
and mallory's inbox stays empty.

### What the demo does (to run it by hand)

```bash
# 1. start a test mail server (SMTP 3025, SMTPS 3465, IMAP 3143, IMAPS 3993)
docker run -d --name perch-greenmail --network host \
  -e GREENMAIL_OPTS='-Dgreenmail.setup.test.all -Dgreenmail.hostname=0.0.0.0 -Dgreenmail.auth.disabled' \
  greenmail/standalone:2.1.0

# 2. send a whitelisted task email
python3 scripts/localtest/send_test_email.py alice@perch.test agent@perch.test "Summarize the logs"

# 3. run perch against the local server, with the stub agent
make build
AGENT_EMAIL=agent@perch.test AGENT_AUTH_CODE=pw ALLOW_FROM=alice@perch.test \
IMAP_ADDR=127.0.0.1:3993 SMTP_ADDR=127.0.0.1:3465 TLS_INSECURE_SKIP_VERIFY=1 \
CLAUDE_BIN="$PWD/scripts/localtest/stub-agent.sh" CLAUDE_WORKDIR=/tmp POLL_INTERVAL=2s \
./bin/perch          # Ctrl-C after you see "task done"

# 4. read alice's inbox — expect perch's threaded reply
python3 scripts/localtest/check_mailbox.py alice@perch.test pw
```

> `TLS_INSECURE_SKIP_VERIFY=1` is **dev/test only** — it lets perch accept GreenMail's
> self-signed cert. Never set it against a real mailbox.

To test with the **real agent** instead of the stub, drop `CLAUDE_BIN` (defaults to
`claude`) and set `CLAUDE_WORKDIR` to a real workspace — `claude` must be logged in.

## Level 2 — real end-to-end with two 163 mailboxes

The production-representative test.

1. Create two 163 mailboxes, e.g. `agent@163.com` and `you@163.com`.
2. In each mailbox's web settings, **enable IMAP/SMTP** and generate an **authorization
   code (授权码)** — this is what perch logs in with, not the account password.
3. Run perch on the agent mailbox, whitelisting your address:

   ```bash
   make build
   AGENT_EMAIL=agent@163.com \
   AGENT_AUTH_CODE=<agent-authcode> \
   ALLOW_FROM=you@163.com \
   CLAUDE_WORKDIR=/home/you/workspace \
   ./bin/perch
   ```
   (Defaults already target `imap.163.com:993` / `smtp.163.com:465`.)
4. From `you@163.com`, email `agent@163.com` a task. Within a few seconds perch runs the
   agent and you get a threaded reply.
5. **Reply again in the same thread** → perch resumes the same agent session (memory
   carries across the conversation).
6. Email the agent from a different, non-whitelisted address → silently ignored (check
   the perch log for `rejected sender`).

## Prompt & session behaviour (no mailbox)

Two harnesses run the **real** agent through the **real** `app.ProcessUnseen` pipeline with
both network edges removed — no IMAP, no SMTP, no Docker. Use them to see what perch
actually sends the agent.

They cost agent tokens (unlike Level 1, which uses a stub), so they are not cheaper than
Level 1 — just narrower.

| | `mailtest` | `perch --testmode` |
|---|---|---|
| Build | `make build-mailtest` | `make build-testmode` |
| Input | YAML file, one email per run | `POST /inject`, many per run |
| Multi-turn | two runs, same workdir | any number, one process |
| Outbound | captured, never sent | captured, never sent |

`--testmode` is a separate build tag. A release binary has no `--testmode` flag and no
injection endpoint, so this cannot leak into production.

### Both features need a thread

`prompt.contracts` and `prompt.strip_quoted` only differ between a **cold** session and a
**resumed** one. A single email is always cold, so a one-shot run proves nothing about
either. Turn on debug logging — the prompt is only logged at DEBUG:

```yaml
prompt:
  contracts: on_resume
  strip_quoted: on_resume     # default is never; must be enabled explicitly
log_level: debug
```

### mailtest — one email per process

```bash
make build-mailtest
mkdir -p /tmp/mtwd
cat > /tmp/mt.yaml <<'EOF'
from: chris@example.com
subject: "quick question"
body: |
  What is the current date? Reply in one sentence.
agent: claude          # claude | nanopi | pi — must be on PATH
workdir: /tmp/mtwd     # NOT the perch repo; the agent writes here
allow_from: ["*"]
log_level: debug
EOF

./bin/mailtest -config /tmp/mt.yaml                  # run 1: cold session
./bin/mailtest -config /tmp/mt.yaml 2>/tmp/r2.log    # run 2: resumes it
```

Run 2 resumes run 1 because the synthetic Message-ID is per-process (`<mtest-1@mailtest>`
every time) and the session map persists at `<workdir>/mailtest-sessions.json`. **Both runs
must use the same `workdir`.**

```bash
grep -oE '\-\-(session-id|resume)' /tmp/r2.log   # expect --resume
grep -c "GREETING PROTOCOL" /tmp/r2.log          # expect 0 (contracts omitted)
grep -c "unchanged from earlier" /tmp/r2.log     # expect 1 (the resume pointer)
grep -o 'prompt_bytes=[0-9]*' /tmp/r2.log        # the actual saving
```

`-agent nanopi` overrides the config's `agent:` without editing the file.

### --testmode — multi-turn threads over HTTP

```bash
make build-testmode
bin/perch-testmode --testmode --testport 9876 --config ~/.perch/perch.yaml 2>&1 \
  | tee /tmp/perch.log        # tee: the evidence lives in the log
```

Then, in another shell:

```bash
./testmode_send.sh -l /tmp/perch.log
```

`testmode_send.sh` drives a three-turn thread with a realistic 163/Foxmail reply shape —
new text on top, quoted history under an `------ 原始邮件 ------` banner. Two turns are
designed to fail visibly rather than only shifting a counter:

- **turn 2** asks the agent to recall a number from turn 1 → wrong answer means the session
  was not actually resumed.
- **turn 3** asks for today's date → a stale date means `GROUNDING` is not reaching the
  agent.

Expected evidence for a clean three-turn run:

| Signal | Count | Meaning |
|---|---|---|
| `GREETING PROTOCOL` | 1 | contracts only on the cold turn |
| `ATTACHMENT PROTOCOL` | 1 | same |
| `unchanged from earlier in this thread` | 2 | the resume pointer replaced them |
| `SAFETY PROTOCOL (hard contract` | 3 | guardrail, sent every turn |
| `GROUNDING (hard contract` | 3 | guardrail, sent every turn |
| `stripped quoted history` | 2 | quote removed on the resumed turns |
| `--session-id` / `--resume` | 1 / 2 | one cold spawn, two resumes |
| `prompt_bytes` | 5265 → 3427 | ~1.8 KB (35%) saved per resumed turn |

`prompt_bytes` is the most direct measure — it is the size of what the agent was actually
handed. The protocol-string counts only corroborate it.

Other flags:

```bash
./testmode_send.sh -1 "帮我看一下 X"    # one-off, no scenario
./testmode_send.sh -p 9912             # non-default port
./testmode_send.sh -r my-thread-2      # different thread root
./testmode_send.sh -?                  # usage
```

To build a thread by hand, chain `message_id` into the next turn's `references`. Angle
brackets are optional; whitespace inside an id is rejected:

```bash
C() { curl -s --noproxy 127.0.0.1 -X POST http://127.0.0.1:9876/inject \
        -H 'Content-Type: application/json' -d "$1"; }
C '{"from":"you@x","subject":"t","body":"记住 42","message_id":"<t1@x>"}'
C '{"from":"you@x","subject":"t","body":"数字是多少？","message_id":"<t2@x>","references":["<t1@x>"]}'
```

The response echoes `message_id` and `thread_root`. **Two turns share a session exactly
when they share `thread_root`** (`references[0]`, else the message's own id).

### Reading the session map

```bash
cat /tmp/perch-sessions.json      # or whatever session_store points at
```

Keys look mangled because Go's JSON encoder escapes `<` and `>` by default, so
the key for `<m1@163.com>` is stored as `\u003cm1@163.com\u003e`. The angle brackets are
part of the Message-ID, so keep them if you hand-edit — a key without them will never
match.

Edit the file only while perch is stopped. Every registry change rewrites the whole map, so
a running perch will overwrite your edit on the next new thread.

The UUID version says who minted it:

| Third group starts with | Minted by | Note |
|---|---|---|
| `4` | perch | used as-is by claude/pi |
| `7` | the agent | nanopi minted it; perch adopted it |

A **v4 entry under nanopi** is a stale entry nanopi never knew about. The next email in that
thread logs `nanopi session lost; caller must retry cold`, perch restarts cold with the full
contracts, adopts the new v7 id, and writes it back — so it self-heals in one round. Confirm
by re-reading the file: the entry should now be v7.

UUIDv7 embeds its creation time in the first 48 bits, so the map doubles as a "when did this
thread start" record and can be pruned by age without extra bookkeeping:

```bash
python3 -c "
import datetime,sys
u=sys.argv[1]; p=u.split('-')
print('v'+p[2][0], datetime.datetime.fromtimestamp(int(p[0]+p[1],16)/1000, datetime.UTC))" \
  01a0378d-6d1c-7fb3-af1a-0c1555d0e53d
```

### Loop protection

Two mail robots answering each other never stop on their own. This happened in production
between two perch instances after `allow_from` was widened to `s".*@163.com"`, which
whitelisted every address at that provider — including another bot and perch's own mailbox.

Four defences, weakest assumption first:

| | Defence | Works against |
|---|---|---|
| 1 | perch stamps `Auto-Submitted: auto-replied` on everything it sends | a counterparty that honours RFC 3834 |
| 2 | `loop_guard.skip_automated` drops inbound mail labelled machine-generated | a counterparty that labels itself |
| 3 | mail from perch's own address is always dropped (no knob) | bounces, Cc-to-self, an over-wide whitelist |
| 4 | `loop_guard.max_replies_per_hour` caps replies per thread | **a robot that labels nothing** — the common case |

Only #4 catches the loop that actually occurred. #1–#3 all assume the other side is
cooperative or identifiable.

To exercise them, inject with the header under test. All three should log a
`(loop guard)` WARN, run no agent, and still mark the message seen:

```bash
C() { curl -s --noproxy 127.0.0.1 -X POST http://127.0.0.1:9876/inject \
        -H 'Content-Type: application/json' -d "$1"; }

# #2 — needs a client that can set arbitrary headers; /inject cannot, so use
# mailtest with a canned .eml, or verify via the unit tests:
go test ./internal/app/ -run TestProcessSkipsAutomatedMail -v
go test ./internal/message/ -run TestIsAutomated -v

# #3 — set `from` to perch's own address
C '{"from":"agent_hellen@163.com","subject":"x","body":"y"}'

# #4 — same thread, more rounds than the cap
for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
  C "{\"from\":\"you@x\",\"subject\":\"t\",\"body\":\"round $i\",\
      \"message_id\":\"<loop-$i@x>\",\"references\":[\"<loop-root@x>\"]}" >/dev/null
done
grep -c "thread reply cap reached" /tmp/perch.log     # expect 2 with the default cap of 10
```

`POST /inject` has no field for arbitrary headers, so #2 cannot be driven over HTTP — use
the unit tests, or feed a canned `.eml` through `mailtest.SendRaw`.

The cap is a **rolling hour, per thread**, and counts attempts rather than successful sends
(a reply that fails at SMTP still consumed an agent run). It lives in memory, so a restart
resets it — that bounds a loop within one run, which is the point; by the time you restart,
you are already looking.

## Troubleshooting

- **`Unsafe Login` / login rejected on 163** — you used the password instead of the
  授权码, or IMAP is not enabled in the mailbox settings. perch already sends the required
  IMAP `ID` command.
- **Nothing happens** — check `ALLOW_FROM` matches the sender exactly (case-insensitive,
  address only). An empty `ALLOW_FROM` denies everyone.
- **`connection refused` in Level 1** — the container needs host networking
  (`--network host`, as in the demo) for `127.0.0.1` to reach it.

### Harness gotchas

Each of these produced a wrong conclusion at least once.

- **Every turn looks cold** — the turns are not in one thread. `/inject` without
  `message_id`/`references` mints a fresh id per call, so each injection is its own thread
  and always `isNew=true`. `mailtest` needs the **same `workdir`** across runs.
- **The counts are all zero** — `log_level` is not `debug`. The prompt and
  `stripped quoted history` are DEBUG-only lines.
- **`strip_quoted` does nothing** — its default is `never`. It must be enabled explicitly,
  unlike `contracts`, which defaults to `on_resume`.
- **Turn 1 is a resume** — the thread root already exists in `session_store` from an earlier
  run. Use `-r <new-root>`, or clear `session_store`. Restarting the server is not enough:
  the map is on disk.
- **`curl` hangs or 502s on `127.0.0.1`** — `http_proxy` is set and the request went to the
  proxy. Use `--noproxy 127.0.0.1`.
- **The agent wrote files into the perch repo** — `workdir` pointed at the repo. Point it at
  a scratch directory. `task_only: true` allows anything *inside* cwd by design, so cwd is
  the blast radius.
- **`grep -c -- "--resume"` reports a nonsense count** — the `--` was consumed as the
  pattern and you counted every line containing `--`. Escape instead: `grep -c '\-\-resume'`.
- **Sign-off reads `Best,\nthere`** — `mailtest.yaml` has no `email:` field, so
  `agentDisplayName("")` falls back to `there`. Cosmetic; production derives the name from
  `email:` (`agent_hellen@163.com` → `hellen`).
- **Thread continuity vanished after a reboot** — `session_store` defaults to
  `$TMPDIR/perch-sessions.json`, and many systems wipe `/tmp`. Point it somewhere durable
  for a long-running deployment.
- **Two forwards of the same chain answered as one conversation** — the session key is the
  *thread root*, not the sender. Forwarding the same chain twice yields the same
  `References[0]`, so both land in one agent session. Start a new email for a clean session.
- **`pkill -f perch-testmode` killed your shell** — the pattern matches the command line of
  the process running `pkill`. Use a character class: `pkill -f 'perch-testmod[e]'`.
- **A whitelisted correspondent went quiet after a busy exchange** — the thread hit
  `loop_guard.max_replies_per_hour` (default 10). Look for `thread reply cap reached`. Raise
  it, or set 0 to disable, but understand that the cap is the only defence against a robot
  that sets no headers.
- **perch ignores a monitoring job's mail** — that job sets `Auto-Submitted: auto-generated`,
  which `skip_automated` treats as a loop risk. Set `loop_guard.skip_automated: false`, and
  rely on the per-thread cap instead.
- **Replies never arrive in a real inbox under `--testmode`** — by design. `InjectSender`
  holds no transport at all; the reply comes back in the `/inject` response. Use Level 1 or
  2 to test actual delivery.

## Daemon mode

The `./perch --daemon` flag re-execs perch detached. The smoke script
covers the happy path automatically:

```bash
./scripts/localtest/daemon.sh
```

For manual verification on a real machine:

1. Launch: `./perch --daemon`. Expected: parent exits 0, prints
   `perch daemon started, pid N, pidfile /home/.../.perch/perch.pid`.
2. Verify: `cat ~/.perch/perch.pid` then `kill -0 $(cat ~/.perch/perch.pid)`.
   Expected: both succeed.
3. Confirm detachment: close the launching terminal / SSH session.
   Expected: perch keeps running.
4. Stop: `kill $(cat ~/.perch/perch.pid)`. Expected: process exits
   within a second, pidfile disappears.
5. Stale-pidfile recovery: write a fake pid into the pidfile
   (`echo 9999999 > ~/.perch/perch.pid`), then launch `--daemon`
   again. Expected: a WARN line on the parent stderr ("stale pidfile,
   removing"), then daemonization proceeds normally.

If a real daemon is already running and you try to launch another, the
parent exits non-zero with "already running" and points at the existing
pidfile. Do not delete a live daemon's pidfile manually — use SIGTERM.
