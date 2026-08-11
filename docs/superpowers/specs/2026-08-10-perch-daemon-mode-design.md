# Perch Daemon Mode — Design

**Date:** 2026-08-10
**Status:** Approved (brainstorming)

## Purpose

Add a `--daemon` flag so perch can run detached from its launching terminal.
The user wants to run perch on resource-constrained machines where they close
the SSH session and expect the watcher to keep going. Today, closing the
terminal delivers SIGHUP to the foreground process and perch dies.

This is **not** a replacement for systemd/launchd on production hosts. It is
the minimum needed to make `./perch --daemon` a usable, single-binary
long-running form on a dev machine.

### Non-goals (deferred)

- **Log file output.** Out of scope. Writing logs to disk without rotation
  risks filling the disk on the very machines this daemon mode targets.
  Tracked as a roadmap item: log file + rotation/compression.
- **`./perch stop` subcommand.** Out of scope. The PID file is the contract;
  users `kill <pid>` themselves. SIGTERM is already handled by the existing
  signal handler in `cmd/perch/main.go`.
- **Replacing the systemd unit in README.** The unit example stays for users
  who already run perch that way. `--daemon` is the alternative for users
  who do not.

## Behavior

When `./perch --daemon` is invoked:

1. The current process **re-execs itself** as a child process.
2. The child is detached from the controlling terminal via `setsid(2)` and
   has stdin/stdout/stderr redirected to `/dev/null`.
3. The parent writes the child's PID to `~/.perch/perch.pid` (mode `0644`,
   inside the existing `~/.perch/` directory which the wizard sets to `0700`).
4. The parent prints confirmation to its stderr (still the terminal) and
   exits 0.
5. The child runs the existing main flow unchanged: load config, build
   logger, ensure setup, build the app, run the watcher loop.
6. When the child receives SIGINT/SIGTERM and shuts down cleanly, it removes
   `~/.perch/perch.pid` before exiting.

When `--daemon` is **not** set, behavior is unchanged.

## PID file semantics

- **Path:** `~/.perch/perch.pid`. Resolved via `os.UserHomeDir()` +
  `filepath.Join(".perch", "perch.pid")`. Not configurable in this spec
  (kept simple; can become a `--pidfile` flag later if needed).
- **Mode:** `0644`. The directory itself is `0700`, so the file is not
  world-readable in practice.
- **Owner:** the user who invoked `--daemon`. No root required.
- **Created by:** the parent process, immediately after `os.StartProcess`
  returns a non-error. The parent's stderr message includes the resolved
  path so users can find it.
- **Removed by:** the child, in a `defer` in `main()` after the signal
  context is cancelled, before the final log line.
- **Stale refusal:** if the pidfile exists and the recorded PID is not
  alive (probe via `syscall.Kill(pid, 0)` returns `ESRCH`), the parent
  logs a WARN, refuses to start, and instructs the operator to inspect
  and remove the file manually. The pidfile is **not** touched —
  auto-remove would mask a dying daemon that the user needs to
  investigate.
- **Live refusal:** if the pidfile exists and the recorded PID **is**
  alive, the parent prints an ERROR and exits 1. The pidfile is **not**
  touched.
- **Unknown probe result:** any error from `kill(pid, 0)` other than
  `ESRCH` (e.g. EPERM on a PID owned by another user) is treated as
  "alive" → fail-closed, refuse to start. We never overwrite a pidfile
  we cannot verify is dead.

## Re-exec mechanics

The re-exec uses `os.StartProcess` with:

- `SysProcAttr.Setsid = true` — new session, breaks controlling-tty link,
  immune to SIGHUP from the launching terminal.
- `Files = []*os.File{devnull, devnull, devnull}` where `devnull` is the
  result of `os.OpenFile("/dev/null", os.O_RDWR, 0)`.
- `argv` is the parent's `os.Args[1:]` with `--daemon` and `-D` removed.
  No recursion possible because the child never sees the flag.

The re-exec happens **before** config loading so that the early startup
log line ("perch starting") is not written to `/dev/null`. The trade-off
is intentional: in daemon mode, only the daemonization handoff messages
go to the parent's stderr. All subsequent child-process logs go to
`/dev/null` until the log-file feature lands.

The parent writes the pidfile **after** `os.StartProcess` returns
successfully, never before. If the re-exec fails, no pidfile is left
behind and the parent exits 1 with the error on stderr.

## Flag surface

One new flag in `cmd/perch/main.go`:

```go
daemon := flag.Bool("daemon", false,
    "detach from terminal, write pidfile, exit parent. "+
        "Logs after this point go to /dev/null until log files land.")
flag.BoolVar(daemon, "D", false, "alias for --daemon")
```

Short alias `-D` mirrors the existing `-V` alias for `--version`.

If `--daemon` is set, the very first action `main()` takes (after flag
parse) is:

```go
if *daemon {
    childPID, err := daemon.Daemonize(os.Args[1:], pidfilePath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "perch: daemonize: %v\n", err)
        os.Exit(1)
    }
    fmt.Fprintf(os.Stderr, "perch daemon started, pid %d, pidfile %s\n",
        childPID, pidfilePath)
    os.Exit(0)
}
```

The child process runs the rest of `main()` unchanged. After
`signal.NotifyContext` is wired and the existing shutdown path is in
place, we add:

```go
defer func() {
    if !daemonMode {
        return
    }
    if err := os.Remove(pidfilePath); err != nil && !os.IsNotExist(err) {
        log.Warn("pidfile remove", "path", pidfilePath, "err", err)
    }
}()
```

`daemonMode` is a local boolean set from `*daemon`. The `defer` runs
after `a.Run(ctx)` returns, regardless of cause.

## Code layout

New package `internal/daemon`:

- `daemon.go` — public API:
  - `Daemonize(argv []string, pidfilePath string) (childPID int, err error)`
  - `processAlive(pid int) bool` — exported for tests
  - Sentinel errors: `ErrAlreadyRunning`, `ErrReexecFailed`
- `daemon_test.go` — unit tests for stale-pidfile cleanup, live-pid
  refusal, argv stripping, pidfile cleanup on re-exec failure.

`cmd/perch/main.go` changes:

- Add `--daemon` / `-D` flag.
- Add daemonization handoff at top of `main()`.
- Add `defer` to remove pidfile on shutdown in the child.
- Pass `*daemon` and the resolved pidfile path into the deferred removal.

No changes to `internal/app/`, `internal/mailbox/`, `internal/agent/`, or
any other package.

## Testing

### Unit tests (`internal/daemon/daemon_test.go`)

| Test | Asserts |
|---|---|
| `TestStripDaemonFlag` | `--daemon` and `-D` removed from a sample argv; other flags preserved |
| `TestStalePidfileRefused` | pidfile with a known-dead PID → `Daemonize` returns `ErrStalePidfile`; pidfile is **not** removed (operator must inspect) |
| `TestLivePidRefusesStart` | pidfile with an alive PID (current process) → `Daemonize` returns `ErrAlreadyRunning`; pidfile unchanged |
| `TestReexecFailureLeavesNoPidfile` | injected re-exec error → pidfile does not exist on disk afterwards |
| `TestProcessAlive_UnknownError` | `processAlive` on a PID where `kill` returns EPERM returns true (fail-closed) |

All tests use a tempdir for the pidfile path via `t.TempDir()` so they
do not touch `~/.perch/`.

### Integration smoke (`scripts/localtest/daemon.sh`, new)

1. Build the binary.
2. Export `AGENT_EMAIL=daemon-test@perch.local`,
   `AGENT_AUTH_CODE=test`, `ALLOW_FROM=` (empty).
3. Run `./perch --daemon --config /tmp/empty.yaml`. Assert exit 0.
4. Assert `~/.perch/perch.pid` exists and contains a PID that responds
   to `kill -0`.
5. Run `./perch --daemon` again. Assert non-zero exit and "already
   running" message.
6. `kill $(cat ~/.perch/perch.pid)`. Wait up to 2s. Assert pidfile is
   gone.
7. Run `./perch --daemon` once more. Assert exit 0 (clean restart).

The script does **not** require a real IMAP server — the daemon will
fail to dial, but the daemonization itself happens before the dial
in the existing flow, so we can test the handoff without network.

### Manual matrix (`docs/TESTING.md` update)

Add a "Daemon mode" subsection documenting:

- How to launch (`./perch --daemon`)
- Where the pidfile lives
- How to stop (`kill $(cat ~/.perch/perch.pid)` or `pkill -TERM perch`)
- How to verify (`kill -0 $(cat ~/.perch/perch.pid)`)
- What to do if you see "stale pidfile" on restart

## Error handling summary

| Failure | Detection | User-visible message | Exit |
|---|---|---|---|
| Re-exec fails | `os.StartProcess` returns err | `perch: daemonize: <err>` | 1 |
| `~/.perch/` not writable | `os.WriteFile` returns err | `perch: daemonize: <err>` | 1 |
| Live pidfile | `processAlive(pid)` true | `perch: already running: pid <N> (pidfile <path>)` | 1 |
| Stale pidfile | `processAlive(pid)` false, ESRCH | `perch: stale pidfile at <path>: pid <N> not alive, removing` (WARN), then proceeds | n/a |
| `/dev/null` missing | `os.OpenFile` returns err | `perch: daemonize: <err>` | 1 |
| Child shutdown cannot remove pidfile | `os.Remove` returns non-ENOENT err | logged at WARN in child | 0 |

All parent-side messages go to the parent's stderr (still attached to the
terminal at this point). All child-side messages go to `/dev/null` until
log files land.

## Compatibility

- No change to behavior when `--daemon` is absent.
- No change to flag precedence or config loading.
- No change to the systemd/launchd story in the README; the unit example
  stays for users who want it.
- No new dependencies. The implementation uses only `os`, `os/exec`,
  `path/filepath`, `syscall`, and `fmt` from stdlib.

## Open questions

None at design time. The roadmap items (log file + rotation, optional
`--pidfile` flag, optional `./perch stop` subcommand) are tracked
separately and intentionally not part of this spec.