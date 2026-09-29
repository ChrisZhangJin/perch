# email-cli

Send one email over SMTP, using a mailbox address and an authorization code
(授权码) from the environment.

It is part of the perch module but is a standalone binary with no dependency
on the daemon: the only thing it borrows is `internal/provider`, for the
built-in SMTP endpoints. Everything else is the Go standard library.

```
make build-email          # -> ./bin/email-cli
```

## Credentials

email-cli reuses perch's environment convention, so a shell already set up to
run the daemon needs nothing extra:

| variable          | meaning                                    | flag override             |
|-------------------|--------------------------------------------|---------------------------|
| `AGENT_EMAIL`     | the mailbox address to send as             | `--from`                  |
| `AGENT_AUTH_CODE` | the authorization code (授权码)             | `--account`/`--grant-file`|
| `PERCH_GRANT_DIR` | where `*.grant_code` files live (`./grant`) | `--grant-dir`             |

Resolution order — explicit flags beat the ambient environment:

```
address:  --from        >  $AGENT_EMAIL
code:     --grant-file  >  --account  >  $AGENT_AUTH_CODE
```

**There is no `--grant-code` flag.** A secret passed in argv is visible in
`ps` to every user on the box and lands in shell history. Put it in the
environment or in a file.

The `--account` form reads `<grant-dir>/<account>.grant_code`, which is the
layout already in this repo (`grant/easenet.tommy.grant_code`, …). Those files
are gitignored. `chmod 600` them — email-cli prints a warning if the mode is
looser, since it is an SMTP password in cleartext.

The authorization code is **not** your web login password. Each provider
issues it separately (163/126: 设置 → POP3/SMTP/IMAP → 开启服务; QQ: 设置 →
账户 → 生成授权码). A 535 from the server almost always means the login
password was used by mistake; exit code 3 and the error text say so.

## Providers

The SMTP host is inferred from the sender's domain when it can be:

| domain                              | endpoint                  |
|-------------------------------------|---------------------------|
| `163.com`                           | `smtp.163.com:465`        |
| `126.com`                           | `smtp.126.com:465`        |
| `qq.com`, `vip.qq.com`, `foxmail.com` | `smtp.qq.com:465`       |

A custom/enterprise domain cannot be guessed, so email-cli refuses rather than
producing a confusing TLS error. Name it instead:

```
--provider easenet            # 网易企业邮箱 -> smtp.qiye.163.com:465
--smtp-addr host:port         # anything else
--smtp-addr host:587 --starttls
```

All the built-in endpoints are implicit TLS on `:465`. Use `--starttls` for
`:587`-style endpoints that upgrade an initially-plaintext connection.

## Usage

```sh
export AGENT_EMAIL=you@163.com AGENT_AUTH_CODE=xxxxxxxxxxxxxxxx

# the simple case
email-cli --to a@example.com -s "日报" -b "今天完成了 X"

# body on stdin, several recipients, an attachment
cat report.md | email-cli --to "a@x.com,b@y.com" --cc boss@x.com \
                          -s "周报" --attach chart.png

# HTML with a plain-text fallback (sent as multipart/alternative)
email-cli --to a@x.com -s "release" --body-file notes.txt --html-file notes.html

# 网易企业邮箱 account whose code lives in grant/easenet.tommy.grant_code
email-cli --from tommy@corp.example --account easenet.tommy --provider easenet \
          --to boss@corp.example -s hi -b hi

# print the exact bytes that would go on the wire; connect to nothing
email-cli --to a@x.com -s test -b test --dry-run
```

Bodies are resolved per type as `--body` > `--body-file` (where `-` means
stdin). If nothing names a body at all and stdin is a pipe, stdin becomes the
plain-text body — so `echo hi | email-cli --to x@y.com` does what it looks
like, while an interactive invocation with no body fails fast instead of
hanging on a terminal read.

`--bcc` recipients go in the SMTP envelope only, never into a header.

### Exit codes

| code | meaning                                                  |
|------|----------------------------------------------------------|
| 0    | sent                                                     |
| 2    | usage or configuration error (bad flag, missing creds)   |
| 3    | SMTP rejected the account / authorization code           |
| 4    | send failed (network, or every recipient was refused)    |
| 5    | partial delivery — some recipients rejected, rest sent   |

Code 5 exists because one bad address on a ten-recipient line should not cost
the other nine their copy: email-cli records the rejection, delivers to the
rest, and reports both.

Transient failures (`421`/`450`/`451`, dropped connections, timeouts) are
retried with exponential backoff, `--max-attempts` times. Authentication
failures and rejected addresses are permanent and fail immediately.

## Relationship to `internal/replier`

perch's replier sends mail too, but it composes *auto-replies*: it forces a
`Re: ` subject prefix and stamps `Auto-Submitted: auto-replied` so that two
perch instances talking to each other do not loop forever. Both are wrong for
a message a human asked for — an RFC3834-aware recipient suppresses its own
response to anything marked auto-replied. email-cli therefore has its own
composer, which additionally does three things the replier does not:

- RFC2047-encodes the subject, so a Chinese subject does not arrive as
  mojibake on servers that are strict about 8-bit headers
- emits a `Date` header, which threading and spam scoring both want
- quoted-printable-encodes bodies and wraps base64 attachments at 76 columns,
  keeping every line inside RFC5322's 998-octet limit

## Tests

```
go test ./email-cli/
```

The suite runs a real SMTP transaction against an in-process fake server —
plaintext on loopback for the transport-level tests, and behind a self-signed
TLS certificate for the end-to-end ones, so the production `tls.Dial` path and
the exit-code mapping are both covered. No test touches the network.
