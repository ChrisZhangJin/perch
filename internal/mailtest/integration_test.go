//go:build integration

package mailtest_test

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ChrisZhangJin/perch/internal/agent"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/mailtest"
	"github.com/ChrisZhangJin/perch/internal/runner"
)

// TestIntegrationEmailRoundTrip uses the real claude agent to process an
// email and prints the reply. Pass your content via flags or stdin.
//
// Examples:
//
//	go test -v -tags integration ./internal/mailtest/ \
//	    -run TestIntegrationEmailRoundTrip \
//	    -args \
//	    -from chris@x -subject hi -body 'What is the current date?'
//
//	echo "What is the current date? Reply in one sentence." | go test \
//	    -v -tags integration ./internal/mailtest/ \
//	    -run TestIntegrationEmailRoundTrip \
//	    -args -from-stdin
//
// Env vars (used when flags are absent):
//
//	PERCH_FROM     sender address
//	PERCH_SUBJECT  email subject
//	PERCH_BODY     email body
func TestIntegrationEmailRoundTrip(t *testing.T) {
	fs := flag.NewFlagSet("integration", flag.ContinueOnError)
	from := fs.String("from", "chris@example.com", "sender address")
	subject := fs.String("subject", "quick question", "email subject")
	body := fs.String("body", "What is the current date? Reply in one sentence.", "email body")
	fromStdin := fs.Bool("from-stdin", false, "read body from stdin instead of -body")
	if err := fs.Parse(os.Args[1:]); err != nil {
		t.Fatal(err)
	}
	if v := os.Getenv("PERCH_FROM"); v != "" {
		*from = v
	}
	if v := os.Getenv("PERCH_SUBJECT"); v != "" {
		*subject = v
	}
	if v := os.Getenv("PERCH_BODY"); v != "" {
		*body = v
	}
	if *fromStdin {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			t.Fatalf("read stdin: %v", err)
		}
		*body = strings.TrimRight(string(data), "\n")
	}

	ag, err := agent.Lookup("claude")
	if err != nil {
		t.Skipf("claude agent not found: %v", err)
	}

	cfg := &config.Config{
		MaxPromptBytes: 4096,
		AgentWorkdir:   t.TempDir(),
		TaskTimeout:    2 * time.Minute,
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	run := runner.New(&ag, cfg.AgentWorkdir, "acceptEdits", cfg.TaskTimeout, log)

	mt, err := mailtest.New(cfg, []string{"*"}, run)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Logf("input: from=%q subject=%q body_len=%d", *from, *subject, len(*body))
	uid, err := mt.Send(*from, "agent@perch.local", *subject, *body)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	t.Logf("queued message UID=%d", uid)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := mt.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	replies := mt.Replies()
	if len(replies) == 0 {
		t.Fatal("agent produced no reply")
	}

	for i, r := range replies {
		fmt.Printf("\n--- Reply %d ---\n", i+1)
		fmt.Printf("To:      %s\n", r.To)
		fmt.Printf("Subject: %s\n", r.Subject)
		fmt.Printf("Body:\n%s\n", r.Body)
		if len(r.Attachments) > 0 {
			fmt.Printf("Attachments: %v\n", r.Attachments)
		}
	}

	seen := mt.SeenUIDs()
	t.Logf("seen UIDs: %v", seen)
}