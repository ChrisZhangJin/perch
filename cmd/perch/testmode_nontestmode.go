//go:build !testmode

package main

import (
	"log/slog"

	"github.com/ChrisZhangJin/perch/internal/agent"
	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/provider"
	"github.com/ChrisZhangJin/perch/internal/replier"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
)

// buildApp constructs the *app.App for non-testmode builds. Dials the real
// IMAP mailbox and instantiates the real SMTP replier.
func buildApp(cfg *config.Config, p provider.Provider, ag agent.Agent, g *gate.Gate, sess *session.Registry, log *slog.Logger) (*app.App, error) {
	strat, err := mailbox.BuildStrategy(cfg, p)
	if err != nil {
		return nil, err
	}
	mode := "poll-only"
	note := ""
	if p.Caps.SupportsIDLE {
		mode = "idle+poll"
	} else {
		note = "server has no IMAP IDLE; using POLL_INTERVAL"
	}
	log.Info("mailbox ready", "mode", mode, "note", note)
	return app.New(cfg, strat.Box, g, sess,
		runner.New(&ag, cfg.AgentWorkdir, cfg.AgentPermMode, cfg.TaskTimeout, log).
			WithAppendSystemPrompt(runner.NewSystemPrompt(cfg.AppendSystemPrompt, log)),
		replier.New(cfg, p.SMTPAddr),
		log,
		strat.Triggers...,
	), nil
}

// InitTestMode is a no-op in non-testmode builds. The --testmode and
// --testport flags are not registered; this function is never reached.
func InitTestMode(_ *app.App, _ *config.Config, _ *slog.Logger) error { return nil }