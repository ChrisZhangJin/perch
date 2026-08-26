package runner

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/ChrisZhangJin/perch/internal/message"
)

// placeholderRe matches an unfilled template slot from helpdesk.md.example:
// [[COMPANY]], [[ESCALATION CONTACT — ...]]. Anchored on an uppercase first
// letter so it cannot fire on markdown wiki-links ([[some page]]), which are
// legitimate prose.
var placeholderRe = regexp.MustCompile(`\[\[[A-Z][^\]\n]{0,120}\]\]`)

// setupNoteMarker is the sentinel in helpdesk.md.example's operator comment.
// Its presence in a loaded prompt means the block was never removed, so setup
// instructions are reaching the agent. Keep this string and the one in
// helpdesk.md.example identical — TestSystemPromptWarnsOnUnfilledTemplate
// loads the real file and fails if they drift.
const setupNoteMarker = "SETUP NOTE FOR THE OPERATOR"

// inspect warns about a system prompt that was obviously copied from
// helpdesk.md.example and never filled in. Observed 2026-08-25: a live deploy
// shipped the template verbatim, so the agent was told to escalate to a
// literal "[[ESCALATION CONTACT]]" and to avoid committing "[[COMPANY]]" to
// anything — and the operator's setup comment ("delete this comment block")
// arrived as an instruction the agent could act on, against a file sitting
// inside its writable cwd.
//
// WARN, not an error: an unfilled template still works well enough that
// refusing to start would be the worse failure, and perch never blocks mail
// over prompt content.
func (s *SystemPrompt) inspect(text string) {
	if hits := placeholderRe.FindAllString(text, -1); len(hits) > 0 {
		s.log.Warn("append_system_prompt still has unfilled template placeholders; the agent is reading them literally",
			"source", s.describeLocked(), "count", len(hits),
			"placeholders", strings.Join(dedupe(hits), " "),
			"hint", "replace every [[...]] in the file, then check with: grep -n '\\[\\[' <file>")
	}
	if strings.Contains(text, setupNoteMarker) {
		s.log.Warn("append_system_prompt still contains the example file's setup comment; it is being sent to the agent as instructions",
			"source", s.describeLocked(),
			"hint", "delete the <!-- ... --> block at the top of your copy")
	}
}

// dedupe returns the distinct values of xs, in first-seen order, capped so one
// pathological file cannot produce a log line thousands of entries long.
func dedupe(xs []string) []string {
	const max = 8
	seen := make(map[string]bool, len(xs))
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
		if len(out) == max {
			out = append(out, "…")
			break
		}
	}
	return out
}

// MaxSystemPromptBytes caps what perch hands to --append-system-prompt. The
// text travels as one argv entry and Linux rejects the whole exec above
// MAX_ARG_STRLEN (128 KiB), which would take down every email rather than
// just truncating one. 64 KiB is far past any hand-written role definition.
const MaxSystemPromptBytes = 65536

// SystemPrompt resolves the configured --append-system-prompt payload.
//
// The config value is text-or-path, matching what pi's own flag accepts: a
// single-line value naming a readable file is read as a file, anything else is
// literal text. perch resolves it rather than passing a path through, because
// claude's flag takes text only — resolving here means all three agents get
// the same bytes.
//
// A file is re-read on every email, so editing the role definition takes
// effect on the next message with no restart. If a re-read fails (file moved
// mid-edit, permissions changed) the last good content is used and the failure
// logged: an operator editing their help-desk definition must not silently
// turn the agent back into a generic assistant halfway through.
type SystemPrompt struct {
	path string // "" when the value was literal text
	log  *slog.Logger

	mu   sync.Mutex
	text string // literal text, or the last successfully read file content
}

// NewSystemPrompt resolves value once and returns a *SystemPrompt, or nil when
// value is empty (the feature is off). A nil *SystemPrompt is safe to use —
// every method handles it — so callers never branch.
//
// A path is made absolute against perch's cwd, so the file perch reads does
// not depend on where the daemon was launched from. A configured path that
// does not exist is NOT an error: it is treated as literal text (which is what
// pi would do), and the startup log says which reading won, so a typo shows up
// as "inline text (34 bytes)" where the operator expected a file.
func NewSystemPrompt(value string, log *slog.Logger) *SystemPrompt {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	sp := &SystemPrompt{log: log}
	if p, ok := looksLikePath(value); ok {
		sp.path = p
		if text, err := readCapped(p); err == nil {
			sp.text = text
			sp.inspect(text)
		} else {
			// Stat said it was there a moment ago; report and keep going —
			// Text() retries on every email.
			log.Warn("append_system_prompt file unreadable at startup", "path", p, "err", err)
		}
		return sp
	}
	sp.text = message.TruncateUTF8(value, MaxSystemPromptBytes)
	sp.inspect(sp.text)
	return sp
}

// looksLikePath reports whether value should be read as a file, returning the
// absolute path. The test is deliberately narrow: a multi-line value is prose,
// not a filename, and a single-line value only counts when it actually names a
// regular file. That keeps an inline one-liner from being mistaken for a path
// and vice versa.
func looksLikePath(value string) (string, bool) {
	v := strings.TrimSpace(value)
	if v == "" || strings.ContainsAny(v, "\n\r") {
		return "", false
	}
	p := v
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return "", false
	}
	return p, true
}

func readCapped(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return message.TruncateUTF8(string(data), MaxSystemPromptBytes), nil
}

// Text returns the payload for this run: the literal text, or the file's
// current content. Empty for a nil receiver.
func (s *SystemPrompt) Text() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return s.text
	}
	text, err := readCapped(s.path)
	if err != nil {
		s.log.Warn("append_system_prompt re-read failed; using last known content",
			"path", s.path, "err", err, "cached_bytes", len(s.text))
		return s.text
	}
	// Announce a CHANGE, and only a change. The per-email re-read is the
	// feature — edit the role, next email uses it — but without a line here
	// the operator has no confirmation that perch picked the edit up, and
	// would have to diff the DEBUG argv dump to find out. Logging every read
	// instead would put one line per email in the log saying nothing happened,
	// so silence means "same as before".
	//
	// INFO, not DEBUG: changing the agent's standing role is a rare and
	// operationally significant event, and it is the thing you want in the log
	// when a reply's behaviour changes and you are asking why.
	if text != s.text {
		s.log.Info("append_system_prompt reloaded",
			"path", s.path, "bytes", len(text), "previous_bytes", len(s.text))
		s.inspect(text)
	}
	s.text = text
	return text
}

// Describe renders a one-line summary for the startup log, so an operator can
// see at a glance whether their value was read as a file or as text.
func (s *SystemPrompt) Describe() string {
	if s == nil {
		return "(none)"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.describeLocked()
}

// describeLocked is Describe without the lock, for callers that already hold
// it (inspect runs inside Text's critical section).
func (s *SystemPrompt) describeLocked() string {
	if s.path != "" {
		return "file " + s.path
	}
	return "inline text"
}

// Bytes reports the size of the currently held payload. Startup logging only.
func (s *SystemPrompt) Bytes() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.text)
}
