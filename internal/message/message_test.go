package message

import (
	"os"
	"strings"
	"testing"
)

func TestParseBasic(t *testing.T) {
	f, err := os.Open("testdata/basic.eml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	m, err := Parse(f, 42, 1024, 0)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.UID != 42 {
		t.Errorf("UID = %d", m.UID)
	}
	if m.From != "alice@163.com" {
		t.Errorf("From = %q", m.From)
	}
	if m.MessageID != "<root-1@163.com>" {
		t.Errorf("MessageID = %q", m.MessageID)
	}
	if m.InReplyTo != "<prev@163.com>" {
		t.Errorf("InReplyTo = %q", m.InReplyTo)
	}
	if len(m.References) != 2 || m.References[0] != "<thread-root@163.com>" {
		t.Errorf("References = %#v", m.References)
	}
	if m.Subject != "Please summarize this" {
		t.Errorf("Subject = %q", m.Subject)
	}
	if m.Body != "Hello agent, please summarize the attached log.\n" {
		t.Errorf("Body = %q", m.Body)
	}
}

func TestThreadRoot(t *testing.T) {
	withRefs := &Message{MessageID: "<self@x>", References: []string{"<root@x>", "<mid@x>"}}
	if withRefs.ThreadRoot() != "<root@x>" {
		t.Errorf("ThreadRoot with refs = %q", withRefs.ThreadRoot())
	}
	noRefs := &Message{MessageID: "<self@x>"}
	if noRefs.ThreadRoot() != "<self@x>" {
		t.Errorf("ThreadRoot no refs = %q", noRefs.ThreadRoot())
	}
}

func TestParseGBKBody(t *testing.T) {
	// Regression: real 163/126 mail is often GBK-encoded. The charset side-effect
	// import must decode it to UTF-8 rather than erroring on "unhandled charset".
	f, err := os.Open("testdata/gbk.eml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	m, err := Parse(f, 7, 4096, 0)
	if err != nil {
		t.Fatalf("Parse GBK: %v", err)
	}
	if m.From != "zhangjin0602@126.com" {
		t.Errorf("From = %q", m.From)
	}
	if !strings.Contains(m.Body, "你好") || !strings.Contains(m.Body, "GBK 编码") {
		t.Errorf("GBK body not decoded to UTF-8: %q", m.Body)
	}
}

func TestParseTruncatesBody(t *testing.T) {
	f, _ := os.Open("testdata/basic.eml")
	defer f.Close()
	m, err := Parse(f, 1, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Body) > 5 {
		t.Errorf("body not truncated: len=%d", len(m.Body))
	}
}

func TestParseAttachments(t *testing.T) {
	f, err := os.Open("testdata/attachment.eml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	m, err := Parse(f, 9, 4096, 1<<20)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Attachments) != 2 {
		t.Fatalf("Attachments = %d, want 2", len(m.Attachments))
	}
	if m.Attachments[0].Name != "app.log" {
		t.Errorf("Name[0] = %q, want app.log", m.Attachments[0].Name)
	}
	if string(m.Attachments[0].Data) != "log line 1\nlog line 2\n" {
		t.Errorf("Data[0] = %q", m.Attachments[0].Data)
	}
	// Path traversal in the MIME filename must be collapsed to a basename.
	if m.Attachments[1].Name != "evil.txt" {
		t.Errorf("Name[1] = %q, want evil.txt (traversal stripped)", m.Attachments[1].Name)
	}
	if string(m.Attachments[1].Data) != "evil" {
		t.Errorf("Data[1] = %q", m.Attachments[1].Data)
	}
	// The text/plain body is still picked up alongside attachments.
	if !strings.Contains(m.Body, "Please read the attached log") {
		t.Errorf("Body = %q", m.Body)
	}
}

func TestParseDropsOversizedAttachment(t *testing.T) {
	f, _ := os.Open("testdata/attachment.eml")
	defer f.Close()
	// app.log is 12 bytes, evil.txt is 4 bytes. Cap at 3 so both exceed it
	// and get dropped — the parse must survive and keep the body.
	m, err := Parse(f, 9, 4096, 3)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("Attachments = %d, want 0 (both oversized)", len(m.Attachments))
	}
	if !strings.Contains(m.Body, "Please read the attached log") {
		t.Errorf("body should survive oversized attachment drop: %q", m.Body)
	}
}
