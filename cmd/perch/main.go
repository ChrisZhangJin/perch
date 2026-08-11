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
	"strings"
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
	// Diagnostic stderr redirect for --daemon. When the parent was
	// invoked with --daemon-stderr <path>, it propagated the path via
	// PERCH_DAEMON_STDERR; we open that file as our stderr so anything
	// the child would have written to /dev/null ends up readable.
	// Must happen BEFORE config load / logger setup / wizard, since
	// any of those can be the first thing that errors.
	if stderrPath := os.Getenv("PERCH_DAEMON_STDERR"); stderrPath != "" {
		_ = daemon.RedirectStderr(stderrPath)
		// If redirect fails (open error, dup3 error, or building on a
		// platform where --daemon-stderr is a no-op), we have no stderr
		// to log to — fall through silently. The user will see an
		// empty debug file, which is itself useful signal.
	}

	// pidfilePath is populated in two cases:
	//   1. We are the daemon child: Daemonize set PERCH_DAEMON_PIDFILE
	//      in our env, so we read it here. This must happen BEFORE any
	//      exitInChild call so early exits can clean up the pidfile.
	//   2. We are the parent running --daemon: set inside the daemon
	//      handoff block below, then we os.Exit(0) before reaching
	//      any exitInChild call.
	var pidfilePath string
	if env := os.Getenv("PERCH_DAEMON_PIDFILE"); env != "" {
		pidfilePath = env
	}

	// exitInChild is a closure capturing pidfilePath. It removes the
	// daemon pidfile (if we are the daemon child) before exiting. The
	// child-process check is the env var, not the --daemon flag, because
	// stripDaemonFlags removes --daemon from the child's argv.
	exitInChild := func(code int) {
		if pidfilePath != "" && os.Getenv("PERCH_DAEMON_CHILD") != "" {
			if err := os.Remove(pidfilePath); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "perch: pidfile remove: %v\n", err)
			}
		}
		os.Exit(code)
	}

	configPath := flag.String("config", "", "path to YAML config file (default: ./perch.yaml, then ~/.perch/perch.yaml). Env: PERCH_CONFIG.")
	showVersion := flag.Bool("version", false, "print version and exit. Alias: --V.")
	logLevel := flag.String("log-level", "", "override log_level (debug|info|warn|error). Wins over YAML log_level. Effective for this run only.")
	daemonMode := flag.Bool("daemon", false,
		"detach from terminal, write pidfile, exit parent. "+
			"Logs after this point go to /dev/null until log files land. Alias: --D.")
	daemonStderr := flag.String("daemon-stderr", "",
		"diagnostic: redirect the daemon child's stderr to this file instead of /dev/null. "+
			"For debugging why --daemon exits immediately; remove once root-caused.")
	flag.BoolVar(daemonMode, "D", false, "alias for --daemon")
	flag.BoolVar(showVersion, "V", false, "alias for --version")
	flag.Parse()

	// GNU convention: single-letter flags accept both -x and --x; multi-
	// letter flags only accept --flag. Go's flag package accepts both
	// forms for everything; reject single-hyphen forms of any multi-
	// letter flag here so `-config` doesn't silently work.
	for _, arg := range os.Args[1:] {
		if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") {
			continue
		}
		// arg is "-x" (single letter) or "-foo" (multi letter we reject).
		name := strings.TrimPrefix(arg, "-")
		if len(name) == 1 {
			continue // single-letter flag, both forms OK
		}
		fmt.Fprintf(os.Stderr,
			"perch: %q requires double-hyphen (use --%s)\n", arg, name)
		os.Exit(2)
	}

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
		// Refuse if config is not populated enough to run unattended. The
		// child runs with stdio on /dev/null, so the wizard can't prompt
		// there — catch the gap here while the parent still has a real
		// stdin. We only Load() (no wizard), because the wizard would
		// silently print the non-TTY hint and exit 2 — confusing in a
		// daemon context. The user should run `./perch` first (no flag)
		// to fill the wizard interactively, then re-run with --daemon.
		preCfg, preErr := config.Load(*configPath)
		if preErr != nil {
			fmt.Fprintf(os.Stderr, "perch: daemon: config: %v\n", preErr)
			fmt.Fprintln(os.Stderr, "hint: run `./perch` (no flag) to set up, then re-run with --daemon.")
			os.Exit(1)
		}
		if missing := setup.MissingFields(preCfg); len(missing) > 0 {
			fmt.Fprintf(os.Stderr,
				"perch: daemon: refusing to start — required fields not set: %s\n",
				strings.Join(missing, ", "))
			fmt.Fprintln(os.Stderr,
				"hint: run `./perch` (no flag) once interactively to fill the wizard, then re-run with --daemon.")
			os.Exit(2)
		}
		// Preflight the agent binary too. Without this, a stale config
		// pointing at a missing binary causes the daemon child to crash
		// silently on first email (stdio is /dev/null, so the runner's
		// exec error is invisible).
		if _, err := agent.BinaryPath(preCfg.AgentName); err != nil {
			fmt.Fprintf(os.Stderr, "perch: daemon: %v\n", err)
			fmt.Fprintln(os.Stderr,
				"hint: install the binary, fix PATH, or re-run `./perch` to pick a different agent.")
			os.Exit(1)
		}
		pidfilePath = filepath.Join(homeDir(), ".perch", "perch.pid")
		var daemonExtraEnv []string
		if *daemonStderr != "" {
			daemonExtraEnv = append(daemonExtraEnv, "PERCH_DAEMON_STDERR="+*daemonStderr)
		}
		childPID, err := daemon.Daemonize(os.Args[0], os.Args[1:], pidfilePath, daemonExtraEnv, nil)
		if err != nil {
			switch {
			case errors.Is(err, daemon.ErrAlreadyRunning):
				fmt.Fprintf(os.Stderr,
					"perch: already running: see pidfile %s\n", pidfilePath)
			case errors.Is(err, daemon.ErrStalePidfile):
				fmt.Fprintf(os.Stderr,
					"perch: pidfile %s references a dead process — refusing to auto-remove.\n"+
						"Inspect with: cat %s\n"+
						"Remove with: rm %s\n",
					pidfilePath, pidfilePath, pidfilePath)
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
