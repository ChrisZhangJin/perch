#!/usr/bin/env python3
"""Inject a test task email into the local GreenMail server (plaintext SMTP :3025).

Usage: send_test_email.py <from> <to> [subject] [body]
Defaults simulate a whitelisted sender emailing the perch agent.
"""
import smtplib
import sys
from email.message import EmailMessage

frm = sys.argv[1] if len(sys.argv) > 1 else "alice@perch.test"
to = sys.argv[2] if len(sys.argv) > 2 else "agent@perch.test"
subject = sys.argv[3] if len(sys.argv) > 3 else "Please do the thing"
body = sys.argv[4] if len(sys.argv) > 4 else "Hi agent, here is a task. Please handle it."

msg = EmailMessage()
msg["From"] = frm
msg["To"] = to
msg["Subject"] = subject
msg["Message-ID"] = f"<test-{subject.replace(' ', '-')}@perch.test>"
msg.set_content(body)

with smtplib.SMTP("127.0.0.1", 3025) as s:
    s.send_message(msg)
print(f"sent: {frm} -> {to} | {subject!r}")
