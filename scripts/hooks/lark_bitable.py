#!/usr/bin/env python3
"""perch on-email hook: append every inbound email to a Lark (Feishu) Bitable.

Wire it up in perch.yaml:

    hooks:
      on_email: /path/to/perch/scripts/hooks/lark_bitable.py
      timeout: 30s

perch calls this with its five positional arguments:

    $1 email_id   Message-Id header, e.g. "<abc@163.com>" ("" when absent)
    $2 subject
    $3 body       text/plain as received
    $4 sender     lowercased address
    $5 new_thread | reply_thread

and treats the exit status as advisory: a failure here is logged at WARN and
the email is answered anyway. So this script's job is to be loud on stderr
(perch puts it in the log) and never to hang — every request is bounded and
the whole run has to fit inside hooks.timeout.

Credentials come from a .env file (next to this script by default), never from
argv, so they don't show up in `ps`:

    APP_ID=cli_xxxxxxxx
    APP_SECRET=xxxxxxxx

Stdlib only — no pip install on the host running perch.
"""

from __future__ import annotations

import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

# --- target ----------------------------------------------------------------
# Both ids come from the Bitable URL:
#   https://<host>/base/<APP_TOKEN>?table=<TABLE_ID>&view=...
APP_TOKEN = os.environ.get("LARK_APP_TOKEN", "QSbnbpXGfa9bbqsMbu9cwXiKn0c")
TABLE_ID = os.environ.get("LARK_TABLE_ID", "tblvPJSXmlLQ6BCo")

# open.feishu.cn for a Feishu (China) tenant; open.larksuite.com for Lark.
BASE_URL = os.environ.get("LARK_BASE_URL", "https://open.feishu.cn/open-apis").rstrip("/")

# Column names as they exist in the table. Edit here if the table is renamed —
# Bitable addresses fields by name, so a mismatch is a 400 with a "FieldNameNotFound"
# style message, not a silently empty cell.
F_SEND_TIME = "SendTime"  # DateTime  — epoch milliseconds
F_SUBJECT = "Subject"     # Text
F_CONTENT = "Content"     # Text
F_TICKET_NO = "TicketNo"  # Text      — unique key per row
F_SENDER = "sender"       # Text

# --- behaviour knobs (all optional) ----------------------------------------
# Path to the file holding APP_ID / APP_SECRET.
ENV_FILE = os.environ.get("LARK_ENV_FILE", "")
# "1" records only the first email of a thread (perch's $5 == new_thread).
ONLY_NEW_THREADS = os.environ.get("LARK_ONLY_NEW_THREADS", "0") == "1"
# Bitable text cells hold a lot but not everything, and a 64 KiB email body in
# a grid cell is unreadable anyway.
MAX_CONTENT = int(os.environ.get("LARK_MAX_CONTENT", "20000"))
# "1" folds the body onto one line (matches how a grid preview reads).
COLLAPSE_BODY = os.environ.get("LARK_COLLAPSE_BODY", "0") == "1"
# Per-request timeout and retry count. Keep timeout * (retries+1) * 2 requests
# comfortably under perch's hooks.timeout.
TIMEOUT = float(os.environ.get("LARK_TIMEOUT", "8"))
RETRIES = int(os.environ.get("LARK_RETRIES", "2"))
# perch may run behind an http_proxy that cannot reach the Feishu endpoint (or
# that the endpoint is directly reachable without). Proxies are ignored unless
# you opt in.
USE_ENV_PROXY = os.environ.get("LARK_USE_ENV_PROXY", "0") == "1"

# Lark error codes worth a second attempt: rate limits and internal errors.
RETRYABLE_CODES = {99991400, 1254290, 1254291, 1255001, 1255040}


def log(msg: str) -> None:
    """Write to stderr — perch surfaces it in the WARN it logs for a failure."""
    print(f"lark_bitable: {msg}", file=sys.stderr)


def load_env(path: Path) -> None:
    """Merge KEY=VALUE lines from path into os.environ without clobbering it.

    Real env vars win, so a systemd unit can inject the secret instead of
    shipping a file. Deliberately not a full dotenv parser: `export` prefixes,
    surrounding quotes and comments are handled, expansion is not.
    """
    try:
        text = path.read_text(encoding="utf-8")
    except OSError:
        return
    for raw in text.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        line = line.removeprefix("export ").strip()
        key, sep, value = line.partition("=")
        if not sep:
            continue
        key = key.strip()
        value = value.strip().strip("'\"")
        if key and key not in os.environ:
            os.environ[key] = value


def env_candidates() -> list[Path]:
    """.env search order: explicit override, next to this script, cwd, ~/.perch.

    perch runs hooks with cwd = ai_agent.workdir, which is not necessarily
    where this script lives — hence both the script-relative candidates and
    ~/.perch, where perch already keeps its own config.
    """
    if ENV_FILE:
        return [Path(ENV_FILE).expanduser()]
    here = Path(__file__).resolve().parent
    return [
        here / ".env",
        here.parent.parent / ".env",
        Path.cwd() / ".env",
        Path.home() / ".perch" / ".env",
    ]


def post(url: str, payload: dict, token: str | None) -> dict:
    """POST JSON, return the decoded body. Retries transient failures."""
    data = json.dumps(payload).encode("utf-8")
    headers = {"Content-Type": "application/json; charset=utf-8"}
    if token:
        headers["Authorization"] = f"Bearer {token}"

    opener = urllib.request.build_opener()
    if not USE_ENV_PROXY:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    last: Exception | None = None
    for attempt in range(RETRIES + 1):
        if attempt:
            time.sleep(min(2 ** attempt, 4))
        req = urllib.request.Request(url, data=data, headers=headers, method="POST")
        try:
            with opener.open(req, timeout=TIMEOUT) as resp:
                body = json.loads(resp.read().decode("utf-8"))
        except urllib.error.HTTPError as exc:
            detail = exc.read().decode("utf-8", "replace")[:500]
            last = RuntimeError(f"HTTP {exc.code}: {detail}")
            if 500 <= exc.code < 600:
                continue
            raise last from exc
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            last = RuntimeError(f"{type(exc).__name__}: {exc}")
            continue

        code = body.get("code", -1)
        if code == 0:
            return body
        msg = body.get("msg", "")
        last = RuntimeError(f"lark code {code}: {msg}")
        if code in RETRYABLE_CODES:
            continue
        raise last

    raise last if last else RuntimeError("request failed with no error recorded")


def tenant_token(app_id: str, app_secret: str) -> str:
    """Exchange app credentials for a tenant_access_token (valid ~2h).

    Not cached: a cache file shared by concurrent hook runs buys one saved
    request and costs a locking problem. If your volume ever makes that matter,
    cache to a tmpfile keyed by app_id with an expiry a few minutes short of
    the returned `expire`.
    """
    body = post(
        f"{BASE_URL}/auth/v3/tenant_access_token/internal",
        {"app_id": app_id, "app_secret": app_secret},
        token=None,
    )
    token = body.get("tenant_access_token", "")
    if not token:
        raise RuntimeError(f"no tenant_access_token in response: {body}")
    return token


def create_record(token: str, fields: dict) -> str:
    """Append one row; returns its record_id."""
    url = f"{BASE_URL}/bitable/v1/apps/{APP_TOKEN}/tables/{TABLE_ID}/records"
    body = post(url, {"fields": fields}, token)
    return body.get("data", {}).get("record", {}).get("record_id", "")


def clean_body(body: str) -> str:
    """Normalise CRLF, drop trailing blank lines, cap length."""
    text = body.replace("\r\n", "\n").replace("\r", "\n").strip()
    if COLLAPSE_BODY:
        text = re.sub(r"\s+", " ", text).strip()
    if len(text) > MAX_CONTENT:
        text = text[:MAX_CONTENT] + f"\n…[truncated at {MAX_CONTENT} chars]"
    return text


def ticket_no(email_id: str) -> str:
    """The row's unique key: the Message-Id, which is unique per email.

    perch passes "" when the email had no Message-Id header, so fall back to a
    generated uuid — a blank key would make the row impossible to point at.
    """
    stamped = email_id.strip().strip("<>")
    return stamped or f"noid-{uuid.uuid4()}"


def main(argv: list[str]) -> int:
    # perch always passes five arguments; pad so a manual invocation with
    # fewer doesn't IndexError, and reject a call with none at all.
    if len(argv) < 2:
        log("usage: lark_bitable.py <email_id> <subject> <body> <sender> "
            "<new_thread|reply_thread>")
        return 2
    args = (argv[1:6] + ["", "", "", "", ""])[:5]
    email_id, subject, body, sender, thread_flag = args

    if ONLY_NEW_THREADS and thread_flag != "new_thread":
        # Not a failure: the operator asked for new threads only.
        print(f"skipped ({thread_flag})")
        return 0

    for path in env_candidates():
        load_env(path)
    app_id = os.environ.get("APP_ID", "").strip()
    app_secret = os.environ.get("APP_SECRET", "").strip()
    if not app_id or not app_secret:
        log("APP_ID / APP_SECRET not set — put them in a .env next to this "
            f"script or set LARK_ENV_FILE (looked in: "
            f"{', '.join(str(p) for p in env_candidates())})")
        return 1

    fields = {
        # DateTime fields take epoch milliseconds. perch's hook contract does
        # not carry the email's Date header, so this is receive time — which is
        # within the poll interval of when it was sent.
        F_SEND_TIME: int(time.time() * 1000),
        F_SUBJECT: subject.strip() or "(no subject)",
        F_CONTENT: clean_body(body),
        F_TICKET_NO: ticket_no(email_id),
        F_SENDER: sender.strip(),
    }

    try:
        token = tenant_token(app_id, app_secret)
        record_id = create_record(token, fields)
    except Exception as exc:  # noqa: BLE001 — the message is the product here
        log(f"failed to record {fields[F_TICKET_NO]} from {sender!r}: {exc}")
        return 1

    print(f"recorded {record_id} ({thread_flag}) from {sender}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
