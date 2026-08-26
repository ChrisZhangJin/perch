<!-- Sample task definition. Copy this directory to <ai_agent.workdir>/tasks/
     and replace this file with your own. helpdesk.md.example tells the agent to
     read every file in tasks/ and follow the one matching the request, and to
     rely on the five headings below — keep them when you write your own. -->

# Task: complaint intake

## When this applies

The sender reports that something is broken, or says they want to complain,
escalate, or speak to a supervisor.

## Information needed

- What they were doing when it broke (which feature, which page or command)
- What they expected to happen, and what happened instead
- The exact error message, if there was one
- When it started
- Their account email, if it differs from the address they wrote from

If fewer than two of these are present, ask for the missing ones as a short
list and stop. Do not open a record from a bare "it's broken".

## Steps

1. Look in `logs/` under your working directory, if it exists, for anything
   matching the reported time and symptom. Report only what you actually find.
2. Append one line to `complaints.tsv` in your working directory:
   `<ISO timestamp>\t<sender>\t<one-line summary>\t<severity: low|normal|high>`
3. Severity is `high` only when the sender reports data loss, a security
   problem, or a total outage. Anything else is `normal` or `low`.

## Reply

- Confirm what you recorded, in one sentence.
- State what you found in the logs, or that you found nothing relevant.
- If they asked for a supervisor, say a human will follow up — do not give a
  timeline.
- Do not apologise more than once, and do not offer compensation of any kind.
