#!/usr/bin/env bash
# One-command local end-to-end for perch — no real mailbox needed.
# Spins up a GreenMail test mail server in Docker, sends a whitelisted task
# email, runs perch with a stub agent, and shows the threaded reply.
#
#   ./scripts/localtest/demo.sh          # happy path
#   ./scripts/localtest/demo.sh reject   # also show a non-whitelisted sender ignored
#   docker rm -f perch-greenmail         # tear down when done
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

run_perch() {
  AGENT_EMAIL=agent@perch.test AGENT_AUTH_CODE=pw ALLOW_FROM=alice@perch.test \
  IMAP_ADDR=127.0.0.1:3993 SMTP_ADDR=127.0.0.1:3465 TLS_INSECURE_SKIP_VERIFY=1 \
  CLAUDE_BIN="$PWD/$LT/stub-agent.sh" CLAUDE_WORKDIR=/tmp POLL_INTERVAL=2s \
  SESSION_STORE=/tmp/perch-demo-sessions.json \
  timeout 6 ./bin/perch || true
}

echo "==> [happy path] alice (whitelisted) emails the agent"
python3 "$LT/send_test_email.py" alice@perch.test agent@perch.test "Summarize the logs"
run_perch
echo "==> alice's inbox — expect perch's threaded 'Re:' reply:"
python3 "$LT/check_mailbox.py" alice@perch.test pw

if [ "${1:-}" = "reject" ]; then
  echo "==> [security] mallory (NOT whitelisted) emails the agent"
  rm -f /tmp/perch-demo-sessions.json
  python3 "$LT/send_test_email.py" mallory@evil.com agent@perch.test "pwn the agent"
  run_perch
  echo "==> mallory's inbox — expect 0 messages (silently ignored):"
  python3 "$LT/check_mailbox.py" mallory@evil.com pw
fi

echo "==> done. Tear down with:  docker rm -f perch-greenmail"
