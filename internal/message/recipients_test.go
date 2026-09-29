package message

import (
	"strings"
	"testing"
)

// TestParseRecipients pins the normalisation To/Cc share with From:
// lowercased addr-specs, display names dropped, header order kept. Mixed
// case matters because the cc-only check in the app compares against
// cfg.Email, which an operator writes however they like.
func TestParseRecipients(t *testing.T) {
	raw := "From: Alice <Alice@163.com>\r\n" +
		"To: \"Zhang, Chris\" <Agent@163.com>, bob@example.com\r\n" +
		"Cc: Carol <CAROL@Example.COM>\r\n" +
		"Subject: hi\r\nMessage-ID: <x@y>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n"

	m, err := Parse(strings.NewReader(raw), 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.To, ","); got != "agent@163.com,bob@example.com" {
		t.Errorf("To = %q", got)
	}
	if got := strings.Join(m.Cc, ","); got != "carol@example.com" {
		t.Errorf("Cc = %q", got)
	}
	// Case-insensitive lookup is what the cc-only guard depends on.
	if !HasRecipient(m.To, "AGENT@163.COM") {
		t.Error("HasRecipient should be case-insensitive")
	}
	if HasRecipient(m.Cc, "") || HasRecipient(nil, "a@b.c") {
		t.Error("empty needle or empty list must not match")
	}
}
