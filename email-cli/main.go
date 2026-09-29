// Command email-cli sends one email over SMTP using a mailbox address and an
// authorization code ("授权码") taken from the environment.
//
// It reuses perch's environment convention — AGENT_EMAIL and AGENT_AUTH_CODE —
// so a shell configured to run the daemon can run this with no extra setup,
// and falls back to the per-account files in grant/ that perch users already
// keep (see --account).
//
// Exit codes are distinct so scripts can branch on the failure kind:
//
//	0  sent
//	2  usage or configuration error (bad flag, missing credentials)
//	3  SMTP authentication rejected the account/authorization code
//	4  send failed (network, or the server refused every recipient)
//	5  partial delivery — some recipients were rejected, the rest got it
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// version is stamped by the build (-ldflags "-X main.version=...").
var version = "dev"

// Exit codes. See the package comment.
const (
	exitOK      = 0
	exitUsage   = 2
	exitAuth    = 3
	exitSend    = 4
	exitPartial = 5
)

// defaultMaxAttachBytes caps the total size of all attachments. 25 MiB is the
// ceiling 163/126/QQ enforce on a single message; going over produces a 552
// after the whole body has already been uploaded, which is a slow way to find
// out. Base64 inflates the wire size by ~4/3, so this is a soft bound.
const defaultMaxAttachBytes = 25 << 20

// stringList collects a repeatable flag, additionally splitting each value on
// commas so both of these work:
//
//	--to a@x.com --to b@y.com
//	--to "a@x.com,b@y.com"
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error {
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

// rawList collects a repeatable flag verbatim, with no comma splitting —
// for values that legitimately contain commas (file paths, header values).
type rawList []string

func (l *rawList) String() string { return strings.Join(*l, ",") }
func (l *rawList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

type options struct {
	to, cc, bcc                  stringList
	attach, headers              rawList
	from, fromName, replyTo      string
	subject                      string
	body, bodyFile               string
	html, htmlFile               string
	inReplyTo                    string
	references                   stringList
	provider, smtpAddr           string
	account, grantFile, grantDir string
	starttls, tlsInsecure        bool
	timeout                      time.Duration
	maxAttempts                  int
	maxAttachBytes               int64
	dryRun, verbose, quiet       bool
	showVersion                  bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main's testable body: everything goes through the passed streams and
// it returns an exit code rather than calling os.Exit.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var o options
	fs := flag.NewFlagSet("email-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr, fs) }

	fs.Var(&o.to, "to", "recipient address; repeatable, or comma-separated")
	fs.Var(&o.cc, "cc", "Cc address; repeatable, or comma-separated")
	fs.Var(&o.bcc, "bcc", "Bcc address (envelope only, never a header); repeatable")
	fs.StringVar(&o.subject, "subject", "", "subject line (UTF-8 is RFC2047-encoded automatically)")
	fs.StringVar(&o.subject, "s", "", "shorthand for --subject")
	fs.StringVar(&o.body, "body", "", "plain-text body")
	fs.StringVar(&o.body, "b", "", "shorthand for --body")
	fs.StringVar(&o.bodyFile, "body-file", "", `read the plain-text body from a file ("-" means stdin)`)
	fs.StringVar(&o.html, "html", "", "HTML body")
	fs.StringVar(&o.htmlFile, "html-file", "", `read the HTML body from a file ("-" means stdin)`)
	fs.Var(&o.attach, "attach", "file to attach; repeatable")
	fs.Var(&o.headers, "header", `extra header as "Name: value"; repeatable`)
	fs.StringVar(&o.from, "from", "", "sender address (default $"+envEmail+")")
	fs.StringVar(&o.fromName, "from-name", "", "display name shown next to the sender address")
	fs.StringVar(&o.replyTo, "reply-to", "", "Reply-To address")
	fs.StringVar(&o.inReplyTo, "in-reply-to", "", "Message-ID this mail replies to (threading)")
	fs.Var(&o.references, "references", "References header Message-IDs; repeatable")
	fs.StringVar(&o.provider, "provider", "", "SMTP provider: "+strings.Join(providerNames(), " | ")+" (default: inferred from the sender domain)")
	fs.StringVar(&o.smtpAddr, "smtp-addr", "", "SMTP endpoint host:port, overriding --provider")
	fs.StringVar(&o.account, "account", "", "read the authorization code from <grant-dir>/<account>.grant_code")
	fs.StringVar(&o.grantFile, "grant-file", "", "read the authorization code from this exact path")
	fs.StringVar(&o.grantDir, "grant-dir", "", "directory holding *.grant_code (default $"+envGrantDir+", then ./"+defaultGrantDir+")")
	fs.BoolVar(&o.starttls, "starttls", false, "dial plaintext and upgrade with STARTTLS (for :587) instead of implicit TLS")
	fs.BoolVar(&o.tlsInsecure, "tls-insecure", false, "skip TLS certificate verification (development only)")
	fs.DurationVar(&o.timeout, "timeout", 30*time.Second, "per-attempt network timeout")
	fs.IntVar(&o.maxAttempts, "max-attempts", 3, "SMTP attempts before giving up, including the first")
	fs.Int64Var(&o.maxAttachBytes, "max-attach-bytes", defaultMaxAttachBytes, "reject the send if attachments total more than this")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print the composed message and the envelope; do not connect")
	fs.BoolVar(&o.verbose, "verbose", false, "log resolution steps and per-attempt progress to stderr")
	fs.BoolVar(&o.quiet, "quiet", false, "suppress the success line and advisory warnings")
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if o.showVersion {
		fmt.Fprintf(stdout, "email-cli %s\n", version)
		return exitOK
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "email-cli: unexpected argument %q (all input is flags)\n", fs.Arg(0))
		return exitUsage
	}

	logf := func(format string, a ...any) {
		if o.verbose {
			fmt.Fprintf(stderr, "email-cli: "+format+"\n", a...)
		}
	}
	warnf := func(format string, a ...any) {
		if !o.quiet {
			fmt.Fprintf(stderr, "email-cli: warning: "+format+"\n", a...)
		}
	}

	code, err := send(&o, stdin, stdout, logf, warnf)
	if err != nil {
		fmt.Fprintf(stderr, "email-cli: %v\n", err)
	}
	return code
}

// send does the real work and returns the exit code plus the error to print.
func send(o *options, stdin io.Reader, stdout io.Writer, logf, warnf func(string, ...any)) (int, error) {
	// --- credentials -------------------------------------------------------
	cr, err := resolveCreds(credOpts{
		From:      o.from,
		Account:   o.account,
		GrantFile: o.grantFile,
		GrantDir:  o.grantDir,
	})
	if err != nil {
		return exitUsage, err
	}
	logf("sender %s, authorization code from %s", cr.Email, cr.Source)
	if cr.Path != "" {
		warnIfWorldReadable(cr.Path, warnf)
	}

	// --- message -----------------------------------------------------------
	msg, err := buildMessage(o, cr, stdin)
	if err != nil {
		return exitUsage, err
	}
	raw, err := msg.Build()
	if err != nil {
		return exitUsage, err
	}
	rcpts := msg.Recipients()

	// --- endpoint ----------------------------------------------------------
	addr, err := resolveSMTP(o.smtpAddr, o.provider, cr.Email)
	if err != nil {
		return exitUsage, err
	}
	logf("smtp endpoint %s (%s)", addr, tlsMode(o.starttls))

	if o.dryRun {
		fmt.Fprintf(stdout, "; dry run — nothing was sent\n")
		fmt.Fprintf(stdout, "; smtp   %s (%s)\n", addr, tlsMode(o.starttls))
		fmt.Fprintf(stdout, "; auth   %s\n", cr.Email)
		fmt.Fprintf(stdout, "; rcpt   %s\n", strings.Join(rcpts, ", "))
		fmt.Fprintf(stdout, "; size   %d bytes\n\n", len(raw))
		stdout.Write(raw)
		return exitOK, nil
	}

	// --- send --------------------------------------------------------------
	s := &sender{
		Addr:        addr,
		User:        cr.Email,
		Pass:        cr.Code,
		STARTTLS:    o.starttls,
		Insecure:    o.tlsInsecure,
		Timeout:     o.timeout,
		MaxAttempts: o.maxAttempts,
		onAttempt: func(attempt int, err error) {
			if err != nil {
				logf("attempt %d failed: %v", attempt, err)
			} else {
				logf("attempt %d ok", attempt)
			}
		},
	}
	res, err := s.send(cr.Email, rcpts, raw)
	if err != nil {
		if errors.Is(err, ErrAuth) {
			return exitAuth, fmt.Errorf("%w\nhint: the authorization code is the one the provider issues for SMTP, not the web login password", err)
		}
		return exitSend, err
	}

	for addr, rerr := range res.Rejected {
		warnf("recipient rejected: %s: %v", addr, rerr)
	}
	if !o.quiet {
		fmt.Fprintf(stdout, "sent %d bytes to %s\n", len(raw), strings.Join(res.Accepted, ", "))
	}
	if len(res.Rejected) > 0 {
		return exitPartial, nil
	}
	return exitOK, nil
}

// buildMessage turns the flags into a Message, reading bodies and attachments
// off disk (or stdin) as it goes.
func buildMessage(o *options, cr creds, stdin io.Reader) (*Message, error) {
	fromAddr, err := mail.ParseAddress(cr.Email)
	if err != nil {
		return nil, fmt.Errorf("invalid sender address %q: %w", cr.Email, err)
	}
	if o.fromName != "" {
		fromAddr.Name = o.fromName
	}

	to, err := parseAddrList(o.to)
	if err != nil {
		return nil, err
	}
	cc, err := parseAddrList(o.cc)
	if err != nil {
		return nil, err
	}
	bcc, err := parseAddrList(o.bcc)
	if err != nil {
		return nil, err
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		return nil, fmt.Errorf("no recipients: pass at least one --to (or --cc / --bcc)")
	}

	var replyTo *mail.Address
	if o.replyTo != "" {
		if replyTo, err = mail.ParseAddress(o.replyTo); err != nil {
			return nil, fmt.Errorf("invalid --reply-to: %w", err)
		}
	}

	text, html, err := resolveBodies(o, stdin)
	if err != nil {
		return nil, err
	}

	attachments, err := readAttachments(o.attach, o.maxAttachBytes)
	if err != nil {
		return nil, err
	}

	extra, err := parseHeaders(o.headers)
	if err != nil {
		return nil, err
	}

	return &Message{
		From:       fromAddr,
		ReplyTo:    replyTo,
		To:         to,
		Cc:         cc,
		Bcc:        bcc,
		Subject:    o.subject,
		Text:       text,
		HTML:       html,
		Attach:     attachments,
		Extra:      extra,
		InReplyTo:  o.inReplyTo,
		References: o.references,
	}, nil
}

// resolveBodies works out the plain-text and HTML bodies.
//
// Precedence per body is flag > file. Stdin is consumed when a --*-file is
// "-", and otherwise when nothing named a body at all and stdin is a pipe —
// so `echo hi | email-cli --to x` does what it looks like, while an
// interactive `email-cli --to x` with no body fails fast instead of hanging
// on a terminal read.
func resolveBodies(o *options, stdin io.Reader) (text, html string, err error) {
	stdinUsed := false
	read := func(path string) (string, error) {
		if path == "-" {
			if stdinUsed {
				return "", fmt.Errorf(`stdin can only be read once; "-" was given twice`)
			}
			stdinUsed = true
			b, err := io.ReadAll(stdin)
			return string(b), err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read body: %w", err)
		}
		return string(b), nil
	}

	text = o.body
	if text == "" && o.bodyFile != "" {
		if text, err = read(o.bodyFile); err != nil {
			return "", "", err
		}
	}
	html = o.html
	if html == "" && o.htmlFile != "" {
		if html, err = read(o.htmlFile); err != nil {
			return "", "", err
		}
	}

	if text == "" && html == "" {
		if !stdinIsPipe(stdin) {
			return "", "", fmt.Errorf("no body: pass --body, --body-file, --html, --html-file, or pipe the body on stdin")
		}
		b, rerr := io.ReadAll(stdin)
		if rerr != nil {
			return "", "", fmt.Errorf("read body from stdin: %w", rerr)
		}
		text = string(b)
		if strings.TrimSpace(text) == "" {
			return "", "", fmt.Errorf("body read from stdin is empty")
		}
	}
	return text, html, nil
}

// stdinIsPipe reports whether stdin is redirected (a pipe or a file) rather
// than an interactive terminal. Anything that is not *os.File — i.e. a test
// injecting a buffer — counts as redirected.
func stdinIsPipe(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	if !ok {
		return true
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// readAttachments loads every --attach path, failing if the running total
// exceeds limit. Reading into memory is deliberate: the message has to be one
// contiguous DATA payload anyway, and the limit keeps that bounded.
func readAttachments(paths []string, limit int64) ([]Attachment, error) {
	var total int64
	out := make([]Attachment, 0, len(paths))
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("attachment: %w", err)
		}
		if fi.IsDir() {
			return nil, fmt.Errorf("attachment %s is a directory", p)
		}
		total += fi.Size()
		if limit > 0 && total > limit {
			return nil, fmt.Errorf("attachments total %d bytes, over the %d-byte --max-attach-bytes limit", total, limit)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("attachment: %w", err)
		}
		out = append(out, Attachment{Filename: filepath.Base(p), Data: data})
	}
	return out, nil
}

// parseHeaders splits each --header "Name: value" into its two halves.
func parseHeaders(vals []string) ([]Header, error) {
	out := make([]Header, 0, len(vals))
	for _, v := range vals {
		name, value, ok := strings.Cut(v, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf(`--header %q must be "Name: value"`, v)
		}
		out = append(out, Header{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return out, nil
}

func tlsMode(starttls bool) string {
	if starttls {
		return "STARTTLS"
	}
	return "implicit TLS"
}

func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(w, `email-cli %s — send one email over SMTP.

Credentials come from the environment, matching perch's convention:
  %s      the mailbox address              (or --from)
  %s  the authorization code (授权码)   (or --account / --grant-file)

There is no flag for the code itself: an argv secret is visible in ps and
lands in shell history.

Examples:
  export %s=you@163.com %s=xxxxxxxxxxxxxxxx
  email-cli --to a@example.com -s "日报" -b "今天完成了 X"

  # body on stdin, two recipients, one attachment
  cat report.md | email-cli --to "a@x.com,b@y.com" -s "周报" --attach chart.png

  # 网易企业邮箱 account whose code lives in grant/easenet.tommy.grant_code
  email-cli --from tommy@corp.example --account easenet.tommy \
            --provider easenet --to boss@corp.example -s hi -b hi

  # see exactly what would go on the wire
  email-cli --to a@x.com -s test -b test --dry-run

Exit codes: 0 sent · 2 usage/config · 3 auth rejected · 4 send failed
            5 partial delivery (some recipients rejected)

Flags:
`, version, envEmail, envAuthCode, envEmail, envAuthCode)
	fs.PrintDefaults()
}
