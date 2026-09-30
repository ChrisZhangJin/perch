//go:build manual

package setup

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/ChrisZhangJin/perch/internal/config"
)

func TestManualWizardOutput(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	prev := isTTYFn
	isTTYFn = func(_ interface { /* dummy to match io.Reader shape */
	}) bool { return true }
	_ = prev
	isTTYFn = func(r interface { /* io.Reader cast */
	}) bool { return true }
	_ = prev

	cfg := &config.Config{
		ProviderName:  "163",
		AgentName:     "claude",
		AgentWorkdir:  ".",
		AgentPermMode: "acceptEdits",
	}
	in := strings.NewReader("\n\n\n\nbob@qq.com\nagent@qq.com\n")
	pw := func(fd int) ([]byte, error) { return []byte("supersecret"), nil }
	err := Ensure(cfg, in, &strings.Builder{}, &strings.Builder{}, pw)
	fmt.Printf("err=%v\n", err)

	data, _ := os.ReadFile(tmpHome + "/.perch/perch.yaml")
	fmt.Println("---PERSISTED---")
	fmt.Println(string(data))
}
