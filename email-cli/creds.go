package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Environment variable names. These are perch's existing convention
// (internal/config/config.go reads the same two) so a shell that is already
// set up to run the daemon can run email-cli with no extra exports.
const (
	envEmail    = "AGENT_EMAIL"
	envAuthCode = "AGENT_AUTH_CODE"
	envGrantDir = "PERCH_GRANT_DIR"
)

// defaultGrantDir is where perch users keep their per-account authorization
// codes. *.grant_code is gitignored at the repo root.
const defaultGrantDir = "grant"

// creds is a resolved (address, authorization code) pair plus a human
// description of where the code came from, for --verbose. The code itself is
// never logged.
type creds struct {
	Email  string
	Code   string
	Source string // human description for --verbose, e.g. "$AGENT_AUTH_CODE"
	Path   string // file the code came from, "" when it came from the env
}

// credOpts are the command-line inputs that can override the environment.
type credOpts struct {
	From      string // --from
	Account   string // --account: names grant/<account>.grant_code
	GrantFile string // --grant-file: explicit path
	GrantDir  string // --grant-dir, defaults to $PERCH_GRANT_DIR then ./grant
}

// resolveCreds works out which mailbox to authenticate as and which
// authorization code ("授权码", what the providers hand out in place of your
// login password) to use.
//
// Address:  --from  >  $AGENT_EMAIL
// Code:     --grant-file  >  --account  >  $AGENT_AUTH_CODE
//
// Explicit flags beat the ambient environment, because the environment is
// whatever the shell happened to be carrying and a typed flag is a stated
// intent. There is deliberately no --grant-code flag: a secret passed as an
// argv element is visible in `ps` to every user on the box and lands in shell
// history.
func resolveCreds(o credOpts) (creds, error) {
	c := creds{Email: strings.TrimSpace(o.From)}
	if c.Email == "" {
		c.Email = strings.TrimSpace(os.Getenv(envEmail))
	}
	if c.Email == "" {
		return creds{}, fmt.Errorf("no sender address: pass --from you@example.com or set %s", envEmail)
	}

	switch {
	case o.GrantFile != "":
		code, err := readGrantFile(o.GrantFile)
		if err != nil {
			return creds{}, err
		}
		c.Code, c.Source, c.Path = code, o.GrantFile, o.GrantFile

	case o.Account != "":
		path := grantPath(o.GrantDir, o.Account)
		code, err := readGrantFile(path)
		if err != nil {
			return creds{}, fmt.Errorf("--account %s: %w", o.Account, err)
		}
		c.Code, c.Source, c.Path = code, path, path

	default:
		code := strings.TrimSpace(os.Getenv(envAuthCode))
		if code == "" {
			return creds{}, fmt.Errorf("no authorization code: set %s, or pass --account <name> (reads %s) or --grant-file <path>",
				envAuthCode, grantPath(o.GrantDir, "<name>"))
		}
		c.Code, c.Source = code, "$"+envAuthCode
	}
	return c, nil
}

// grantPath builds <dir>/<account>.grant_code, honouring $PERCH_GRANT_DIR
// when --grant-dir was not given.
func grantPath(dir, account string) string {
	if dir == "" {
		dir = os.Getenv(envGrantDir)
	}
	if dir == "" {
		dir = defaultGrantDir
	}
	return filepath.Join(dir, account+".grant_code")
}

// readGrantFile reads an authorization code from disk. The file holds the
// code and nothing else; surrounding whitespace and the trailing newline a
// text editor adds are stripped. An empty file is an error rather than an
// empty password, because an empty password produces a 535 from the server
// and that error does not point back at the empty file.
func readGrantFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read authorization code: %w", err)
	}
	code := strings.TrimSpace(string(b))
	if code == "" {
		return "", fmt.Errorf("authorization code file %s is empty", path)
	}
	return code, nil
}

// warnIfWorldReadable prints a one-line notice when a grant file's mode lets
// anyone on the box read the code. Advisory only — we do not refuse to send,
// because on a single-user container the mode is usually just the umask.
func warnIfWorldReadable(path string, warn func(string, ...any)) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if fi.Mode().Perm()&0o044 != 0 {
		warn("authorization code file %s is mode %04o (readable beyond its owner); chmod 600 %s", path, fi.Mode().Perm(), path)
	}
}
