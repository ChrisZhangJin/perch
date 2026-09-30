package provider

import (
	"strings"
	"testing"
)

func TestProviderTableConsistent(t *testing.T) {
	for name, p := range providers {
		if p.Name != name {
			t.Errorf("map key %q != provider.Name %q", name, p.Name)
		}
	}
}

func TestProviderEndpoints(t *testing.T) {
	cases := []struct {
		name, imap, smtp          string
		needsIMAPID, supportsIDLE bool
	}{
		{"163", "imap.163.com:993", "smtp.163.com:465", true, false},
		{"126", "imap.126.com:993", "smtp.126.com:465", true, false},
		{"qq", "imap.qq.com:993", "smtp.qq.com:465", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Lookup(c.name)
			if err != nil {
				t.Fatalf("Lookup(%q): %v", c.name, err)
			}
			if got.IMAPAddr != c.imap {
				t.Errorf("IMAPAddr = %q, want %q", got.IMAPAddr, c.imap)
			}
			if got.SMTPAddr != c.smtp {
				t.Errorf("SMTPAddr = %q, want %q", got.SMTPAddr, c.smtp)
			}
			if got.Caps.NeedsIMAPID != c.needsIMAPID {
				t.Errorf("Caps.NeedsIMAPID = %v, want %v", got.Caps.NeedsIMAPID, c.needsIMAPID)
			}
			if got.Caps.SupportsIDLE != c.supportsIDLE {
				t.Errorf("Caps.SupportsIDLE = %v, want %v", got.Caps.SupportsIDLE, c.supportsIDLE)
			}
		})
	}
}

func TestProviderLookupUnknown(t *testing.T) {
	_, err := Lookup("gmail")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "163") || !strings.Contains(err.Error(), "126") || !strings.Contains(err.Error(), "qq") {
		t.Errorf("error should list valid names, got: %v", err)
	}
}
