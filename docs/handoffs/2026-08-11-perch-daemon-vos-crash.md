# Handoff: perch daemon child dies on `vos` (post-fix investigation)

## Session state at handoff

- **Branch**: `main`, ahead of `origin/main` by 22 commits
- **Last 2 commits** (already on disk and built into `/root/workspace/perch/bin/perch`):
  - `ef3f53e` — `fix(daemon): refuse on stale pidfile; verify child alive post-write`
  - `846f904` — `fix(runner): derive agent identity from mailbox; ban internal monologue`
- **Binary**: `/root/workspace/perch/bin/perch` (2.9 MB, UPX-compressed, v0.2.0-21-gf937322-dirty)
- **Tests**: `go test ./...` all pass

## What the original 4 issues were + what got fixed

| # | Issue | Status | Fix |
|---|---|---|---|
| 1 | `perch -D` silently proceeds when pidfile exists | **fixed** | New `daemon.ErrStalePidfile`; refuse on any existing pidfile; surface inspect/remove instructions. |
| 2 | Daemonized child not verified after `os.StartProcess` | **fixed (partially)** | Added 300ms grace + `processAlive(child.Pid)` recheck in `daemon.Daemonize`. If dead, remove pidfile + return wrapped `ErrReexecFailed`. |
| 3 | Agent's internal monologue ("I'll start by...", "Now I have...") leaks into email body | **fixed** | Added "Do NOT write internal-reasoning summaries" rule naming the exact phrases + "ZERO-thought rule" in `runner.BuildPrompt`. |
| 4 | Agent identity hardcoded as "Tommy" but mailbox is `agent_phillip@163.com` | **fixed** | New `agentDisplayName(email)` helper in `runner.go`; `BuildPrompt` takes `agentEmail` param; `app.ProcessUnseen` passes `a.cfg.Email`. |

## What the user reported AFTER the fix was deployed

On `vos` (user's machine), the rebuilt binary:

```
$ perch -D
perch daemon started, pid 9051, pidfile /home/zhangjin/.perch/perch.pid
$ ps -ef | grep 9051
zhangjin  9150  3475  0 16:34 pts/1    00:00:00 grep 9051   # only grep shows up
```

But with `--daemon-stderr /tmp/perch-debug.log`:

```
$ perch -D --daemon-stderr /tmp/perch-debug.log
perch daemon started, pid 10564
$ ps -ef | grep 10564
zhangjin 10564     1  0 16:44 ?        00:00:00 --daemon-stderr /tmp/perch-debug.log
```

The child stays alive **only when `--daemon-stderr` is passed**. The difference:
- Without: child stdio is `/dev/null` (set by `os.StartProcess` `Files: []*os.File{devnull, devnull, devnull}`).
- With: child's stderr is dup2'd onto the log file at the very top of `main()` (lines 64-70 of `cmd/perch/main.go`), before flag.Parse.

## Diagnostic data the user provided

With `--daemon-stderr /tmp/perch-debug.log`, the child logs through "watcher started" and then stays alive in the poll loop:

```
2026-08-11 16:41:01.327 INFO "perch starting" version=v0.2.0-21-gf937322-dirty log_level=DEBUG
2026-08-11 16:41:01.327 INFO "config loaded" path=/home/zhangjin/.perch/perch.yaml
2026-08-11 16:41:01.327 INFO "mailbox ready" mode=poll-only note="server has no IMAP IDLE; using POLL_INTERVAL"
2026-08-11 16:41:01.327 INFO "watcher started" email="agent_phillip@163.com" provider=163 agent=nanopi poll=1m0s allow_from=[s".*@wiz\.ai"]
```

So the child's full init sequence completes when stderr is redirected to a file. With stderr on /dev/null, the child is dead by the time the user greps.

## Hypotheses (unresolved)

1. **Race in the 300ms grace window**: child dies AFTER the parent's `processAlive(child.Pid)` check at line 194 of `daemon.go` but BEFORE the parent exits. The grace is 300ms; the child's full init (config + logger + provider + mailbox + session) takes longer on `vos` somehow. **Worth testing**: bump `verifyGrace` to 1-2 seconds and see if the bug disappears on vos.

2. **/dev/null vs file fd keeps process alive**: There is some quirk on vos where a process whose stderr is /dev/null exits silently. Maybe a kernel-level process group cleanup, maybe a SIGHUP from the controlling terminal that a file-fd-anchored process ignores. **Worth testing**: leave the `--daemon-stderr` flag in place BUT point it at `/dev/null` (e.g., `--daemon-stderr /dev/null`). If that still works, hypothesis 1 is wrong and it's specifically about /dev/null behavior. If that fails, the fd-holding is what keeps the child alive.

3. **UPX decompression + stdio race**: the binary is UPX-compressed. UPX decompresses on startup, then jumps to the real entry point. If the child's stdio is closed (/dev/null) during UPX's decompression phase, something goes wrong. **Worth testing**: rebuild WITHOUT UPX (`PERCH_NO_UPX=1 make build`) and see if `perch -D` works on vos.

4. **Setsid + stdin = /dev/null combination**: `Sys: &syscall.SysProcAttr{Setsid: true}` puts the child in its own session, and stdin is /dev/null. Maybe vos has session-leader weirdness. **Worth testing**: keep `--daemon-stderr` flag for now (the workaround) and accept the current behavior, or try a different approach (use a socketpair / pipe to keep a reference fd open).

## What I did NOT figure out

- **Why** the child dies on vos. Reproduced in `/tmp/argvtest.go` that empty `argv` works fine on this dev machine, so empty argv alone is not the cause. The bug is specific to vos.
- Whether the 300ms grace is long enough on vos or whether we need 1-2s.
- Whether the fix is to keep `--daemon-stderr` permanent (drop it from "diagnostic" framing), or find a different way to keep the child alive.

## What the user needs to do next session

1. **Run this on vos to narrow down**:
   ```bash
   # Test 1: is the fd-holding what keeps it alive?
   rm -f /home/zhangjin/.perch/perch.pid
   perch -D --daemon-stderr /dev/null
   ps -ef | grep perch
   ```

   If alive → it's not /dev/null specifically; hypothesis 1 (race) is the likely cause. Bump `verifyGrace` to 1-2s.
   If dead → /dev/null is the trigger. Keep `--daemon-stderr` as a non-diagnostic flag, or find another anchor fd.

2. **Test 2 (only if Test 1 still dead)**: rebuild without UPX:
   ```bash
   PERCH_NO_UPX=1 make build
   ./bin/perch -D
   ```
   If alive → UPX is involved; the fix is to skip UPX or to make `os.StartProcess` set stdio AFTER decompression (impossible — already decompressed by then).
   If still dead → it's a real env quirk on vos.

3. **If neither test resolves it**: ask the user to run `strace -f -o /tmp/strace.log perch -D` and inspect the trace to see what syscall the child fails on.

## Files to look at if you continue

- `/root/workspace/perch/internal/daemon/daemon.go:185-198` — the verify grace + liveness check (the fix from this session). Bump `verifyGrace` constant on line 34 if hypothesis 1 is confirmed.
- `/root/workspace/perch/cmd/perch/main.go:64-73` — early stderr redirect that makes `--daemon-stderr` work. If `--daemon-stderr` becomes permanent, change the help text on the flag definition (line 106-108) from "diagnostic" to "always".
- `/root/workspace/perch/internal/daemon/daemon.go:155-165` — `os.StartProcess` with `Files: []*os.File{devnull, devnull, devnull}`. If the fix turns out to be "always keep a real fd open", the change goes here.

## What's currently committed

Two new commits on `main` (ahead of `origin/main` by 22):
- `846f904` fix(runner): derive agent identity from mailbox; ban internal monologue
- `ef3f53e` fix(daemon): refuse on stale pidfile; verify child alive post-write

Both ready to push when the user wants to.
