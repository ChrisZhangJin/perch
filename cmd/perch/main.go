package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"

	"github.com/ChrisZhangJin/perch/internal/agent"
	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/provider"
	"github.com/ChrisZhangJin/perch/internal/replier"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
	"github.com/ChrisZhangJin/perch/internal/setup"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// realReadPassword is the production PasswordFn: it delegates to
// golang.org/x/term so the auth code is not echoed. Test code passes its own.
func realReadPassword(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}

func main() {
	configPath := flag.String("config", "", "path to YAML config file (default: ./perch.yaml, then ~/.config/perch/perch.yaml). Env: PERCH_CONFIG.")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("perch starting", "version", version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if used := config.ResolveConfigPath(*configPath); used != "" {
		log.Info("config loaded", "path", used)
	} else {
		log.Info("config loaded", "path", "(built-in defaults only — no YAML file found)")
	}

	// First-run wizard: prompt for missing fields if stdin is a TTY,
	// otherwise return ErrMissingFields and exit non-zero with a hint.
	if err := setup.Ensure(cfg, os.Stdin, os.Stdout, os.Stderr, realReadPassword); err != nil {
		log.Error("setup", "err", err)
		os.Exit(2)
	}

	// Resolve named providers/agents.
	p, err := provider.Lookup(cfg.ProviderName)
	if err != nil {
		log.Error("provider lookup", "err", err)
		os.Exit(1)
	}
	ag, err := agent.Lookup(cfg.AgentName)
	if err != nil {
		log.Error("agent lookup", "err", err)
		os.Exit(1)
	}

	g, err := gate.New(cfg.AllowFrom)
	if err != nil {
		log.Error("gate build", "err", err)
		os.Exit(1)
	}
	strat, err := mailbox.BuildStrategy(cfg, p)
	if err != nil {
		log.Error("mailbox dial", "err", err)
		os.Exit(1)
	}
	mode := "poll-only"
	if p.Caps.SupportsIDLE {
		mode = "idle+poll"
	}
	note := ""
	if mode == "poll-only" {
		note = "server has no IMAP IDLE; using POLL_INTERVAL"
	}
	log.Info("mailbox ready", "mode", mode, "note", note)
	sess, err := session.Load(cfg.SessionStore)
	if err != nil {
		log.Error("session load", "err", err)
		os.Exit(1)
	}
	a := app.New(cfg, strat.Box, g, sess,
		runner.New(&ag, cfg.AgentWorkdir, cfg.AgentPermMode, cfg.TaskTimeout),
		replier.New(cfg, p.SMTPAddr),
		log,
		strat.Triggers...,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("watcher started", "email", cfg.Email, "provider", p.Name, "agent", ag.Name, "poll", cfg.PollInterval, "allow_from", cfg.AllowFrom)
	if err := a.Run(ctx); err != nil {
		log.Error("run", "err", err)
		os.Exit(1)
	}
	log.Info("watcher stopped")
}
