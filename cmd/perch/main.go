package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"

	"github.com/ChrisZhangJin/perch/internal/agent"
	"github.com/ChrisZhangJin/perch/internal/app"
	"github.com/ChrisZhangJin/perch/internal/config"
	"github.com/ChrisZhangJin/perch/internal/daemon"
	"github.com/ChrisZhangJin/perch/internal/gate"
	plog "github.com/ChrisZhangJin/perch/internal/log"
	"github.com/ChrisZhangJin/perch/internal/mailbox"
	"github.com/ChrisZhangJin/perch/internal/provider"
	"github.com/ChrisZhangJin/perch/internal/replier"
	"github.com/ChrisZhangJin/perch/internal/runner"
	"github.com/ChrisZhangJin/perch/internal/session"
	"github.com/ChrisZhangJin/perch/internal/setup"
)

// version and commit are set at link time via
//
//	-ldflags "-X main.version=0.2.0 -X main.commit=abcdef0"
//
// Defaults match `go run` and local builds without ldflags.
var (
	version = "dev"
	commit  = "unknown"
	// pidfilePath is the daemon-mode pidfile location, populated when
	// --daemon is used. It is package-scope so exitInChild (and any
	// other early-exit helper) can reach it from anywhere in main().
	pidfilePath string
)

// versionString formats the value printed by --version and the startup log.
// When commit is "unknown" we omit it; otherwise append " (commit <hash>)"
// so two dev builds of the same Version are still distinguishable.
func versionString() string {
	if commit == "unknown" {
		return version
	}
	return version + " (commit " + commit + ")"
}

// realReadPassword is the production PasswordFn: it delegates to
// golang.org/x/term so the auth code is not echoed. Test code passes its own.
func realReadPassword(fd int) ([]byte, error) {
	return term.ReadPassword(fd)
}

func main() {
	configPath := flag.String("config", "", "path to YAML config file (default: ./perch.yaml, then ~/.perch/perch.yaml). Env: PERCH_CONFIG.")
	showVersion := flag.Bool("version", false, "print version and exit. Shorthand: -V.")
	logLevel := flag.String("log-level", "", "override log_level (debug|info|warn|error). Wins over YAML log_level. Effective for this run only.")
	daemonMode := flag.Bool("daemon", false,
		"detach from terminal, write pidfile, exit parent. "+
			"Logs after this point go to /dev/null until log files land.")
	flag.BoolVar(daemonMode, "D", false, "alias for --daemon")
	flag.BoolVar(showVersion, "V", false, "alias for --version")
	flag.Parse()

	// --version short-circuits before any config / network work so the
	// flag is useful in scripts and CI without needing a valid config.
	// Must come BEFORE the --daemon handoff so that --daemon --version
	// prints version instead of forking (the handoff os.Exit(0)s).
	if *showVersion {
		fmt.Println("perch " + versionString())
		os.Exit(0)
	}

	// --daemon handoff: re-exec as a detached child, write pidfile,
	// exit parent. Must come AFTER flag.Parse and AFTER --version.
	if *daemonMode {
		pidfilePath = filepath.Join(homeDir(), ".perch", "perch.pid")
		childPID, err := daemon.Daemonize(os.Args[1:], pidfilePath, nil)
		if err != nil {
			switch {
			case errors.Is(err, daemon.ErrAlreadyRunning):
				fmt.Fprintf(os.Stderr,
					"perch: already running: see pidfile %s\n", pidfilePath)
			case errors.Is(err, daemon.ErrReexecFailed):
				fmt.Fprintf(os.Stderr, "perch: daemonize: %v\n", err)
			default:
				fmt.Fprintf(os.Stderr, "perch: daemonize: %v\n", err)
			}
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr,
			"perch daemon started, pid %d, pidfile %s\n", childPID, pidfilePath)
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		// Logger not built yet — fall back to a default-format handler so
		// the failure message is still readable.
		fallback := slog.New(plog.New(os.Stderr, slog.LevelInfo))
		slog.SetDefault(fallback)
		fallback.Error("config", "err", err)
		exitInChild(1)
	}
	// Precedence: CLI --log-level > LOG_LEVEL env (already folded into
	// cfg.LogLevel by config.applyEnv) > YAML log_level > built-in "info".
	levelSource := cfg.LogLevel
	if *logLevel != "" {
		levelSource = *logLevel
	}
	level, lvlErr := config.ParseLogLevel(levelSource)
	log := slog.New(plog.New(os.Stderr, level))
	slog.SetDefault(log)
	if lvlErr != nil {
		log.Warn("log level", "err", lvlErr, "value", levelSource)
	}
	log.Info("perch starting", "version", versionString(), "log_level", level.String())
	if used := config.ResolveConfigPath(*configPath); used != "" {
		log.Info("config loaded", "path", used)
	} else {
		log.Info("config loaded", "path", "(built-in defaults only — no YAML file found)")
	}

	// First-run wizard: prompt for missing fields if stdin is a TTY,
	// otherwise return ErrMissingFields and exit non-zero with a hint.
	if err := setup.Ensure(cfg, os.Stdin, os.Stdout, os.Stderr, realReadPassword); err != nil {
		log.Error("setup", "err", err)
		exitInChild(2)
	}

	// Resolve named providers/agents.
	p, err := provider.Lookup(cfg.ProviderName)
	if err != nil {
		log.Error("provider lookup", "err", err)
		exitInChild(1)
	}
	ag, err := agent.Lookup(cfg.AgentName)
	if err != nil {
		log.Error("agent lookup", "err", err)
		exitInChild(1)
	}

	g, err := gate.New(cfg.AllowFrom)
	if err != nil {
		log.Error("gate build", "err", err)
		exitInChild(1)
	}
	strat, err := mailbox.BuildStrategy(cfg, p)
	if err != nil {
		log.Error("mailbox dial", "err", err)
		exitInChild(1)
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
		exitInChild(1)
	}
	a := app.New(cfg, strat.Box, g, sess,
		runner.New(&ag, cfg.AgentWorkdir, cfg.AgentPermMode, cfg.TaskTimeout, log),
		replier.New(cfg, p.SMTPAddr),
		log,
		strat.Triggers...,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if os.Getenv("PERCH_DAEMON_CHILD") != "" {
		defer func() {
			if err := os.Remove(pidfilePath); err != nil && !os.IsNotExist(err) {
				// Best-effort: log to stderr because the logger may
				// already be torn down by the time defers run, and in
				// daemon mode stderr is /dev/null anyway. Surface to
				// the parent's stderr via a fallback line.
				fmt.Fprintf(os.Stderr, "perch: pidfile remove: %v\n", err)
			}
		}()
	}

	log.Info("watcher started", "email", cfg.Email, "provider", p.Name, "agent", ag.Name, "poll", cfg.PollInterval, "allow_from", cfg.AllowFrom)
	if err := a.Run(ctx); err != nil {
		log.Error("run", "err", err)
		exitInChild(1)
	}
	log.Info("watcher stopped")
}

// homeDir returns the user's home directory or an empty string if
// it cannot be resolved. Empty means the daemon-mode pidfile path
// will be relative ("/.perch/perch.pid") which will then fail to
// write — surfacing the error to the user.
func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// exitInChild removes the daemon pidfile (if we are the daemon child)
// before exiting with the given code. This covers the early-exit paths
// (config load, setup, provider/agent lookup, gate build, mailbox dial)
// where the pidfile-removal defer registered after signal.NotifyContext
// has not yet been reached.
func exitInChild(code int) {
	if os.Getenv("PERCH_DAEMON_CHILD") != "" {
		if err := os.Remove(pidfilePath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "perch: pidfile remove: %v\n", err)
		}
	}
	os.Exit(code)
}
