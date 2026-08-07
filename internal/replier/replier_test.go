package replier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeHeaders(t *testing.T) {
	msg := Compose(
		"agent@163.com",
		"alice@163.com",
		"Do X",
		"<root-1@163.com>",
		[]string{"<thread-root@163.com>", "<root-1@163.com>"},
		"here is the result",
	)
	s := string(msg)
	checks := []string{
		"From: agent@163.com",
		"To: alice@163.com",
		"Subject: Re: Do X",
		"In-Reply-To: <root-1@163.com>",
		"References: <thread-root@163.com> <root-1@163.com>",
		"Content-Type: text/plain; charset=utf-8",
		"here is the result",
	}
	for _, c := range checks {
		if !strings.Contains(s, c) {
			t.Errorf("composed message missing %q\n---\n%s", c, s)
		}
	}
}

func TestComposeDoesNotDoubleRe(t *testing.T) {
	msg := Compose("a@x", "b@x", "Re: Already", "<i@x>", nil, "body")
	if strings.Contains(string(msg), "Subject: Re: Re: Already") {
		t.Error("should not double-prefix Re:")
	}
}

func TestSanitizeHeaderStripsCRLF(t *testing.T) {
	got := sanitizeHeader("evil\r\nBcc: victim@x")
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("CRLF not stripped: %q", got)
	}
}

func TestComposeWithAttachments(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(f, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	msg, err := ComposeWithAttachments(
		"agent@163.com", "alice@163.com", "Do X", "<root-1@163.com>",
		[]string{"<thread-root@163.com>"}, "here is the result", []string{f},
	)
	if err != nil {
		t.Fatal(err)
	}
	s := string(msg)
	for _, c := range []string{
		"Subject: Re: Do X",
		"Content-Type: multipart/mixed",
		"Content-Disposition: attachment; filename=report.txt",
		"ZGF0YQ==", // base64("data")
	} {
		if !strings.Contains(s, c) {
			t.Errorf("multipart message missing %q\n---\n%s", c, s)
		}
	}
}
