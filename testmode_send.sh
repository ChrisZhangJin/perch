#!/usr/bin/env bash
#
# testmode_send.sh — drive a perch --testmode server over POST /inject.
#
# Start the server first (see run_testmode.sh), then:
#
#   ./testmode_send.sh                     # 3-turn threaded scenario
#   ./testmode_send.sh -p 9912             # different port
#   ./testmode_send.sh -l /tmp/perch.log   # also check the log for evidence
#   ./testmode_send.sh -1 "帮我看一下 X"    # one-off single message
#
# The scenario exercises the two features that only show up across a thread:
#
#   prompt.contracts=on_resume   turn 1 sends the GREETING/ATTACHMENT
#                                contracts, turns 2-3 must not repeat them
#   prompt.strip_quoted=on_resume turn 1 keeps the quoted history, turns 2-3
#                                must strip it
#
# Both need the turns to share a thread, which is what message_id/references
# are for. Without them every injection is a new thread and always cold.
#
# Enable the features in the config the server was started with:
#
#   prompt:
#     contracts: on_resume
#     strip_quoted: on_resume
#
set -uo pipefail

PORT=9876
HOST=127.0.0.1
LOG=""
ONESHOT=""
FROM="chris@wiz.ai"
SUBJECT="季度报表"
# Thread root for the scenario. Change it (or restart the server) to start a
# fresh thread — reusing it resumes the previous run's agent session.
ROOT="testmode-thread-1"

while getopts "p:h:l:1:f:s:r:?" opt; do
  case "$opt" in
    p) PORT=$OPTARG ;;
    h) HOST=$OPTARG ;;
    l) LOG=$OPTARG ;;
    1) ONESHOT=$OPTARG ;;
    f) FROM=$OPTARG ;;
    s) SUBJECT=$OPTARG ;;
    r) ROOT=$OPTARG ;;
    *) sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
  esac
done

URL="http://$HOST:$PORT/inject"

# --noproxy matters: http_proxy is set in many of these environments and would
# otherwise send a request for 127.0.0.1 out to the proxy.
post() {
  curl -sS --noproxy "$HOST" --max-time 600 \
       -X POST "$URL" -H 'Content-Type: application/json' -d "$1"
}

show() { # <label> <json>
  local label=$1 json=$2
  echo
  echo "=== $label ==="
  if [ -z "$json" ]; then
    echo "  (no response — is the server running on $HOST:$PORT?)"
    return 1
  fi
  echo "$json" | jq -r '
    if .error then "  ERROR: \(.error)"
    else
      "  uid=\(.uid)  message_id=\(.message_id)  thread_root=\(.thread_root)",
      "  replies=\(.replies // [] | length)",
      (.reply // empty | "  --- reply ---\n\(.body)" )
    end' 2>/dev/null || echo "$json"
}

if ! curl -sS --noproxy "$HOST" --max-time 3 -o /dev/null "http://$HOST:$PORT/" 2>/dev/null; then
  : # the server has no GET route; a connection refusal is what we care about
fi

# ---------------------------------------------------------------- one-off ---
if [ -n "$ONESHOT" ]; then
  body=$(jq -cn --arg f "$FROM" --arg s "$SUBJECT" --arg b "$ONESHOT" \
    '{from:$f, to:"agent@perch.local", subject:$s, body:$b}')
  show "one-off" "$(post "$body")"
  exit $?
fi

# --------------------------------------------------------------- scenario ---
# A realistic 163/Foxmail reply: new text on top, quoted history below the
# 「原始邮件」banner. StripQuoted should remove everything from the banner down.
quoted_block() { # <what the sender is quoting>
  printf '\n\n------------------ 原始邮件 ------------------\n发件人: "Chris"<%s>;\n发送时间: 2026年8月23日(星期六) 下午2:30\n收件人: "agent"<agent@163.com>;\n主题: %s\n\n%s' \
    "$FROM" "$SUBJECT" "$1"
}

turn() { # <n> <message-id> <references-json> <body>
  local n=$1 mid=$2 refs=$3 body=$4
  local json
  json=$(jq -cn --arg f "$FROM" --arg s "$SUBJECT" --arg b "$body" \
                --arg m "$mid" --argjson r "$refs" \
    '{from:$f, to:"agent@perch.local", subject:$s, body:$b, message_id:$m, references:$r}')
  show "turn $n  ($mid)" "$(post "$json")"
}

echo "perch testmode -> $URL"
echo "thread root: <$ROOT>   (use -r to start a different thread)"

turn 1 "<$ROOT>" '[]' \
  "$(printf '记住这个数字：42。先简单确认一下。%s' "$(quoted_block '这是更早之前的邮件历史，第一轮就该保留。')")"

turn 2 "<${ROOT}-2>" "[\"<$ROOT>\"]" \
  "$(printf '我刚才让你记的数字是多少？只回数字。%s' "$(quoted_block '这段历史在第二轮应该被 strip_quoted 剥掉。')")"

turn 3 "<${ROOT}-3>" "[\"<$ROOT>\"]" \
  "$(printf '今天是几号？请实际去查，不要凭记忆。%s' "$(quoted_block '这段历史在第三轮同样应该被剥掉。')")"

cat <<'NOTE'

--- what to look for ---
  turn 1  thread_root == <ROOT>, opens a cold session
  turn 2  same thread_root; the agent should answer "42" from session memory
  turn 3  the date must be looked up, not recalled (GROUNDING)
NOTE

# ------------------------------------------------------------------- log ---
[ -z "$LOG" ] && { echo; echo "pass -l <server log> to check the prompts too."; exit 0; }

if [ ! -r "$LOG" ]; then
  echo; echo "log not readable: $LOG"; exit 1
fi

count() { grep -c -- "$1" "$LOG" 2>/dev/null || echo 0; }

echo
echo "--- log evidence ($LOG) ---"
printf '  %-34s %s  (want 1: only turn 1)\n' "GREETING PROTOCOL"   "$(count 'GREETING PROTOCOL')"
printf '  %-34s %s  (want 1: only turn 1)\n' "ATTACHMENT PROTOCOL" "$(count 'ATTACHMENT PROTOCOL')"
printf '  %-34s %s  (want 2: turns 2-3)\n'   "resume pointer"      "$(count 'unchanged from earlier in this thread')"
printf '  %-34s %s  (want 3: every turn)\n'  "SAFETY PROTOCOL"     "$(count 'SAFETY PROTOCOL (hard contract')"
printf '  %-34s %s  (want 3: every turn)\n'  "GROUNDING"           "$(count 'GROUNDING (hard contract')"
printf '  %-34s %s  (want 2: turns 2-3)\n'   "stripped quoted history" "$(count 'stripped quoted history')"
echo
printf '  %-34s %s  (want 1: only turn 1)\n' "spawned with --session-id" "$(count '\-\-session-id')"
printf '  %-34s %s  (want 2: turns 2-3)\n'   "spawned with --resume"     "$(count '\-\-resume')"

echo
echo "  prompt size per turn (the actual saving):"
grep -o 'is_new=[a-z]* prompt_bytes=[0-9]*' "$LOG" | sed 's/^/    /'
grep -o 'prompt_bytes=[0-9]*' "$LOG" | sed 's/.*=//' | awk '
  NR==1 {cold=$1}
  NR==2 {warm=$1}
  END { if (cold && warm) printf "    cold=%d warm=%d  saved=%d bytes (%.0f%%)\n",
          cold, warm, cold-warm, (cold-warm)*100/cold }'

echo
echo "  quoted text that leaked into a resumed prompt (want none):"
if grep -q '应该被 strip_quoted 剥掉' "$LOG"; then
  grep -c '应该被 strip_quoted 剥掉' "$LOG" | sed 's/^/    LEAKED on /;s/$/ line(s)/'
else
  echo "    none"
fi
echo
echo "note: the counts above need log_level: debug — the prompt is only"
echo "      logged at DEBUG, and 'stripped quoted history' is a DEBUG line."
