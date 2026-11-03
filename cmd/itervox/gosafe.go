package main

import (
	"log/slog"
	"runtime/debug"
)

// failFastOnPanic is cmd/itervox's panic policy for LONG-LIVED background
// loops (CORE-008): the outbox flusher, automation scheduler, deps
// auto-analyze scheduler, heartbeat writer, HTTP accept/serve loops, the
// workflow watcher and the reload-generation runner. Swallowing a panic there
// would silently end the loop while the daemon kept reporting healthy, so the
// policy matches the event loop's: fail fast. What this adds over a bare
// goroutine panic is that the panic and its stack go through slog — the
// redacting handler — so the rotating log file records them, not only
// stderr. The re-panic still terminates the process and, with
// debug.SetCrashOutput armed (CORE-007), lands in crash.log.
//
// Must be deferred DIRECTLY (`defer failFastOnPanic("name")`) so recover()
// is called by the deferred function itself. One-shot leaf goroutines use
// orchestrator.RecoverGoroutine instead, which recovers and reconciles.
func failFastOnPanic(name string) {
	r := recover()
	if r == nil {
		return
	}
	slog.Error("goroutine panic: failing fast",
		"goroutine", name,
		"panic", r,
		"stack", string(debug.Stack()))
	panic(r)
}
