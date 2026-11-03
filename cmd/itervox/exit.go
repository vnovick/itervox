package main

import (
	"os"
)

// fatalExit terminates the process with the given exit code after running
// onFatalExit. It is the only way cmd/itervox exits explicitly
// (`make no-os-exit`).
//
// It used to also run `stty sane`, for exits after the terminal UI had put
// the terminal into raw mode. The terminal UI is gone (#240) and nothing in
// cmd/itervox changes terminal modes any more (`itervox watch` never does;
// the hidden-input prompts of `itervox secret` and `itervox hub slack setup`
// restore echo themselves before they return), so fatalExit no longer
// touches the terminal. It stays for the runtime-file cleanup below.
//
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
	os.Exit(code)
}

// exitWithoutCleanup exits with code and nothing else: no cleanup hook.
// Only for the guardrail shim and hook entrypoints (#204), which run inside
// an agent's terminal and must leave everything exactly as they found it,
// whatever fatalExit does in the future.
func exitWithoutCleanup(code int) {
	os.Exit(code)
}
