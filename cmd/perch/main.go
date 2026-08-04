package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/replier"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("perch starting", "version", version)

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	mb, err := mailbox.Dial(cfg)
	if err != nil {
		log.Error("mailbox dial", "err", err)
		os.Exit(1)
	}
	if mb.IdleSupported() {
		log.Info("mailbox ready", "mode", "idle+poll")
	} else {
		log.Info("mailbox ready", "mode", "poll-only", "note", "server has no IMAP IDLE; using POLL_INTERVAL")
	}
	sess, err := session.Load(cfg.SessionStore)
	if err != nil {
		log.Error("session load", "err", err)
		os.Exit(1)
	}
	a := app.New(cfg, mb, gate.New(cfg.AllowFrom), sess, runner.New(cfg), replier.New(cfg), log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("watcher started", "email", cfg.Email, "poll", cfg.PollInterval, "allow_from", cfg.AllowFrom)
	if err := a.Run(ctx); err != nil {
		log.Error("run", "err", err)
		os.Exit(1)
	}
	log.Info("watcher stopped")
}
