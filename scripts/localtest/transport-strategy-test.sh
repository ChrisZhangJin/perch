#!/usr/bin/env bash
# Integration smoke for the transport-strategy refactor.
#
# Scope: prove the new BuildStrategy wiring produces a perch that starts,
# runs, and shuts down cleanly with the new P/L-mode dispatch — both for
# the poll-only path (provider=163 → Poller + TimerTrigger) and the
# idle+poll path (provider=qq → IMAPMailbox + IDLETrigger).
#
# What this script does NOT prove:
#   - Recovery from a mid-session IMAP bounce in P mode. Per the plan, the
#     short-conn design trivially handles this (every FetchUnseen/MarkSeen
#     is a fresh Dial); no live test is needed.
#   - Recovery from a mid-session IMAP bounce in L mode. That's a
#     NOOP-heartbeat follow-up PR per the plan; out of scope here.
#
# What this script DOES prove:
#   1. BuildStrategy succeeds for both Caps.SupportsIDLE=true and =false.
#   2. The "mailbox ready" log line shows the expected mode.
#   3. main.go wiring (strat.Box + strat.Triggers...) is correct end-to-end
#      for both strategies.

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
for _ in $(seq 1 30); do
  python3 -c "import socket;socket.create_connection(('127.0.0.1',3993),2)" 2>/dev/null && break
  sleep 1
done

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
  timeout 8 ./bin/perch 2>&1 | grep -E "mailbox ready|watcher started|received|task done|ERROR" || true
}

echo "==> [P mode] provider=163 (Caps.SupportsIDLE=false → Poller + TimerTrigger)"
python3 "$LT/send_test_email.py" alice@perch.test agent@perch.test "P-mode task" >/dev/null
run_perch 163 P-mode

echo "==> [L mode] provider=qq (Caps.SupportsIDLE=true → IMAPMailbox + IDLETrigger)"
python3 "$LT/send_test_email.py" alice@perch.test agent@perch.test "L-mode task" >/dev/null
run_perch qq L-mode

echo
echo "==> done. If both runs printed 'mailbox ready' + 'task done', the wiring works."
echo "==> Tear down: docker rm -f perch-greenmail"
