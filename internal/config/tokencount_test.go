package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseTokenCount(t *testing.T) {
	for in, want := range map[string]TokenCount{
		"131072": 131072, "128k": 131072, "128K": 131072,
		"1024k": 1048576, "1m": 1048576, " 16k ": 16384,
	} {
		if got, err := ParseTokenCount(in); err != nil || got != want {
			t.Errorf("ParseTokenCount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "k", "lots", "1.5k"} {
		if _, err := ParseTokenCount(in); err == nil {
			t.Errorf("ParseTokenCount(%q): want error", in)
		}
	}
}

func TestTokenCountYAML(t *testing.T) {
	var y struct {
		M TokenCount `yaml:"m"`
	}
	if err := yaml.Unmarshal([]byte("m: 1024k\n"), &y); err != nil || y.M != 1048576 {
		t.Fatalf("got %d, %v", y.M, err)
	}
}
