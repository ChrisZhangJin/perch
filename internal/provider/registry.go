// Package provider names the email transports perch supports and ships their
// endpoints + per-provider transport quirks so the rest of the codebase does
// not hard-code IMAP/SMTP addresses.
package provider

import "fmt"

// Capabilities groups the transport-specific quirks that callers may need to
// inspect to choose the right code path (e.g. whether to send IMAP ID before
// login, or whether to expect an IDLE response).
type Capabilities struct {
	NeedsIMAPID  bool // 163/126/qq all require IMAP ID before login
	SupportsIDLE bool // 163/126 do not advertise IDLE
}

// Provider is a built-in email transport.
type Provider struct {
	Name     string
	IMAPAddr string // host:port for implicit TLS IMAP
	SMTPAddr string // host:port for implicit TLS SMTP
	Caps     Capabilities
}

// providers is the built-in table. Order is stable; Lookup preserves the
// insertion order in its error message.
var providers = map[string]Provider{
	"163": {Name: "163", IMAPAddr: "imap.163.com:993", SMTPAddr: "smtp.163.com:465", Caps: Capabilities{NeedsIMAPID: true, SupportsIDLE: false}},
	"126": {Name: "126", IMAPAddr: "imap.126.com:993", SMTPAddr: "smtp.126.com:465", Caps: Capabilities{NeedsIMAPID: true, SupportsIDLE: false}},
	"qq":  {Name: "qq", IMAPAddr: "imap.qq.com:993", SMTPAddr: "smtp.qq.com:465", Caps: Capabilities{NeedsIMAPID: true, SupportsIDLE: true}},
}

// Lookup resolves a provider name to its built-in transport. Unknown names
// return an error listing the valid choices.
func Lookup(name string) (Provider, error) {
	if p, ok := providers[name]; ok {
		return p, nil
	}
	return Provider{}, fmt.Errorf("unknown email provider %q (valid: 163, 126, qq)", name)
}