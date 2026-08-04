#!/bin/sh
# Stub agent for local testing. perch invokes it exactly like the real agent:
#   stub-agent.sh -p "<prompt>" --output-format text --permission-mode <m> \
#                 (--session-id <uuid> | --resume <uuid>)
# It ignores the real agent entirely and prints a deterministic reply to stdout,
# which perch captures and emails back. This isolates perch's plumbing from the
# actual agent (no tokens, no auth, no permissions).
echo "Hello from the stub agent. I received your task and this is my reply."
