package main

import (
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
)

// attachmentHeaders builds a one-attachment message and returns that part's
// headers as a client would parse them.
func attachmentHeaders(t *testing.T, a Attachment) map[string][]string {
	t.Helper()
	msg := baseMsg(t)
	msg.Attach = []Attachment{a}
	raw, err := msg.Build()
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	if _, err := mr.NextPart(); err != nil { // body part
		t.Fatal(err)
	}
	att, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	return att.Header
}

func TestAttachmentContentType(t *testing.T) {
	cases := []struct {
		name         string
		att          Attachment
		wantMedia    string
		wantFilename string
	}{
		{
			// The regression: TypeByExtension returns a parameterised type
			// and FormatMediaType rejects it by returning "".
			name:         "known extension",
			att:          Attachment{Filename: "notes.txt", Data: []byte("x")},
			wantMedia:    "text/plain",
			wantFilename: "notes.txt",
		},
		{
			name:         "unknown extension falls back",
			att:          Attachment{Filename: "blob.zzzz", Data: []byte("x")},
			wantMedia:    defaultAttachmentType,
			wantFilename: "blob.zzzz",
		},
		{
			name:         "no extension falls back",
			att:          Attachment{Filename: "Makefile", Data: []byte("x")},
			wantMedia:    defaultAttachmentType,
			wantFilename: "Makefile",
		},
		{
			name:         "explicit type wins",
			att:          Attachment{Filename: "data.txt", MIMEType: "application/json", Data: []byte("{}")},
			wantMedia:    "application/json",
			wantFilename: "data.txt",
		},
		{
			name:         "non-ascii filename",
			att:          Attachment{Filename: "季度报告.pdf", Data: []byte("x")},
			wantMedia:    "application/pdf",
			wantFilename: "季度报告.pdf",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hdr := attachmentHeaders(t, c.att)

			ctRaw := strings.Join(hdr["Content-Type"], "")
			if ctRaw == "" {
				t.Fatal("attachment has no Content-Type header")
			}
			media, ctParams, err := mime.ParseMediaType(ctRaw)
			if err != nil {
				t.Fatalf("Content-Type %q does not parse: %v", ctRaw, err)
			}
			if media != c.wantMedia {
				t.Errorf("media type = %q, want %q", media, c.wantMedia)
			}
			if ctParams["name"] != c.wantFilename {
				t.Errorf("name param = %q, want %q", ctParams["name"], c.wantFilename)
			}

			cdRaw := strings.Join(hdr["Content-Disposition"], "")
			disp, cdParams, err := mime.ParseMediaType(cdRaw)
			if err != nil {
				t.Fatalf("Content-Disposition %q does not parse: %v", cdRaw, err)
			}
			if disp != "attachment" {
				t.Errorf("disposition = %q", disp)
			}
			if cdParams["filename"] != c.wantFilename {
				t.Errorf("filename param = %q, want %q", cdParams["filename"], c.wantFilename)
			}
		})
	}
}
