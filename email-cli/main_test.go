package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI drives run() with in-memory streams and returns (exit code, stdout, stderr).
func runCLI(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errb)
	return code, out.String(), errb.String()
}

// withEnvCreds points the process at a plausible mailbox for the dry-run path.
func withEnvCreds(t *testing.T) {
	t.Helper()
	t.Setenv(envEmail, "sender@163.com")
	t.Setenv(envAuthCode, "abcdefghijklmnop")
}

func TestDryRunPrintsMessageAndSendsNothing(t *testing.T) {
	withEnvCreds(t)
	code, out, _ := runCLI(t, "", "--to", "rcpt@example.com", "-s", "你好", "-b", "正文", "--dry-run")
	if code != exitOK {
		t.Fatalf("exit = %d, out=%q", code, out)
	}
	for _, want := range []string{"dry run", "smtp.163.com:465", "rcpt@example.com", "From: <sender@163.com>", "MIME-Version: 1.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q:\n%s", want, out)
		}
	}
	// The authorization code has no business on stdout.
	if strings.Contains(out, "abcdefghijklmnop") {
		t.Fatal("the authorization code leaked into --dry-run output")
	}
}

func TestBodyFromStdin(t *testing.T) {
	withEnvCreds(t)
	code, out, _ := runCLI(t, "piped body text\n", "--to", "a@example.com", "-s", "x", "--dry-run")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "piped body text") {
		t.Errorf("stdin body not used:\n%s", out)
	}
}

func TestBodyFileDashReadsStdin(t *testing.T) {
	withEnvCreds(t)
	code, out, _ := runCLI(t, "from dash\n", "--to", "a@example.com", "-s", "x", "--body-file", "-", "--dry-run")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "from dash") {
		t.Errorf(`--body-file - did not read stdin:\n%s`, out)
	}
}

func TestCommaSeparatedAndRepeatedRecipients(t *testing.T) {
	withEnvCreds(t)
	code, out, _ := runCLI(t, "",
		"--to", "a@example.com,b@example.com", "--to", "c@example.com",
		"--cc", "d@example.com", "--bcc", "hidden@example.com",
		"-s", "x", "-b", "y", "--dry-run")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	envelope := firstLineWith(out, "; rcpt")
	for _, want := range []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com", "hidden@example.com"} {
		if !strings.Contains(envelope, want) {
			t.Errorf("envelope %q missing %s", envelope, want)
		}
	}
	// Bcc belongs to the envelope only — a Bcc header would show every
	// "blind" recipient to everyone else.
	body := out[strings.Index(out, "From:"):]
	if strings.Contains(body, "hidden@example.com") {
		t.Error("Bcc recipient appeared in the message headers")
	}
}

func TestUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no recipients":   {"-s", "x", "-b", "y", "--dry-run"},
		"bad address":     {"--to", "not-an-address", "-s", "x", "-b", "y", "--dry-run"},
		"bad header form": {"--to", "a@example.com", "-b", "y", "--header", "no-colon", "--dry-run"},
		"owned header":    {"--to", "a@example.com", "-b", "y", "--header", "Subject: dup", "--dry-run"},
		"stray argument":  {"--to", "a@example.com", "-b", "y", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			withEnvCreds(t)
			code, _, errOut := runCLI(t, "", args...)
			if code != exitUsage {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitUsage, errOut)
			}
		})
	}
}

func TestMissingCredentialsIsUsageError(t *testing.T) {
	t.Setenv(envEmail, "")
	t.Setenv(envAuthCode, "")
	code, _, errOut := runCLI(t, "", "--to", "a@example.com", "-s", "x", "-b", "y", "--dry-run")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, envEmail) {
		t.Errorf("stderr should name $%s: %s", envEmail, errOut)
	}
}

func TestAttachmentLimitIsEnforcedBeforeSending(t *testing.T) {
	withEnvCreds(t)
	dir := t.TempDir()
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, 2048), 0o600); err != nil {
		t.Fatal(err)
	}
	// Finding out from the server's 552 means uploading the whole thing first.
	code, _, errOut := runCLI(t, "", "--to", "a@example.com", "-s", "x", "-b", "y",
		"--attach", big, "--max-attach-bytes", "1024", "--dry-run")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "max-attach-bytes") {
		t.Errorf("stderr = %s", errOut)
	}
}

func TestAttachmentAppearsInDryRun(t *testing.T) {
	withEnvCreds(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, "", "--to", "a@example.com", "-s", "x", "-b", "y", "--attach", path, "--dry-run")
	if code != exitOK {
		t.Fatalf("exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "multipart/mixed") || !strings.Contains(out, "report.txt") {
		t.Errorf("attachment missing from output:\n%s", out)
	}
}

func TestExplicitProviderOverridesDomain(t *testing.T) {
	withEnvCreds(t) // sender@163.com would otherwise infer smtp.163.com
	code, out, _ := runCLI(t, "", "--to", "a@example.com", "-s", "x", "-b", "y",
		"--provider", "easenet", "--dry-run")
	if code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "smtp.qiye.163.com:465") {
		t.Errorf("provider override ignored:\n%s", out)
	}
}

func TestAccountFlagReadsGrantFile(t *testing.T) {
	t.Setenv(envEmail, "tommy@corp.example")
	t.Setenv(envAuthCode, "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "easenet.tommy.grant_code"), []byte("file-code\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI(t, "", "--to", "a@example.com", "-s", "x", "-b", "y",
		"--account", "easenet.tommy", "--grant-dir", dir, "--provider", "easenet", "--dry-run")
	if code != exitOK {
		t.Fatalf("exit = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "; auth   tommy@corp.example") {
		t.Errorf("output:\n%s", out)
	}
	if strings.Contains(out+errOut, "file-code") {
		t.Fatal("the authorization code leaked into the output")
	}
}

func TestVersionFlag(t *testing.T) {
	code, out, _ := runCLI(t, "", "--version")
	if code != exitOK || !strings.Contains(out, "email-cli") {
		t.Fatalf("exit=%d out=%q", code, out)
	}
}

func TestParseHeaders(t *testing.T) {
	got, err := parseHeaders([]string{"X-Ticket: ABC-123", "X-Empty:"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "X-Ticket" || got[0].Value != "ABC-123" {
		t.Fatalf("got %+v", got)
	}
	if _, err := parseHeaders([]string{"missing separator"}); err == nil {
		t.Fatal("expected an error")
	}
}

func firstLineWith(s, prefix string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}
