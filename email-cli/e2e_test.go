package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// newFakeSMTPTLS is fakeSMTP behind implicit TLS, matching the :465 endpoints
// the providers actually run. Callers reach it with --smtp-addr and
// --tls-insecure, so these tests exercise run() end to end: flag parsing,
// credential resolution, composition, the real tls.Dial path, and the exit
// code mapping.
func newFakeSMTPTLS(t *testing.T) *fakeSMTP {
	t.Helper()
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := tls.NewListener(raw, &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t)}})
	f := &fakeSMTP{ln: ln, authOK: true, rejectRcpt: map[string]string{}}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestEndToEndSend(t *testing.T) {
	withEnvCreds(t)
	f := newFakeSMTPTLS(t)

	code, out, errOut := runCLI(t, "",
		"--smtp-addr", f.addr(), "--tls-insecure",
		"--to", "rcpt@example.com", "--cc", "copy@example.com", "--bcc", "hidden@example.com",
		"-s", "季度报告", "-b", "正文第一行\n正文第二行",
		"--header", "X-Ticket: ABC-123",
	)
	if code != exitOK {
		t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(out, "sent ") {
		t.Errorf("stdout = %q", out)
	}

	_, sessions := f.snapshot()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d", len(sessions))
	}
	s := sessions[0]
	if strings.Join(s.rcpt, ",") != "rcpt@example.com,copy@example.com,hidden@example.com" {
		t.Errorf("envelope = %v", s.rcpt)
	}
	if !strings.Contains(s.data, "X-Ticket: ABC-123") {
		t.Errorf("custom header missing from DATA:\n%s", s.data)
	}
	if strings.Contains(s.data, "hidden@example.com") {
		t.Error("Bcc leaked into the delivered headers")
	}
	// The subject must have travelled as an encoded-word, not raw UTF-8.
	if !strings.Contains(s.data, "Subject: =?utf-8?b?") {
		t.Errorf("subject was not RFC2047-encoded:\n%s", firstLineWith(s.data, "Subject:"))
	}
}

func TestEndToEndAuthFailureExitCode(t *testing.T) {
	withEnvCreds(t)
	f := newFakeSMTPTLS(t)
	f.authOK = false

	code, _, errOut := runCLI(t, "",
		"--smtp-addr", f.addr(), "--tls-insecure",
		"--to", "rcpt@example.com", "-s", "x", "-b", "y",
	)
	if code != exitAuth {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitAuth, errOut)
	}
	// A 535 almost always means the web login password was used instead of
	// the provider's authorization code; say so rather than echoing the 535.
	if !strings.Contains(errOut, "authorization code") {
		t.Errorf("stderr should explain the likely cause: %s", errOut)
	}
	if strings.Contains(errOut, "abcdefghijklmnop") {
		t.Fatal("the authorization code leaked into stderr")
	}
}

func TestEndToEndPartialDeliveryExitCode(t *testing.T) {
	withEnvCreds(t)
	f := newFakeSMTPTLS(t)
	f.rejectRcpt["bad@example.com"] = "550 5.1.1 User unknown"

	code, out, errOut := runCLI(t, "",
		"--smtp-addr", f.addr(), "--tls-insecure",
		"--to", "bad@example.com,good@example.com", "-s", "x", "-b", "y",
	)
	if code != exitPartial {
		t.Fatalf("exit = %d, want %d", code, exitPartial)
	}
	if !strings.Contains(errOut, "bad@example.com") {
		t.Errorf("stderr should name the rejected recipient: %s", errOut)
	}
	if !strings.Contains(out, "good@example.com") {
		t.Errorf("stdout should confirm the delivered recipient: %s", out)
	}
}

func TestEndToEndSendFailureExitCode(t *testing.T) {
	withEnvCreds(t)
	// Nothing is listening here: net.Listen then close frees the port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()

	code, _, errOut := runCLI(t, "",
		"--smtp-addr", dead, "--tls-insecure", "--max-attempts", "1", "--timeout", "2s",
		"--to", "rcpt@example.com", "-s", "x", "-b", "y",
	)
	if code != exitSend {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitSend, errOut)
	}
}
