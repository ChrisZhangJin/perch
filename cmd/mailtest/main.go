// mailtest is a CLI that wires the real agent runner through the
// internal/mailtest harness, so you can send a specific email and observe
// the agent's reply without IMAP/SMTP.
//
// Configuration lives in a YAML file (default ./mailtest.yaml; copy
// mailtest.yaml.example to get one). Example:
//
//	from: sender@example.com
//	to: agent@perch.local
//	subject: "quick question"
//	body: |
//	  What is the current date? Reply in one sentence.
//	agent: claude
//	workdir: .
//	permission_mode: acceptEdits
//	allow_from: ["*"]
//	task_timeout: 120s
//	log_level: debug
//
// Either `body` (inline) or `body_file` (path) is required. Stdin is also
// supported via -body-stdin; the flag wins over both file fields.
//
// The agent is whatever `agent:` names (claude | nanopi | pi, default
// claude); -agent overrides it without editing the YAML. Run:
//
//	mailtest -config mailtest.yaml
//	mailtest -config mailtest.yaml -agent nanopi
//	mailtest -config mailtest.yaml -body-stdin < message.txt
//	cat message.txt | mailtest -config mailtest.yaml -body-stdin
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ChrisZhangJin/perch/internal/agent"
	"github.com/ChrisZhangJin/perch/internal/config"
	plog "github.com/ChrisZhangJin/perch/internal/log"
	"github.com/ChrisZhangJin/perch/internal/mailtest"
	"github.com/ChrisZhangJin/perch/internal/runner"
)

type mailtestYAML struct {
	From           string        `yaml:"from"`
	To             string        `yaml:"to"`
	Subject        string        `yaml:"subject"`
	Body           string        `yaml:"body"`
	BodyFile       string        `yaml:"body_file"`
	Agent          string        `yaml:"agent"`
	Workdir        string        `yaml:"workdir"`
	PermissionMode string        `yaml:"permission_mode"`
	AllowFrom      []string      `yaml:"allow_from"`
	TaskTimeout    time.Duration `yaml:"task_timeout"`
	LogLevel       string        `yaml:"log_level"`
}

func main() {
	cfgPath := flag.String("config", "mailtest.yaml", "path to YAML config (default ./mailtest.yaml)")
	bodyStdin := flag.Bool("body-stdin", false, "read body from stdin (overrides -config body fields)")
	agentName := flag.String("agent", "", "AI agent to run: claude | nanopi | pi (overrides the config's agent:)")
	flag.Parse()

	data, err := os.ReadFile(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mailtest: read config %s: %v\n", *cfgPath, err)
		os.Exit(1)
	}
	var y mailtestYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		fmt.Fprintf(os.Stderr, "mailtest: parse %s: %v\n", *cfgPath, err)
		os.Exit(1)
	}

	if *bodyStdin {
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			fmt.Fprintf(os.Stderr, "mailtest: read stdin: %v\n", err)
			os.Exit(1)
		}
		y.Body = strings.TrimRight(string(data), "\n")
	}

	// body_file is the fallback for an unset inline body, so it must be
	// resolved before the required-fields check below -- otherwise a config
	// that only sets body_file dies on "body must be set".
	if y.BodyFile != "" && y.Body == "" {
		data, err := os.ReadFile(y.BodyFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mailtest: read body_file %s: %v\n", y.BodyFile, err)
			os.Exit(1)
		}
		y.Body = strings.TrimRight(string(data), "\n")
	}
	if y.From == "" || y.Subject == "" || y.Body == "" {
		fmt.Fprintln(os.Stderr, "mailtest: from, subject, and body (or body_file) must all be set")
		os.Exit(1)
	}
	if y.To == "" {
		y.To = "agent@perch.local"
	}
	if *agentName != "" {
		y.Agent = *agentName
	}
	if y.Agent == "" {
		y.Agent = "claude"
	}
	if y.Workdir == "" {
		y.Workdir = "."
	}
	if y.PermissionMode == "" {
		y.PermissionMode = "acceptEdits"
	}
	if len(y.AllowFrom) == 0 {
		y.AllowFrom = []string{"*"}
	}
	if y.TaskTimeout == 0 {
		y.TaskTimeout = 2 * time.Minute
	}

	ag, err := agent.Lookup(y.Agent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mailtest: %v\n", err)
		os.Exit(1)
	}
	if _, err := agent.BinaryPath(y.Agent); err != nil {
		fmt.Fprintf(os.Stderr, "mailtest: %v\n", err)
		os.Exit(1)
	}

	level := slog.LevelInfo
	switch strings.ToLower(y.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	log := slog.New(plog.New(os.Stderr, level))

	// Start from Defaults so the harness reproduces production behaviour.
	// A bare &config.Config{} would zero the fields whose default is not the
	// zero value — e.g. AgentTaskOnly (SAFETY PROTOCOL) — and
	// this CLI exists precisely to observe what the real agent does.
	perchCfg := config.Defaults()
	perchCfg.MaxPromptBytes = 4096
	perchCfg.AgentWorkdir = y.Workdir
	perchCfg.TaskTimeout = y.TaskTimeout
	run := runner.New(&ag, y.Workdir, y.PermissionMode, y.TaskTimeout, log)

	mt, err := mailtest.New(perchCfg, y.AllowFrom, run)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mailtest: harness init: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "mailtest: from=%q to=%q subject=%q body=%d bytes agent=%s\n",
		y.From, y.To, y.Subject, len(y.Body), y.Agent)

	uid, err := mt.Send(y.From, y.To, y.Subject, y.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mailtest: send: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "mailtest: queued UID=%d\n", uid)

	ctx, cancel := context.WithTimeout(context.Background(), y.TaskTimeout+30*time.Second)
	defer cancel()

	if err := mt.RunOnce(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mailtest: run: %v\n", err)
		os.Exit(1)
	}

	replies := mt.Replies()
	if len(replies) == 0 {
		fmt.Fprintln(os.Stderr, "mailtest: agent produced no reply")
		os.Exit(2)
	}
	for i, r := range replies {
		fmt.Printf("\n--- Reply %d ---\n", i+1)
		fmt.Printf("To:      %s\n", r.To)
		fmt.Printf("Subject: %s\n", r.Subject)
		fmt.Printf("InReplyTo: %s\n", r.InReplyTo)
		fmt.Printf("Body:\n%s\n", r.Body)
		if len(r.Attachments) > 0 {
			fmt.Printf("Attachments: %v\n", r.Attachments)
		}
	}
	fmt.Fprintf(os.Stderr, "mailtest: seen UIDs = %v\n", mt.SeenUIDs())
}
