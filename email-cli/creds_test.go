package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeGrant(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name+".grant_code")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveCredsFromEnv(t *testing.T) {
	t.Setenv(envEmail, "you@163.com")
	t.Setenv(envAuthCode, "abcdefghijklmnop")

	got, err := resolveCreds(credOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "you@163.com" || got.Code != "abcdefghijklmnop" {
		t.Errorf("got %+v", got)
	}
	if got.Path != "" {
		t.Errorf("Path = %q, want empty for an env-sourced code", got.Path)
	}
}

func TestResolveCredsFlagsBeatEnv(t *testing.T) {
	dir := t.TempDir()
	writeGrant(t, dir, "tommy", "from-file-code\n")
	t.Setenv(envEmail, "env@163.com")
	t.Setenv(envAuthCode, "env-code")

	got, err := resolveCreds(credOpts{From: "flag@163.com", Account: "tommy", GrantDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// A typed flag is a stated intent; the environment is whatever the shell
	// happened to be carrying.
	if got.Email != "flag@163.com" {
		t.Errorf("Email = %q, want the --from value", got.Email)
	}
	if got.Code != "from-file-code" {
		t.Errorf("Code = %q, want the --account file to win over $%s", got.Code, envAuthCode)
	}
}

func TestResolveCredsTrimsFileContent(t *testing.T) {
	dir := t.TempDir()
	writeGrant(t, dir, "x", "  code-with-space  \n\n")
	got, err := resolveCreds(credOpts{From: "a@163.com", Account: "x", GrantDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	// An editor's trailing newline would otherwise become part of the
	// password and produce an unexplained 535.
	if got.Code != "code-with-space" {
		t.Errorf("Code = %q", got.Code)
	}
}

func TestResolveCredsEmptyFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeGrant(t, dir, "empty", "\n")
	_, err := resolveCreds(credOpts{From: "a@163.com", Account: "empty", GrantDir: dir})
	if err == nil {
		t.Fatal("expected an error for an empty grant file")
	}
	// The message must point at the file, not leave the user staring at a 535.
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v", err)
	}
}

func TestResolveCredsGrantFileBeatsAccount(t *testing.T) {
	dir := t.TempDir()
	writeGrant(t, dir, "acct", "account-code")
	explicit := writeGrant(t, dir, "explicit", "explicit-code")

	got, err := resolveCreds(credOpts{From: "a@163.com", Account: "acct", GrantFile: explicit, GrantDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != "explicit-code" {
		t.Errorf("Code = %q, want --grant-file to win", got.Code)
	}
}

func TestResolveCredsMissingInputs(t *testing.T) {
	t.Run("no address", func(t *testing.T) {
		t.Setenv(envEmail, "")
		t.Setenv(envAuthCode, "code")
		_, err := resolveCreds(credOpts{})
		if err == nil || !strings.Contains(err.Error(), envEmail) {
			t.Fatalf("err = %v, want it to name $%s", err, envEmail)
		}
	})
	t.Run("no code", func(t *testing.T) {
		t.Setenv(envEmail, "you@163.com")
		t.Setenv(envAuthCode, "")
		_, err := resolveCreds(credOpts{})
		if err == nil || !strings.Contains(err.Error(), envAuthCode) {
			t.Fatalf("err = %v, want it to name $%s", err, envAuthCode)
		}
	})
	t.Run("missing account file", func(t *testing.T) {
		_, err := resolveCreds(credOpts{From: "a@163.com", Account: "nope", GrantDir: t.TempDir()})
		if err == nil || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("err = %v, want it to name the account", err)
		}
	})
}

func TestGrantPathHonoursEnvDir(t *testing.T) {
	t.Setenv(envGrantDir, "/custom/grants")
	if got := grantPath("", "tommy"); got != "/custom/grants/tommy.grant_code" {
		t.Errorf("grantPath = %q", got)
	}
	if got := grantPath("/flag/dir", "tommy"); got != "/flag/dir/tommy.grant_code" {
		t.Errorf("--grant-dir should beat $%s, got %q", envGrantDir, got)
	}
	t.Setenv(envGrantDir, "")
	if got := grantPath("", "tommy"); got != filepath.Join(defaultGrantDir, "tommy.grant_code") {
		t.Errorf("grantPath = %q", got)
	}
}

func TestWarnIfWorldReadable(t *testing.T) {
	dir := t.TempDir()
	open := filepath.Join(dir, "open.grant_code")
	if err := os.WriteFile(open, []byte("code"), 0o644); err != nil {
		t.Fatal(err)
	}
	tight := writeGrant(t, dir, "tight", "code") // written 0600

	var warnings []string
	warn := func(f string, a ...any) { warnings = append(warnings, f) }

	warnIfWorldReadable(tight, warn)
	if len(warnings) != 0 {
		t.Errorf("0600 file warned: %v", warnings)
	}
	warnIfWorldReadable(open, warn)
	if len(warnings) != 1 {
		t.Errorf("0644 file did not warn: %v", warnings)
	}
}
