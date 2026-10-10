package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// TokenCount is a token budget in perch.yaml: a plain integer (131072) or
// one with a binary k/m suffix ("128k" = 131072, "1m" = "1024k" = 1048576).
type TokenCount int

// UnmarshalYAML accepts both forms, case-insensitively.
func (t *TokenCount) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseTokenCount(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	*t = v
	return nil
}

// ParseTokenCount parses "131072", "128k" or "1m".
func ParseTokenCount(s string) (TokenCount, error) {
	raw := s
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1024, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1024*1024, strings.TrimSuffix(s, "m")
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("max_tokens: want a number like 131072, 128k or 1m, got %q", raw)
	}
	return TokenCount(n * mult), nil
}
