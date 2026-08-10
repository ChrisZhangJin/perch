# Testing perch

Three levels, cheapest first.

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

## Troubleshooting

- **`Unsafe Login` / login rejected on 163** — you used the password instead of the
  授权码, or IMAP is not enabled in the mailbox settings. perch already sends the required
  IMAP `ID` command.
- **Nothing happens** — check `ALLOW_FROM` matches the sender exactly (case-insensitive,
  address only). An empty `ALLOW_FROM` denies everyone.
- **`connection refused` in Level 1** — the container needs host networking
  (`--network host`, as in the demo) for `127.0.0.1` to reach it.

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
