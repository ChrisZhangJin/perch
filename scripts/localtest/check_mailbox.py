#!/usr/bin/env python3
"""Print all messages in a GreenMail mailbox via IMAPS (:3993, self-signed cert).

Usage: check_mailbox.py <email> [password]
GreenMail runs with auth disabled, so any password is accepted.
"""
import imaplib
import ssl
import sys

email = sys.argv[1] if len(sys.argv) > 1 else "alice@perch.test"
password = sys.argv[2] if len(sys.argv) > 2 else "pw"

ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE

m = imaplib.IMAP4_SSL("127.0.0.1", 3993, ssl_context=ctx)
m.login(email, password)
m.select("INBOX")
_, data = m.search(None, "ALL")
ids = data[0].split()
print(f"{email}: {len(ids)} message(s) in INBOX")
for i in ids:
    _, msg = m.fetch(i, "(RFC822)")
    raw = msg[0][1].decode(errors="replace")
    for line in raw.splitlines():
        if line.startswith(("From:", "To:", "Subject:", "In-Reply-To:", "References:")):
            print("  " + line)
        if line.strip() == "":
            break
    # print first body line
    body = raw.split("\r\n\r\n", 1)
    if len(body) > 1:
        first = body[1].strip().splitlines()
        if first:
            print("  body> " + first[0])
    print("  ---")
m.logout()
