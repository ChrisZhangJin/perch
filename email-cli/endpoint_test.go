package main

import (
	"strings"
	"testing"
)

func TestResolveSMTPInfersFromDomain(t *testing.T) {
	cases := map[string]string{
		"a@163.com":     "smtp.163.com:465",
		"a@126.com":     "smtp.126.com:465",
		"a@qq.com":      "smtp.qq.com:465",
		"a@VIP.QQ.com":  "smtp.qq.com:465", // domain match is case-insensitive
		"a@foxmail.com": "smtp.qq.com:465",
	}
	for from, want := range cases {
		got, err := resolveSMTP("", "", from)
		if err != nil {
			t.Errorf("%s: %v", from, err)
			continue
		}
		if got != want {
			t.Errorf("%s -> %s, want %s", from, got, want)
		}
	}
}

func TestResolveSMTPRefusesToGuessUnknownDomain(t *testing.T) {
	// Guessing an enterprise domain's SMTP host produces a TLS error that
	// tells the user nothing; an explicit refusal names the way out.
	_, err := resolveSMTP("", "", "tommy@corp.example")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"--provider", "--smtp-addr", "easenet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestResolveSMTPExplicitProvider(t *testing.T) {
	// easenet is email-cli's own addition — 网易企业邮箱 is not in perch's
	// registry, which only lists mailboxes the daemon can also poll by IMAP.
	got, err := resolveSMTP("", "easenet", "tommy@corp.example")
	if err != nil {
		t.Fatal(err)
	}
	if got != "smtp.qiye.163.com:465" {
		t.Errorf("easenet -> %s", got)
	}

	// Names from perch's registry still resolve, and override the domain.
	got, err = resolveSMTP("", "QQ", "a@163.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "smtp.qq.com:465" {
		t.Errorf("--provider qq -> %s", got)
	}
}

func TestResolveSMTPUnknownProvider(t *testing.T) {
	_, err := resolveSMTP("", "gmail", "a@163.com")
	if err == nil || !strings.Contains(err.Error(), "easenet") {
		t.Fatalf("err = %v, want it to list the valid providers", err)
	}
}

func TestResolveSMTPExplicitAddrWins(t *testing.T) {
	got, err := resolveSMTP("smtp.ym.163.com:994", "qq", "a@163.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != "smtp.ym.163.com:994" {
		t.Errorf("got %s", got)
	}
}

func TestResolveSMTPRejectsMalformedAddr(t *testing.T) {
	for _, addr := range []string{"smtp.163.com", "smtp.163.com:", ":465"} {
		if _, err := resolveSMTP(addr, "", "a@163.com"); err == nil {
			t.Errorf("%q was accepted", addr)
		}
	}
}

func TestDomainOf(t *testing.T) {
	cases := map[string]string{
		"a@163.com":    "163.com",
		"a@B.COM":      "b.com",
		"a@b@c.com":    "c.com", // the last @ wins, per RFC5322 local-part rules
		"no-at-sign":   "",
		"trailing-at@": "",
	}
	for in, want := range cases {
		if got := domainOf(in); got != want {
			t.Errorf("domainOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProviderNamesIncludesBuiltinsAndExtras(t *testing.T) {
	names := strings.Join(providerNames(), ",")
	for _, want := range []string{"163", "126", "qq", "easenet"} {
		if !strings.Contains(names, want) {
			t.Errorf("providerNames() = %s, missing %s", names, want)
		}
	}
}
