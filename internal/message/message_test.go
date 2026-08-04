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

	m, err := Parse(f, 42, 1024)
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

	m, err := Parse(f, 7, 4096)
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
	m, err := Parse(f, 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Body) > 5 {
		t.Errorf("body not truncated: len=%d", len(m.Body))
	}
}
