//go:build testmode

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/ChrisZhangJin/perch/internal/agent"
	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/gate"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/provider"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
)

var (
	testModeEnabled bool
	testModePort    string
)

func init() {
	flag.BoolVar(&testModeEnabled, "testmode", false, "enable HTTP injection endpoint (testmode build only)")
	flag.StringVar(&testModePort, "testport", "9876", "HTTP port for --testmode endpoint")
}

// buildApp constructs the *app.App for testmode builds. Both network edges
// are removed: QueuedMailbox replaces the IMAP dial, and InjectSender
// replaces the SMTP replier outright. Nothing a testmode binary produces
// reaches a real inbox -- replies come back in the POST /inject response.
func buildApp(cfg *config.Config, _ provider.Provider, ag agent.Agent, g *gate.Gate, sess *session.Registry, log *slog.Logger) (*app.App, error) {
	qm := &QueuedMailbox{}
	is := &InjectSender{}
	trig := mailbox.TimerTrigger{Interval: cfg.PollInterval}
	log.Info("testmode: IMAP and SMTP both bypassed; replies are captured, never sent")
	return app.New(cfg, qm, g, sess,
		runner.New(&ag, cfg.AgentWorkdir, cfg.AgentPermMode, cfg.TaskTimeout, log).
			WithAppendSystemPrompt(runner.NewSystemPrompt(cfg.AppendSystemPrompt, log)),
		is,
		log,
		trig,
	), nil
}

// InitTestMode starts the HTTP injection endpoint on 127.0.0.1:<testport>.
// In testmode builds the mailbox/sender are already wired by buildApp;
// InitTestMode just stands up the HTTP server.
func InitTestMode(a *app.App, _ *config.Config, log *slog.Logger) error {
	if !testModeEnabled {
		return nil
	}

	qm := a.MailboxForTest().(*QueuedMailbox)
	is := a.ReplySenderForTest().(*InjectSender)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /inject", handleInject(qm, is, a))
	addr := "127.0.0.1:" + testModePort
	srv := &http.Server{Addr: addr, Handler: mux}

	log.Info("testmode HTTP server starting", "addr", addr)
	fmt.Fprintf(os.Stderr, "perch testmode: POST http://%s/inject\n", addr)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("testmode HTTP server stopped", "err", err)
		}
	}()
	return nil
}