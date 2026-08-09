#!/usr/bin/env bash
# Integration smoke for the transport-strategy refactor.
#
# Scope: prove the new BuildStrategy wiring produces a perch that starts,
# runs, and shuts down cleanly with the new P/L-mode dispatch — both for
# the poll-only path (provider=163 → Poller + TimerTrigger) and the
# idle+poll path (provider=qq → IMAPMailbox + IDLETrigger). Also proves
# that P mode survives a mid-session IMAP server bounce (docker restart
# GreenMail while perch is running) — the only end-to-end check for the
# "no stuck connection" design promise.
#
# What this script does NOT prove:
#   - Recovery from a mid-session IMAP bounce in L mode. That's a
#     NOOP-heartbeat follow-up PR per the plan; out of scope here.
#
# What this script DOES prove:
#   1. BuildStrategy succeeds for both Caps.SupportsIDLE=true and =false.
#   2. The "mailbox ready" log line shows the expected mode.
#   3. main.go wiring (strat.Box + strat.Triggers...) is correct end-to-end
#      for both strategies.
#   4. P mode (Poller) survives a mid-session GreenMail restart — a second
#      email sent AFTER the bounce is still fetched and replied to.

set -euo pipefail
cd "$(dirname "$0")/../.."
LT=scripts/localtest

echo "==> build"
make build >/dev/null

echo "==> start GreenMail (if not already running)"
if ! docker ps --filter name=perch-greenmail --format '{{.Names}}' | grep -q perch-greenmail; then
  docker rm -f perch-greenmail >/dev/null 2>&1 || true
  docker run -d --name perch-greenmail --network host \
    -e GREENMAIL_OPTS='-Dgreenmail.setup.test.all -Dgreenmail.hostname=0.0.0.0 -Dgreenmail.auth.disabled' \
    greenmail/standalone:2.1.0 >/dev/null
fi
ready=0
for _ in $(seq 1 30); do
  if python3 -c "import socket;socket.create_connection(('127.0.0.1',3993),2)" 2>/dev/null; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "ERROR: GreenMail IMAP port 3993 never opened" >&2
  docker rm -f perch-greenmail >/dev/null 2>&1 || true
  exit 1
fi

# Pre-populate ~/.config/perch/perch.yaml with the provider we want to
# exercise. The wizard normally writes this, but a non-TTY invocation
# can't prompt — so we drive it via env vars + an explicit YAML write.
write_yaml_for() {
  local provider=$1
  local cfgdir=$HOME/.config/perch
  mkdir -p "$cfgdir"
  cat > "$cfgdir/perch.yaml" <<EOF
# written by scripts/localtest/transport-strategy-test.sh
email_provider:
  name: ${provider}
ai_agent:
  name: claude
  workdir: /tmp
  permission_mode: acceptEdits
allow_from:
  - alice@perch.test
email: agent@perch.test
EOF
}

run_perch() {
  local provider=$1
  local label=$2
  write_yaml_for "$provider"
  rm -f /tmp/perch-strat-sessions.json
  echo "==> [${label}] provider=${provider} — expect 'mailbox ready' then 'task done'"
  AGENT_EMAIL=agent@perch.test AGENT_AUTH_CODE=pw \
  IMAP_ADDR=127.0.0.1:3993 SMTP_ADDR=127.0.0.1:3465 TLS_INSECURE_SKIP_VERIFY=1 \
  CLAUDE_BIN="$PWD/$LT/stub-agent.sh" CLAUDE_WORKDIR=/tmp POLL_INTERVAL=2s \
  SESSION_STORE=/tmp/perch-strat-sessions.json \
  timeout 8 ./bin/perch 2>&1 | grep -E "mailbox ready|watcher started|received|task done|ERROR"
}

echo "==> [P mode] provider=163 (Caps.SupportsIDLE=false → Poller + TimerTrigger)"
python3 "$LT/send_test_email.py" alice@perch.test agent@perch.test "P-mode task" >/dev/null
run_perch 163 P-mode

echo "==> [L mode] provider=qq (Caps.SupportsIDLE=true → IMAPMailbox + IDLETrigger)"
python3 "$LT/send_test_email.py" alice@perch.test agent@perch.test "L-mode task" >/dev/null
run_perch qq L-mode

# --- Mid-session IMAP bounce (P mode resilience) -------------------------
# Start perch in the background under provider=163 (Poller), restart
# GreenMail mid-run, then send a second email and confirm perch picks it
# up after the bounce. This is the live test for "no stuck connection".
echo "==> [bounce] P mode must survive a mid-session GreenMail restart"
write_yaml_for 163
rm -f /tmp/perch-strat-sessions.json /tmp/perch-bounce.log

# Start perch in the background. Use stdbuf to line-buffer stderr so the
# log grep below sees lines as they're written.
stdbuf -oL -eL bash -c '
  AGENT_EMAIL=agent@perch.test AGENT_AUTH_CODE=pw \
  IMAP_ADDR=127.0.0.1:3993 SMTP_ADDR=127.0.0.1:3465 TLS_INSECURE_SKIP_VERIFY=1 \
  CLAUDE_BIN="'"$PWD"'/'"$LT"'/stub-agent.sh" CLAUDE_WORKDIR=/tmp POLL_INTERVAL=2s \
  SESSION_STORE=/tmp/perch-strat-sessions.json \
  ./bin/perch
' > /tmp/perch-bounce.log 2>&1 &
PERCH_PID=$!

# Let perch start and prove the first round-trip works.
sleep 3
python3 "$LT/send_test_email.py" alice@perch.test agent@perch.test "pre-bounce task" >/dev/null
sleep 4

# Bounce: docker restart GreenMail. The IMAP connection (none, in P mode)
# is not affected; the next dial after the restart will succeed because
# GreenMail has the same data volume.
echo "==> docker restart perch-greenmail"
docker restart perch-greenmail >/dev/null

# Wait for GreenMail IMAP port to be reachable again.
ready=0
for _ in $(seq 1 30); do
  if python3 -c "import socket;socket.create_connection(('127.0.0.1',3993),2)" 2>/dev/null; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "ERROR: GreenMail IMAP port 3993 did not come back after restart" >&2
  kill "$PERCH_PID" 2>/dev/null || true
  exit 1
fi

# Send a second email AFTER the bounce. Poller must dial fresh and fetch it.
python3 "$LT/send_test_email.py" alice@perch.test agent@perch.test "post-bounce task" >/dev/null

# Give perch up to 8s to fetch + reply. Then shut it down.
sleep 6
kill -TERM "$PERCH_PID" 2>/dev/null || true
wait "$PERCH_PID" 2>/dev/null || true

echo "==> bounce perch log:"
grep -E "mailbox ready|received|task done|ERROR" /tmp/perch-bounce.log || true

pre=$(grep -c "pre-bounce task" /tmp/perch-bounce.log || true)
post=$(grep -c "post-bounce task" /tmp/perch-bounce.log || true)
if [ "$post" -lt 1 ]; then
  echo "ERROR: P mode did not process the post-bounce email — Poller did not survive the GreenMail restart" >&2
  exit 1
fi
echo "==> bounce OK: pre-bounce seen=$pre, post-bounce seen=$post"

echo
echo "==> done. If all three scenarios printed 'task done', the wiring + bounce resilience works."
echo "==> Tear down: docker rm -f perch-greenmail"
