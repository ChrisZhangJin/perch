package main

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// fixedDate keeps golden assertions stable.
var fixedDate = time.Date(2026, 9, 28, 10, 30, 0, 0, time.FixedZone("CST", 8*3600))

func mustAddr(t *testing.T, s string) *mail.Address {
	t.Helper()
	a, err := mail.ParseAddress(s)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", s, err)
	}
	return a
}

func baseMsg(t *testing.T) *Message {
	t.Helper()
	return &Message{
		From:      mustAddr(t, "sender@163.com"),
		To:        []*mail.Address{mustAddr(t, "rcpt@example.com")},
		Subject:   "hello",
		Text:      "line one\nline two\n",
		Date:      fixedDate,
		MessageID: "<fixed@163.com>",
	}
}

// parse re-reads a built message so tests assert on what a client would see,
// not on the exact bytes we happened to emit.
func parse(t *testing.T, raw []byte) *mail.Message {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("ReadMessage: %v\n---\n%s", err, raw)
	}
	return m
}

func TestBuildPlainText(t *testing.T) {
	raw, err := baseMsg(t).Build()
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, raw)

	if got := m.Header.Get("From"); got != "<sender@163.com>" {
		t.Errorf("From = %q", got)
	}
	if got := m.Header.Get("Subject"); got != "hello" {
		t.Errorf("Subject = %q", got)
	}
	if got := m.Header.Get("Date"); got == "" {
		t.Error("Date header missing — spam filters and threading both want it")
	}
	if got := m.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q", got)
	}
	// Perch's replier stamps Auto-Submitted on every message it sends. A CLI
	// send is not an auto-reply and must not claim to be, or RFC3834-aware
	// recipients will suppress their own responses to it.
	if got := m.Header.Get("Auto-Submitted"); got != "" {
		t.Errorf("Auto-Submitted = %q, want empty", got)
	}
	if got := m.Header.Get("Subject"); strings.HasPrefix(got, "Re:") {
		t.Error("subject must not be given a Re: prefix")
	}

	body := decodeQP(t, m.Body)
	if body != "line one\r\nline two\r\n" {
		t.Errorf("body = %q", body)
	}
}

func TestBuildEncodesUTF8Subject(t *testing.T) {
	msg := baseMsg(t)
	msg.Subject = "今日日报 — 完成情况"
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	// The raw bytes must be 7-bit: a bare UTF-8 subject is what makes a
	// Chinese subject arrive as mojibake on strict servers.
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "Subject:") {
			for _, b := range []byte(line) {
				if b > 127 {
					t.Fatalf("Subject line carries raw 8-bit bytes: %q", line)
				}
			}
		}
	}
	dec := new(mime.WordDecoder)
	got, err := dec.DecodeHeader(parse(t, raw).Header.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "今日日报 — 完成情况" {
		t.Errorf("round-tripped subject = %q", got)
	}
}

func TestBuildUTF8BodyRoundTrips(t *testing.T) {
	msg := baseMsg(t)
	msg.Text = "第一行\n第二行 with ascii\n"
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, raw)
	if got := m.Header.Get("Content-Transfer-Encoding"); got != "quoted-printable" {
		t.Fatalf("CTE = %q, want quoted-printable", got)
	}
	if got := decodeQP(t, m.Body); got != "第一行\r\n第二行 with ascii\r\n" {
		t.Errorf("body = %q", got)
	}
}

func TestBuildHTMLOnly(t *testing.T) {
	msg := baseMsg(t)
	msg.Text = ""
	msg.HTML = "<p>hi</p>"
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := parse(t, raw).Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q", got)
	}
}

func TestBuildAlternative(t *testing.T) {
	msg := baseMsg(t)
	msg.HTML = "<p>hi</p>"
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, raw)
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if mt != "multipart/alternative" {
		t.Fatalf("media type = %q", mt)
	}
	types := partTypes(t, m.Body, params["boundary"])
	// RFC2046: least-rich first, so a client that renders both picks HTML.
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Errorf("parts = %v, want [text/plain text/html]", types)
	}
}

func TestBuildMixedWithAttachment(t *testing.T) {
	msg := baseMsg(t)
	msg.Attach = []Attachment{{Filename: "报告.txt", Data: []byte("hello attachment")}}
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, raw)
	mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if mt != "multipart/mixed" {
		t.Fatalf("media type = %q", mt)
	}

	mr := multipart.NewReader(m.Body, params["boundary"])
	body, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if ct := body.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("first part = %q, want the body", ct)
	}
	att, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	// A non-ASCII filename has to come back intact — RFC2231 continuation,
	// which mime.FormatMediaType/ParseMediaType handle as a pair.
	if got := att.FileName(); got != "报告.txt" {
		t.Errorf("attachment filename = %q", got)
	}
	if got := att.Header.Get("Content-Transfer-Encoding"); got != "base64" {
		t.Errorf("attachment CTE = %q", got)
	}
	// mime.TypeByExtension returns "text/plain; charset=utf-8" for .txt, and
	// FormatMediaType answers an already-parameterised type with "" rather
	// than an error — which used to ship an empty Content-Type header.
	if got := att.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("attachment Content-Type = %q, want a text/plain media type", got)
	}
	// multipart.Part decodes quoted-printable transparently but not base64,
	// so undo the transfer encoding ourselves.
	data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, att))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello attachment" {
		t.Errorf("attachment data = %q", data)
	}
}

func TestBuildMixedWithAlternativeAndAttachment(t *testing.T) {
	msg := baseMsg(t)
	msg.HTML = "<p>hi</p>"
	msg.Attach = []Attachment{{Filename: "a.bin", Data: []byte{0, 1, 2, 3}}}
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, raw)
	_, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	first, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	mt, inner, err := mime.ParseMediaType(first.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if mt != "multipart/alternative" {
		t.Fatalf("first part = %q, want a nested multipart/alternative", mt)
	}
	// The nested boundary must differ from the outer one, or the outer reader
	// terminates early on the inner delimiter.
	if inner["boundary"] == params["boundary"] {
		t.Fatal("nested boundary equals the outer boundary")
	}
	if types := partTypes(t, first, inner["boundary"]); len(types) != 2 {
		t.Errorf("nested parts = %v, want two", types)
	}
}

func TestBuildBase64LinesAreWrapped(t *testing.T) {
	// One unwrapped base64 line past 998 octets is an illegal RFC5322 line.
	msg := baseMsg(t)
	msg.Attach = []Attachment{{Filename: "big.bin", Data: make([]byte, 64*1024)}}
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d octets exceeds the RFC5322 limit", len(line))
		}
	}
}

func TestBuildRejectsHeaderInjection(t *testing.T) {
	msg := baseMsg(t)
	msg.Subject = "hi\r\nBcc: attacker@evil.example"
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	if parse(t, raw).Header.Get("Bcc") != "" {
		t.Fatal("newline in --subject injected a Bcc header")
	}
}

func TestBuildOmitsBccHeader(t *testing.T) {
	msg := baseMsg(t)
	msg.Bcc = []*mail.Address{mustAddr(t, "hidden@example.com")}
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hidden@example.com") {
		t.Fatal("Bcc recipient leaked into the message headers")
	}
	if got := msg.Recipients(); len(got) != 2 || got[1] != "hidden@example.com" {
		t.Errorf("Recipients() = %v, want the Bcc in the envelope", got)
	}
}

func TestRecipientsDeduplicates(t *testing.T) {
	msg := baseMsg(t)
	msg.Cc = []*mail.Address{mustAddr(t, "RCPT@example.com")}
	msg.Bcc = []*mail.Address{mustAddr(t, "other@example.com")}
	got := msg.Recipients()
	if len(got) != 2 {
		t.Fatalf("Recipients() = %v, want the case-insensitive duplicate collapsed", got)
	}
}

func TestBuildRejectsOwnedExtraHeader(t *testing.T) {
	msg := baseMsg(t)
	msg.Extra = []Header{{Name: "Subject", Value: "second subject"}}
	if _, err := msg.Build(); err == nil {
		t.Fatal("expected an error for a --header that duplicates an owned header")
	}
}

func TestBuildValidatesInputs(t *testing.T) {
	for name, mutate := range map[string]func(*Message){
		"no recipients": func(m *Message) { m.To = nil },
		"no body":       func(m *Message) { m.Text = "" },
		"no from":       func(m *Message) { m.From = nil },
	} {
		t.Run(name, func(t *testing.T) {
			msg := baseMsg(t)
			mutate(msg)
			if _, err := msg.Build(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestBuildFoldsLongRecipientList(t *testing.T) {
	msg := baseMsg(t)
	msg.To = nil
	for _, n := range []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel"} {
		msg.To = append(msg.To, mustAddr(t, n+"@some-fairly-long-domain.example.com"))
	}
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	// Folding must stay parseable: mail.ReadMessage rejoins continuation lines.
	if got := parse(t, raw).Header.Get("To"); strings.Count(got, "@") != 8 {
		t.Errorf("To survived folding as %q", got)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if strings.HasPrefix(line, "To:") && len(line) > 78 {
			t.Errorf("unfolded To line of %d chars", len(line))
		}
	}
}

func TestBuildDisplayNameEncoding(t *testing.T) {
	msg := baseMsg(t)
	msg.From = &mail.Address{Name: "张三", Address: "sender@163.com"}
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	from, err := mail.ParseAddress(parse(t, raw).Header.Get("From"))
	if err != nil {
		t.Fatal(err)
	}
	if from.Name != "张三" || from.Address != "sender@163.com" {
		t.Errorf("From = %+v", from)
	}
}

func TestParseAddrList(t *testing.T) {
	got, err := parseAddrList([]string{"a@x.com", "张三 <b@y.com>", "  "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Name != "张三" {
		t.Fatalf("got %+v", got)
	}
	if _, err := parseAddrList([]string{"not-an-address"}); err == nil {
		t.Fatal("expected an error for a malformed address")
	}
}

// --- helpers ---------------------------------------------------------------

func decodeQP(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(quotedprintable.NewReader(r))
	if err != nil {
		t.Fatalf("quoted-printable decode: %v", err)
	}
	return string(b)
}

func partTypes(t *testing.T, r io.Reader, boundary string) []string {
	t.Helper()
	mr := multipart.NewReader(r, boundary)
	var out []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		out = append(out, p.Header.Get("Content-Type"))
	}
}
