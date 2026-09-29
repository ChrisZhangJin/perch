package main

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/ChrisZhangJin/perch/internal/provider"
)

// extraProviders are SMTP endpoints email-cli knows about beyond perch's
// built-in registry (internal/provider, which carries 163 / 126 / qq because
// those are the mailboxes the daemon polls over IMAP).
//
// easenet is 网易企业邮箱. Its implicit-TLS SMTP lives on
// smtp.qiye.163.com:465 — smtp.ym.163.com accepts a TCP connection on 465 but
// never completes a TLS handshake there, so it is NOT an alias; point
// --smtp-addr at it explicitly with --starttls if you need that host.
//
// These live here rather than in internal/provider because that registry is
// the set of mailboxes perch can *receive* on (it pairs every SMTP address
// with an IMAP one and IMAP capability quirks), and email-cli only sends.
var extraProviders = map[string]string{
	"easenet": "smtp.qiye.163.com:465",
}

// domainProvider infers a provider from the sender's domain so the common
// cases need no --provider flag. Deliberately conservative: a domain that is
// not listed here is an enterprise/vanity domain whose SMTP host we cannot
// guess, and guessing wrong produces a confusing TLS error rather than a
// useful one.
var domainProvider = map[string]string{
	"163.com":     "163",
	"126.com":     "126",
	"qq.com":      "qq",
	"vip.qq.com":  "qq",
	"foxmail.com": "qq",
}

// providerNames lists every name resolveSMTP accepts, for error messages.
func providerNames() []string {
	names := []string{"163", "126", "qq"}
	for n := range extraProviders {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resolveSMTP picks the SMTP endpoint to dial, in this order:
//
//  1. an explicit --smtp-addr "host:port"
//  2. an explicit --provider name (extras first, then perch's registry)
//  3. the provider inferred from the sender's domain
//
// It returns "host:port". A sender on an unrecognised domain with no
// --provider / --smtp-addr is an error, not a guess.
func resolveSMTP(explicitAddr, providerName, fromAddr string) (string, error) {
	if explicitAddr != "" {
		host, port, err := net.SplitHostPort(explicitAddr)
		if err != nil || host == "" || port == "" {
			return "", fmt.Errorf("--smtp-addr %q must be host:port (e.g. smtp.163.com:465)", explicitAddr)
		}
		return explicitAddr, nil
	}

	name := strings.ToLower(strings.TrimSpace(providerName))
	if name == "" {
		domain := domainOf(fromAddr)
		if domain == "" {
			return "", fmt.Errorf("cannot infer SMTP host: sender %q has no domain; pass --provider (%s) or --smtp-addr host:port",
				fromAddr, strings.Join(providerNames(), " | "))
		}
		inferred, ok := domainProvider[domain]
		if !ok {
			return "", fmt.Errorf("cannot infer SMTP host for domain %q; pass --provider (%s) or --smtp-addr host:port"+
				"\nhint: 网易企业邮箱 (custom domain) is --provider easenet", domain,
				strings.Join(providerNames(), " | "))
		}
		name = inferred
	}

	if addr, ok := extraProviders[name]; ok {
		return addr, nil
	}
	p, err := provider.Lookup(name)
	if err != nil {
		return "", fmt.Errorf("unknown provider %q (valid: %s)", name, strings.Join(providerNames(), ", "))
	}
	return p.SMTPAddr, nil
}

// domainOf returns the lowercased domain of an address, or "" if there is no
// "@". The address is expected to be bare (already through mail.ParseAddress).
func domainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}
