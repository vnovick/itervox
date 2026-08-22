package main

import (
	"os"
	"os/exec"

	"github.com/charmbracelet/x/term"
)

// fatalExit terminates the process with the given exit code, restoring the
// terminal to a sane mode if stdin is a TTY. This is the SAFE alternative to
// raw os.Exit for any code path that may run AFTER `go statusui.Run` puts the
// terminal into alt-screen / raw mode — without the cooked-mode restore, the
// shell prompt comes back garbled (no echo, broken line discipline).
//
// The main()-level `defer recover()` in main.go (T-12) handles the panic case;
// fatalExit covers the explicit-exit case. Callers that exit BEFORE statusui
// has been launched are equally safe to use this — the stty call is a no-op
// when the TTY is already in cooked mode.
//
// Pair with the CLAUDE.md invariant: any new os.Exit() outside this file is
// a regression candidate (see scripts/check-no-os-exit.sh).
// onFatalExit, when set, runs immediately before the process exits.
//
// os.Exit does not run deferred functions, so the runtime-file cleanup
// registered with `defer` in main() was skipped on every fatalExit path — a
// failed rebind or an invalid config on the first iteration left a stale
// daemon.pid, dashboard_url and HEARTBEAT.md behind. Those are precisely the
// files `itervox doctor` and `itervox status` read to decide whether a daemon
// is alive, so the daemon died while leaving evidence that it was running
// (#44's residual). Registering the same cleanup here covers every fatalExit
// path, not just the two known ones.
//
// Set once, from main(), at the point those files first exist. Never called
// concurrently: fatalExit terminates the process.
var onFatalExit func()

func fatalExit(code int) {
	if onFatalExit != nil {
		// Best-effort: a cleanup failure must never stop the exit, and each
		// remover is already idempotent, so running here as well as via the
		// deferred copy on the normal path is harmless.
		onFatalExit()
	}
	if term.IsTerminal(os.Stdin.Fd()) {
		_ = exec.Command("stty", "sane").Run()
	}
	os.Exit(code)
}
