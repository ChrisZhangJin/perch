// Package log provides perch's default slog handler. The format is:
//
//	2026-08-09 15:47:10.606 ERROR "process failed" err="..." from="..."
//
// Time (2006-01-02 15:04:05.000) and level (INFO/WARN/ERROR/DEBUG) are
// positional; everything else is key=value with quoting on values that
// contain spaces, equals signs, or quotes. msg is always quoted so it
// stays visually distinct from the attribute keys.
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// New returns a slog.Handler that writes to w with perch's format.
// Use slog.New to wrap it as a *slog.Logger, or SetDefault to apply
// it to package-level slog.Warn / slog.Error calls.
func New(w io.Writer, level slog.Level) slog.Handler {
	return &handler{w: w, mu: &sync.Mutex{}, min: level}
}

const timeLayout = "2006-01-02 15:04:05.000"

type handler struct {
	w   io.Writer
	mu  *sync.Mutex
	min slog.Level
	// attrs holds pre-bound attributes (from .With); prepended to every record.
	attrs []slog.Attr
	// groups holds open group names (from .WithGroup); each non-empty name
	// is prepended to attribute keys when rendered.
	groups []string
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.min
}

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Format(timeLayout))
	b.WriteByte(' ')
	b.WriteString(strings.ToUpper(r.Level.String()))
	b.WriteByte(' ')
	b.WriteString(quoteIfNeeded(r.Message))

	// Pre-bound attrs first, then record attrs.
	for _, a := range h.attrs {
		writeAttr(&b, h.groups, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, h.groups, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	combined := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	combined = append(combined, h.attrs...)
	combined = append(combined, attrs...)
	return &handler{w: h.w, mu: h.mu, min: h.min, attrs: combined, groups: h.groups}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	groups := make([]string, 0, len(h.groups)+1)
	groups = append(groups, h.groups...)
	groups = append(groups, name)
	return &handler{w: h.w, mu: h.mu, min: h.min, attrs: h.attrs, groups: groups}
}

// writeAttr renders " key=value" (or " key=\"value with spaces\"") into b.
// If h.groups is non-empty, the group prefix is prepended to key with dots
// (matches slog.TextHandler's behaviour).
func writeAttr(b *strings.Builder, groups []string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if len(groups) > 0 {
		key = strings.Join(groups, ".") + "." + key
	}
	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteByte('=')
	writeValue(b, a.Value)
}

func writeValue(b *strings.Builder, v slog.Value) {
	switch v.Kind() {
	case slog.KindString:
		s := v.String()
		if needsQuote(s) {
			b.WriteByte('"')
			b.WriteString(strings.ReplaceAll(s, `"`, `\"`))
			b.WriteByte('"')
		} else {
			b.WriteString(s)
		}
	case slog.KindInt64:
		b.WriteString(strconv.FormatInt(v.Int64(), 10))
	case slog.KindUint64:
		b.WriteString(strconv.FormatUint(v.Uint64(), 10))
	case slog.KindFloat64:
		b.WriteString(strconv.FormatFloat(v.Float64(), 'g', -1, 64))
	case slog.KindBool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case slog.KindDuration:
		b.WriteString(v.Duration().String())
	case slog.KindTime:
		b.WriteString(v.Time().Format(time.RFC3339Nano))
	case slog.KindGroup:
		// Flatten one level: group attrs become key.value at the call site.
		// No current call site uses groups; keep the renderer simple.
		attrs := v.Group()
		for i, a := range attrs {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(a.Key)
			b.WriteByte('=')
			writeValue(b, a.Value)
		}
	default:
		// Fall back to slog's default renderer for LogValuer / any Kind we
		// don't recognise. Safe but rarely hit; logs stay readable.
		b.WriteString(fmt.Sprintf("%v", v.Any()))
	}
}

// needsQuote reports whether a string attribute must be wrapped in "..." so
// it doesn't get split by downstream log readers (jq, awk, grep). The rules
// match slog.TextHandler: any rune that's not a letter, digit, '-', '.', '_',
// or '/' triggers quoting.
func needsQuote(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '.', r == '_', r == '/':
		default:
			return true
		}
	}
	return false
}

func quoteIfNeeded(s string) string {
	if needsQuote(s) {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}